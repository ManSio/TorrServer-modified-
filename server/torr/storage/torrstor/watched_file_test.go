package torrstor

import (
	"testing"
	"time"

	"server/settings"
)

// WatchedFileEndBytes is the bound that keeps the pause-unbounded path and
// the background fetch inside the files anyone actually watches, instead of
// queuing a whole 79 GB season when episode 1 pauses.
func TestWatchedFileEndBytes(t *testing.T) {
	if got := (*Cache)(nil).WatchedFileEndBytes(); got != 0 {
		t.Errorf("nil cache: got %d, want 0", got)
	}

	initTestSettings(t)
	settings.BTsets.UseDisk = false

	stor := NewStorage(8 << 30)
	defer stor.Close()

	c := NewCache(8<<30, stor)
	c.pieceLength = 4 << 20
	c.pieceCount = 512
	c.pieces = make(map[int]*Piece, 512)
	stor.caches[[20]byte{13}] = c

	// A single-file stand-in: one reader watching. The end must be exactly
	// this file's end, not the torrent's.
	file := fakeBigFile(t, 2<<30, 4<<20)
	rd := c.NewReader(file)
	defer c.CloseReader(rd)
	rd.offset = 0
	rd.lastAccess = time.Now().Unix()
	rd.isUse = true

	want := file.Offset() + file.Length()
	if got := c.WatchedFileEndBytes(); got != want {
		t.Errorf("one viewer: got %d, want file end %d", got, want)
	}

	// A parked reader watches nothing and must not move the bound.
	rd.isUse = false
	if got := c.WatchedFileEndBytes(); got != 0 {
		t.Errorf("parked reader: got %d, want 0", got)
	}
	rd.isUse = true

	// No readers at all: zero, and the caller falls back to torrent length.
	c.CloseReader(rd)
	if got := c.WatchedFileEndBytes(); got != 0 {
		t.Errorf("no readers: got %d, want 0", got)
	}
}
