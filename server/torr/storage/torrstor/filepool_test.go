package torrstor

import (
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"sync"
	"testing"
)

// pooled is a test helper that always closes the pool, so TempDir cleanup does
// not collide with a handle the pool still holds (Windows refuses to unlink an
// open file; Linux does not care, but production never unlinks a whole cache
// directory without forgetting its handles first either).
func pooled(t *testing.T, limit int) *filePool {
	t.Helper()
	fp := newFilePool(limit)
	t.Cleanup(fp.CloseAll)
	return fp
}

// The pool exists to keep a handle open across calls instead of paying
// open+close per piece read/write. These tests cover the parts where a mistake
// is silent: reusing a handle with the wrong flags, unlinking a file that is
// still open, and exceeding the descriptor bound.

func TestFilePoolReusesHandle(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "0")
	fp := pooled(t, 4)

	f1, err := fp.get(path, classWrite)
	if err != nil {
		t.Fatalf("first get: %v", err)
	}
	if _, err := f1.WriteAt([]byte("abc"), 0); err != nil {
		t.Fatalf("write: %v", err)
	}
	fp.Put(f1)

	f2, err := fp.get(path, classWrite)
	if err != nil {
		t.Fatalf("second get: %v", err)
	}
	if f2 != f1 {
		t.Error("handle was reopened instead of reused")
	}
	buf := make([]byte, 3)
	if _, err := f2.ReadAt(buf, 0); err != nil {
		t.Fatalf("read from reused handle: %v", err)
	}
	if string(buf) != "abc" {
		t.Errorf("read %q through reused handle, want %q", buf, "abc")
	}
}

// Reads must be reusable in their own right. Keying the pool on the exact flag
// bits made every read evict the write handle and back, which reopened as often
// as it reused and defeated the whole pool.
func TestFilePoolReadClassReusesSeparately(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "0")
	fp := pooled(t, 8)

	w, err := fp.get(path, classWrite)
	if err != nil {
		t.Fatalf("write get: %v", err)
	}
	w.WriteAt([]byte("xyz"), 0)
	fp.Put(w)

	r1, err := fp.get(path, classRead)
	if err != nil {
		t.Fatalf("read get: %v", err)
	}
	fp.Put(r1)

	w2, err := fp.get(path, classWrite)
	if err != nil {
		t.Fatalf("second write get: %v", err)
	}
	if w2 != w {
		t.Error("write handle was not reused across a read")
	}
	fp.Put(w2)

	r2, err := fp.get(path, classRead)
	if err != nil {
		t.Fatalf("second read get: %v", err)
	}
	if r2 != r1 {
		t.Error("read handle was not reused")
	}
	fp.Put(r2)

	buf := make([]byte, 3)
	r3, _ := fp.get(path, classRead)
	r3.ReadAt(buf, 0)
	fp.Put(r3)
	if string(buf) != "xyz" {
		t.Errorf("read %q, want %q", buf, "xyz")
	}
}

// O_RDONLY must not create a file: every peer asks for pieces that do not
// exist yet, and an empty file per such request would litter the cache with
// bytes the accounting never counted.
func TestFilePoolReadDoesNotCreate(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "absent")
	fp := pooled(t, 4)

	f, err := fp.get(path, classRead)
	if !os.IsNotExist(err) {
		t.Fatalf("get on absent path: err = %v, want IsNotExist", err)
	}
	if f != nil {
		t.Error("file returned for a path that does not exist")
	}
	if entries, _ := os.ReadDir(dir); len(entries) != 0 {
		t.Errorf("read created %d files in an empty dir", len(entries))
	}
}

// The whole point of Forget: unlink an open file and every later write goes to
// an inode no path can reach.
func TestFilePoolForgetClosesHandle(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "0")
	fp := pooled(t, 4)

	f, err := fp.get(path, classWrite)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	f.WriteAt([]byte("data"), 0)
	fp.Put(f)

	if !fp.Forget(path) {
		t.Fatal("Forget reported the handle was still in use with nobody holding it")
	}
	if fp.Open() != 0 {
		t.Errorf("pool still holds %d handles after Forget", fp.Open())
	}

	// The file must really be gone, and a later write must recreate it.
	if err := os.Remove(path); err != nil {
		t.Fatalf("remove after Forget: %v", err)
	}
	f2, err := fp.get(path, classWrite)
	if err != nil {
		t.Fatalf("recreate after remove: %v", err)
	}
	f2.WriteAt([]byte("new"), 0)
	fp.Put(f2)

	buf := make([]byte, 3)
	r, _ := fp.get(path, classRead)
	r.ReadAt(buf, 0)
	fp.Put(r)
	if string(buf) != "new" {
		t.Errorf("recreated file holds %q, want %q", buf, "new")
	}
}

// Forget must drop both classes: closing only the write handle would leave the
// read one holding the inode alive, and the unlink that follows would then
// strand it.
func TestFilePoolForgetDropsBothClasses(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "0")
	fp := pooled(t, 8)

	w, _ := fp.get(path, classWrite)
	w.WriteAt([]byte("d"), 0)
	fp.Put(w)
	r, _ := fp.get(path, classRead)
	fp.Put(r)

	if fp.Open() != 2 {
		t.Fatalf("pool holds %d handles, want one per class", fp.Open())
	}
	if !fp.Forget(path) {
		t.Fatal("Forget refused with no handle in use")
	}
	if fp.Open() != 0 {
		t.Errorf("pool holds %d handles after Forget, want 0", fp.Open())
	}
	if err := os.Remove(path); err != nil {
		t.Fatalf("unlink after Forget: %v", err)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Error("file still on disk after unlink")
	}
}

// Forget must not close a handle a caller is using. It refuses instead, and the
// file is left on disk for the orphan scan.
func TestFilePoolForgetKeepsHandleInUse(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "0")
	fp := pooled(t, 4)

	f, err := fp.get(path, classWrite)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	defer fp.Put(f)

	if fp.Forget(path) {
		t.Error("Forget claimed the handle was free while a caller holds it")
	}
	if fp.Open() != 1 {
		t.Errorf("pool holds %d handles, want 1 while in use", fp.Open())
	}

	// The caller's handle must still be usable: this is the case that would
	// otherwise corrupt data without any error.
	if _, err := f.WriteAt([]byte("z"), 0); err != nil {
		t.Fatalf("write through retained handle: %v", err)
	}

	// Unlinking a file that is still open is the production reason Forget
	// refuses. Linux allows it and silently strands the inode; Windows refuses
	// the unlink outright, so the two platforms prove the same contract from
	// opposite directions.
	if runtime.GOOS == "windows" {
		if err := os.Remove(path); err == nil {
			t.Error("windows allowed unlinking a file with an open handle; " +
				"the test premise for the linux stranding case no longer holds here")
		}
		return
	}
	if err := os.Remove(path); err != nil {
		t.Fatalf("removing a file with a live handle should still work on linux: %v", err)
	}
}

func TestFilePoolForgetDir(t *testing.T) {
	dir := t.TempDir()
	torrentDir := filepath.Join(dir, "deadbeef")
	other := filepath.Join(dir, "other")
	for _, d := range []string{torrentDir, other} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}

	fp := pooled(t, 8)
	var outside string
	for _, p := range []string{filepath.Join(torrentDir, "0"), filepath.Join(torrentDir, "1"), filepath.Join(other, "0")} {
		f, err := fp.get(p, classWrite)
		if err != nil {
			t.Fatalf("get %s: %v", p, err)
		}
		outside = p
		fp.Put(f)
	}

	if stuck := fp.ForgetDir(torrentDir + string(os.PathSeparator)); stuck != 0 {
		t.Errorf("ForgetDir reported %d stuck handles, want 0", stuck)
	}
	if fp.Open() != 1 {
		t.Fatalf("pool holds %d handles, want just the one outside the dir", fp.Open())
	}
	// The other torrent's handle must survive and still work.
	f, err := fp.get(outside, classWrite)
	if err != nil {
		t.Fatalf("handle outside the dir was closed: %v", err)
	}
	if _, err := f.WriteAt([]byte("q"), 0); err != nil {
		t.Fatalf("write through surviving handle: %v", err)
	}
	fp.Put(f)
}

func TestFilePoolRespectsLimit(t *testing.T) {
	dir := t.TempDir()
	fp := pooled(t, 3)

	var held []*os.File
	for i := 0; i < 10; i++ {
		f, err := fp.get(filepath.Join(dir, string(rune('a'+i))), classWrite)
		if err != nil {
			t.Fatalf("get %d: %v", i, err)
		}
		held = append(held, f)
		fp.Put(f)
	}
	if got := fp.Open(); got > 3 {
		t.Errorf("pool holds %d handles, limit is 3", got)
	}
	for _, f := range held[len(held)-3:] {
		if _, err := f.WriteAt([]byte("1"), 0); err != nil {
			t.Errorf("recently used handle is dead: %v", err)
		}
	}
}

// Handles in use are not evictable, so the pool may exceed its limit briefly —
// but it must come back down once they are released.
func TestFilePoolEvictsAfterRelease(t *testing.T) {
	dir := t.TempDir()
	fp := pooled(t, 2)

	busy := make([]*os.File, 0, 6)
	for i := 0; i < 6; i++ {
		f, err := fp.get(filepath.Join(dir, string(rune('a'+i))), classWrite)
		if err != nil {
			t.Fatalf("get %d: %v", i, err)
		}
		busy = append(busy, f)
	}
	if got := fp.Open(); got <= 2 {
		t.Errorf("pool holds %d handles while 6 are in use, it should not be forced under the limit", got)
	}
	for _, f := range busy {
		fp.Put(f)
	}
	fp.evictLocked()
	if got := fp.Open(); got > 2 {
		t.Errorf("pool holds %d handles after release, limit is 2", got)
	}
}

func TestFilePoolCloseAll(t *testing.T) {
	dir := t.TempDir()
	fp := pooled(t, 4)

	f, err := fp.get(filepath.Join(dir, "0"), classWrite)
	if err != nil {
		t.Fatal(err)
	}
	fp.Put(f)
	fp.CloseAll()

	if _, err := f.WriteAt([]byte("x"), 0); err == nil {
		t.Error("write through a handle that CloseAll was supposed to close")
	}
	if fp.Open() != 0 {
		t.Errorf("pool holds %d handles after CloseAll", fp.Open())
	}
	if _, err := fp.get(filepath.Join(dir, "1"), classWrite); !errors.Is(err, os.ErrClosed) {
		t.Errorf("get after CloseAll: err = %v, want os.ErrClosed", err)
	}
}

// Concurrent reads and writes of different pieces share one pool; the failure
// mode of a mistake here is a corrupted piece, which only shows up as a hash
// mismatch later. Every piece is written and read back through the pool, so a
// handle mixed up between two paths is caught here.
func TestFilePoolConcurrentPieces(t *testing.T) {
	dir := t.TempDir()
	fp := pooled(t, 8)
	const pieces = 24
	const rounds = 50

	var wg sync.WaitGroup
	for p := 0; p < pieces; p++ {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()
			path := filepath.Join(dir, string(rune('A'+id)))
			for r := 0; r < rounds; r++ {
				want := []byte{byte(id), byte(r)}
				wf, err := fp.get(path, classWrite)
				if err != nil {
					t.Errorf("piece %d round %d write get: %v", id, r, err)
					return
				}
				if _, err := wf.WriteAt(want, int64(r*2)); err != nil {
					t.Errorf("piece %d round %d write: %v", id, r, err)
					fp.Put(wf)
					return
				}
				fp.Put(wf)

				rf, err := fp.get(path, classRead)
				if err != nil {
					t.Errorf("piece %d round %d read get: %v", id, r, err)
					return
				}
				got := make([]byte, 2)
				if _, err := rf.ReadAt(got, int64(r*2)); err != nil {
					t.Errorf("piece %d round %d read: %v", id, r, err)
					fp.Put(rf)
					return
				}
				fp.Put(rf)
				if got[0] != want[0] || got[1] != want[1] {
					t.Errorf("piece %d round %d: read %v, wrote %v", id, r, got, want)
					return
				}
			}
		}(p)
	}
	wg.Wait()
}
