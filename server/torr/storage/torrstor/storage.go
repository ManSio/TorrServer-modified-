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

	caches     map[metainfo.Hash]*Cache
	capacity   int64
	filledSize atomic.Int64 // Bytes held by caches currently in memory
	// orphanBytes is cache data that lives on disk but belongs to no cache in
	// s.caches (the torrent was dropped from the client — expired() does that
	// automatically TorrentDisconnectTimeout seconds after playback stops).
	//
	// It exists because the cache limit is a promise about DISK, and disk
	// outlives memory. Counting only in-memory caches made 115 GiB of live
	// bytes invisible to the limit (measured 2026-10-01: counter 8.6 GiB vs
	// 123.8 GiB on disk) — the LRU never even started. filledSize is
	// *transferred* to orphanBytes on Close, so the sum is preserved.
	orphanBytes atomic.Int64
	mu          sync.Mutex
	// orphanDirs is the last disk scan of TorrentsSavePath: size + newest
	// mtime per hash dir that has no cache in s.caches.
	orphanDirs      []orphanDir
	muOrphans       sync.Mutex    // guards orphanDirs (written by Close and by the scanner)
	orphanScannedAt atomic.Int64  // Unix time of the last disk reconciliation
	cleanupRunning  atomic.Bool   // Prevent concurrent cleanup (Thundering Herd guard)
	lastCleanupTime atomic.Int64  // Unix timestamp of last cleanup (debounce)
	lastStuckLog    atomic.Int64  // Rate limit for the "over limit, nothing removable" log
	cleanupCh       chan struct{} // Non-blocking signal: hot path only signals, worker cleans
	closed          atomic.Bool
}

type orphanDir struct {
	hash   string
	size   int64
	newest int64 // Unix time of the newest file in the directory
}

const (
	// How often the disk is walked to reconcile the in-memory counters with
	// reality. The counters are maintained incrementally and are exact in the
	// normal path; this is the self-heal for drift (failed os.Remove, manual
	// file drops, restore from backup).
	orphanScanInterval = 5 * time.Minute
	// While over budget, re-derive from disk at least this often.
	orphanActiveScanGap = 60 * time.Second
	// A directory must be untouched this long before it can be evicted.
	orphanMinAge = int64(300)
	// Bounded work per cycle so a 100 GiB overshoot converges without an
	// I/O storm; the worker re-runs on the next tick.
	orphanDirsPerCycle = 3
)

func NewStorage(capacity int64) *Storage {
	stor := new(Storage)
	stor.capacity = capacity
	stor.caches = make(map[metainfo.Hash]*Cache)
	stor.cleanupCh = make(chan struct{}, 1)
	go stor.cleanupLoop()
	// Cache data of torrents that are no longer loaded is invisible to the
	// in-memory counters; find it before the limit is consulted for the
	// first time, otherwise the first minutes after a restart are blind.
	go stor.reconcileOrphans(true)
	return stor
}

// requestCleanup is the ONLY thing the hot path (WriteAt/ReadAt) may call.
// It never blocks and never takes s.mu: just signals the background worker.
func (s *Storage) requestCleanup() {
	select {
	case s.cleanupCh <- struct{}{}:
	default:
	}
}

// cleanupLoop runs in background: periodic tick + on-demand signal.
// Heavy work (sort + remove) happens here, never under piece locks.
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

// reconcileOrphans walks TorrentsSavePath and rebuilds orphanBytes/orphanDirs
// from what is actually on disk. force=true runs it immediately (startup).
//
// This is the single source of truth for "how much cache data exists": the
// counters are fast but only as good as every add/subtract path, and history
// shows they are not (see orphanBytes doc). A directory whose hash has a live
// cache is skipped — filledSize already accounts for those bytes.
func (s *Storage) reconcileOrphans(force bool) {
	if !diskMode() || settings.BTsets.TorrentsSavePath == "" {
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

	// Pick the candidates under the lock, stat them outside it: walking 40k
	// piece files must never block OpenTorrent/CloseHash.
	candidates := make([]string, 0, len(entries))
	s.mu.Lock()
	for _, e := range entries {
		name := e.Name()
		if !e.IsDir() || len(name) != 40 {
			continue
		}
		// NB: metainfo.NewHashFromHex panics on non-hex input; a stray
		// directory in the cache path must never kill the cleanup goroutine.
		var hash metainfo.Hash
		if err := hash.FromHexString(name); err != nil {
			continue
		}
		if ch, ok := s.caches[hash]; ok && !ch.isClosed.Load() {
			continue // accounted by filledSize
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

	if drift := prev - total; drift > 1<<30 || drift < -(1<<30) {
		log.TLogln("[CACHE] orphan drift:", drift/1024/1024, "MB — counter was off, disk wins")
	}
	log.TLogln("[CACHE] scan: dirs", len(dirs), "orphan", total/1024/1024, "MB in",
		time.Since(start).Milliseconds(), "ms")
}

// Stats exposes the budget numbers for the /stat page and diagnostics.
func (s *Storage) Stats() (total, capacity, open, orphan int64) {
	return s.totalOnDisk(), s.capacity, s.filledSize.Load(), s.orphanBytes.Load()
}

// diskMode tolerates settings not being loaded yet (early startup, tests):
// the RAM path must still work without a BTSets.
func diskMode() bool {
	return settings.BTsets != nil && settings.BTsets.UseDisk
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
	if !diskMode() {
		// RAM mode: everything lives in caches, filledSize is the truth.
		if s.filledSize.Load() <= s.capacity {
			return
		}
		s.evictPiecesTo(s.totalOnDisk(), hysteresisTarget(s.capacity))
		return
	}
	hysteresis := settings.BTsets.CacheHysteresis
	if hysteresis <= 0 || hysteresis > 1.0 {
		hysteresis = 0.90
	}
	if s.totalOnDisk() <= s.capacity {
		return
	}
	s.triggerGlobalCleanup(hysteresisTarget(s.capacity))
}

// totalOnDisk is the budget number: in-memory cache bytes + cache bytes whose
// torrent is no longer in memory. Comparing only filledSize is what let the
// cache grow 26% past the configured limit.
func (s *Storage) totalOnDisk() int64 {
	return s.filledSize.Load() + s.orphanBytes.Load()
}

func hysteresisTarget(capacity int64) int64 {
	return int64(float64(capacity) * 0.9)
}

// addFilledSize updates the global cache counter
func (s *Storage) addFilledSize(delta int64) {
	s.filledSize.Add(delta)
}

// getFilledSize returns total cache size across all torrents
func (s *Storage) getFilledSize() int64 {
	return s.filledSize.Load()
}

// addOrphanBytes accounts cache data that lives on disk but has no cache in
// memory (see the orphanBytes field doc).
func (s *Storage) addOrphanBytes(delta int64) {
	s.orphanBytes.Add(delta)
}

// triggerGlobalCleanup enforces the cache limit against what is actually on
// disk, in two phases:
//
//	phase 1 — cache dirs whose torrent is no longer in memory, coldest first.
//	          These are pure cache data: no readers, no client state. Cheapest
//	          and safest thing to reclaim, and historically the whole leak
//	          (115 GiB of 124 GiB, measured 2026-10-01).
//	phase 2 — pieces of live caches, LRU, skipping anything a player is using
//	          (existing behaviour, kept because readers need piece granularity).
//
// Both phases are incremental: the worker re-runs every tick until the total
// is under target, so a large overshoot converges without an I/O storm.
func (s *Storage) triggerGlobalCleanup(targetSize int64) {
	// Guard 1: Prevent concurrent cleanup (Thundering Herd protection)
	if !s.cleanupRunning.CompareAndSwap(false, true) {
		return
	}
	defer s.cleanupRunning.Store(false)

	// Guard 2: Debounce (min 5 seconds between cleanups)
	now := time.Now().Unix()
	if now-s.lastCleanupTime.Load() < 5 {
		return // Skip silently (happens often during active write)
	}
	s.lastCleanupTime.Store(now)

	// Counters are only as good as every add/subtract path. While we are over
	// budget the truth matters more than the scan cost, so re-derive at least
	// once a minute (a full walk is milliseconds for a few thousand files);
	// under budget the periodic 5-minute scan is enough.
	if diskMode() && time.Now().Unix()-s.orphanScannedAt.Load() >= int64(orphanActiveScanGap/time.Second) {
		s.reconcileOrphans(true)
	}

	total := s.totalOnDisk()
	if total <= targetSize {
		// Under the target there is nothing to do and nothing to alarm about.
		// This path used to call logStatus(total, 0, 0), which printed
		// "OVER LIMIT ... 88755 MB > capacity 100240 MB" while the cache was
		// actually BELOW capacity — a false alarm every 5 minutes for as long
		// as the cache sat between 90% and 100% (seen 4x on 2026-10-01..03).
		s.logStatus(total, 0, 0, false)
		return
	}

	removedDirs, freedDirs := s.evictOrphanDirs(total, targetSize)
	total -= freedDirs
	if removedDirs > 0 {
		// Another actor may have removed these directories concurrently
		// (server.cleanCache does exactly that at startup), in which case we
		// measured 0 bytes and our counters are now stale. Force the next
		// cycle to re-derive them from disk instead of waiting 5 minutes.
		s.orphanScannedAt.Store(0)
	}
	if total > targetSize {
		removed, freed := s.evictPiecesTo(total, targetSize)
		removedDirs += removed
		freedDirs += freed
	}
	s.logStatus(s.totalOnDisk(), removedDirs, freedDirs, true)
}

// logStatus reports one cleanup cycle.
//
// overLimit says whether the cache is genuinely above the capacity. It is a
// separate argument on purpose: the "nothing removable" state is only alarming
// when we really are over, and conflating it with "removed == 0" produced
// false alarms while the cache sat between the target and the capacity.
func (s *Storage) logStatus(total int64, removed int, freed int64, overLimit bool) {
	if removed == 0 {
		if !overLimit || total <= s.capacity {
			// Normal: at or below target, or below capacity with nothing to do.
			return
		}
		// Over capacity but nothing removable (everything is protected by an
		// active reader or too fresh). This is the state that used to be
		// invisible — report it, but not on every 10s tick.
		now := time.Now().Unix()
		prev := s.lastStuckLog.Load()
		if now-prev < 300 || !s.lastStuckLog.CompareAndSwap(prev, now) {
			return
		}
		log.TLogln("[CACHE] OVER LIMIT and nothing removable:", total/1024/1024,
			"MB > capacity", s.capacity/1024/1024, "MB — every piece is protected or fresh")
		return
	}
	log.TLogln("[CACHE] total:", total/1024/1024, "MB (open:", s.filledSize.Load()/1024/1024,
		"MB, orphan:", s.orphanBytes.Load()/1024/1024, "MB) capacity:", s.capacity/1024/1024,
		"MB target:", hysteresisTarget(s.capacity)/1024/1024, "MB removed:", removed,
		"units,", freed/1024/1024, "MB")
}

// evictOrphanDirs deletes cache dirs of torrents that are not in memory,
// coldest directory first. Returns how many dirs and bytes were removed.
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
		// Guard: untouched for 5 minutes — never yank a directory that a
		// torrent may be re-opening right now.
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

// removeOrphanDir deletes one cache directory after re-checking under s.mu
// that no live cache claimed it (TOCTOU: the torrent may have been re-opened
// between the scan and now).
func (s *Storage) removeOrphanDir(hex string) (int64, bool) {
	var hash metainfo.Hash
	if err := hash.FromHexString(hex); err != nil {
		return 0, false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if ch, ok := s.caches[hash]; ok && !ch.isClosed.Load() {
		return 0, false // back in memory — filledSize owns those bytes now
	}
	size, _ := dirStats(filepath.Join(settings.BTsets.TorrentsSavePath, hex))
	if _, err := os.Stat(filepath.Join(settings.BTsets.TorrentsSavePath, hex)); os.IsNotExist(err) {
		// Someone else removed it (cleanCache at startup, an external
		// reaper). Forget it instead of counting bytes that are not there.
		s.dropOrphanDir(hex)
		return 0, false
	}
	if err := os.RemoveAll(filepath.Join(settings.BTsets.TorrentsSavePath, hex)); err != nil {
		log.TLogln("[CACHE] failed to remove orphan dir", hex, err)
		return 0, false
	}
	s.orphanBytes.Add(-size)
	s.dropOrphanDir(hex)
	return size, true
}

// addOrphanDir registers a cache dir that just lost its cache, so it can be
// evicted without waiting for the next disk scan (which runs every 5 min).
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

// evictPiecesTo evicts pieces of live caches until the total is under target.
func (s *Storage) evictPiecesTo(total, target int64) (int, int64) {
	// Collect all pieces from all torrents, plus per-cache reader ranges.
	// Pieces inside an active reader's range (or the protected file
	// head/tail around it) are NEVER eviction candidates, no matter how
	// old: pausing or slow buffering must not wipe the episode being
	// watched (observed live: E03 at 0/605 completed after 60s-idle
	// eviction while still open in player).
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

	// NOTE: do NOT write the recomputed total back into filledSize. Pieces are
	// being written right now, and a Store() from the cleanup worker clobbers
	// those increments — the counter silently drifts low and the limit stops
	// binding (found by TestLimitEvictsOpenCache: 16 MB written, 9 MB counted).
	// The single writer for filledSize is the piece itself (DiskPiece/
	// MemPiece Write/Release); the disk reconciler is the source of truth.
	if openFilled+s.orphanBytes.Load() <= target {
		return 0, 0 // No cleanup needed (silent)
	}

	// Sort: complete pieces first (oldest first), still-downloading
	// pieces last. Evicting a partial piece deletes bytes the client
	// still counts as received: a later reader gets zeros, the hash
	// check fails and a peer is dropped (upstream PR #875 port).
	sort.Slice(allPieces, func(i, j int) bool {
		ci, cj := allPieces[i].piece.Complete, allPieces[j].piece.Complete
		if ci != cj {
			return ci && !cj
		}
		return allPieces[i].accessed < allPieces[j].accessed
	})

	// INCREMENTAL CLEANUP: bounded per cycle so a large overshoot converges
	// without an I/O storm; the worker re-runs on the next tick.
	//
	// The old bound was a flat 200 pieces per cycle. Measured on the live box
	// (2026-10-01, /opt, 4 MiB pieces): unlink costs 0.23-0.33 ms per file, a
	// full rmtree of 4000 files takes 0.92 s, and a full accounting scan of
	// 43k files takes 1.5 s. So the bound was ~100x too tight: shedding a 40 GiB
	// overshoot at 200 pieces/10s took ~9 minutes and lost the race against any
	// real download speed. Bound by BYTES now (what the limit is about), with a
	// piece cap only to keep the sort/lock work bounded.
	const maxRemoveBytesPerCycle = 2 << 30
	const maxRemovePiecesPerCycle = 2000
	nowUnix := time.Now().Unix()
	removed := 0
	var removedBytes int64

	for _, entry := range allPieces {
		// Stop after batch (next worker run continues if still over target)
		if removed >= maxRemovePiecesPerCycle || removedBytes >= maxRemoveBytesPerCycle {
			break
		}

		// Guard 1: pieces of files currently open in a player are
		// untouchable (reader range + protected head/tail).
		if ranges, ok := protected[entry.cache]; ok {
			if inRanges(ranges, entry.id) || entry.cache.isIdInFileBE(ranges, entry.id) {
				continue
			}
		}

		// Guard 2: don't remove pieces touched in the last 5 minutes.
		// 60s was proven too aggressive live: a paused episode was
		// wiped while still open, forcing re-download from thin swarm.
		if nowUnix-entry.accessed < 300 {
			continue
		}

		// Capture size BEFORE removal: removePiece zeroes it and Release()
		// decrements filledSize.
		sz := entry.piece.Size
		// Remove piece (thread-safe within cache)
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

func (s *Storage) OpenTorrent(info *metainfo.Info, infoHash metainfo.Hash) (ts.TorrentImpl, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if ch, ok := s.caches[infoHash]; ok {
		return ch, nil
	}
	ch := NewCache(s.capacity, s)
	ch.Init(info, infoHash)
	s.caches[infoHash] = ch
	return ch, nil
}

func (s *Storage) CloseHash(hash metainfo.Hash) {
	s.mu.Lock()
	ch, ok := s.caches[hash]
	if ok {
		delete(s.caches, hash)
	}
	s.mu.Unlock()
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
