package torr

import (
	"errors"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"server/torrshash"

	utils2 "server/utils"

	"github.com/anacrolix/torrent"
	"github.com/anacrolix/torrent/metainfo"

	"server/log"
	"server/settings"
	"server/torr/state"
	cacheSt "server/torr/storage/state"
	"server/torr/storage/torrstor"
	"server/torr/utils"
)

type Torrent struct {
	Title    string
	Category string
	Poster   string
	Data     string
	*torrent.TorrentSpec

	Stat      state.TorrentStat
	Timestamp int64
	Size      int64

	*torrent.Torrent
	muTorrent sync.Mutex

	bt    *BTServer
	cache *torrstor.Cache

	lastTimeSpeed       time.Time
	DownloadSpeed       float64
	UploadSpeed         float64
	BytesReadUsefulData int64
	BytesWrittenData    int64

	PreloadSize    int64
	PreloadedBytes int64

	DurationSeconds float64
	BitRate         string

	expiredTime time.Time

	closed <-chan struct{}

	progressTicker *time.Ticker

	// CacheState coalescing (settings.BTSets.CacheStateTTL).
	//
	// A `CacheState` is the single most expensive value this server produces:
	// `Cache.GetState` walks every piece of the torrent and `Status()` re-packs
	// the torrs hash with zlib. Clients ask for it far more often than it can
	// change — measured 555 of the last 600 requests in access.log from two
	// pollers. Reusing the last snapshot for a second turns N requests into
	// one computation.
	//
	// The mutex is held across the computation on purpose: that is what
	// coalesces concurrent copies of the same request instead of letting them
	// queue up two identical expensive builds. It is a per-Torrent mutex, so it
	// never serialises different torrents.
	cacheStateMu sync.Mutex
	cacheStateAt time.Time
	cacheState   *cacheSt.CacheState

	// torrshash token memoisation. Pack() compresses with zlib at
	// BestCompression; the inputs (hash, title, poster, category, size,
	// trackers) change only when a torrent is saved or renamed, so the token is
	// recomputed only when one of them changes.
	torrsHashMu    sync.Mutex
	torrsHashKey   string
	torrsHashCache string

	// Throttle for `Torrent.BytesCompleted()`, which walks every piece under
	// the client lock. anacrolix upstream suggests keeping an incremental
	// counter instead (issue #634); the refresh rate is the same one that
	// `progressEvent` already walks at, so the value still lands within a
	// second where every other "live" number in `Status()` does.
	loadedMu   sync.Mutex
	loadedAt   time.Time
	loadedSize int64
}

func NewTorrent(spec *torrent.TorrentSpec, bt *BTServer) (*Torrent, error) {
	// https://github.com/anacrolix/torrent/issues/747
	if bt == nil || bt.client == nil {
		return nil, errors.New("BT client not connected")
	}
	switch settings.BTsets.RetrackersMode {
	case 1:
		spec.Trackers = append(spec.Trackers, [][]string{utils.GetDefTrackers()}...)
	case 2:
		spec.Trackers = nil
	case 3:
		spec.Trackers = [][]string{utils.GetDefTrackers()}
	}

	trackers := utils.GetTrackerFromFile()
	if len(trackers) > 0 {
		spec.Trackers = append(spec.Trackers, [][]string{trackers}...)
	}

	// The read lock is taken and released here explicitly, NOT with defer: the code
	// below takes bt.mu.Lock() for the torrents map, and holding an RLock across
	// that would self-deadlock (sync.RWMutex is not reentrant).
	bt.mu.RLock()
	if bt.client == nil {
		bt.mu.RUnlock()
		return nil, errors.New("BT client not connected")
	}
	goTorrent, _, err := bt.client.AddTorrentSpec(spec)
	bt.mu.RUnlock()
	if err != nil {
		return nil, err
	}

	bt.mu.Lock()
	defer bt.mu.Unlock()
	if tor, ok := bt.torrents[spec.InfoHash]; ok {
		return tor, nil
	}

	timeout := time.Second * time.Duration(settings.BTsets.TorrentDisconnectTimeout)
	if timeout > time.Minute {
		timeout = time.Minute
	}

	torr := new(Torrent)
	torr.Torrent = goTorrent
	torr.Stat = state.TorrentAdded
	torr.lastTimeSpeed = time.Now()
	torr.bt = bt
	torr.closed = goTorrent.Closed()
	torr.TorrentSpec = spec
	torr.AddExpiredTime(timeout)
	torr.Timestamp = time.Now().Unix()

	go torr.watch()

	bt.torrents[spec.InfoHash] = torr
	return torr, nil
}

func (t *Torrent) WaitInfo() bool {
	if t == nil || t.Torrent == nil {
		return false
	}

	// Close torrent if no info in 1 minute + TorrentDisconnectTimeout config option
	tm := time.NewTimer(time.Minute + time.Second*time.Duration(settings.BTsets.TorrentDisconnectTimeout))

	select {
	case <-t.Torrent.GotInfo():
		// bt.storage is replaced by Connect() under bt.mu; read it under the lock
		// and use the snapshot afterwards, because the storage object keeps its own
		// locking and is never closed. Reading the field unlocked was flagged by the
		// race detector and can hand SetTorrent a torn interface value.
		if t.bt != nil {
			t.bt.mu.RLock()
			storage := t.bt.storage
			t.bt.mu.RUnlock()
			if storage != nil {
				// GetCache returns nil when the storage does not know this hash
				// yet. Assigning that nil over a working cache and then calling
				// SetTorrent on it panicked the stream handler, which is why
				// seeking died silently: the player opened a new range request,
				// the handler panicked, and nothing was served from the new
				// position. It had happened 23 times since 2026-08-29,
				// independently of any readahead change.
				//
				// Keep whatever cache we already had rather than losing it to a
				// lookup that came back empty.
				if cache := storage.GetCache(t.Hash()); cache != nil {
					t.cache = cache
					cache.SetTorrent(t.Torrent)
				}
			}
		}
		return true
	case <-t.closed:
		return false
	case <-tm.C:
		return false
	}
}

func (t *Torrent) GotInfo() bool {
	// log.TLogln("GotInfo state:", t.Stat)
	if t == nil || t.Stat == state.TorrentClosed {
		return false
	}
	// assume we have info in preload state
	// and dont override with TorrentWorking
	if t.Stat == state.TorrentPreload {
		return true
	}
	t.Stat = state.TorrentGettingInfo
	if t.WaitInfo() {
		t.Stat = state.TorrentWorking
		t.AddExpiredTime(time.Second * time.Duration(settings.BTsets.TorrentDisconnectTimeout))
		return true
	} else {
		t.Close()
		return false
	}
}

func (t *Torrent) AddExpiredTime(duration time.Duration) {
	newExpiredTime := time.Now().Add(duration)
	if t.expiredTime.Before(newExpiredTime) {
		t.expiredTime = newExpiredTime
	}
}

func (t *Torrent) watch() {
	t.progressTicker = time.NewTicker(time.Second)
	defer t.progressTicker.Stop()

	for {
		select {
		case <-t.progressTicker.C:
			go t.progressEvent()
		case <-t.closed:
			return
		}
	}
}

func (t *Torrent) progressEvent() {
	if t.expired() {
		if t.TorrentSpec != nil {
			log.TLogln("Torrent close by timeout", t.TorrentSpec.InfoHash.HexString())
		}
		t.bt.RemoveTorrent(t.Hash())
		return
	}

	t.muTorrent.Lock()
	if t.Torrent != nil && t.Torrent.Info() != nil {
		st := t.Torrent.Stats()
		deltaDlBytes := st.BytesRead.Int64() - t.BytesReadUsefulData
		deltaUpBytes := st.BytesWritten.Int64() - t.BytesWrittenData
		deltaTime := time.Since(t.lastTimeSpeed).Seconds()

		t.DownloadSpeed = float64(deltaDlBytes) / deltaTime
		t.UploadSpeed = float64(deltaUpBytes) / deltaTime

		t.BytesReadUsefulData = st.BytesRead.Int64()
		t.BytesWrittenData = st.BytesWritten.Int64()

		if t.cache != nil {
			t.PreloadedBytes = t.cache.GetState().Filled
		}
	} else {
		t.DownloadSpeed = 0
		t.UploadSpeed = 0
	}
	t.muTorrent.Unlock()

	t.lastTimeSpeed = time.Now()
	t.updateRA()
}

// updateRA pushes the configured read-ahead window to the cache readers.
// The window also defines how much around the playback position is protected
// from eviction (Reader.getOffsetRange), so it is the knob the user actually
// feels: bigger = smoother playback under a slow swarm, but more cache pinned.
// readAheadBytes is the configured read-ahead window, clamped: never below
// 4 MB (a stalled player), never above the cache share this reader deserves.
// Split out from updateRA so the arithmetic can be tested without a torrent.
func readAheadBytes(cacheCapacity int64, readers, configuredMB int) int64 {
	adj := int64(configuredMB) << 20
	if adj < settings.MinReadAheadMB<<20 {
		adj = settings.MinReadAheadMB << 20
	}
	// Clamp again here, not only in settings validation: this window decides
	// how much cache eviction may not touch, so the bound must hold at the
	// point of use even if a value reaches us unvalidated.
	if adj > settings.MaxReadAheadMB<<20 {
		adj = settings.MaxReadAheadMB << 20
	}
	if readers > 0 {
		if share := cacheCapacity / int64(readers); adj > share {
			adj = share
		}
	}
	return adj
}

// pauseWindowMB picks which configured window to use: the playing one, or the
// larger pause one once a viewer has been silent long enough.
//
// Idle time is the signal because there is no "paused" flag: from inside the
// server a paused player and a stalled one look identical, and both want
// buffer rather than the tight playing window. Split out from updateRA so the
// decision can be tested without a torrent, the same reason readAheadBytes is
// separate.
func pauseWindowMB(playingMB, pauseMB int, idleSec int64, afterSec int) int {
	// Never shrink: a boost below the playing window is a misconfiguration,
	// and applying it would make a pause *reduce* buffering.
	if pauseMB <= playingMB {
		return playingMB
	}
	if pauseMB <= 0 {
		return playingMB
	}
	if idleSec < int64(afterSec) {
		return playingMB
	}
	return pauseMB
}

func (t *Torrent) updateRA() {
	if t.cache == nil || settings.BTsets == nil {
		return
	}
	// PausePreloadUnbounded: while paused, download to the end of the file
	// (or until the cache is full) instead of stopping at a megabyte window.
	// This bypasses both the RAM cap and the megabyte clamp below, because
	// the bound here is the disk cache, not memory: the data lands on disk
	// through the normal piece path. The in-flight portion passes through
	// RAM, which is the trade the setting documents.
	idleSec := t.cache.MaxReaderIdleSec()
	paused := idleSec >= int64(settings.BTsets.PausePreloadAfterSec)
	var adj int64
	if settings.BTsets.PausePreloadUnbounded && paused {
		// Backpressure applies in RAM mode only. In disk mode the chunks land
		// straight in files through pwrite, so StagedBytes counts disk bytes
		// of incomplete pieces, not memory held: capping it would idle the
		// network while the disk has space, which is exactly the stall this
		// feature exists to prevent. In RAM mode the buffers really are
		// memory, so the cap stays.
		//
		// Disk mode relies on two natural brakes instead: free cache space
		// (the window below never exceeds it) and the per-connection request
		// pipeline, which bounds in-flight regardless of window size.
		if capMB := settings.BTsets.PauseFetchStagingMB; capMB > 0 &&
			!settings.BTsets.UseDisk &&
			t.cache.StagedBytes() >= int64(capMB)<<20 {
			paused = false
		} else {
			// Bound by the watched files, not the whole torrent: a season
			// holds many episodes, but pausing episode 1 must not queue all
			// 79 GB. WatchedFileEndBytes is the furthest byte anyone watches;
			// fall back to the torrent length only when no reader tells us
			// better (no active viewers yet).
			fileEnd := t.cache.WatchedFileEndBytes()
			if fileEnd <= 0 {
				fileEnd = t.Length()
			}
			free := t.cache.GetCapacity() - t.cache.GetState().Filled
			unboundedMB := torrstor.UnboundedPauseWindowMB(
				fileEnd,
				free,
				t.cache.ActiveViewerCount(),
			)
			adj = int64(unboundedMB) << 20
		}
	}
	if !settings.BTsets.PausePreloadUnbounded || !paused {
		// AutoTune: derive the pause window from the hardware instead of the
		// written setting. The server measures RAM and sizes the window to a
		// safe share of it, so a small box gets a small window and a large
		// box gets a large one with no configuration. An explicit 0 still
		// disables the feature even with AutoTune on.
		pauseMB := settings.BTsets.PausePreloadMB
		if settings.BTsets.AutoTune && pauseMB != 0 {
			pauseMB = torrstor.AutoPauseWindowMB(
				torrstor.TotalMemoryBytes(),
				t.cache.ActiveViewerCount(),
			)
		}
		configured := pauseWindowMB(
			settings.BTsets.ReaderReadAheadMB,
			pauseMB,
			idleSec,
			settings.BTsets.PausePreloadAfterSec,
		)
		// The pause window is downloaded-but-unread data, so it is RAM rather
		// than disk cache, and the cache size says nothing about whether it
		// fits. On the live server (3.45 GiB RAM, measured) a 1 GiB window
		// per viewer would take a third of the machine and the box would
		// start swapping long before anything visibly failed.
		configured = torrstor.CapPauseWindowToMemory(
			configured,
			t.cache.ActiveViewerCount(),
			torrstor.TotalMemoryBytes(),
		)
		adj = readAheadBytes(t.cache.GetCapacity(), t.cache.Readers(), configured)
	}
	// Global share. readAheadBytes only divides the window by the readers of
	// THIS cache while measuring against the global capacity, so two films with
	// two viewers each asked for twice the limit between them and the cache sat
	// at 164% of it (measured 2026-10-05). Split the eviction target between all
	// active viewers instead: buffering more than the cache can hold is data
	// that eviction will delete again a few minutes later.
	if n := t.cache.ActiveViewerCount(); n > 1 {
		if share := t.cache.ProtectionTarget() / int64(n); adj > share {
			adj = share
		}
	}
	go t.cache.AdjustRA(adj)
}

func (t *Torrent) expired() bool {
	if t.cache == nil {
		return false
	}
	return t.cache.Readers() == 0 && t.expiredTime.Before(time.Now()) && (t.Stat == state.TorrentWorking || t.Stat == state.TorrentClosed)
}

func (t *Torrent) Files() []*torrent.File {
	if t.Torrent != nil && t.Torrent.Info() != nil {
		files := t.Torrent.Files()
		return files
	}
	return nil
}

func (t *Torrent) Hash() metainfo.Hash {
	if t.Torrent != nil {
		return t.Torrent.InfoHash()
	}
	if t.TorrentSpec != nil {
		return t.TorrentSpec.InfoHash
	}
	return [20]byte{}
}

func (t *Torrent) Length() int64 {
	if t.Info() == nil {
		return 0
	}
	return t.Torrent.Length()
}

func (t *Torrent) NewReader(file *torrent.File) *torrstor.Reader {
	if t.Stat == state.TorrentClosed {
		return nil
	}
	reader := t.cache.NewReader(file)
	return reader
}

func (t *Torrent) CloseReader(reader *torrstor.Reader) {
	t.cache.CloseReader(reader)
	t.AddExpiredTime(time.Second * time.Duration(settings.BTsets.TorrentDisconnectTimeout))
}

func (t *Torrent) GetCache() *torrstor.Cache {
	return t.cache
}

func (t *Torrent) drop() {
	t.muTorrent.Lock()
	defer t.muTorrent.Unlock()
	if t.Torrent != nil {
		t.Torrent.Drop()
		t.Torrent = nil
	}
}

func (t *Torrent) Close() bool {
	if t == nil {
		return false
	}
	if t.Stat == state.TorrentClosed {
		return true
	}
	if settings.ReadOnly && t.cache != nil && t.cache.GetUseReaders() > 0 {
		return false
	}
	t.Stat = state.TorrentClosed

	if t.bt != nil {
		t.bt.mu.Lock()
		if _, ok := t.bt.torrents[t.Hash()]; ok {
			delete(t.bt.torrents, t.Hash())
		}
		t.bt.mu.Unlock()
	}

	t.drop()
	return true
}

func (t *Torrent) Status() *state.TorrentStatus {
	t.muTorrent.Lock()
	defer t.muTorrent.Unlock()

	st := new(state.TorrentStatus)

	st.Stat = t.Stat
	st.StatString = t.Stat.String()
	st.Title = t.Title
	st.Category = t.Category
	st.Poster = t.Poster
	st.Data = t.Data
	st.Timestamp = t.Timestamp
	st.TorrentSize = t.Size
	st.BitRate = t.BitRate
	st.DurationSeconds = t.DurationSeconds

	if t.TorrentSpec != nil {
		st.Hash = t.TorrentSpec.InfoHash.HexString()
	}
	if t.Torrent != nil {
		st.Name = t.Torrent.Name()
		st.Hash = t.Torrent.InfoHash().HexString()
		st.LoadedSize = t.loadedSizeBytes()

		st.PreloadedBytes = t.PreloadedBytes
		st.PreloadSize = t.PreloadSize
		st.DownloadSpeed = t.DownloadSpeed
		st.UploadSpeed = t.UploadSpeed

		tst := t.Torrent.Stats()
		st.BytesWritten = tst.BytesWritten.Int64()
		st.BytesWrittenData = tst.BytesWrittenData.Int64()
		st.BytesRead = tst.BytesRead.Int64()
		st.BytesReadData = tst.BytesReadData.Int64()
		st.BytesReadUsefulData = tst.BytesReadUsefulData.Int64()
		st.ChunksWritten = tst.ChunksWritten.Int64()
		st.ChunksRead = tst.ChunksRead.Int64()
		st.ChunksReadUseful = tst.ChunksReadUseful.Int64()
		st.ChunksReadWasted = tst.ChunksReadWasted.Int64()
		st.PiecesDirtiedGood = tst.PiecesDirtiedGood.Int64()
		st.PiecesDirtiedBad = tst.PiecesDirtiedBad.Int64()
		st.TotalPeers = tst.TotalPeers
		st.PendingPeers = tst.PendingPeers
		st.ActivePeers = tst.ActivePeers
		st.ConnectedSeeders = tst.ConnectedSeeders
		st.HalfOpenPeers = tst.HalfOpenPeers

		if t.Torrent.Info() != nil {
			st.TorrentSize = t.Torrent.Length()

			files := t.Files()
			sort.Slice(files, func(i, j int) bool {
				return utils2.CompareStrings(files[i].Path(), files[j].Path())
			})
			for i, f := range files {
				st.FileStats = append(st.FileStats, &state.TorrentFileStat{
					Id:     i + 1, // in web id 0 is undefined
					Path:   f.Path(),
					Length: f.Length(),
				})
			}

			var trackers []string
			if t.TorrentSpec != nil && len(t.TorrentSpec.Trackers) > 0 && len(t.TorrentSpec.Trackers[0]) > 0 {
				trackers = t.TorrentSpec.Trackers[0]
			}
			if token := t.torrsHashToken(st.Hash, st.Title, st.Poster, st.Category, st.TorrentSize, trackers); token != "" {
				st.TorrsHash = token
			}
		}
	}

	return st
}

// torrsHashToken returns the packed torrs:// token for a torrent, memoised
// against the fields that feed it.
//
// `torrshash.Pack` runs zlib at BestCompression and allocates a buffer per
// call, and it was one of the two largest allocation sources on the production
// box (about a quarter of all bytes allocated in a 30 s heap profile) purely
// because `Status()` is called several times a second and the token only
// changes when a title, poster, category, size or tracker does.
//
// Returns "" if packing fails, matching the old inline `if err == nil`.
func (t *Torrent) torrsHashToken(hash, title, poster, category string, size int64, trackers []string) string {
	// Length-prefix every field rather than joining on a separator: a NUL join
	// is ambiguous as soon as a title or poster contains a NUL (title "a\0b"
	// with poster "c" and title "a" with poster "b\0c" produce the same key),
	// and titles are user-supplied. Caught by the separator-collision test.
	var key strings.Builder
	for _, f := range []string{hash, title, poster, category, strconv.FormatInt(size, 10), strconv.Itoa(len(trackers))} {
		key.WriteString(strconv.Itoa(len(f)))
		key.WriteByte(':')
		key.WriteString(f)
		key.WriteByte(';')
	}
	for _, tr := range trackers {
		key.WriteString(strconv.Itoa(len(tr)))
		key.WriteByte(':')
		key.WriteString(tr)
		key.WriteByte(';')
	}
	k := key.String()

	t.torrsHashMu.Lock()
	defer t.torrsHashMu.Unlock()
	if t.torrsHashCache != "" && t.torrsHashKey == k {
		return t.torrsHashCache
	}

	th := torrshash.New(hash)
	th.AddField(torrshash.TagTitle, title)
	th.AddField(torrshash.TagPoster, poster)
	th.AddField(torrshash.TagCategory, category)
	th.AddField(torrshash.TagSize, strconv.FormatInt(size, 10))
	for _, tr := range trackers {
		th.AddField(torrshash.TagTracker, tr)
	}

	token, err := torrshash.Pack(th)
	if err != nil {
		return ""
	}
	t.torrsHashKey = k
	t.torrsHashCache = token
	return token
}

func (t *Torrent) CacheState() *cacheSt.CacheState {
	if t.Torrent == nil || t.cache == nil {
		return nil
	}
	ttl := settings.BTsets.CacheStateTTLDuration()
	if ttl <= 0 {
		return t.computeCacheState()
	}

	t.cacheStateMu.Lock()
	defer t.cacheStateMu.Unlock()
	if t.cacheState != nil && cacheStateFresh(t.cacheStateAt, ttl, time.Now()) {
		return t.cacheState
	}
	st := t.computeCacheState()
	if st == nil {
		return nil
	}
	t.cacheState = st
	t.cacheStateAt = time.Now()
	return st
}

// cacheStateFresh reports whether a snapshot computed at `at` may still be
// served when it is now `now`. Split out of CacheState so the rule can be tested
// without a torrent: an unset timestamp is never fresh, and a non-positive ttl
// disables the cache entirely.
func cacheStateFresh(at time.Time, ttl time.Duration, now time.Time) bool {
	return ttl > 0 && !at.IsZero() && now.Sub(at) < ttl
}

// computeCacheState is the uncached version of CacheState, kept separate so
// both paths share exactly one definition of what the state contains.
func (t *Torrent) computeCacheState() *cacheSt.CacheState {
	if t.Torrent != nil && t.cache != nil {
		st := t.cache.GetState()
		if st == nil {
			return nil
		}
		st.Torrent = t.Status()
		return st
	}
	return nil
}

// loadedSizeBytes returns `Torrent.BytesCompleted()`, refreshed at most once a
// second (see the field comment). Returns 0 when there is no info yet, which is
// what BytesCompleted() itself returns.
func (t *Torrent) loadedSizeBytes() int64 {
	if t.Torrent == nil {
		return 0
	}
	const refresh = time.Second
	t.loadedMu.Lock()
	defer t.loadedMu.Unlock()
	if !t.loadedAt.IsZero() && time.Since(t.loadedAt) < refresh {
		return t.loadedSize
	}
	t.loadedSize = t.Torrent.BytesCompleted()
	t.loadedAt = time.Now()
	return t.loadedSize
}
