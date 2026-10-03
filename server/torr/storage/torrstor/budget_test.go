package torrstor

import (
	"bytes"
	stdlog "log"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/anacrolix/torrent/metainfo"

	"server/settings"
)

// Tests for the disk-truth global cache budget (BTsets.OneCacheForAll).
//
// The failure this exists for: the budget was compared against in-memory
// counters only, so cache data of torrents that are no longer loaded stayed on
// disk and outside the limit. On a live server the cache reached 129.5 GiB
// against a 97.9 GiB limit (+32 %) and no eviction ran for a month, because the
// counter said 8.6 GiB.

func budgetTestSettings(t *testing.T, oneCacheForAll bool) string {
	t.Helper()
	dir := t.TempDir()
	settings.BTsets = &settings.BTSets{
		UseDisk:           true,
		TorrentsSavePath:  dir,
		RemoveCacheOnDrop: false,
		ConnectionsLimit:  25,
		OneCacheForAll:    oneCacheForAll,
	}
	t.Cleanup(func() { settings.BTsets = nil })
	return dir
}

func budgetInfo(pieceLen, length int64) *metainfo.Info {
	return &metainfo.Info{
		Name: "t", PieceLength: pieceLen, Length: length,
		Pieces: make([]byte, 20*int(length/pieceLen)),
	}
}

func dirBytes(t *testing.T, path string) int64 {
	t.Helper()
	var total int64
	entries, err := os.ReadDir(path)
	if err != nil {
		return 0
	}
	for _, e := range entries {
		if info, err := e.Info(); err == nil && !e.IsDir() {
			total += info.Size()
		}
	}
	return total
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

func captureLog(t *testing.T, fn func()) string {
	t.Helper()
	var buf bytes.Buffer
	prev := stdlog.Writer()
	stdlog.SetOutput(&buf)
	defer stdlog.SetOutput(prev)
	fn()
	return buf.String()
}

// A cache that is still in memory is trimmed to the target.
func TestBudgetEvictsOpenCache(t *testing.T) {
	dir := budgetTestSettings(t, true)
	const capacity = 8 << 20
	const pieceLen = 1 << 20

	stor := NewStorage(capacity)
	defer stor.Close()
	stor.lastCleanupTime.Store(0)

	var hash metainfo.Hash
	hash[0] = 1
	c, _ := stor.OpenTorrent(budgetInfo(pieceLen, 32*pieceLen), hash)
	cc := c.(*Cache)
	buf := make([]byte, pieceLen)
	for i := 0; i < 16; i++ { // 16 MB, twice the capacity
		if _, err := cc.pieces[i].WriteAt(buf, 0); err != nil {
			t.Fatal(err)
		}
		cc.pieces[i].Accessed = time.Now().Unix() - 3600
	}
	backdateDir(t, filepath.Join(dir, hash.HexString()))

	stor.lastCleanupTime.Store(0)
	stor.triggerGlobalCleanup(cleanTarget(capacity))

	got := dirBytes(t, filepath.Join(dir, hash.HexString()))
	if got > cleanTarget(capacity) {
		t.Fatalf("CONTROL FAILED: open cache not trimmed: %d MB on disk", got>>20)
	}
	t.Logf("open cache trimmed to %d MB (capacity %d MB)", got>>20, capacity>>20)
}

// The regression: after the torrent is dropped, its bytes must stay inside the
// budget and must eventually be reclaimed from disk.
func TestBudgetEvictsClosedCacheBytesFromDisk(t *testing.T) {
	dir := budgetTestSettings(t, true)
	const capacity = 8 << 20
	const pieceLen = 1 << 20

	stor := NewStorage(capacity)
	defer stor.Close()

	var hash metainfo.Hash
	hash[0] = 2
	c, _ := stor.OpenTorrent(budgetInfo(pieceLen, 32*pieceLen), hash)
	cc := c.(*Cache)
	buf := make([]byte, pieceLen)
	for i := 0; i < 16; i++ {
		if _, err := cc.pieces[i].WriteAt(buf, 0); err != nil {
			t.Fatal(err)
		}
	}
	path := filepath.Join(dir, hash.HexString())

	stor.CloseHash(hash)

	onDisk := dirBytes(t, path)
	if got := stor.totalOnDisk(); got < onDisk {
		t.Fatalf("accounting lost %d bytes on close: disk=%d accounted=%d",
			onDisk-got, onDisk, got)
	}
	t.Logf("after close: disk=%d MB, accounted=%d MB (capacity %d MB)",
		onDisk>>20, stor.totalOnDisk()>>20, capacity>>20)

	// Production sequence: the files age past the guard, the periodic scan
	// refreshes what is evictable, then eviction runs.
	for i := 0; i < 20; i++ {
		backdateDir(t, path)
		stor.reconcileOrphans(true)
		stor.lastCleanupTime.Store(0)
		stor.runCleanupIfNeeded()
		if stor.totalOnDisk() <= cleanTarget(capacity) {
			break
		}
	}

	final := dirBytes(t, path)
	if final > cleanTarget(capacity) {
		t.Errorf("LEAK: %d MB still on disk after reconcile (target %d MB)",
			final>>20, cleanTarget(capacity)>>20)
	}
	if got := stor.totalOnDisk(); got != final {
		t.Errorf("counter desync: accounted=%d disk=%d", got, final)
	}
}

// Pre-existing orphans (restart, restore, failed remove) must be discovered,
// not waited for.
func TestReconcileFindsPreExistingOrphans(t *testing.T) {
	dir := budgetTestSettings(t, true)
	const capacity = 8 << 20
	const pieceLen = 1 << 20

	stor := NewStorage(capacity)
	defer stor.Close()

	var hash metainfo.Hash
	hash[0] = 3
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

	stor.reconcileOrphans(true)
	if got, want := stor.totalOnDisk(), int64(16<<20); got != want {
		t.Fatalf("reconcile missed pre-existing data: accounted %d, disk %d", got, want)
	}
	t.Log("reconcile found 16 MB of pre-existing orphan data")

	stor.lastCleanupTime.Store(0)
	stor.runCleanupIfNeeded()
	if got := dirBytes(t, stale); got != 0 {
		t.Errorf("stale orphan dir not reclaimed: %d bytes left", got)
	}
}

// Negative control: a directory written a moment ago must survive even when the
// total is over budget — the 300s guard has to hold.
func TestReconcileKeepsFreshDirs(t *testing.T) {
	dir := budgetTestSettings(t, true)
	const capacity = 8 << 20

	stor := NewStorage(capacity)
	defer stor.Close()

	var hash metainfo.Hash
	hash[0] = 4
	fresh := filepath.Join(dir, hash.HexString())
	if err := os.MkdirAll(fresh, 0o777); err != nil {
		t.Fatal(err)
	}
	buf := make([]byte, 1<<20)
	for i := 0; i < 16; i++ {
		if err := os.WriteFile(filepath.Join(fresh, itoa(i)), buf, 0o666); err != nil {
			t.Fatal(err)
		}
	}

	stor.reconcileOrphans(true)
	stor.lastCleanupTime.Store(0)
	stor.runCleanupIfNeeded()
	if got := dirBytes(t, fresh); got == 0 {
		t.Error("fresh directory was deleted — the 300s guard does not hold")
	}
}

// RemoveCacheOnDrop=true must still reclaim on close, and must not orphan bytes.
func TestRemoveCacheOnDropReclaimsOnClose(t *testing.T) {
	dir := budgetTestSettings(t, true)
	settings.BTsets.RemoveCacheOnDrop = true
	const pieceLen = 1 << 20

	stor := NewStorage(capacity8())
	defer stor.Close()

	var hash metainfo.Hash
	hash[0] = 5
	c, _ := stor.OpenTorrent(budgetInfo(pieceLen, 8*pieceLen), hash)
	cc := c.(*Cache)
	buf := make([]byte, pieceLen)
	for i := 0; i < 8; i++ {
		if _, err := cc.pieces[i].WriteAt(buf, 0); err != nil {
			t.Fatal(err)
		}
	}
	p := filepath.Join(dir, hash.HexString())
	if dirBytes(t, p) == 0 {
		t.Fatal("setup wrong: nothing written")
	}
	stor.CloseHash(hash)
	time.Sleep(300 * time.Millisecond)
	if got := dirBytes(t, p); got != 0 {
		t.Errorf("expected disk reclaimed on close, %d bytes left", got)
	}
	if got := stor.totalOnDisk(); got != 0 {
		t.Errorf("RemoveCacheOnDrop must not orphan bytes, accounted=%d", got)
	}
}

// The flag is the safety switch: with it off nothing changes — no worker, no
// counters, no eviction, even when the directory is far over the limit.
func TestBudgetDisabledByDefault(t *testing.T) {
	dir := budgetTestSettings(t, false)
	const capacity = 8 << 20

	stor := NewStorage(capacity)
	defer stor.Close()
	if stor.budgetEnabled() {
		t.Fatal("budget reported enabled with OneCacheForAll=false")
	}
	if stor.cleanupCh != nil {
		t.Error("cleanup worker started while the budget is disabled")
	}

	var hash metainfo.Hash
	hash[0] = 6
	stale := filepath.Join(dir, hash.HexString())
	if err := os.MkdirAll(stale, 0o777); err != nil {
		t.Fatal(err)
	}
	buf := make([]byte, 1<<20)
	for i := 0; i < 16; i++ {
		if err := os.WriteFile(filepath.Join(stale, itoa(i)), buf, 0o666); err != nil {
			t.Fatal(err)
		}
	}
	stor.reconcileOrphans(true)
	stor.lastCleanupTime.Store(0)
	stor.runCleanupIfNeeded()

	if got := dirBytes(t, stale); got == 0 {
		t.Error("data was deleted with the budget disabled — upstream behaviour changed")
	}
}

// The alarm must only fire when the cache is genuinely over the capacity. It
// used to print from the under-target path too, e.g. "88755 MB > capacity
// 100240 MB" — four false alarms on a live server.
func TestNoFalseOverLimitAlarmBelowCapacity(t *testing.T) {
	budgetTestSettings(t, true)
	const capacity = 100 << 20

	stor := NewStorage(capacity)
	defer stor.Close()
	stor.lastStuckLog.Store(0)
	stor.filledSize.Store(capacity * 80 / 100) // under target and capacity

	out := captureLog(t, func() {
		stor.triggerGlobalCleanup(cleanTarget(capacity))
	})
	if strings.Contains(out, "OVER LIMIT") {
		t.Errorf("false alarm while under capacity:\n%s", out)
	}
	if strings.Contains(out, "[CACHE] total:") {
		t.Errorf("claimed a cleanup that did not happen:\n%s", out)
	}
}

// The exact band that produced the live false alarms: above target, below
// capacity.
func TestNoAlarmBetweenTargetAndCapacity(t *testing.T) {
	budgetTestSettings(t, true)
	const capacity = 100 << 20

	stor := NewStorage(capacity)
	defer stor.Close()
	stor.lastStuckLog.Store(0)
	stor.filledSize.Store(capacity * 95 / 100)

	out := captureLog(t, func() {
		stor.triggerGlobalCleanup(cleanTarget(capacity))
	})
	if strings.Contains(out, "OVER LIMIT") {
		t.Errorf("false alarm between target and capacity:\n%s", out)
	}
}

// The state the alarm exists for: above capacity with nothing removable. The
// bytes are really on disk (so the reconciler cannot correct them away) but
// fresh, so the 300s guard protects them.
func TestAlarmFiresWhenGenuinelyStuckAboveCapacity(t *testing.T) {
	dir := budgetTestSettings(t, true)
	const capacity = 8 << 20
	const pieceLen = 1 << 20

	stor := NewStorage(capacity)
	defer stor.Close()
	stor.lastStuckLog.Store(0)

	var hash metainfo.Hash
	hash[0] = 8
	sub := filepath.Join(dir, hexHash(hash))
	if err := os.MkdirAll(sub, 0o777); err != nil {
		t.Fatal(err)
	}
	buf := make([]byte, pieceLen)
	for i := 0; i < 12; i++ {
		if err := os.WriteFile(filepath.Join(sub, itoa(i)), buf, 0o666); err != nil {
			t.Fatal(err)
		}
	}
	// NewStorage already scanned while the directory was still empty; age that
	// scan so the cycle re-derives from disk, as it does 5 minutes into normal
	// operation.
	stor.orphanScannedAt.Store(0)

	out := captureLog(t, func() {
		stor.triggerGlobalCleanup(cleanTarget(capacity))
	})
	if !strings.Contains(out, "OVER LIMIT and nothing removable") {
		t.Errorf("genuinely stuck state was NOT reported:\n%s", out)
	}
	if !strings.Contains(out, "12 MB > capacity 8 MB") {
		t.Errorf("alarm must show the real numbers, got:\n%s", out)
	}
}

// A non-hex directory in the cache path must not crash the reconciler
// (metainfo.NewHashFromHex panics on bad input, and this runs in a goroutine).
func TestReconcileIgnoresForeignDirs(t *testing.T) {
	dir := budgetTestSettings(t, true)
	stor := NewStorage(capacity8())
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
		t.Errorf("foreign dir counted as cache data: %d bytes", got)
	}
}

func capacity8() int64 { return 8 << 20 }

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

// hexHash mirrors metainfo.Hash.HexString() without importing metainfo.
func hexHash(h [20]byte) string {
	const hex = "0123456789abcdef"
	b := make([]byte, 40)
	for i, v := range h {
		b[i*2] = hex[v>>4]
		b[i*2+1] = hex[v&0x0f]
	}
	return string(b)
}