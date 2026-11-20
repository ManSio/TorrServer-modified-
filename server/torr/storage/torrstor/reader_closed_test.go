package torrstor

import (
	"testing"

	"github.com/anacrolix/torrent/metainfo"

	"server/settings"
)

// A stream request that arrives while the cache is closing must get "no reader",
// not a panic: Close nils the reader map, and writing into it panicked the
// handler (prod log 2026-11-20, "assignment to entry in nil map", recovered by
// gin into a 500 for the viewer).
func TestNewReaderOnClosedCacheReturnsNil(t *testing.T) {
	prev := settings.BTsets
	t.Cleanup(func() { settings.BTsets = prev })

	settings.BTsets = &settings.BTSets{
		UseDisk:          false,
		TorrentsSavePath: t.TempDir(),
		CacheHysteresis:  0.9,
	}
	stor := NewStorage(16 << 20)
	stor.WaitInitialScan()
	t.Cleanup(func() { _ = stor.Close() })

	c := NewCache(stor.capacity, stor)
	var h metainfo.Hash
	c.Init(&metainfo.Info{
		Name:        "closed-reader.bin",
		PieceLength: 1 << 20,
		Length:      1 << 20,
		Pieces:      make([]byte, 20),
	}, h)

	if err := c.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}

	// The file argument is not touched when the cache is already closed, so nil
	// is enough to exercise the path a real request takes.
	if r := c.NewReader(nil); r != nil {
		t.Fatal("NewReader on a closed cache returned a reader")
	}
}
