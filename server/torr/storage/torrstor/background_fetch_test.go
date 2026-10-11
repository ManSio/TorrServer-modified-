package torrstor

import (
	"testing"
	"time"

	"github.com/anacrolix/torrent"
	"server/settings"
)

// Background fetch keeps downloading past the read window while playing, so
// the disk fills whenever bandwidth is left over instead of stopping at the
// window edge. These tests pin the two properties that make it safe: it only
// fires past the window (never inside it, where the urgent priorities live),
// and it stops when the switch is off.
func TestBackgroundFetchReachesPastWindow(t *testing.T) {
	initTestSettings(t)
	settings.BTsets.UseDisk = false
	settings.BTsets.ConnectionsLimit = 60
	settings.BTsets.BackgroundFetch = true

	const (
		budget   = int64(8) << 30
		windowMB = 16
	)
	pieceLen := int64(4) << 20
	fileSize := int64(2) << 30 // 512 pieces, far larger than the window

	file := fakeBigFile(t, fileSize, pieceLen)
	numPieces := int(fileSize / pieceLen)

	stor := NewStorage(budget)
	defer stor.Close()
	stor.lastCleanupTime.Store(0)

	c := NewCache(budget, stor)
	c.pieceLength = pieceLen
	c.pieceCount = numPieces
	c.pieces = make(map[int]*Piece, numPieces)
	stor.caches[[20]byte{11}] = c
	c.SetTorrent(file.Torrent())

	rd := c.NewReader(file)
	defer c.CloseReader(rd)
	rd.offset = 500 << 20
	rd.readahead = int64(windowMB) << 20
	rd.lastAccess = time.Now().Unix()
	rd.isUse = true

	// Fill the whole window as complete, so there is nothing left inside it
	// to prioritise: if the background path works, the priorities it sets
	// must appear strictly past the window edge.
	win := rd.getPiecesRange()
	for i := win.Start; i < win.End && i < numPieces; i++ {
		p := NewPiece(i, c)
		p.Size = pieceLen
		p.Complete = true
		c.pieces[i] = p
	}
	// Pieces past the window exist and are incomplete.
	past := win.End + 2
	if past >= numPieces {
		t.Fatalf("window covers the whole test file, cannot test past it")
	}
	for i := win.End; i < win.End+10 && i < numPieces; i++ {
		p := NewPiece(i, c)
		p.Size = 0
		p.Complete = false
		c.pieces[i] = p
	}

	c.setLoadPriority([]Range{win})

	// The first incomplete piece past the window must carry Normal: the
	// background fetch reached it. Complete pieces inside the window must
	// not have been touched (they need nothing).
	st := c.torrent.PieceState(past)
	t.Logf("piece %d past window %d..%d has priority %v", past, win.Start, win.End, st.Priority)
	if st.Priority != torrent.PiecePriorityNormal {
		t.Errorf("piece past a full window has priority %v, want Normal (background fetch did not reach it)", st.Priority)
	}
}

// The same setup with the switch off must leave past-window pieces alone.
// This is the negative control: without it, a regression that always fetches
// in the background would still pass the test above.
func TestBackgroundFetchOffLeavesPastWindowAlone(t *testing.T) {
	initTestSettings(t)
	settings.BTsets.UseDisk = false
	settings.BTsets.ConnectionsLimit = 60
	settings.BTsets.BackgroundFetch = false

	const (
		budget   = int64(8) << 30
		windowMB = 16
	)
	pieceLen := int64(4) << 20
	fileSize := int64(2) << 30

	file := fakeBigFile(t, fileSize, pieceLen)
	numPieces := int(fileSize / pieceLen)

	stor := NewStorage(budget)
	defer stor.Close()
	stor.lastCleanupTime.Store(0)

	c := NewCache(budget, stor)
	c.pieceLength = pieceLen
	c.pieceCount = numPieces
	c.pieces = make(map[int]*Piece, numPieces)
	stor.caches[[20]byte{12}] = c
	c.SetTorrent(file.Torrent())

	rd := c.NewReader(file)
	defer c.CloseReader(rd)
	rd.offset = 500 << 20
	rd.readahead = int64(windowMB) << 20
	rd.lastAccess = time.Now().Unix()
	rd.isUse = true

	win := rd.getPiecesRange()
	for i := win.Start; i < win.End && i < numPieces; i++ {
		p := NewPiece(i, c)
		p.Size = pieceLen
		p.Complete = true
		c.pieces[i] = p
	}
	past := win.End + 2
	for i := win.End; i < win.End+10 && i < numPieces; i++ {
		p := NewPiece(i, c)
		p.Size = 0
		p.Complete = false
		c.pieces[i] = p
	}

	c.setLoadPriority([]Range{win})

	if st := c.torrent.PieceState(past); st.Priority != torrent.PiecePriorityNone {
		t.Errorf("piece past a full window has priority %v with the switch off, want None", st.Priority)
	}
}
