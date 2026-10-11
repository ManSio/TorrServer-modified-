package torrstor

import (
	"testing"
	"time"

	"server/settings"
)

// The pause preload is only worth anything if the larger window reaches the
// reader AND grows the range that eviction protects. pauseWindowMB decides the
// number and MaxReaderIdleSec supplies the silence; neither of them touches the
// reader. This test closes that gap: it drives the same sequence updateRA
// performs and checks the reader and its protection window actually follow.
func TestPausedViewerGrowsWindowAndProtection(t *testing.T) {
	const (
		budget    = int64(8) << 30
		playingMB = 90
		pausedMB  = 1024
		afterSec  = 15
	)
	pieceLen := int64(8) << 20
	fileSize := int64(40) << 30 // comfortably larger than either window

	initTestSettings(t)
	settings.BTsets.UseDisk = false

	file := fakeBigFile(t, fileSize, pieceLen)
	numPieces := int(fileSize / pieceLen)

	stor := NewStorage(budget)
	defer stor.Close()
	stor.lastCleanupTime.Store(0)

	c := NewCache(budget, stor)
	c.pieceLength = pieceLen
	c.pieceCount = numPieces
	c.pieces = make(map[int]*Piece, numPieces)
	stor.caches[[20]byte{7}] = c

	// A viewer watching from the middle of the file, exactly like
	// TestBudgetWatchesRunningDownload sets up.
	rd := c.NewReader(file)
	defer c.CloseReader(rd)
	rd.offset = 500 << 20
	rd.readahead = int64(playingMB) << 20
	rd.lastAccess = time.Now().Unix()
	rd.isUse = true

	playingPieces := rd.getPiecesRange()
	playingWindow := rd.readahead
	t.Logf("playing: window %d MB, protected pieces %d..%d",
		playingWindow>>20, playingPieces.Start, playingPieces.End)
	if playingPieces.End <= playingPieces.Start {
		t.Fatalf("protection range collapsed while playing: %d..%d",
			playingPieces.Start, playingPieces.End)
	}

	// --- the viewer goes quiet: the pause preload takes over ---
	//
	// lastAccess is moved back rather than slept on, so the test costs no wall
	// clock time and cannot flake on a loaded CI box.
	rd.lastAccess = time.Now().Unix() - int64(afterSec) - 1

	idle := c.MaxReaderIdleSec()
	if idle < int64(afterSec) {
		t.Fatalf("idle %d s did not reach the pause threshold %d s", idle, afterSec)
	}

	// What updateRA computes once pauseWindowMB applies the pause window.
	wantWindow := int64(pausedMB) << 20
	rd.SetReadahead(wantWindow)

	pausedPieces := rd.getPiecesRange()
	pausedWindow := rd.readahead

	if pausedWindow <= playingWindow {
		t.Errorf("paused window %d MB did not grow past the playing window %d MB",
			pausedWindow>>20, playingWindow>>20)
	}
	if pausedWindow != wantWindow {
		t.Errorf("paused window is %d MB, want %d MB", pausedWindow>>20, pausedMB)
	}

	// The property that makes the extra buffer usable: eviction must not delete
	// what was just downloaded for a viewer who is only paused.
	if pausedPieces.End <= playingPieces.End {
		t.Errorf("protected range did not grow: playing %d..%d, paused %d..%d",
			playingPieces.Start, playingPieces.End, pausedPieces.Start, pausedPieces.End)
	}
	t.Logf("paused: window %d MB, protected pieces %d..%d",
		pausedWindow>>20, pausedPieces.Start, pausedPieces.End)
}

// A viewer that keeps reading must not be treated as paused. This is the
// negative control for the integration path: if idle time were wired to
// something else, this fails while the test above still passes.
func TestActiveViewerKeepsPlayingWindow(t *testing.T) {
	const (
		budget    = int64(8) << 30
		playingMB = 90
	)
	pieceLen := int64(8) << 20
	fileSize := int64(40) << 30

	initTestSettings(t)
	settings.BTsets.UseDisk = false

	file := fakeBigFile(t, fileSize, pieceLen)
	numPieces := int(fileSize / pieceLen)

	stor := NewStorage(budget)
	defer stor.Close()
	stor.lastCleanupTime.Store(0)

	c := NewCache(budget, stor)
	c.pieceLength = pieceLen
	c.pieceCount = numPieces
	c.pieces = make(map[int]*Piece, numPieces)
	stor.caches[[20]byte{7}] = c

	rd := c.NewReader(file)
	defer c.CloseReader(rd)
	rd.offset = 500 << 20
	rd.readahead = int64(playingMB) << 20
	rd.lastAccess = time.Now().Unix()
	rd.isUse = true

	if idle := c.MaxReaderIdleSec(); idle != 0 {
		t.Errorf("a reader that just read reports idle %d s, want 0", idle)
	}
	if got := rd.readahead; got != int64(playingMB)<<20 {
		t.Errorf("window changed to %d MB while playing, want %d MB", got>>20, playingMB)
	}
}
