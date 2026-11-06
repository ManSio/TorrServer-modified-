package torrstor

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/anacrolix/torrent/metainfo"

	"server/settings"
)

// Regression guards for the cache limit. The failure this file exists for:
// the limit was enforced against in-memory counters only, so cache data of
// torrents no longer loaded (expired() drops them from the client after
// TorrentDisconnectTimeout) stayed on disk and outside the budget — measured
// 2026-10-01 on a live server: 115 GiB of 124 GiB, counter said 8.6 GiB, so
// the LRU never ran.

func diskTestSettings(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	settings.BTsets = &settings.BTSets{
		UseDisk:           true,
		TorrentsSavePath:  dir,
		RemoveCacheOnDrop: false,
		ReaderReadAheadMB: 16,
		ConnectionsLimit:  25,
		CacheHysteresis:   0.9,
	}
	// Do NOT nil settings.BTsets here. Background workers (cleanupLoop,
	// Cache.priorityWatchdog) read this global, and niling it in a cleanup
	// races with any worker that has not finished yet — the race detector
	// reported it as a data race inside whichever test ran next. Every test
	// configures the fields it needs, so leaving the struct is harmless.
	return dir
}

func newIdleStorage(t *testing.T, capacity int64) *Storage {
	t.Helper()
	stor := NewStorage(capacity)
	stor.WaitInitialScan()
	stor.lastCleanupTime.Store(0)
	// Close it for the caller: an unclosed Storage keeps its cleanupLoop
	// goroutine alive for the whole package run, and that loop reads
	// settings.BTsets — so the next test's writes of that global race with it.
	// Storage.Close is idempotent, so a test may also defer stor.Close().
	t.Cleanup(func() { stor.Close() })
	return stor
}

// writePieces fills a cache with n pieces of pieceLen, then backdates the
// in-memory access time and the file mtimes so the 5-minute guards (which
// protect a paused player) do not mask what we are testing. Ageing happens
// after ALL writes: ageing mid-loop would let the background worker evict
// pieces while the fixture is still being built.
func writePieces(t *testing.T, c *Cache, n int, pieceLen int64) {
	t.Helper()
	buf := make([]byte, pieceLen)
	for i := 0; i < n; i++ {
		if _, err := c.pieces[i].WriteAt(buf, 0); err != nil {
			t.Fatalf("write piece %d: %v", i, err)
		}
	}
	old := time.Now().Unix() - 3600
	for i := 0; i < n; i++ {
		c.pieces[i].Accessed = old
	}
	backdateDir(t, filepath.Join(settings.BTsets.TorrentsSavePath, c.hash.HexString()))
	time.Sleep(100 * time.Millisecond) // let any in-flight cleanup settle
}

func backdateDir(t *testing.T, path string) {
	t.Helper()
	entries, err := os.ReadDir(path)
	if err != nil {
		return
	}
	old := time.Now().Add(-2 * time.Hour)
	for _, e := range entries {
		_ = os.Chtimes(filepath.Join(path, e.Name()), old, old)
	}
}

func cacheDirBytes(t *testing.T, path string) int64 {
	t.Helper()
	var total int64
	entries, err := os.ReadDir(path)
	if err != nil {
		return 0
	}
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		// Stat each entry rather than trusting e.Info(): the size in a directory
		// entry is not refreshed for a file that still has an open handle on
		// Windows, and the pool deliberately keeps handles open. Production is
		// Linux, but a test that can only pass there is a test nobody runs.
		if info, serr := os.Stat(filepath.Join(path, e.Name())); serr == nil && !info.IsDir() {
			total += info.Size()
		}
	}
	return total
}

// waitCacheDir polls until the async evict worker (evictChan -> os.Remove)
// has reclaimed the bytes, since eviction deletes files off the hot path.
func waitCacheDir(t *testing.T, path string, timeout time.Duration) int64 {
	t.Helper()
	deadline := time.Now().Add(timeout)
	var last int64
	for time.Now().Before(deadline) {
		last = cacheDirBytes(t, path)
		if last == 0 {
			return 0
		}
		time.Sleep(50 * time.Millisecond)
	}
	return last
}

func limitTestInfo(pieceLen, length int64) *metainfo.Info {
	return &metainfo.Info{
		Name: "t", PieceLength: pieceLen, Length: length,
		Pieces: make([]byte, 20*int(length/pieceLen)),
	}
}

func countAlivePieces(c *Cache) int {
	n := 0
	for _, p := range c.getPieces() {
		if p.Size > 0 {
			n++
		}
	}
	return n
}

// Positive control: a cache that is still in memory is evicted normally.
func TestLimitEvictsOpenCache(t *testing.T) {
	dir := diskTestSettings(t)
	const capacity = 8 << 20
	const pieceLen = 1 << 20

	stor := newIdleStorage(t, capacity)
	defer stor.Close()

	var hash metainfo.Hash
	hash[0] = 1
	c, _ := stor.OpenTorrent(limitTestInfo(pieceLen, 32*pieceLen), hash)
	cc := c.(*Cache)
	writePieces(t, cc, 16, pieceLen) // 16 MB, twice the capacity

	stor.lastCleanupTime.Store(0) // the worker may have run; debounce would skip us
	stor.triggerGlobalCleanup(capacity * 9 / 10)

	got := waitCacheDir(t, filepath.Join(dir, hash.HexString()), 3*time.Second)
	if got > capacity*9/10 {
		t.Fatalf("CONTROL FAILED: open cache not trimmed: %d MB on disk", got>>20)
	}
	t.Logf("open cache trimmed to %d MB (capacity %d MB)", got>>20, capacity>>20)
}

// The regression: after the torrent is dropped, its bytes must stay inside
// the budget and must eventually be reclaimed from disk.
func TestLimitEvictsClosedCacheBytesFromDisk(t *testing.T) {
	dir := diskTestSettings(t)
	const capacity = 8 << 20
	const pieceLen = 1 << 20

	stor := newIdleStorage(t, capacity)
	defer stor.Close()

	var hash metainfo.Hash
	hash[0] = 2
	c, _ := stor.OpenTorrent(limitTestInfo(pieceLen, 32*pieceLen), hash)
	cc := c.(*Cache)
	writePieces(t, cc, 16, pieceLen)

	// The torrent is dropped from the client (what expired() does
	// automatically). Files stay on disk.
	// Production sequence: the torrent is dropped, the files age past the
	// 5-minute guard, the periodic scan refreshes what is evictable, then
	// eviction runs. The test fast-forwards the ageing.
	stor.CloseHash(hash)
	path := filepath.Join(dir, hash.HexString())

	onDisk := cacheDirBytes(t, path)
	total := stor.totalOnDisk()
	t.Logf("after close: disk=%d MB, server accounts %d MB (capacity %d MB)",
		onDisk>>20, total>>20, capacity>>20)
	if total < onDisk {
		t.Fatalf("accounting lost %d MB on close: disk=%d accounted=%d",
			(onDisk-total)>>20, onDisk>>20, total>>20)
	}

	for i := 0; i < 20; i++ {
		backdateDir(t, path)
		stor.reconcileOrphans(true) // stands in for the 5-minute periodic scan
		stor.lastCleanupTime.Store(0)
		stor.runCleanupIfNeeded()
		if stor.totalOnDisk() <= capacity*9/10 {
			break
		}
	}

	final := cacheDirBytes(t, path)
	t.Logf("after reconcile: disk=%d MB, accounted=%d MB", final>>20, stor.totalOnDisk()>>20)
	if final > capacity*9/10 {
		t.Errorf("LEAK: %d MB still on disk after reconcile (limit %d MB)",
			final>>20, (capacity*9/10)>>20)
	}
	if stor.totalOnDisk() != final {
		t.Errorf("counter desync after reconcile: accounted=%d disk=%d",
			stor.totalOnDisk(), final)
	}
}

// Pre-existing orphans (restart, restore from backup, failed remove) must be
// discovered by the reconciler, not waited for.
func TestReconcileFindsPreExistingOrphans(t *testing.T) {
	dir := diskTestSettings(t)
	const capacity = 8 << 20
	const pieceLen = 1 << 20

	stor := newIdleStorage(t, capacity)
	defer stor.Close()

	var hash metainfo.Hash
	hash[0] = 3
	// A cache dir left behind by a previous run — no cache object at all.
	stale := filepath.Join(dir, hash.HexString())
	if err := os.MkdirAll(stale, 0o777); err != nil {
		t.Fatal(err)
	}
	buf := make([]byte, pieceLen)
	for i := 0; i < 16; i++ {
		if err := os.WriteFile(filepath.Join(stale, itoa(i)), buf, 0o666); err != nil {
			t.Fatal(err)
		}
	}
	backdateDir(t, stale)

	if got := stor.totalOnDisk(); got != 0 {
		t.Fatalf("setup wrong: server already counts %d bytes", got)
	}

	stor.reconcileOrphans(true)
	if got, want := stor.totalOnDisk(), int64(16<<20); got != want {
		t.Fatalf("reconcile missed pre-existing data: accounted %d, disk %d", got, want)
	}
	t.Log("reconcile found 16 MB of pre-existing orphan data")

	stor.lastCleanupTime.Store(0)
	stor.runCleanupIfNeeded()
	if got := cacheDirBytes(t, stale); got != 0 {
		t.Errorf("stale orphan dir not reclaimed: %d bytes left", got)
	}
}

// Negative control: a directory the user just wrote is not deleted even when
// over budget (it may be a torrent that is being re-opened right now).
func TestReconcileKeepsFreshDirs(t *testing.T) {
	diskTestSettings(t)
	const capacity = 8 << 20

	stor := newIdleStorage(t, capacity)
	defer stor.Close()

	var hash metainfo.Hash
	hash[0] = 4
	fresh := filepath.Join(settings.BTsets.TorrentsSavePath, hash.HexString())
	if err := os.MkdirAll(fresh, 0o777); err != nil {
		t.Fatal(err)
	}
	buf := make([]byte, 1<<20)
	for i := 0; i < 16; i++ { // 16 MB, over the 8 MB limit
		if err := os.WriteFile(filepath.Join(fresh, itoa(i)), buf, 0o666); err != nil {
			t.Fatal(err)
		}
	}

	stor.reconcileOrphans(true)
	stor.lastCleanupTime.Store(0)
	stor.runCleanupIfNeeded()

	if got := cacheDirBytes(t, fresh); got == 0 {
		t.Error("fresh directory was deleted — the 300s guard does not hold")
	} else {
		t.Logf("fresh dir kept: %d bytes", got)
	}
}

// RemoveCacheOnDrop=true must still reclaim on close (that path deletes the
// files itself, so nothing is orphaned).
func TestRemoveCacheOnDropReclaimsOnClose(t *testing.T) {
	dir := diskTestSettings(t)
	settings.BTsets.RemoveCacheOnDrop = true
	const pieceLen = 1 << 20

	stor := newIdleStorage(t, 8<<20)
	defer stor.Close()

	var hash metainfo.Hash
	hash[0] = 5
	c, _ := stor.OpenTorrent(limitTestInfo(pieceLen, 8*pieceLen), hash)
	cc := c.(*Cache)
	buf := make([]byte, pieceLen)
	for i := 0; i < 8; i++ {
		if _, err := cc.pieces[i].WriteAt(buf, 0); err != nil {
			t.Fatal(err)
		}
	}
	p := filepath.Join(dir, hash.HexString())
	if cacheDirBytes(t, p) == 0 {
		t.Fatal("setup wrong: nothing written")
	}
	stor.CloseHash(hash)
	time.Sleep(300 * time.Millisecond) // async delete via evictChan/worker
	if got := cacheDirBytes(t, p); got != 0 {
		t.Errorf("expected disk reclaimed on close, %d bytes left", got)
	}
	if got := stor.totalOnDisk(); got != 0 {
		t.Errorf("RemoveCacheOnDrop must not orphan bytes, accounted=%d", got)
	}
}

// A non-hex directory in the cache path must not crash the reconciler
// (metainfo.NewHashFromHex panics on bad input, and this runs in a goroutine).
func TestReconcileIgnoresForeignDirs(t *testing.T) {
	dir := diskTestSettings(t)
	stor := newIdleStorage(t, 8<<20)
	defer stor.Close()

	junk := filepath.Join(dir, "not-a-hash-but-40-chars-long-directory")
	if err := os.MkdirAll(junk, 0o777); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(junk, "x"), make([]byte, 4096), 0o666); err != nil {
		t.Fatal(err)
	}

	defer func() {
		if r := recover(); r != nil {
			t.Fatalf("reconcile panicked on a foreign directory: %v", r)
		}
	}()
	stor.reconcileOrphans(true)
	if got := stor.totalOnDisk(); got != 0 {
		t.Errorf("foreign dir was counted as cache data: %d bytes", got)
	}
}

// Negative control for the previous test: a cache whose files were written a
// moment ago must survive even though the total is over budget — the 300s
// guard has to hold, otherwise the fix would eat data the user just fetched.
// Ageing the files then makes it evictable, which proves the guard (not some
// accident) is what kept it.
func TestFreshlyClosedCacheIsNotDeleted(t *testing.T) {
	dir := diskTestSettings(t)
	const capacity = 8 << 20
	const pieceLen = 1 << 20

	stor := newIdleStorage(t, capacity)
	defer stor.Close()

	var hash metainfo.Hash
	hash[0] = 6
	c, _ := stor.OpenTorrent(limitTestInfo(pieceLen, 32*pieceLen), hash)
	cc := c.(*Cache)
	// Deliberately NOT aged: files keep their current mtime.
	buf := make([]byte, pieceLen)
	for i := 0; i < 16; i++ {
		if _, err := cc.pieces[i].WriteAt(buf, 0); err != nil {
			t.Fatal(err)
		}
	}
	path := filepath.Join(dir, hash.HexString())

	stor.CloseHash(hash)
	stor.reconcileOrphans(true)
	for i := 0; i < 5; i++ {
		stor.lastCleanupTime.Store(0)
		stor.runCleanupIfNeeded()
	}
	if got := cacheDirBytes(t, path); got == 0 {
		t.Fatal("cache written seconds ago was deleted — the 300s guard does not hold")
	}
	t.Logf("freshly dropped cache kept: %d bytes", cacheDirBytes(t, path))

	// Now the data is cold: the same cache becomes evictable.
	backdateDir(t, path)
	stor.reconcileOrphans(true)
	stor.lastCleanupTime.Store(0)
	stor.runCleanupIfNeeded()
	if got := cacheDirBytes(t, path); got != 0 {
		t.Errorf("cold cache kept while over budget: %d bytes", got)
	} else {
		t.Log("cold cache evicted once the 300s guard passed")
	}
}

func itoa(i int) string {
	if i == 0 {
		return "0"
	}
	var b []byte
	for i > 0 {
		b = append([]byte{byte('0' + i%10)}, b...)
		i /= 10
	}
	return string(b)
}
