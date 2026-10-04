package torr

import (
	"testing"

	sets "server/settings"
)

func TestPreloadSizeMBOverride(t *testing.T) {
	sets.BTsets = &sets.BTSets{
		CacheSize:      100 * 1024 * 1024 * 1024, // 100GB
		PreloadCache:   50,                       // percent (ignored when MB set)
		PreloadCacheMB: 1,                        // 1MB manual
	}
	defer func() { sets.BTsets = nil }()

	if got := preloadSize(); got != 1*1024*1024 {
		t.Fatalf("MB override: expected 1MB, got %d", got)
	}
	t.Log("✅ 1MB preload at 100GB cache")
}

func TestPreloadSizePercentFallback(t *testing.T) {
	sets.BTsets = &sets.BTSets{
		CacheSize:      100 * 1024 * 1024 * 1024, // 100GB
		PreloadCache:   0.01,                     // hundredths minimum
		PreloadCacheMB: 0,                        // percent mode
	}
	defer func() { sets.BTsets = nil }()

	// 0.01% of 100GB ≈ 10MB
	got := preloadSize()
	cache := float64(100 * 1024 * 1024 * 1024)
	want := int64(cache / 100.0 * 0.01)
	if got != want {
		t.Fatalf("percent fallback: expected %d, got %d", want, got)
	}
	t.Logf("✅ percent fallback: 0.01%% of 100GB = %dMB", got/1024/1024)
}

func TestPreloadSizeCappedByCache(t *testing.T) {
	sets.BTsets = &sets.BTSets{
		CacheSize:      64 * 1024 * 1024, // 64MB
		PreloadCacheMB: 1024,             // 1GB > cache
	}
	defer func() { sets.BTsets = nil }()

	if got := preloadSize(); got != 64*1024*1024 {
		t.Fatalf("cap: expected 64MB, got %d", got)
	}
	t.Log("✅ MB capped by cache size")
}
