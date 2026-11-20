package torrstor

import (
	"bytes"
	"testing"

	"github.com/anacrolix/torrent/metainfo"

	"server/settings"
)

// setUseDisk replaces the global settings the way SetSettings does (a new
// pointer), so the test exercises the same transition the production code sees.
func setUseDisk(t *testing.T, useDisk bool, savePath string) {
	t.Helper()
	settings.BTsets = &settings.BTSets{
		UseDisk:          useDisk,
		TorrentsSavePath: savePath,
		CacheHysteresis:  0.9,
	}
}

func pieceFixture(t *testing.T, useDisk bool) (*Storage, *Cache, *Piece) {
	t.Helper()
	prev := settings.BTsets
	t.Cleanup(func() { settings.BTsets = prev })

	savePath := t.TempDir()
	setUseDisk(t, useDisk, savePath)
	stor := NewStorage(16 << 20)
	stor.WaitInitialScan()
	t.Cleanup(func() { _ = stor.Close() })

	// Init, not a hand-built cache: it creates the pieces and, in disk mode, the
	// per-torrent directory a DiskPiece writes into.
	c := NewCache(stor.capacity, stor)
	var h metainfo.Hash
	c.Init(&metainfo.Info{
		Name:        "piece-backend.bin",
		PieceLength: 1 << 20,
		Length:      1 << 20,
		Pieces:      make([]byte, 20),
	}, h)
	t.Cleanup(func() {
		if !c.isClosed.Load() {
			_ = c.Close()
		}
	})
	return stor, c, c.pieces[0]
}

// Saving settings with a different UseDisk value must not break the pieces that
// already exist: SetSettings replaces BTsets first and drops the torrents after,
// and in between the engine keeps writing and reading the old pieces. Picking
// the backend from the live setting made the other half of the piece nil, so the
// engine goroutine panicked and took the whole server down (upstream 9eb0f9f7).
func TestPieceKeepsItsBackendWhenUseDiskChanges(t *testing.T) {
	_, _, p := pieceFixture(t, false)
	settings.BTsets.UseDisk = true // was "disk" from now on; the piece is RAM

	data := bytes.Repeat([]byte{0x42}, 16<<10)
	if _, err := p.WriteAt(data, 0); err != nil {
		t.Fatalf("WriteAt after the setting change: %v", err)
	}
	got := make([]byte, len(data))
	if _, err := p.ReadAt(got, 0); err != nil {
		t.Fatalf("ReadAt after the setting change: %v", err)
	}
	if !bytes.Equal(got, data) {
		t.Fatal("ReadAt returned different data after the setting change")
	}

	// Release must free the RAM backend, not look for a disk one.
	p.Release()
	if p.mPiece.buffer != nil {
		t.Error("Release did not free the RAM buffer")
	}
}

// The mirror case: a piece created with UseDisk=true must survive the switch to
// RAM and keep reading its disk file.
func TestDiskPieceKeepsItsBackendWhenUseDiskChanges(t *testing.T) {
	_, _, p := pieceFixture(t, true)
	settings.BTsets.UseDisk = false // was "ram" from now on; the piece is on disk

	data := bytes.Repeat([]byte{0x7e}, 16<<10)
	if _, err := p.WriteAt(data, 0); err != nil {
		t.Fatalf("WriteAt after the setting change: %v", err)
	}
	got := make([]byte, len(data))
	if _, err := p.ReadAt(got, 0); err != nil {
		t.Fatalf("ReadAt after the setting change: %v", err)
	}
	if !bytes.Equal(got, data) {
		t.Fatal("ReadAt returned different data after the setting change")
	}
	p.Release()
}

// The backend choice is a property of the piece, not of the settings at call
// time: a second piece created after the switch must use the new one.
func TestNewPieceFollowsCurrentUseDisk(t *testing.T) {
	_, c, ramPiece := pieceFixture(t, false)
	if ramPiece.useDisk {
		t.Fatal("piece created in RAM mode reports the disk backend")
	}
	settings.BTsets.UseDisk = true
	diskPiece := NewPiece(1, c)
	if !diskPiece.useDisk {
		t.Fatal("piece created after the switch reports the RAM backend")
	}
	if diskPiece.dPiece == nil || diskPiece.mPiece != nil {
		t.Fatal("disk piece was not given exactly the disk backend")
	}
}
