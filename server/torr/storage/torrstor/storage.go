package torrstor

import (
	"os"
	"path/filepath"
	"sort"
	"sync"
	"sync/atomic"
	"time"

	"server/log"
	"server/settings"
	"server/torr/storage"

	"github.com/anacrolix/torrent/metainfo"
	ts "github.com/anacrolix/torrent/storage"
)

type Storage struct {
	storage.Storage

	caches   map[metainfo.Hash]*Cache
	capacity int64
	mu       sync.Mutex

	// --- global cache budget -------------------------------------------------
	// Active only when BTsets.OneCacheForAll is set. Without that flag this
	// type behaves exactly as before: no counters, no worker, no eviction.
	//
	// The budget is a promise about DISK, and disk outlives memory: a torrent
	// is closed by torrent.go ("Close torrent if no info in 1 minute +
	// TorrentDisconnectTimeout") long after playback stopped, and with
	// RemoveCacheOnDrop=false its files stay in the cache directory. Counting
	// only the caches that happen to be loaded makes those bytes invisible, so
	// on a real server the cache grew to 129.5 GiB against a 97.9 GiB limit and
	// eviction never started (measured 2026-10-01, 22 cache dirs, 115 GiB of it
	// belonging to closed caches).
	filledSize atomic.Int64 // bytes held by caches currently in memory
	// orphanBytes is cache data on disk whose torrent is no longer in memory.
	// Cache.Close() *transfers* its byte total here, so the sum below never
	// changes when a torrent is dropped.
	orphanBytes atomic.Int64
	orphanDirs  []orphanDir
	muOrphans   sync.Mutex // guards orphanDirs (written by Close and by the scanner)

	orphanScannedAt atomic.Int64 // unix time of the last disk reconciliation
	cleanupRunning  atomic.Bool  // prevent concurrent cleanup (thundering herd)
	lastCleanupTime atomic.Int64 // unix time of last cleanup (debounce)
	lastStuckLog    atomic.Int64 // rate limit for the "over limit, nothing removable" log

	cleanupCh chan struct{} // non-blocking signal: the hot path only signals
	closed    atomic.Bool
}

type orphanDir struct {
	hash   string
	size   int64
	newest int64 // unix time of the newest file in the directory
}

const (
	// cleanDownTo is the fraction of the limit a cleanup aims for. Cleaning to
	// exactly the limit would re-trigger on the next write.
	cleanDownTo = 0.90
	// How often the cache directory is walked to reconcile the counters with
	// reality. The counters are maintained incrementally and are exact in the
	// normal path; this is the self-heal for drift. Measured on the live host:
	// 43 164 files = 1.5 s, so this is cheap; while over budget we re-derive
	// more often, because that is exactly when the numbers must be right.
	orphanScanInterval   = 5 * time.Minute
	orphanActiveScanGap  = 60 * time.Second
	orphanMinAge         = int64(300) // a directory must be untouched this long
	orphanDirsPerCycle   = 3         // bounded work per cycle
	maxRemoveBytesCycle  = 2 << 30   // unlink measured at 0.23-0.33 ms/file
	maxRemovePiecesCycle = 2000
	stuckLogInterval     = int64(300)
)

func NewStorage(capacity int64) *Storage {
	stor := new(Storage)
	stor.capacity = capacity
	stor.caches = make(map[metainfo.Hash]*Cache)
	if stor.budgetEnabled() {
		stor.cleanupCh = make(chan struct{}, 1)
		go stor.cleanupLoop()
		// Bytes of torrents that are not loaded are invisible to the counters;
		// find them before the limit is consulted for the first time.
		go stor.reconcileOrphans(true)
	}
	return stor
}

// budgetEnabled reports whether the global disk budget applies. It is read on
// every cleanup cycle so toggling the setting does not need a restart.
func (s *Storage) budgetEnabled() bool {
	return settings.BTsets != nil && settings.BTsets.OneCacheForAll
}

// requestCleanup is the ONLY thing the hot path may call: it never blocks and
// never takes s.mu.
func (s *Storage) requestCleanup() {
	if s.cleanupCh == nil {
		return
	}
	select {
	case s.cleanupCh <- struct{}{}:
	default:
	}
}

func (s *Storage) cleanupLoop() {
	ticker := time.NewTicker(10 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-s.cleanupCh:
			s.runCleanupIfNeeded()
		case <-ticker.C:
			if s.closed.Load() {
				return
			}
			s.reconcileOrphans(false)
			s.runCleanupIfNeeded()
		}
		if s.closed.Load() {
			return
		}
	}
}

// reconcileOrphans rebuilds orphanBytes/orphanDirs from what is actually on
// disk. force=true runs it immediately regardless of the interval.
func (s *Storage) reconcileOrphans(force bool) {
	if !s.budgetEnabled() || !diskMode() || settings.BTsets.TorrentsSavePath == "" {
		return
	}
	now := time.Now().Unix()
	if !force && now-s.orphanScannedAt.Load() < int64(orphanScanInterval/time.Second) {
		return
	}
	s.orphanScannedAt.Store(now)

	start := time.Now()
	root := settings.BTsets.TorrentsSavePath
	entries, err := os.ReadDir(root)
	if err != nil {
		return
	}

	// Pick the candidates under the lock, stat them outside it: walking tens of
	// thousands of piece files must never block OpenTorrent/CloseHash.
	candidates := make([]string, 0, len(entries))
	s.mu.Lock()
	for _, e := range entries {
		name := e.Name()
		if !e.IsDir() || len(name) != 40 {
			continue
		}
		// metainfo.NewHashFromHex panics on non-hex input; a stray directory in
		// the cache path must never kill the cleanup goroutine.
		var hash metainfo.Hash
		if err := hash.FromHexString(name); err != nil {
			continue
		}
		if ch, ok := s.caches[hash]; ok && !ch.isClosed.Load() {
			continue // already accounted by filledSize
		}
		candidates = append(candidates, name)
	}
	s.mu.Unlock()

	dirs := make([]orphanDir, 0, len(candidates))
	var total int64
	for _, name := range candidates {
		size, newest := dirStats(filepath.Join(root, name))
		if size == 0 {
			continue
		}
		dirs = append(dirs, orphanDir{hash: name, size: size, newest: newest})
		total += size
	}

	prev := s.orphanBytes.Swap(total)
	s.muOrphans.Lock()
	s.orphanDirs = dirs
	s.muOrphans.Unlock()

	if drift := prev - total; drift > 1<<30 || drift < -(1 << 30) {
		log.TLogln("[CACHE] orphan drift:", drift/1024/1024, "MB — counter was off, disk wins")
	}
	log.TLogln("[CACHE] scan: dirs", len(dirs), "orphan", total/1024/1024, "MB in",
		time.Since(start).Milliseconds(), "ms")
}

func dirStats(path string) (total int64, newest int64) {
	entries, err := os.ReadDir(path)
	if err != nil {
		return 0, 0
	}
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		info, err := e.Info()
		if err != nil {
			continue
		}
		total += info.Size()
		if m := info.ModTime().Unix(); m > newest {
			newest = m
		}
	}
	return total, newest
}

func (s *Storage) runCleanupIfNeeded() {
	if !s.budgetEnabled() {
		return
	}
	if !diskMode() {
		// RAM mode: everything lives in caches, filledSize is the whole truth.
		if s.filledSize.Load() <= s.capacity {
			return
		}
		s.triggerGlobalCleanup(cleanTarget(s.capacity))
		return
	}
	if s.totalOnDisk() <= s.capacity {
		return
	}
	s.triggerGlobalCleanup(cleanTarget(s.capacity))
}

// totalOnDisk is the budget number: in-memory cache bytes plus cache bytes whose
// torrent is no longer in memory.
func (s *Storage) totalOnDisk() int64 {
	return s.filledSize.Load() + s.orphanBytes.Load()
}

func cleanTarget(capacity int64) int64 {
	return int64(float64(capacity) * cleanDownTo)
}

// Stats exposes the budget numbers for diagnostics and the status page.
func (s *Storage) Stats() (total, capacity, open, orphan int64) {
	return s.totalOnDisk(), s.capacity, s.filledSize.Load(), s.orphanBytes.Load()
}

// addFilledSize updates the in-memory cache counter. The piece is the only
// writer of its own transition — nothing recomputes this from scratch, because
// a Store() from a background worker clobbers concurrent writes.
func (s *Storage) addFilledSize(delta int64) {
	s.filledSize.Add(delta)
}

func (s *Storage) addOrphanBytes(delta int64) {
	s.orphanBytes.Add(delta)
}

// getFilledSize is the in-memory total, used by the write hot path as a cheap
// "are we over budget yet" test.
func (s *Storage) getFilledSize() int64 {
	return s.filledSize.Load()
}

func (s *Storage) addOrphanDir(d orphanDir) {
	s.muOrphans.Lock()
	defer s.muOrphans.Unlock()
	for i, existing := range s.orphanDirs {
		if existing.hash == d.hash {
			s.orphanDirs[i] = d
			return
		}
	}
	s.orphanDirs = append(s.orphanDirs, d)
}

func (s *Storage) dropOrphanDir(hex string) {
	s.muOrphans.Lock()
	defer s.muOrphans.Unlock()
	out := s.orphanDirs[:0]
	for _, d := range s.orphanDirs {
		if d.hash != hex {
			out = append(out, d)
		}
	}
	s.orphanDirs = out
}

// triggerGlobalCleanup enforces the limit against what is on disk, in two
// phases:
//
//	phase 1 — cache dirs whose torrent is no longer in memory, coldest first.
//	          Pure cache data: no readers, no client state. Cheapest and safest
//	          thing to reclaim, and historically the whole leak.
//	phase 2 — pieces of live caches, LRU, skipping anything a player is using.
//
// Both phases are incremental: the worker re-runs each tick until the total is
// under target, so a large overshoot converges without an I/O storm.
func (s *Storage) triggerGlobalCleanup(targetSize int64) {
	if !s.cleanupRunning.CompareAndSwap(false, true) {
		return
	}
	defer s.cleanupRunning.Store(false)

	now := time.Now().Unix()
	if now-s.lastCleanupTime.Load() < 5 {
		return
	}
	s.lastCleanupTime.Store(now)

	if diskMode() && now-s.orphanScannedAt.Load() >= int64(orphanActiveScanGap/time.Second) {
		s.reconcileOrphans(true)
	}

	total := s.totalOnDisk()
	if total <= targetSize {
		s.logStatus(total, 0, 0, false)
		return
	}

	removedDirs, freedDirs := s.evictOrphanDirs(total, targetSize)
	total -= freedDirs
	if removedDirs > 0 {
		// Another actor may have removed these directories concurrently, in
		// which case we measured 0 bytes and our counters are now stale.
		s.orphanScannedAt.Store(0)
	}
	if total > targetSize {
		removed, freed := s.evictPiecesTo(targetSize)
		removedDirs += removed
		freedDirs += freed
	}
	s.logStatus(s.totalOnDisk(), removedDirs, freedDirs, true)
}

// logStatus reports one cleanup cycle. overLimit is a separate argument on
// purpose: "nothing removable" is only alarming when we really are over, and
// conflating it with "removed == 0" produced false alarms while the cache sat
// between the target and the capacity.
func (s *Storage) logStatus(total int64, removed int, freed int64, overLimit bool) {
	if removed == 0 {
		if !overLimit || total <= s.capacity {
			return
		}
		now := time.Now().Unix()
		prev := s.lastStuckLog.Load()
		if now-prev < stuckLogInterval || !s.lastStuckLog.CompareAndSwap(prev, now) {
			return
		}
		log.TLogln("[CACHE] OVER LIMIT and nothing removable:", total/1024/1024,
			"MB > capacity", s.capacity/1024/1024, "MB — every piece is protected or fresh")
		return
	}
	log.TLogln("[CACHE] total:", total/1024/1024, "MB (open:", s.filledSize.Load()/1024/1024,
		"MB, orphan:", s.orphanBytes.Load()/1024/1024, "MB) capacity:", s.capacity/1024/1024,
		"MB target:", cleanTarget(s.capacity)/1024/1024, "MB removed:", removed,
		"units,", freed/1024/1024, "MB")
}

func (s *Storage) evictOrphanDirs(total, target int64) (int, int64) {
	s.muOrphans.Lock()
	dirs := make([]orphanDir, len(s.orphanDirs))
	copy(dirs, s.orphanDirs)
	s.muOrphans.Unlock()
	if len(dirs) == 0 {
		return 0, 0
	}
	sort.Slice(dirs, func(i, j int) bool { return dirs[i].newest < dirs[j].newest })

	nowUnix := time.Now().Unix()
	var removed int
	var freed int64

	for _, d := range dirs {
		if total <= target || removed >= orphanDirsPerCycle {
			break
		}
		// Never yank a directory a torrent may be re-opening right now.
		if nowUnix-d.newest < orphanMinAge {
			continue
		}
		freedHere, ok := s.removeOrphanDir(d.hash)
		if !ok {
			continue
		}
		removed++
		freed += freedHere
		total -= freedHere
		log.TLogln("[CACHE] evicted orphan cache dir", d.hash, freedHere/1024/1024, "MB")
	}
	return removed, freed
}

// removeOrphanDir deletes one cache directory after re-checking under s.mu that
// no live cache claimed it (the torrent may have been re-opened since the scan).
func (s *Storage) removeOrphanDir(hex string) (int64, bool) {
	var hash metainfo.Hash
	if err := hash.FromHexString(hex); err != nil {
		return 0, false
	}
	path := filepath.Join(settings.BTsets.TorrentsSavePath, hex)
	s.mu.Lock()
	defer s.mu.Unlock()
	if ch, ok := s.caches[hash]; ok && !ch.isClosed.Load() {
		return 0, false // back in memory — filledSize owns those bytes now
	}
	size, _ := dirStats(path)
	if _, err := os.Stat(path); os.IsNotExist(err) {
		s.dropOrphanDir(hex)
		return 0, false
	}
	if err := os.RemoveAll(path); err != nil {
		log.TLogln("[CACHE] failed to remove orphan dir", hex, err)
		return 0, false
	}
	s.orphanBytes.Add(-size)
	s.dropOrphanDir(hex)
	return size, true
}

// evictPiecesTo evicts pieces of live caches until the total is under target.
func (s *Storage) evictPiecesTo(target int64) (int, int64) {
	type pieceEntry struct {
		piece    *Piece
		cache    *Cache
		accessed int64
		id       int
	}

	s.mu.Lock()
	var allPieces []pieceEntry
	var openFilled int64
	protected := make(map[*Cache][]Range)

	for _, ch := range s.caches {
		if ch.isClosed.Load() {
			continue
		}
		var ranges []Range
		for _, r := range ch.readersSnapshot() {
			r.checkReader()
			if r.isUse {
				ranges = append(ranges, r.getPiecesRange())
			}
		}
		if len(ranges) > 0 {
			protected[ch] = mergeRange(ranges)
		}
		for id, p := range ch.getPieces() {
			if p.Size > 0 {
				openFilled += p.Size
				allPieces = append(allPieces, pieceEntry{
					piece:    p,
					cache:    ch,
					accessed: p.Accessed,
					id:       id,
				})
			}
		}
	}
	s.mu.Unlock()

	// Do NOT write the recomputed total back into filledSize: pieces are being
	// written right now and a Store() here would clobber those increments.
	if openFilled+s.orphanBytes.Load() <= target {
		return 0, 0
	}

	// Complete pieces first (oldest first), still-downloading pieces last:
	// evicting a partial piece deletes bytes the client still counts as
	// received, so a later reader gets zeros and the hash check drops a peer.
	sort.Slice(allPieces, func(i, j int) bool {
		ci, cj := allPieces[i].piece.Complete, allPieces[j].piece.Complete
		if ci != cj {
			return ci && !cj
		}
		return allPieces[i].accessed < allPieces[j].accessed
	})

	nowUnix := time.Now().Unix()
	removed := 0
	var removedBytes int64

	for _, entry := range allPieces {
		if removed >= maxRemovePiecesCycle || removedBytes >= maxRemoveBytesCycle {
			break
		}
		// Pieces of files currently open in a player are untouchable.
		if ranges, ok := protected[entry.cache]; ok {
			if inRanges(ranges, entry.id) || entry.cache.isIdInFileBE(ranges, entry.id) {
				continue
			}
		}
		// Anything touched in the last 5 minutes survives: a paused episode must
		// not be wiped (observed live before, 60 s was too aggressive).
		if nowUnix-entry.accessed < orphanMinAge {
			continue
		}
		sz := entry.piece.Size
		entry.cache.removePiece(entry.piece)
		openFilled -= sz
		removedBytes += sz
		removed++
		if s.totalOnDisk() <= target {
			break
		}
	}

	if removed > 0 {
		log.TLogln("[GLOBAL CLEANUP] Removed", removed, "pieces (incremental). Open filled:",
			openFilled/1024/1024, "MB total:", s.totalOnDisk()/1024/1024, "MB")
	}
	return removed, removedBytes
}

// diskMode tolerates settings not being loaded yet (early startup): the RAM
// path must work without a BTSets.
func diskMode() bool {
	return settings.BTsets != nil && settings.BTsets.UseDisk
}

func (s *Storage) OpenTorrent(info *metainfo.Info, infoHash metainfo.Hash) (ts.TorrentImpl, error) {
	// capFunc := func() (int64, bool) { //	NE
	// 	return s.capacity, true //	NE
	// } //	NE
	s.mu.Lock()
	defer s.mu.Unlock()
	if ch, ok := s.caches[infoHash]; ok {
		return ch, nil
	}
	ch := NewCache(s.capacity, s)
	ch.Init(info, infoHash)
	s.caches[infoHash] = ch
	return ch, nil //	OE
	// return ts.TorrentImpl{ //	NE
	// 	Piece:    ch.Piece,
	// 	Close:    ch.Close,
	// 	Capacity: &capFunc,
	// }, nil //	NE
}

func (s *Storage) CloseHash(hash metainfo.Hash) {
	s.mu.Lock()
	ch, ok := s.caches[hash]
	if ok {
		delete(s.caches, hash)
	}
	s.mu.Unlock()
	// Cache.Close must be called without s.mu held: it calls removeCache
	// and blocks on disk/anacrolix operations
	if ok {
		ch.Close()
	}
}

func (s *Storage) Close() error {
	s.closed.Store(true)
	s.mu.Lock()
	caches := make([]*Cache, 0, len(s.caches))
	for _, ch := range s.caches {
		caches = append(caches, ch)
	}
	s.caches = make(map[metainfo.Hash]*Cache)
	s.mu.Unlock()
	for _, ch := range caches {
		ch.Close()
	}
	return nil
}

func (s *Storage) removeCache(hash metainfo.Hash) {
	s.mu.Lock()
	delete(s.caches, hash)
	s.mu.Unlock()
}

func (s *Storage) GetCache(hash metainfo.Hash) *Cache {
	s.mu.Lock()
	defer s.mu.Unlock()
	if cache, ok := s.caches[hash]; ok {
		return cache
	}
	return nil
}