package torr

import (
	"testing"

	"server/settings"
)

// ReaderReadAheadMB is the knob the user actually feels: how much is buffered
// ahead of the playback position, and therefore how much around it eviction
// must not touch. It used to be "percent of CacheSize", which made a 98 GiB
// cache protect ~80 GiB around one player (measured 2026-10-01).
const liveCapacity = int64(97_891_069_696) // 91.1 GiB — the live server's limit

func TestReadAheadBytesFollowsConfiguredMB(t *testing.T) {
	cases := []struct {
		name     string
		capacity int64
		mb       int
		readers  int
		wantMB   int64
	}{
		{"configured 16 MB is honoured", liveCapacity, 16, 1, 16},
		{"configured 64 MB is honoured", liveCapacity, 64, 1, 64},
		{"floor at MinReadAheadMB", liveCapacity, 0, 1, settings.MinReadAheadMB},
		{"ceiling at MaxReadAheadMB", liveCapacity, 99999, 1, settings.MaxReadAheadMB},
		// capacity/4 is 22 GiB, so the configured maximum wins here
		{"configured maximum beats a huge share", liveCapacity, 1024, 4, 1024},
		// 32 MB cache, 1 reader: the share (32 MB) is below the configured 1 GB
		{"share clamp wins on a small cache", 32 << 20, 1024, 1, 32},
	}
	for _, c := range cases {
		if got := readAheadBytes(c.capacity, c.readers, c.mb) >> 20; got != c.wantMB {
			t.Errorf("%s: got %d MB, want %d MB", c.name, got, c.wantMB)
		}
	}
}

// The window must stay a small fraction of the cache even at the top of the
// range — this is the property whose absence made eviction impossible.
// The ceiling is 5%: at 4 GiB on a 91 GiB cache the window is ~4.4%, and
// eviction still has 95% of the cache to work with. On small machines the RAM
// cap binds long before this ceiling matters.
func TestReadAheadWindowStaysSmallFractionOfCache(t *testing.T) {
	window := readAheadBytes(liveCapacity, 1, settings.MaxReadAheadMB)
	fraction := float64(window) / float64(liveCapacity) * 100
	t.Logf("max window %d MB = %.3f%% of a 91 GiB cache", window>>20, fraction)
	if fraction > 5 {
		t.Errorf("window is %.3f%% of the cache — eviction would be blocked", fraction)
	}
}

// Legacy configs stored a percent (5..100) under the same JSON key; they must
// survive the switch to MB without becoming absurd.
func TestLegacyReadAheadPercentStaysSane(t *testing.T) {
	for _, legacy := range []int{5, 50, 81, 95, 100} {
		window := readAheadBytes(liveCapacity, 1, legacy) >> 20
		if window < settings.MinReadAheadMB || window > settings.MaxReadAheadMB {
			t.Errorf("legacy value %d became %d MB — outside %d..%d",
				legacy, window, settings.MinReadAheadMB, settings.MaxReadAheadMB)
		}
	}
	t.Log("legacy percents 5..100 map to 5..100 MB, all inside the valid range")
}
