package torrstor

import (
	"container/list"
	"os"
	"sync"
)

// PieceFilePoolLimit is how many piece files may stay open at once.
//
// Measured on the production box: DiskPiece used to OpenFile+Close on every
// WriteAt and ReadAt, and during a download phase `pwrite` plus the
// open/close syscalls around it were the single largest CPU cost (syscalls
// 31% of the process). A 97.9 GiB cache holds ~50k piece files, far past any
// descriptor limit, so this is an LRU over the working set rather than "keep
// everything open" — the same job libtorrent's file_pool_size does.
const PieceFilePoolLimit = 512

// filePool keeps a bounded number of piece files open, keyed by path.
//
// Two invariants make it simple enough to be safe:
//
//   - All operations on one piece are serialised by that piece's own mutex, so
//     a given path is never handed to two goroutines at the same time. The
//     refcount exists only for the orphan scan, which deletes a whole torrent
//     directory without knowing which piece a reader is in.
//   - A handle is keyed by path AND access class, not by the exact flag bits.
//     Reads and writes want different flags (O_RDONLY vs O_RDWR|O_CREATE), so
//     keying on the flags alone would make every read evict the write handle
//     and back — the pool would reopen as often as it reuses, which is the one
//     thing it is there to prevent. Keying on the class lets each side reuse
//     its own handle, at the cost of two descriptors for a piece that is being
//     read and written at the same time.
type filePool struct {
	mu     sync.Mutex
	limit  int
	byPath map[string]*pooledFile
	lru    *list.List
	closed bool
}

type pooledFile struct {
	path string
	f    *os.File
	uses int
	elem *list.Element
}

// accessClass is the intent behind a get(), collapsed from the os flags.
type accessClass int

const (
	classWrite accessClass = iota // O_RDWR|O_CREATE
	classRead                     // O_RDONLY, never creates

	// classKeySuffix separates the two classes in the pool key so a path can
	// hold one handle per class.
	classKeySuffix = "\x00"
)

func classKey(path string, class accessClass) string {
	if class == classRead {
		return path + classKeySuffix + "r"
	}
	return path + classKeySuffix + "w"
}

func classFlags(class accessClass) int {
	if class == classRead {
		return os.O_RDONLY
	}
	return os.O_RDWR | os.O_CREATE
}

func newFilePool(limit int) *filePool {
	if limit <= 0 {
		limit = PieceFilePoolLimit
	}
	return &filePool{
		limit:  limit,
		byPath: make(map[string]*pooledFile),
		lru:    list.New(),
	}
}

// get returns the open file for path, opening it if needed.
// The caller must Put the result when it is done with it.
//
// It returns the file only; an unopenable path is an error the caller handles
// exactly as it did when it opened the file itself.
func (fp *filePool) get(path string, class accessClass) (*os.File, error) {
	fp.mu.Lock()
	defer fp.mu.Unlock()

	if fp.closed {
		return nil, os.ErrClosed
	}

	key := classKey(path, class)
	if e, ok := fp.byPath[key]; ok {
		e.uses++
		fp.lru.MoveToFront(e.elem)
		return e.f, nil
	}

	f, err := os.OpenFile(path, classFlags(class), 0o666)
	if err != nil {
		return nil, err
	}
	e := &pooledFile{path: key, f: f, uses: 1}
	e.elem = fp.lru.PushFront(e)
	fp.byPath[key] = e
	fp.evictLocked()
	return f, nil
}

// Put releases a file previously returned by Get.
func (fp *filePool) Put(f *os.File) {
	if f == nil {
		return
	}
	fp.mu.Lock()
	defer fp.mu.Unlock()

	// The handle was opened with its path, so the pool key has a class suffix
	// the file itself does not carry; try both.
	name := f.Name()
	for _, class := range []accessClass{classWrite, classRead} {
		if e, ok := fp.byPath[classKey(name, class)]; ok && e.f == f {
			if e.uses > 0 {
				e.uses--
			}
			fp.lru.MoveToFront(e.elem)
			return
		}
	}
	// Closed already by Forget, or replaced. Do not close it again here: the
	// caller is the only owner at this point.
}

// Forget drops the cached handle for path. It reports whether the handle is
// gone and the path may now be unlinked.
//
// false means a reader still holds it: the file is left on disk and the next
// orphan scan reclaims it, which is the whole reason that scan exists. The
// alternative — unlinking underneath an open handle — silently sends every
// later write to an inode no one can read.
func (fp *filePool) Forget(path string) bool {
	fp.mu.Lock()
	defer fp.mu.Unlock()

	// Refuse for the whole path if either class is in use: unlinking while one
	// handle is live strands whatever the other class would have written.
	for _, class := range []accessClass{classWrite, classRead} {
		if e, ok := fp.byPath[classKey(path, class)]; ok && e.uses > 0 {
			return false
		}
	}
	for _, class := range []accessClass{classWrite, classRead} {
		if e, ok := fp.byPath[classKey(path, class)]; ok {
			fp.dropLocked(e)
			e.f.Close()
		}
	}
	// Nothing cached still counts as success: with no handle there is no inode
	// being kept alive, so the path may be unlinked.
	return true
}

// ForgetDir does the same for every open handle under dir, reporting how many
// files were left behind because a reader held them.
func (fp *filePool) ForgetDir(dir string) int {
	fp.mu.Lock()
	defer fp.mu.Unlock()

	var stuck int
	for _, e := range fp.byPath {
		if len(e.path) > len(dir) && e.path[:len(dir)] == dir {
			if e.uses > 0 {
				stuck++
				continue
			}
			fp.dropLocked(e)
			e.f.Close()
		}
	}
	return stuck
}

// CloseAll drops every handle. Called when the storage is torn down.
func (fp *filePool) CloseAll() {
	fp.mu.Lock()
	defer fp.mu.Unlock()

	fp.closed = true
	for _, e := range fp.byPath {
		fp.dropLocked(e)
		e.f.Close()
	}
	fp.byPath = make(map[string]*pooledFile)
	fp.lru.Init()
}

// Open reports how many handles are currently cached, for /stat and tests.
func (fp *filePool) Open() int {
	fp.mu.Lock()
	defer fp.mu.Unlock()
	return len(fp.byPath)
}

func (fp *filePool) dropLocked(e *pooledFile) {
	if e.elem != nil {
		fp.lru.Remove(e.elem)
		e.elem = nil
	}
	delete(fp.byPath, e.path)
}

// evictLocked closes least-recently-used handles until the pool is within its
// limit. Handles in use are skipped, so the pool may temporarily exceed the
// limit while several pieces are being read at once — bounded by the number of
// concurrent operations, not by the cache.
func (fp *filePool) evictLocked() {
	for len(fp.byPath) > fp.limit {
		evicted := false
		for el := fp.lru.Back(); el != nil; el = el.Prev() {
			e := el.Value.(*pooledFile)
			if e.uses > 0 {
				continue
			}
			fp.dropLocked(e)
			e.f.Close()
			evicted = true
			break
		}
		if !evicted {
			return
		}
	}
}
