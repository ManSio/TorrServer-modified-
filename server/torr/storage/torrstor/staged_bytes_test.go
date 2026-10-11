package torrstor

import (
	"testing"

	"server/settings"
)

// StagedBytes is the backpressure signal for the pause fetch: downloaded but
// incomplete bytes held in RAM-backed buffers. These cases pin what counts.
func TestStagedBytes(t *testing.T) {
	if got := (*Cache)(nil).StagedBytes(); got != 0 {
		t.Errorf("nil cache: got %d, want 0", got)
	}

	initTestSettings(t)
	settings.BTsets.UseDisk = false

	stor := NewStorage(8 << 30)
	defer stor.Close()

	c := NewCache(8<<30, stor)
	c.pieceLength = 4 << 20
	c.pieceCount = 8
	c.pieces = make(map[int]*Piece, 8)
	stor.caches[[20]byte{9}] = c

	// Two incomplete pieces with data, one complete, one empty.
	// Incomplete with 1 MiB and 2 MiB staged; complete with 4 MiB (not
	// staged - it is done); empty with 0.
	mk := func(id int, size int64, complete bool) {
		p := NewPiece(id, c)
		p.Size = size
		p.Complete = complete
		c.pieces[id] = p
	}
	mk(0, 1<<20, false)
	mk(1, 2<<20, false)
	mk(2, 4<<20, true)
	mk(3, 0, false)

	// Staged = 1 MiB + 2 MiB. The complete piece and the empty one add
	// nothing: one is done, the other holds no bytes.
	if got, want := c.StagedBytes(), int64(3<<20); got != want {
		t.Errorf("got %d, want %d", got, want)
	}

	// Completing a piece drains the staging.
	c.pieces[0].Complete = true
	if got, want := c.StagedBytes(), int64(2<<20); got != want {
		t.Errorf("after completing one piece: got %d, want %d", got, want)
	}
}
