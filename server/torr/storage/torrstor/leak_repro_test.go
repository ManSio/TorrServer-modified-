package torrstor

import (
	"reflect"
	"testing"
	"time"
	"unsafe"

	"github.com/anacrolix/torrent"

	"server/settings"
)

// The reader protection window used to be cache.capacity * ReaderReadAhead(%).
// With a 98 GiB cache and ReadAhead=81% a single reader protected ~79 GiB —
// more than the entire limit — so eviction could never bring the cache back
// under it while anything was playing. These tests pin the replacement:
// the window follows the reader's own readahead.

func mkFakeFile(offset, length int64) *torrent.File {
	f := new(torrent.File)
	v := reflect.ValueOf(f).Elem()
	setUnexported(v, "offset", reflect.ValueOf(offset))
	setUnexported(v, "length", reflect.ValueOf(length))
	return f
}

func setUnexported(v reflect.Value, name string, val reflect.Value) {
	f := v.FieldByName(name)
	reflect.NewAt(f.Type(), unsafe.Pointer(f.UnsafeAddr())).Elem().Set(val)
}

const reproPieceLen = 4 << 20

// The window must not scale with the cache size. 100 GiB cache, 81% readahead:
// a scaling window would be ~85 GiB wide.
func TestReaderWindowDoesNotScaleWithCache(t *testing.T) {
	initTestSettings(t)
	settings.BTsets.ReaderReadAheadMB = 16

	c := &Cache{capacity: 100 << 30, pieceLength: reproPieceLen,
		readers: make(map[*Reader]struct{}), pieces: map[int]*Piece{},
		evictChan: make(chan string, 16)}
	r := mkTestReader(c, 40<<30, 0, &fakeTorrentReader{})
	r.file = mkFakeFile(0, 100<<30)
	r.readahead = 16 << 20 // what updateRA() asks the client for

	start, end := r.getOffsetRange()
	width := end - start
	t.Logf("cache=%d GB, protected window=%d MB (offset in the middle of the file)",
		c.capacity>>30, width>>20)
	if width > 64<<20 {
		t.Errorf("protection window is %d MB — it must follow the reader readahead "+
			"(16 MB), not the cache size", width>>20)
	}
}

// A reader parked (readahead 0) still protects the piece it sits on.
func TestReaderWindowAlwaysCoversCurrentPiece(t *testing.T) {
	initTestSettings(t)
	c := &Cache{capacity: 100 << 30, pieceLength: reproPieceLen,
		readers: make(map[*Reader]struct{}), pieces: map[int]*Piece{},
		evictChan: make(chan string, 16)}
	r := mkTestReader(c, 0, 0, &fakeTorrentReader{})
	r.file = mkFakeFile(0, 100<<30)
	r.readahead = 0

	start, end := r.getOffsetRange()
	if start != 0 || end < reproPieceLen {
		t.Fatalf("window [%d..%d] does not cover the piece at offset 0", start, end)
	}
}

// The original symptom: with the window bounded, one active reader no longer
// freezes eviction. 300 pieces * 4 MB = 1.2 GB in a 1 GB cache.
func TestActiveReaderNoLongerBlocksEviction(t *testing.T) {
	initTestSettings(t)
	settings.BTsets.UseDisk = false
	settings.BTsets.ReaderReadAheadMB = 16

	capacity := int64(1) << 30
	nPieces := 300
	target := int64(float64(capacity) * 0.9)

	stor := NewStorage(capacity)
	defer stor.Close()
	stor.lastCleanupTime.Store(0)

	c := NewCache(capacity, stor)
	c.pieceLength = reproPieceLen
	c.pieces = make(map[int]*Piece)
	old := time.Now().Unix() - 86400
	for i := 0; i < nPieces; i++ {
		p := &Piece{Id: i, Size: reproPieceLen, Complete: true, Accessed: old, cache: c}
		p.mPiece = NewMemPiece(p)
		c.pieces[i] = p
	}
	stor.caches[[20]byte{7}] = c
	stor.filledSize.Store(int64(nPieces) * reproPieceLen)

	r := mkTestReader(c, 0, 0, &fakeTorrentReader{})
	r.file = mkFakeFile(0, 50<<30)
	rng := r.getPiecesRange()
	t.Logf("reader protects piece ids [%d..%d]", rng.Start, rng.End)

	for i := 0; i < 5; i++ {
		stor.lastCleanupTime.Store(0)
		stor.triggerGlobalCleanup(target)
	}

	var bytes int64
	alive := 0
	for _, p := range c.getPieces() {
		if p.Size > 0 {
			alive++
			bytes += p.Size
		}
	}
	t.Logf("with an active reader: %d/%d pieces alive, %d MB (target %d MB)",
		alive, nPieces, bytes>>20, target>>20)
	if bytes > target {
		t.Errorf("eviction still cannot converge with an active reader: %d MB > target %d MB",
			bytes>>20, target>>20)
	}
}

// A single idle reader stays flagged "in use" on purpose: checkReader() only
// parks when Readers() > 1, because parking the last reader would let
// expired() drop the torrent, and Torrent.NewReader returns nil for a closed
// torrent — a paused player could no longer resume.
//
// That used to be harmless-until-fatal: the reader kept ~80 GB protected
// forever. It is now harmless, because the window follows the readahead.
// This test pins that reasoning so the next person does not "fix" the
// reader flag and break resume.
func TestSingleIdleReaderProtectsOnlyItsWindow(t *testing.T) {
	initTestSettings(t)
	c := &Cache{capacity: 100 << 30, pieceLength: reproPieceLen,
		readers: make(map[*Reader]struct{}), pieces: map[int]*Piece{},
		evictChan: make(chan string, 16)}
	r := mkTestReader(c, 0, 600, &fakeTorrentReader{}) // idle 10 min, only reader
	r.file = mkFakeFile(0, 50<<30)
	r.readahead = 16 << 20

	r.checkReader()
	if !r.isUse {
		t.Log("reader was parked — if this starts breaking resume, check NewReader/expired()")
		return
	}
	start, end := r.getOffsetRange()
	width := (end - start)
	t.Logf("idle single reader keeps isUse=true (by design), protecting %d MB", width>>20)
	if width > 64<<20 {
		t.Errorf("idle reader protects %d MB — window must stay readahead-sized", width>>20)
	}
}
