package torrstor

import (
	"testing"

	"github.com/anacrolix/torrent/metainfo"

	"server/settings"
)

func TestFilledBytesMatchesGetState(t *testing.T) {
	old := settings.BTsets
	settings.BTsets = &settings.BTSets{UseDisk: false, ReaderReadAheadMB: 16, ConnectionsLimit: 25, CacheHysteresis: 0.9}
	t.Cleanup(func() { settings.BTsets = old })

	stor := newIdleStorage(t, 8*benchPieceLen)
	c := NewCache(stor.capacity, stor)
	var h metainfo.Hash
	h[0] = 9
	info := &metainfo.Info{
		Name:        "filled",
		PieceLength: benchPieceLen,
		Length:      4 * benchPieceLen,
		Pieces:      make([]byte, 20*4),
	}
	c.Init(info, h)
	t.Cleanup(func() { _ = c.Close() })

	buf := make([]byte, benchPieceLen)
	for i := 0; i < 3; i++ {
		if _, err := c.pieces[i].WriteAt(buf, 0); err != nil {
			t.Fatal(err)
		}
	}
	if got, want := c.FilledBytes(), c.GetState().Filled; got != want {
		t.Fatalf("FilledBytes = %d, GetState().Filled = %d", got, want)
	}
}
