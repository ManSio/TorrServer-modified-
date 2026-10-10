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
	muOrphans       sync.Mutex   // guards orphanDirs (written by Close and by the scanner)
	orphanScannedAt atomic.Int64 // Unix time of the last disk reconciliation
	cleanupRunning  atomic.Bool  // Prevent concurrent cleanup (Thundering Herd guard)
	// doneCh is closed by Close to stop cleanupLoop immediately.
	doneCh chan struct{}
	// wg tracks every background worker this storage owns. Close waits for it,
	// so after Close returns nothing of this storage is running any more.
	//
	// That contract is what keeps the test suite race-free: a worker from a
	// finished test used to keep reading settings.BTsets (diskMode) while the
	// next test rewrote it, and the race detector reported it as a data race
	// inside whichever test happened to run. It was also wrong in production:
	// for up to a tick after Close the loop could still reconcile and delete
	// cache directories of a storage that was already shut down.
	wg sync.WaitGroup
	lastCleanupTime atomic.Int64 // Unix timestamp of last cleanup (debounce)
	// Diagnostics for the last eviction pass: how many bytes the viewers
	// protect and how many windows there were. Exposed so a stuck cache can be
	// recognised from /stat instead of guessed at from symptoms.
	lastProtectedBytes atomic.Int64
	lastViewers        atomic.Int64
	lastStuckLog       atomic.Int64  // Rate limit for the "over limit, nothing removable" log
	cleanupCh          chan struct{} // Non-blocking signal: hot path only signals, worker cleans
	closed             atomic.Bool
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
	stor.doneCh = make(chan struct{})
	stor.wg.Add(1)
	go stor.cleanupLoop()
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
	// Cache data of torrents that are no longer loaded is invisible to the
	// in-memory counters; find it before the limit is consulted for the first
	// time, otherwise the first minutes after a restart are blind. It runs here
	// rather than as its own goroutine so Close() can cancel it: as a separate
	// goroutine it outlived the storage and read settings.BTsets while the next
	// owner of that global was already writing it.
	s.reconcileOrphans(true)

	ticker := time.NewTicker(10 * time.Second)
	defer ticker.Stop()
	defer s.wg.Done()
	for {
		select {
		case <-s.doneCh:
			return
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

// pieceEntry is one eviction candidate captured under s.mu.
type pieceEntry struct {
	piece *Piece
	cache *Cache
	// size/complete/accessed are sampled under the pieces own lock while the
	// candidate list is built. Reading them live during the sort or during
	// eviction races a concurrent WriteAt — see Piece.state.
	size     int64
	complete bool
	accessed int64
	id       int
}

// viewerWindow is one reader's protected span plus what it wants and what it
// may never lose.
type viewerWindow struct {
	cache *Cache
	rng   Range
	// active is a viewer that is actually reading right now. A paused or
	// buffering one keeps its protection (it is not abandoned), but it is
	// served after the active ones when the budget is contended.
	active bool
	// want is the window the viewer asked for, in bytes.
	want int64
	// floor is what it may never lose: the piece being read plus one ahead.
	floor int64
}

// viewerAlloc is the outcome of one allocation, for logs and /stat.
type viewerAlloc struct {
	budget   int64
	reserved int64
	active   int
	idle     int
	capped   bool // the budget bound the allocation
	starved  int  // viewers clamped to their floor, budget too small for all
}

const (
	// freshPieceMinAge: куски, тронутые меньше 5 минут назад, не трогаем.
	// 60 s было слишком жёстко: стёрло эпизод на паузе (E03 @ 0/605).
	freshPieceMinAge = 300
	// freshPieceRelaxedAge: второй проход под давлением лимита. Активная
	// загрузка пишет куски с Accessed=сейчас, поэтому первый проход не может
	// ничего удалить, и кэш растёт выше лимита вместо честной сходимости.
	//
	// Ноль здесь безопасен именно для полностью скачанных кусков: читателя на
	// них нет (окно зрителя защищено отдельно, Guard 1), а удаление проходит
	// через Release → UpdateCompletion, то есть клиент перезапросит их, а не
	// отдаст читателю нули.
	freshPieceRelaxedAge = 0
	// viewerWindowFloorPieces: минимум кусков на зрителя, когда бюджет защиты
	// сжимает окно, — кусок под позицией читателя и один впереди.
	viewerWindowFloorPieces = 2
)

func viewerIdleGrace() int64 {
	if settings.BTsets != nil && settings.BTsets.ViewerIdleGraceSec >= settings.ViewerIdleGraceMin {
		return int64(settings.BTsets.ViewerIdleGraceSec)
	}
	return settings.ViewerIdleGraceDefault
}

// protectViewerRanges turns viewer windows into the per-cache protected set,
// with their total clamped to cap.
//
// Why the clamp is mandatory: eviction stops at the target, so a protected sum
// above the target means the cache can never get back under its limit. Before
// this, each window was sized from the GLOBAL capacity against the readers of
// ONE cache, so N viewers protected N × the limit (measured 2026-10-05: three
// viewers → 164% of the limit, stuck).
//
// Shrinking a window is safe: eviction of a piece the client counted as
// received goes through Piece.Release → UpdateCompletion, so the client stops
// treating it as available and re-requests it. The cost is re-download, never
// zeros under the reader.
func protectViewerRanges(windows []viewerWindow, cap int64) (map[*Cache][]Range, viewerAlloc) {
	report := viewerAlloc{budget: cap, active: 0, idle: 0}
	protected := make(map[*Cache][]Range)
	if len(windows) == 0 {
		return protected, report
	}
	for _, w := range windows {
		if w.active {
			report.active++
		} else {
			report.idle++
		}
	}

	granted := allocateViewerBudget(windows, cap)
	for i, w := range windows {
		report.reserved += granted[i]
		if granted[i] <= w.floor {
			report.starved++
		}
		pieces := int(granted[i] / w.cache.pieceLength)
		if pieces < viewerWindowFloorPieces {
			pieces = viewerWindowFloorPieces
		}
		rng := trimRange(w.rng, pieces)
		protected[w.cache] = append(protected[w.cache], rng)
	}
	report.capped = report.reserved >= cap
	for c, rs := range protected {
		protected[c] = mergeRange(rs)
	}
	return protected, report
}

// allocateViewerBudget splits budget bytes between viewer windows.
//
// Order of service encodes the policy:
//  1. everyone gets floor — a viewer keeps the piece it is on plus one ahead,
//     nobody can be starved completely;
//  2. the rest is shared between ACTIVE viewers by max-min fairness, so a
//     viewer that needs little does not reserve capacity it never uses and a
//     viewer that needs a lot is not given everything;
//  3. whatever is left goes to IDLE viewers (paused or buffering ones), who keep
//     their protection but yield to active playback first.
//
// The sum of grants never exceeds budget, which is the whole point: eviction
// must always be able to reach its target.
func allocateViewerBudget(windows []viewerWindow, budget int64) []int64 {
	granted := make([]int64, len(windows))

	// If the budget cannot even cover the floors, handing them out in order
	// starves whoever is last: the first viewers take everything and the rest
	// get 0, which means their current piece can be evicted and the stream
	// stalls. Split the whole budget evenly instead — everyone keeps a share,
	// and the shortfall is visible in the report instead of hidden.
	var sumFloors int64
	for _, w := range windows {
		sumFloors += w.floor
	}
	if sumFloors > budget {
		noFloors := make([]viewerWindow, len(windows))
		copy(noFloors, windows)
		all := make([]int, len(noFloors))
		for i := range all {
			all[i] = i
		}
		return waterFill(all, noFloors, granted, budget)
	}

	remaining := budget
	for i, w := range windows {
		g := w.floor
		if g > remaining {
			g = remaining
		}
		if g < 0 {
			g = 0
		}
		granted[i] = g
		remaining -= g
	}
	if remaining <= 0 {
		return granted
	}

	// Pass 1: active viewers, then idle ones.
	for _, activePass := range []bool{true, false} {
		if remaining <= 0 {
			break
		}
		pool := make([]int, 0, len(windows))
		for i, w := range windows {
			if w.active == activePass && w.want > granted[i] {
				pool = append(pool, i)
			}
		}
		share := waterFill(pool, windows, granted, remaining)
		for i, g := range share {
			granted[pool[i]] += g
			remaining -= g
		}
	}
	return granted
}

// waterFill hands budget out over pool by max-min fairness: every viewer in
// the pool gets the same marginal share until its want is reached, then the
// surplus is redistributed among the rest. Deterministic and starvation free.
//
// It is pure: granted is read as the starting point and never written, so the
// caller adds the result without double counting.
func waterFill(pool []int, windows []viewerWindow, granted []int64, budget int64) []int64 {
	out := make([]int64, len(pool))
	if budget <= 0 || len(pool) == 0 {
		return out
	}
	cur := make([]int64, len(pool))
	for k, i := range pool {
		cur[k] = granted[i]
	}
	left := func(k int) int64 {
		if l := windows[pool[k]].want - cur[k]; l > 0 {
			return l
		}
		return 0
	}

	remaining := budget
	open := make([]int, len(pool))
	for k := range open {
		open[k] = k
	}

	// Bounded by the number of viewers: each round either finishes one viewer
	// or hands every open viewer a share, so it cannot spin.
	for round := 0; len(open) > 0 && remaining > 0 && round <= len(pool)+1; round++ {
		share := remaining / int64(len(open))
		if share <= 0 {
			// Fewer bytes than viewers: hand them out one by one so nobody is
			// skipped entirely.
			for _, k := range open {
				if remaining <= 0 {
					break
				}
				give := left(k)
				if give > remaining {
					give = remaining
				}
				if give > 0 {
					out[k] += give
					cur[k] += give
					remaining -= give
				}
			}
			break
		}
		var next []int
		for _, k := range open {
			l := left(k)
			if l <= share {
				// This viewer is satisfied; the rest of its share goes back.
				out[k] += l
				cur[k] += l
				remaining -= l
				continue
			}
			out[k] += share
			cur[k] += share
			remaining -= share
			next = append(next, k)
		}
		open = next
	}
	return out
}

func windowBytes(rng Range, pieceLength int64) int64 {
	n := int64(rng.End - rng.Start + 1)
	if n < 0 {
		return 0
	}
	return n * pieceLength
}

// trimRange shortens r to at most keep pieces, keeping the tail (the piece the
// reader is on and what follows) and dropping the far side.
func trimRange(r Range, keep int) Range {
	if keep < viewerWindowFloorPieces {
		keep = viewerWindowFloorPieces
	}
	if r.End-r.Start+1 > keep {
		r.End = r.Start + keep - 1
	}
	if r.End < r.Start {
		r.End = r.Start
	}
	return r
}

// evictPiecesTo evicts pieces of live caches until the total is under target.
func (s *Storage) evictPiecesTo(total, target int64) (int, int64) {
	// Collect all pieces from all torrents, plus per-cache reader ranges.
	s.mu.Lock()
	var allPieces []pieceEntry
	var openFilled int64
	var windows []viewerWindow
	nowUnix := time.Now().Unix()
	grace := viewerIdleGrace()

	for _, ch := range s.caches {
		if ch.isClosed.Load() {
			continue
		}
		for _, r := range ch.readersSnapshot() {
			r.checkReader()
			// Protection no longer depends on isUse. checkReader parks a reader
			// after 60 s of silence whenever the cache has more than one reader,
			// and a paused/buffering TV is not an abandoned one: parking used to
			// drop the window and the data behind it (measured: pause 120 s →
			// 14/14 pieces of the paused window evicted). The window now survives
			// the park and expires only after the idle grace.
			if r.isUse || nowUnix-r.lastAccess <= grace {
				rng := r.getPiecesRange()
				want := windowBytes(rng, ch.pieceLength)
				floor := viewerWindowFloorPieces * ch.pieceLength
				if want < floor {
					want = floor
				}
				windows = append(windows, viewerWindow{
					cache:  ch,
					rng:    rng,
					active: r.isUse,
					want:   want,
					floor:  floor,
				})
			}
		}
		for id, p := range ch.getPieces() {
			size, complete, accessed := p.state()
			if size > 0 {
				openFilled += size
				allPieces = append(allPieces, pieceEntry{
					piece:    p,
					cache:    ch,
					size:     size,
					complete: complete,
					accessed: accessed,
					id:       id,
				})
			}
		}
	}
	s.mu.Unlock()

	// The sum of viewer windows is clamped to the eviction target. It must be:
	// eviction stops at target, so if the viewers protect more than target the
	// cache can never come back under its limit. Measured 2026-10-05 — three
	// viewers on three films protected 1848 MB against a 921 MB target and the
	// cache settled at 164% of the limit for good.
	protected, alloc := protectViewerRanges(windows, target)
	if alloc.capped {
		log.TLogln("[CACHE] viewer budget allocated:", alloc.reserved/1024/1024,
			"MB of target", target/1024/1024, "MB; active:", alloc.active,
			"idle:", alloc.idle, "at floor:", alloc.starved)
	}
	s.lastProtectedBytes.Store(alloc.reserved)
	s.lastViewers.Store(int64(alloc.active + alloc.idle))

	// NOTE: do NOT write the recomputed total back into filledSize. Pieces are
	// being written right now, and a Store() from the cleanup worker clobbers
	// those increments — the counter silently drifts low and the limit stops
	// binding (found by TestLimitEvictsOpenCache: 16 MB written, 9 MB counted).
	// The single writer for filledSize is the piece itself (DiskPiece/
	// MemPiece Write/Release); the disk reconciler is the source of truth.
	if openFilled+s.orphanBytes.Load() <= target {
		return 0, 0 // No cleanup needed (silent)
	}

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

	removed, removedBytes, openFilled := s.evictPass(allPieces, protected, openFilled,
		nowUnix, target, freshPieceMinAge, false)

	// Second pass under pressure. While a download is running every fresh piece
	// has Accessed=now, so the first pass finds nothing removable and the cache
	// sits above the limit instead of converging. Retry on COMPLETE pieces only:
	// evicting a partially written piece deletes bytes the client still counts,
	// and the reader then gets zeros with a failing hash check.
	if removed == 0 && s.totalOnDisk() > target {
		removed2, freed2, filled2 := s.evictPass(allPieces, protected, openFilled,
			nowUnix, target, freshPieceRelaxedAge, true)
		if removed2 > 0 {
			log.TLogln("[GLOBAL CLEANUP] second pass removed", removed2, "pieces,",
				freed2/1024/1024, "MB (freshness relaxed to", freshPieceRelaxedAge,
				"s, complete pieces only) total:", s.totalOnDisk()/1024/1024, "MB")
			removed, removedBytes, openFilled = removed2, freed2, filled2
		}
	}

	if removed > 0 {
		log.TLogln("[GLOBAL CLEANUP] Removed", removed, "pieces (incremental). Open filled:",
			openFilled/1024/1024, "MB total:", s.totalOnDisk()/1024/1024, "MB")
	}
	return removed, removedBytes
}

// evictPass walks the candidates in eviction order and frees bytes until the
// cache is under target. freshLimit is the minimum age a piece must have to be
// considered; completeOnly skips partially written pieces.
//
// Order matters and must not change: complete pieces first, then still
// downloading ones, oldest access first inside each group.
func (s *Storage) evictPass(allPieces []pieceEntry, protected map[*Cache][]Range,
	openFilled, nowUnix, target, freshLimit int64, completeOnly bool) (int, int64, int64) {

	const maxRemoveBytesPerCycle = 2 << 30
	const maxRemovePiecesPerCycle = 2000

	// Sort: complete pieces first (oldest first), still-downloading
	// pieces last. Evicting a partial piece deletes bytes the client
	// still counts as received: a later reader gets zeros, the hash
	// check fails and a peer is dropped (upstream PR #875 port).
	sort.Slice(allPieces, func(i, j int) bool {
		ci, cj := allPieces[i].complete, allPieces[j].complete
		if ci != cj {
			return ci && !cj
		}
		return allPieces[i].accessed < allPieces[j].accessed
	})

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
		if nowUnix-entry.accessed < freshLimit {
			continue
		}

		// Partial pieces are only eligible in the relaxed second pass.
		if completeOnly && !entry.complete {
			continue
		}

		// Capture size BEFORE removal: removePiece zeroes it and Release()
		// decrements filledSize.
		sz := entry.size
		// Remove piece (thread-safe within cache)
		entry.cache.removePiece(entry.piece)
		openFilled -= sz
		removedBytes += sz
		removed++
		if s.totalOnDisk() <= target {
			break
		}
	}
	return removed, removedBytes, openFilled
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
	// Stop the background worker NOW, not on its next 10 s tick. It used to keep
	// running for up to a tick after Close and could still reconcile and delete
	// cache directories of a storage that was already shut down. It also made
	// every storage outlive its test in the race detector, because the worker
	// kept reading settings.BTsets while the next test rewrote it.
	if s.closed.CompareAndSwap(false, true) {
		close(s.doneCh)
	}
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
	// Every worker exits on doneCh; wait for them so that after Close returns
	// this storage really is finished (see the wg doc).
	s.wg.Wait()
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

// ProtectionTarget is the byte ceiling shared by all viewer windows. It is the
// eviction target: eviction stops there, so protecting more than this makes the
// limit unreachable (see protectViewerRanges).
func (s *Storage) ProtectionTarget() int64 { return hysteresisTarget(s.capacity) }

// ActiveViewers counts viewers across every live cache — the global count the
// protection budget is shared between, not the per-cache one.
func (s *Storage) ActiveViewers() int {
	s.mu.Lock()
	caches := make([]*Cache, 0, len(s.caches))
	for _, ch := range s.caches {
		caches = append(caches, ch)
	}
	s.mu.Unlock()
	n := 0
	for _, ch := range caches {
		if ch.isClosed.Load() {
			continue
		}
		n += ch.GetUseReaders()
	}
	return n
}

// ProtectedBytesNow sums what viewers hold right now, without waiting for an
// eviction pass.
//
// lastProtectedBytes only updates inside evictPiecesTo, which does not run while
// the cache is under its target — so it reported "0 B" on a freshly deployed
// server with two TVs playing, which reads like "nothing is protected" when in
// fact protection is working. This is the live figure: the sum of the viewer
// windows as they are right now (uncapped, i.e. what they would like).
func (s *Storage) ProtectedBytesNow() int64 {
	s.mu.Lock()
	caches := make([]*Cache, 0, len(s.caches))
	for _, ch := range s.caches {
		caches = append(caches, ch)
	}
	s.mu.Unlock()

	nowUnix := time.Now().Unix()
	grace := viewerIdleGrace()
	var total int64
	for _, ch := range caches {
		if ch.isClosed.Load() || ch.pieceLength <= 0 {
			continue
		}
		for _, r := range ch.readersSnapshot() {
			if !r.isUse && nowUnix-r.lastAccess > grace {
				continue
			}
			total += windowBytes(r.getPiecesRange(), ch.pieceLength)
		}
	}
	return total
}

// ViewerDiagnostics reports the last eviction pass: bytes protected by viewers
// and how many windows there were. Surfaced in /stat so a cache stuck above
// its limit is recognisable instead of being guessed at from symptoms.
func (s *Storage) ViewerDiagnostics() (protectedBytes int64, viewers int64) {
	return s.lastProtectedBytes.Load(), s.lastViewers.Load()
}

// ProtectionTarget exposes the shared protection ceiling to callers outside
// this package (used to cap read-ahead globally).
func (c *Cache) ProtectionTarget() int64 { return c.storage.ProtectionTarget() }

// ActiveViewerCount exposes the global viewer count (see Storage.ActiveViewers).
func (c *Cache) ActiveViewerCount() int { return c.storage.ActiveViewers() }
