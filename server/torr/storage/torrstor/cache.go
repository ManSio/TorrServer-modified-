package torrstor

import (
	"container/list"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"sync/atomic"
	"time"

	"github.com/anacrolix/torrent"

	"server/log"
	"server/settings"
	"server/torr/storage/state"
	"server/torr/utils"

	"github.com/anacrolix/torrent/metainfo"
	"github.com/anacrolix/torrent/storage"
)

type Cache struct {
	storage.TorrentImpl
	storage *Storage

	capacity   int64
	filled     int64
	filledSize atomic.Int64 // Thread-safe O(1) RAM counter in bytes
	hash       metainfo.Hash

	pieceLength int64
	pieceCount  int

	// LRU & Eviction
	evictionList *list.List
	muEvict      sync.Mutex
	evictChan    chan string // Async delete channel

	// pieces content is immutable after Init; muPieces guards the map
	// reference itself (nilled in Close), so holders of a snapshot may
	// safely iterate it without the lock
	pieces   map[int]*Piece
	muPieces sync.RWMutex

	readers   map[*Reader]struct{}
	muReaders sync.RWMutex

	isRemove atomic.Bool
	isClosed atomic.Bool
	muRemove sync.Mutex
	// muPrio serializes clearPriority and setLoadPriority so that the priority
	// reset of a reader that has just closed cannot wipe the priorities a
	// freshly created reader has already set.
	muPrio  sync.Mutex
	torrent *torrent.Torrent
}

func NewCache(capacity int64, storage *Storage) *Cache {
	ret := &Cache{
		capacity:     capacity,
		filled:       0,
		pieces:       make(map[int]*Piece),
		storage:      storage,
		readers:      make(map[*Reader]struct{}),
		evictionList: list.New(),
		evictChan:    make(chan string, 4096), // Buffer to avoid blocking producers
	}

	return ret
}

func (c *Cache) Init(info *metainfo.Info, hash metainfo.Hash) {
	log.TLogln("Create cache for:", info.Name, hash.HexString())
	if c.capacity == 0 {
		c.capacity = info.PieceLength * 4
	}

	c.pieceLength = info.PieceLength
	c.pieceCount = info.NumPieces()
	c.hash = hash

	if settings.BTsets.UseDisk {
		name := filepath.Join(settings.BTsets.TorrentsSavePath, hash.HexString())
		err := os.MkdirAll(name, 0o777)
		if err != nil {
			log.TLogln("Error create dir:", err)
		}
	}

	for i := 0; i < c.pieceCount; i++ {
		c.pieces[i] = NewPiece(i, c)
	}

	c.storage.wg.Add(2)
	go c.evictionWorker()
	go c.priorityWatchdog()
}

func (c *Cache) evictionWorker() {
	defer c.storage.wg.Done()
	for {
		select {
		case <-c.storage.doneCh:
			// Storage is shutting down: flush, then leave. Without this the
			// worker outlived Close by up to a second and kept deleting files
			// of a storage that was already closed.
			for len(c.evictChan) > 0 {
				_ = os.Remove(<-c.evictChan)
			}
			return
		case path := <-c.evictChan:
			_ = os.Remove(path)
		case <-time.After(time.Second):
			if c.isClosed.Load() {
				// Flush remaining paths before exiting
				for len(c.evictChan) > 0 {
					_ = os.Remove(<-c.evictChan)
				}
				return
			}
		}
	}
}

func (c *Cache) updateLRU(p *Piece) {
	if c.isClosed.Load() {
		return
	}
	c.muEvict.Lock()
	defer c.muEvict.Unlock()

	if p.evictionElem == nil {
		p.evictionElem = c.evictionList.PushFront(p)
	} else {
		c.evictionList.MoveToFront(p.evictionElem)
	}
}

// priorityWatchdog re-arms piece priorities while readers are active.
//
// setLoadPriority is only reached through the cache cleanup path, which is
// driven by piece reads and writes (see mempiece.go and diskpiece.go). Should
// priorities ever end up cleared while a reader still needs data, nothing is
// downloaded, so no piece I/O happens, so cleanup never runs and the
// priorities are never restored - the torrent stalls indefinitely with peers
// connected. Re-arming them periodically breaks that cycle regardless of how
// the priorities were lost.
func (c *Cache) priorityWatchdog() {
	defer c.storage.wg.Done()
	for {
		select {
		case <-c.storage.doneCh:
			return // storage closed: nothing left to prioritise
		case <-time.After(5 * time.Second):
			if c.isClosed.Load() {
				return
			}
			if c.torrent == nil {
				continue
			}
			if c.GetUseReaders() > 0 {
				c.rearmPriorities()
			}
		}
	}
}

// rearmPriorities restores the piece priorities of active readers without
// doing any of the rest of getRemPieces.
//
// The watchdog's whole job is to break the deadlock described on it: priorities
// are cleared somewhere, nothing downloads, so the piece I/O that normally
// triggers getRemPieces never happens. For that it needs exactly the two
// priority calls getRemPieces makes — clearPriority() then setLoadPriority()
// (the old full call did those too, so the sequence is preserved, not invented:
// skipping the clear leaves stale priorities on pieces past the window and the
// client keeps downloading outside it).
//
// What it drops is only what getRemPieces did for *eviction*, which the watchdog
// threw away anyway: walking every piece, summing the fill, building and
// sorting the removal list. On a 28 GB torrent that is a 14k-piece walk plus a
// sort every five seconds while anyone is watching.
func (c *Cache) rearmPriorities() {
	readers := c.readersSnapshot()
	if len(readers) == 0 {
		return
	}
	ranges := make([]Range, 0, len(readers))
	for _, r := range readers {
		r.checkReader()
		if r.isUse {
			ranges = append(ranges, r.getPiecesRange())
		}
	}
	merged := mergeRange(ranges)
	if len(merged) == 0 {
		return
	}
	c.clearPriority()
	c.setLoadPriority(merged)
}

func (c *Cache) SetTorrent(torr *torrent.Torrent) {
	// Defence in depth. Storage.GetCache returns nil for an unknown hash, and
	// a nil *Cache reaching this method used to take the whole stream handler
	// down with it. Callers are now checked, but a nil-receiver method that
	// panics is one missed check away from the same panic again.
	if c == nil {
		return
	}
	c.torrent = torr
}

func (c *Cache) getPieces() map[int]*Piece {
	c.muPieces.RLock()
	defer c.muPieces.RUnlock()
	return c.pieces
}

func (c *Cache) readersSnapshot() []*Reader {
	c.muReaders.RLock()
	defer c.muReaders.RUnlock()
	list := make([]*Reader, 0, len(c.readers))
	for r := range c.readers {
		list = append(list, r)
	}
	return list
}

func (c *Cache) Piece(m metainfo.Piece) storage.PieceImpl {
	if val, ok := c.getPieces()[m.Index()]; ok {
		return val
	}
	return &PieceFake{}
}

func (c *Cache) Close() error {
	if c.torrent != nil {
		log.TLogln("Close cache for:", c.torrent.Name(), c.hash)
	} else {
		log.TLogln("Close cache for:", c.hash)
	}
	c.isClosed.Store(true)

	// Hand this cache's bytes over to the orphan accounting BEFORE the cache
	// leaves s.caches (files stay on disk unless RemoveCacheOnDrop).
	c.releaseOnClose()

	c.storage.removeCache(c.hash)

	// Drop the handles before unlinking anything, not after: Windows refuses to
	// remove a file that is still open, and on Linux the unlink would strand the
	// inode while the pool kept writing to it.
	closePieceFiles(c)

	if settings.BTsets.RemoveCacheOnDrop {
		name := filepath.Join(settings.BTsets.TorrentsSavePath, c.hash.HexString())
		if name != "" && name != "/" {
			for _, v := range c.getPieces() {
				if v.dPiece != nil {
					os.Remove(v.dPiece.name)
				}
			}
			os.Remove(name)
		}
	}

	c.muReaders.Lock()
	c.readers = nil
	c.muReaders.Unlock()

	c.muPieces.Lock()
	c.pieces = nil
	c.muPieces.Unlock()

	utils.FreeOSMemGC()
	return nil
}

// closePieceFiles drops the cached handles of a cache's pieces. Called from
// Close before any of its files are unlinked, for the same reason
// DiskPiece.Release does it per piece: an open handle outlives the name.
func closePieceFiles(c *Cache) {
	if c.storage == nil || c.storage.files == nil {
		return
	}
	c.storage.files.ForgetDir(filepath.Join(settings.BTsets.TorrentsSavePath, c.hash.HexString()) + string(os.PathSeparator))
}

// releaseOnClose keeps the global counters honest when a cache goes away.
//
// RemoveCacheOnDrop=true: the files are deleted right below, so the bytes must
// leave filledSize.
//
// RemoveCacheOnDrop=false: the files stay on disk with no cache in memory, so
// the bytes are TRANSFERRED to orphanBytes. Without the transfer the limit
// silently stops applying to them — measured 115 GiB invisible to a 98 GiB
// limit (2026-10-01).
func (c *Cache) releaseOnClose() {
	if c.storage == nil {
		return
	}
	var left int64
	for _, p := range c.getPieces() {
		left += p.Size
	}
	if left <= 0 {
		return
	}
	c.storage.addFilledSize(-left)
	if diskMode() && !settings.BTsets.RemoveCacheOnDrop {
		c.storage.addOrphanBytes(left)
		// Register it as an eviction candidate right away. newest = now: a
		// directory that was in use a moment ago must sit out the 5-minute
		// guard before it can be deleted, exactly like its files' mtimes.
		c.storage.addOrphanDir(orphanDir{
			hash:   c.hash.HexString(),
			size:   left,
			newest: time.Now().Unix(),
		})
	}
}

func (c *Cache) removePiece(piece *Piece) {
	if !c.isClosed.Load() {
		// Release handles decrementing counters (both local and global)
		piece.Release()
	}
}

func (c *Cache) AdjustRA(readahead int64) {
	if c == nil {
		return
	}
	if settings.BTsets.CacheSize == 0 {
		c.capacity = readahead * 3
	}
	for _, r := range c.readersSnapshot() {
		r.SetReadahead(readahead)
	}
}

func (c *Cache) GetState() *state.CacheState {
	cState := new(state.CacheState)

	piecesState := make(map[int]state.ItemState, 0)
	var fill int64 = 0

	for _, p := range c.getPieces() {
		if p.Size > 0 {
			fill += p.Size
			// R1: torrent is nil during reconnect/teardown — never panic here.
			prio := 0
			if c.torrent != nil {
				prio = int(c.torrent.PieceState(p.Id).Priority)
			}
			piecesState[p.Id] = state.ItemState{
				Id:        p.Id,
				Size:      p.Size,
				Length:    c.pieceLength,
				Completed: p.Complete,
				Priority:  prio,
			}
		}
	}

	readersState := make([]*state.ReaderState, 0)

	for _, r := range c.readersSnapshot() {
		rng := r.getPiecesRange()
		pc := r.getReaderPiece()
		readersState = append(readersState, &state.ReaderState{
			Start:  rng.Start,
			End:    rng.End,
			Reader: pc,
		})
	}

	// The accumulation above is NOT `filledSize`. They answer different
	// questions and are only equal with UseDisk:
	//
	//   filledSize — what the cache is charged against `capacity`. MemPiece adds
	//                the whole allocated buffer (mempiece.go), DiskPiece the
	//                file size (diskpiece.go).
	//   sum(p.Size) — bytes of data actually written into those pieces.
	//
	// Replacing this loop's `fill` with `filledSize.Load()` over-reports by a
	// partially-written buffer in RAM mode (measured: 1024 vs 100), and
	// under-counts nothing, but "how much is preloaded" must mean data, not
	// allocation. See TestFilledIsNotTheFilledSize for the guard.
	c.filled = fill
	cState.Capacity = c.capacity
	cState.PiecesLength = c.pieceLength
	cState.PiecesCount = c.pieceCount
	cState.Hash = c.hash.HexString()
	cState.Filled = fill
	cState.Pieces = piecesState
	cState.Readers = readersState
	return cState
}

func (c *Cache) getRemPieces() []*Piece {
	readers := c.readersSnapshot()

	// Collect read ranges from active readers
	ranges := make([]Range, 0)
	for _, r := range readers {
		r.checkReader()
		if r.isUse {
			ranges = append(ranges, r.getPiecesRange())
		}
	}
	ranges = mergeRange(ranges)

	piecesRemove := make([]*Piece, 0)
	fill := int64(0)

	// Determine which chunks can be deleted
	for id, p := range c.getPieces() {
		// Sample under the piece lock: this runs in priorityWatchdog while a
		// download may be writing the same piece (-race, 2026-10-06).
		size, _, accessed := p.state()
		if size > 0 {
			fill += size
		}
		if len(ranges) > 0 {
			if !inRanges(ranges, id) {
				if size > 0 && !c.isIdInFileBE(ranges, id) {
					piecesRemove = append(piecesRemove, p)
				}
			}
		} else {
			// When preloading, clear everything except the beginning and end of the file
			if size > 0 && !c.isIdInFileBE(ranges, id) {
				piecesRemove = append(piecesRemove, p)
			}
		}
		_ = accessed
	}

	c.clearPriority()
	c.setLoadPriority(ranges)

	// Sort by last access time (oldest first)
	sort.Slice(piecesRemove, func(i, j int) bool {
		return piecesRemove[i].Accessed < piecesRemove[j].Accessed
	})

	c.filled = fill
	return piecesRemove
}

// priorityBudget is how many pieces each reader may mark for loading.
//
// It divides the connection limit between active readers, but never below one.
// With more viewers than the limit allows, plain division goes to zero or
// negative, the assignment loop never runs, and no piece gets a priority at
// all - a silent stall with peers connected. One piece keeps every viewer
// moving, however slowly.
//
// Split out from setLoadPriority so the arithmetic is testable without a
// cache, the same reason readAheadBytes is separate in the torr package.
func priorityBudget(connectionsLimit, readers int) int {
	if readers <= 0 {
		return connectionsLimit
	}
	budget := connectionsLimit / readers
	if budget < 1 {
		return 1
	}
	return budget
}

// backgroundFetchEnd extends the background fetch past the current file's
// end by two episodes ahead, so the next ones are already there when the
// viewer moves on. Two file lengths, not a fixed megabyte count: episodes
// vary in size, and a fixed number either starves a large episode or wastes
// on a small extra. Files lie back to back in piece space, so the pieces
// right past the end are the coming episodes' start, and the window simply
// continues through them.
//
// Clamped to the torrent end and floored so a zero-length file still moves
// one piece. Split out so the bound is testable without a cache.
func backgroundFetchEnd(fileEndPiece, pieceCount int, pieceLength, fileLength int64, nextFilesEndPiece int) int {
	// Two files ahead by index when the list resolves them, not two lengths:
	// a 10 GB episode followed by 2 GB ones must prefetch 4 GB, not 20 GB.
	// Clamp the file end first: a stale end past the torrent must not push
	// the fetch out as well.
	if fileEndPiece > pieceCount {
		fileEndPiece = pieceCount
	}
	if fileEndPiece < 0 {
		fileEndPiece = 0
	}
	var fetchEnd int
	if nextFilesEndPiece > fileEndPiece {
		fetchEnd = nextFilesEndPiece
	} else {
		// No file list: two lengths as the heuristic fallback.
		fetchEnd = fileEndPiece + int((2*fileLength)/pieceLength)
		if fetchEnd <= fileEndPiece {
			fetchEnd = fileEndPiece + 1
		}
	}
	if fetchEnd > pieceCount {
		fetchEnd = pieceCount
	}
	return fetchEnd
}

// fileSpan is a torrent file reduced to what the prefetch bound needs. Kept
// separate from torrent.File because its offset/length are unexported, so a
// function taking []*torrent.File cannot be built in a test at all.
type fileSpan struct {
	offset int64
	length int64
}

// torrentFileSpans projects a torrent's file list onto fileSpan.
func torrentFileSpans(files []*torrent.File) []fileSpan {
	if len(files) == 0 {
		return nil
	}
	out := make([]fileSpan, len(files))
	for i, f := range files {
		if f != nil {
			out[i] = fileSpan{offset: f.Offset(), length: f.Length()}
		}
	}
	return out
}

// nextTwoFilesEndPiece resolves the end of the second file after the
// reader's own, in pieces. Returns 0 when the list is unavailable or the
// reader's file is not found in it, and the caller falls back to the length
// heuristic.
//
// The current file is matched by offset+length rather than by identity:
// callers reach it through a reader, so the pointer is not guaranteed to be
// the same *torrent.File the list holds.
func nextTwoFilesEndPiece(spans []fileSpan, curOffset, curLength, pieceLength int64) int {
	if len(spans) == 0 || pieceLength <= 0 {
		return 0
	}
	idx := -1
	for i, s := range spans {
		if s.offset == curOffset && s.length == curLength {
			idx = i
			break
		}
	}
	if idx < 0 {
		return 0
	}
	last := idx + 2
	if last >= len(spans) {
		last = len(spans) - 1
	}
	s := spans[last]
	return int((s.offset + s.length) / pieceLength)
}

func (c *Cache) setLoadPriority(ranges []Range) {
	readers := c.readersSnapshot()
	pieces := c.getPieces()
	// R2: torrent/pieces may be gone during teardown — never panic here.
	if len(readers) == 0 || pieces == nil || c.torrent == nil {
		return
	}
	c.muPrio.Lock()
	defer c.muPrio.Unlock()
	for _, r := range readers {
		if !r.isUse {
			continue
		}
		if c.isIdInFileBE(ranges, r.getReaderPiece()) {
			continue
		}
		readerPos := r.getReaderPiece()
		readerRAHPos := r.getReaderRAHPiece()
		end := r.getPiecesRange().End
		// R2: clamp to real piece count — End at file edge can equal
		// pieceCount, and pieces[i] would be a nil deref.
		if end > c.pieceCount {
			end = c.pieceCount
		}
		count := priorityBudget(settings.BTsets.ConnectionsLimit, len(readers))
		limit := 0
		for i := readerPos; i < end && limit < count; i++ {
			pc, ok := pieces[i]
			if !ok || pc == nil {
				continue
			}
			if !pc.Complete {
				if i == readerPos {
					c.torrent.Piece(i).SetPriority(torrent.PiecePriorityNow)
				} else if i == readerPos+1 {
					c.torrent.Piece(i).SetPriority(torrent.PiecePriorityNext)
				} else if i > readerPos && i <= readerRAHPos {
					c.torrent.Piece(i).SetPriority(torrent.PiecePriorityReadahead)
				} else if i > readerRAHPos && i <= readerRAHPos+5 && c.torrent.PieceState(i).Priority != torrent.PiecePriorityHigh {
					c.torrent.Piece(i).SetPriority(torrent.PiecePriorityHigh)
				} else if i > readerRAHPos+5 && c.torrent.PieceState(i).Priority != torrent.PiecePriorityNormal {
					c.torrent.Piece(i).SetPriority(torrent.PiecePriorityNormal)
				}
				limit++
			}
		}
		// Background fetch: past the window, at Normal priority, so the disk
		// keeps filling while playing instead of stopping at the window edge.
		// Bounded by free cache space divided between readers (never crowds
		// out anyone's window) and by the same budget (never crowds out other
		// viewers). A paused viewer is just a viewer whose window stopped
		// moving, so this covers pause too with no special pause path.
		//
		// Reaches two episodes past this reader's file end, so the next ones
		// are already there. Bounded by the same budget and free space as
		// the rest of the background: prefetching never crowds out the
		// window or other viewers.
		if settings.BTsets.BackgroundFetch {
			freePieces := int((c.capacity - c.filledSize.Load()) / c.pieceLength / int64(len(readers)))
			if freePieces < 0 {
				freePieces = 0
			}
			fileEndPiece := int((r.file.Offset() + r.file.Length()) / c.pieceLength)
			if fileEndPiece > c.pieceCount {
				fileEndPiece = c.pieceCount
			}
			fetchEnd := backgroundFetchEnd(fileEndPiece, c.pieceCount, c.pieceLength, r.file.Length(),
				nextTwoFilesEndPiece(torrentFileSpans(c.torrent.Files()),
					r.file.Offset(), r.file.Length(), c.pieceLength))
			for i := end; i < fetchEnd && limit < count && freePieces > 0; i++ {
				pc, ok := pieces[i]
				if !ok || pc == nil {
					continue
				}
				if !pc.Complete {
					if c.torrent.PieceState(i).Priority != torrent.PiecePriorityNormal {
						c.torrent.Piece(i).SetPriority(torrent.PiecePriorityNormal)
					}
					limit++
					freePieces--
				}
			}
		}
	}
}

func (c *Cache) isIdInFileBE(ranges []Range, id int) bool {
	// keep 8/16 MB
	FileRangeNotDelete := int64(c.pieceLength)
	if FileRangeNotDelete < 8<<20 {
		FileRangeNotDelete = 8 << 20
	}

	for _, rng := range ranges {
		ss := int(rng.File.Offset() / c.pieceLength)
		se := int((rng.File.Offset() + FileRangeNotDelete) / c.pieceLength)

		es := int((rng.File.Offset() + rng.File.Length() - FileRangeNotDelete) / c.pieceLength)
		ee := int((rng.File.Offset() + rng.File.Length()) / c.pieceLength)

		if id >= ss && id < se || id > es && id <= ee {
			return true
		}
	}
	return false
}

//////////////////
// Reader section
////////

func (c *Cache) NewReader(file *torrent.File) *Reader {
	return newReader(file, c)
}

func (c *Cache) GetUseReaders() int {
	if c == nil {
		return 0
	}
	c.muReaders.RLock()
	defer c.muReaders.RUnlock()
	readers := 0
	for reader := range c.readers {
		if reader.isUse {
			readers++
		}
	}
	return readers
}

func (c *Cache) Readers() int {
	if c == nil {
		return 0
	}
	c.muReaders.RLock()
	defer c.muReaders.RUnlock()
	return len(c.readers)
}

// MaxReaderIdleSec is how long the longest-idle active reader has gone without
// a read, in seconds. 0 means someone read just now.
//
// It drives the pause preload: a viewer that stopped reading is a viewer that
// pressed pause, and the readahead window should grow while it waits instead of
// stopping at the configured size (measured: 90 MB, then nothing at all until
// playback drained it again).
func (c *Cache) MaxReaderIdleSec() int64 {
	if c == nil {
		return 0
	}
	now := time.Now().Unix()
	var worst int64
	for _, r := range c.readersSnapshot() {
		if !r.isUse {
			continue
		}
		if idle := now - r.lastAccess; idle > worst {
			worst = idle
		}
	}
	return worst
}

func (c *Cache) CloseReader(r *Reader) {
	r.cache.muReaders.Lock()
	delete(r.cache.readers, r)
	r.cache.muReaders.Unlock()
	// Reader.Close touches anacrolix internals, keep it outside muReaders.
	// R3: r.Close() already spawns getRemPieces() (which runs clearPriority
	// + setLoadPriority) — a second explicit clearPriority() here was a
	// redundant full client-lock sweep per reader close. Removed.
	r.Close()
}

func (c *Cache) clearPriority() {
	if c.torrent == nil {
		return
	}
	// This used to sleep for a second before clearing priorities. A reader
	// created during that window could have its PiecePriorityNow/Next/Readahead
	// reset to None right after setLoadPriority had assigned them, starving the
	// player. A mutex provides the same ordering without the race window.
	c.muPrio.Lock()
	defer c.muPrio.Unlock()
	ranges := make([]Range, 0)
	for _, r := range c.readersSnapshot() {
		r.checkReader()
		if r.isUse {
			ranges = append(ranges, r.getPiecesRange())
		}
	}
	ranges = mergeRange(ranges)

	for id := range c.getPieces() {
		if len(ranges) > 0 {
			if !inRanges(ranges, id) {
				if c.torrent.PieceState(id).Priority != torrent.PiecePriorityNone {
					c.torrent.Piece(id).SetPriority(torrent.PiecePriorityNone)
				}
			}
		} else {
			if c.torrent.PieceState(id).Priority != torrent.PiecePriorityNone {
				c.torrent.Piece(id).SetPriority(torrent.PiecePriorityNone)
			}
		}
	}
}

func (c *Cache) GetCapacity() int64 {
	if c == nil {
		return 0
	}
	return c.capacity
}

// WatchedFileEndBytes is the furthest byte any active viewer can reach: the
// end of the latest file anyone is watching. A series torrent holds many
// files, but only the watched ones need data; everything past this point
// belongs to episodes nobody opened.
//
// The read window already respects file ends (getOffsetRange clamps there),
// but the pause-unbounded path and the background fetch were bounded by the
// whole torrent instead - so pausing episode 1 queued all 79 GB of the
// season. This is the bound they should have used.
func (c *Cache) WatchedFileEndBytes() int64 {
	if c == nil {
		return 0
	}
	var furthest int64
	for _, r := range c.readersSnapshot() {
		if !r.isUse || r.file == nil {
			continue
		}
		if end := r.file.Offset() + r.file.Length(); end > furthest {
			furthest = end
		}
	}
	return furthest
}

// StagedBytes is how much downloaded data is held in incomplete pieces:
// chunks that arrived from the network but whose piece has not finished yet,
// so the bytes sit in RAM-backed buffers rather than as complete, evictable
// units on disk.
//
// The pause fetch uses it as backpressure: while paused it keeps requesting
// ahead, but when the staging grows past the configured cap it stops asking
// for more until pieces complete and drain to disk. Without that, a fast
// swarm on pause would pile unverified data into memory faster than hashing
// and flushing can retire it. At 10 MB/s a 256 MB cap is ~25 seconds of
// buffer - plenty to smooth bursts, small enough to never threaten the box.
func (c *Cache) StagedBytes() int64 {
	if c == nil {
		return 0
	}
	var staged int64
	for _, p := range c.getPieces() {
		if p == nil || p.Complete {
			continue
		}
		staged += p.Size
	}
	return staged
}
