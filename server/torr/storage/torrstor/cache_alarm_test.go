package torrstor

import (
	"bytes"
	stdlog "log"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"server/settings"
)

// Regression: the "OVER LIMIT and nothing removable" line was also printed from
// the under-target path, so it claimed e.g. "88755 MB > capacity 100240 MB"
// while the cache was BELOW capacity — four false alarms on the live server
// between 2026-10-01 and 2026-10-03.
//
// The signal must fire only when the cache is genuinely above capacity AND
// eviction found nothing removable.

func alarmTestSettings(t *testing.T) string {
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
	// settings.BTsets is left in place on purpose — see cache_limit_test.go.
	return dir
}

// captureLog redirects the standard logger (server/log writes through it) for
// the duration of fn and returns what was written.
func captureLog(t *testing.T, fn func()) string {
	t.Helper()
	var buf bytes.Buffer
	prev := stdlog.Writer()
	stdlog.SetOutput(&buf)
	defer stdlog.SetOutput(prev)
	fn()
	return buf.String()
}

// Below capacity: no alarm, and no status line either — nothing happened.
func TestNoAlarmBelowTarget(t *testing.T) {
	alarmTestSettings(t)
	const capacity = 100 << 20

	stor := newIdleStorage(t, capacity)
	defer stor.Close()
	stor.lastStuckLog.Store(0)
	stor.filledSize.Store(capacity * 80 / 100) // 80% — under target and capacity

	out := captureLog(t, func() {
		stor.triggerGlobalCleanup(hysteresisTarget(capacity))
	})
	if strings.Contains(out, "OVER LIMIT") {
		t.Errorf("false alarm while under capacity:\n%s", out)
	}
	// No cleanup status line either — nothing was reclaimed. (The reconciler's
	// own "[CACHE] scan:" line is expected and is not a cleanup claim.)
	if strings.Contains(out, "[CACHE] total:") {
		t.Errorf("claimed a cleanup that did not happen:\n%s", out)
	}
}

// The exact band that produced the live false alarms: above the target, below
// the capacity.
func TestNoAlarmBetweenTargetAndCapacity(t *testing.T) {
	alarmTestSettings(t)
	const capacity = 100 << 20

	stor := newIdleStorage(t, capacity)
	defer stor.Close()
	stor.lastStuckLog.Store(0)
	stor.filledSize.Store(capacity * 95 / 100) // 95%

	out := captureLog(t, func() {
		stor.triggerGlobalCleanup(hysteresisTarget(capacity))
	})
	if strings.Contains(out, "OVER LIMIT") {
		t.Errorf("false alarm between target and capacity:\n%s", out)
	}
}

// The state the alarm exists for: above capacity with nothing removable.
// Realistic version — the bytes are really on disk (so the reconciler cannot
// simply "correct" them away) but they are fresh, so the 300s guard protects
// them and no phase can free anything.
func TestAlarmFiresWhenGenuinelyStuckAboveCapacity(t *testing.T) {
	dir := alarmTestSettings(t)
	const capacity = 8 << 20
	const pieceLen = 1 << 20

	stor := newIdleStorage(t, capacity)
	defer stor.Close()
	stor.lastStuckLog.Store(0)

	// An orphan cache dir (no cache object) holding 12 MB of fresh pieces.
	var hash [20]byte
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

	// NewStorage already ran a forced scan when the directory was still empty.
	// Age it so the cycle re-derives from disk, exactly as it does 5 minutes
	// into normal operation.
	stor.orphanScannedAt.Store(0)

	out := captureLog(t, func() {
		stor.triggerGlobalCleanup(hysteresisTarget(capacity))
	})
	if !strings.Contains(out, "OVER LIMIT and nothing removable") {
		t.Errorf("genuinely stuck state was NOT reported:\n%s", out)
	}
	if !strings.Contains(out, "12 MB > capacity 8 MB") {
		t.Errorf("alarm must show the real numbers (total > capacity), got:\n%s", out)
	}
}

// A cleanup that actually frees space reports the normal status line.
func TestStatusLineOnRealEviction(t *testing.T) {
	alarmTestSettings(t)
	const capacity = 8 << 20
	const pieceLen = 1 << 20

	stor := newIdleStorage(t, capacity)
	defer stor.Close()
	stor.lastStuckLog.Store(0)

	var hash [20]byte
	hash[0] = 7
	c, _ := stor.OpenTorrent(limitTestInfo(pieceLen, 32*pieceLen), hash)
	cc := c.(*Cache)
	writePieces(t, cc, 16, pieceLen)
	stor.lastCleanupTime.Store(0)

	out := captureLog(t, func() {
		stor.triggerGlobalCleanup(hysteresisTarget(capacity))
	})
	if !strings.Contains(out, "[CACHE] total:") {
		t.Errorf("expected a status line after a real eviction, got:\n%s", out)
	}
	if strings.Contains(out, "OVER LIMIT") {
		t.Errorf("alarm fired although eviction succeeded:\n%s", out)
	}
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
