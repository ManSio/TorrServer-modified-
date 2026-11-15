package torrstor

import (
	"bytes"
	"testing"
	"time"

	"github.com/anacrolix/torrent"
	"github.com/anacrolix/torrent/bencode"
	"github.com/anacrolix/torrent/metainfo"

	"server/settings"
)

// passFixture builds a cache attached to a real torrent client, because the
// cost this test guards against is per-piece client calls (PieceState /
// SetPriority), not plain map iteration.
func passFixture(tb testing.TB, pieces int) (*torrent.Torrent, *Cache) {
	return passFixtureInterval(tb, pieces, defaultPassInterval)
}

func passFixtureInterval(tb testing.TB, pieces int, interval time.Duration) (*torrent.Torrent, *Cache) {
	tb.Helper()
	prev := settings.BTsets
	tb.Cleanup(func() { settings.BTsets = prev })
	settings.BTsets = &settings.BTSets{
		UseDisk:          false,
		TorrentsSavePath: tb.TempDir(),
		ConnectionsLimit: 25,
		CacheHysteresis:  0.9,
	}

	stor := NewStorage(64 << 20)
	stor.WaitInitialScan()
	tb.Cleanup(func() { _ = stor.Close() })

	cfg := torrent.NewDefaultClientConfig()
	cfg.DefaultStorage = stor
	cfg.NoDHT = true
	cfg.DisableTCP = true
	cfg.DisableUTP = true
	cfg.DisablePEX = true
	cfg.ListenPort = 0
	cfg.NoUpload = true
	cl, err := torrent.NewClient(cfg)
	if err != nil {
		tb.Fatalf("client: %v", err)
	}
	tb.Cleanup(cl.Close)

	info := &metainfo.Info{
		Name:        "pass.bin",
		PieceLength: 2 << 20,
		Length:      int64(pieces) * (2 << 20),
		Pieces:      make([]byte, 20*pieces),
	}
	var buf bytes.Buffer
	if err := bencode.NewEncoder(&buf).Encode(info); err != nil {
		tb.Fatalf("encode info: %v", err)
	}
	tt, err := cl.AddTorrent(&metainfo.MetaInfo{InfoBytes: buf.Bytes()})
	if err != nil {
		tb.Fatalf("add torrent: %v", err)
	}
	<-tt.GotInfo()

	c := NewCache(stor.capacity, stor)
	c.passInterval = interval
	c.Init(info, tt.InfoHash())
	c.torrent = tt
	tb.Cleanup(func() {
		if !c.isClosed.Load() {
			_ = c.Close()
		}
	})
	return tt, c
}

func raisePieces(c *Cache, tt *torrent.Torrent, ids ...int) {
	c.muPrio.Lock()
	defer c.muPrio.Unlock()
	for _, id := range ids {
		tt.Piece(id).SetPriority(torrent.PiecePriorityNormal)
		c.prioritized[id] = struct{}{}
	}
}

// A pass must not walk the whole torrent when this cache raised nothing: on a
// 92 245 piece remux the full sweep cost 88 ms of client-lock calls per seek,
// and a seek used to run one pass (issue #968).
func TestPassDoesNotSweepPiecesWhenNothingRaised(t *testing.T) {
	_, c := passFixture(t, 2000)

	c.getRemPieces()

	if got := c.prioScanned.Load(); got != 0 {
		t.Fatalf("clearPriority visited %d pieces with nothing raised, want 0", got)
	}
}

func TestClearPriorityUndoesRaisedPieces(t *testing.T) {
	tt, c := passFixture(t, 64)
	raisePieces(c, tt, 0, 1, 2)

	c.getRemPieces()

	for _, id := range []int{0, 1, 2} {
		if p := tt.PieceState(id); p.Priority != torrent.PiecePriorityNone {
			t.Errorf("piece %d priority = %v, want None after the pass", id, p.Priority)
		}
	}
	if n := len(c.prioritized); n != 0 {
		t.Errorf("raised set holds %d ids after the pass, want 0", n)
	}
	if got := c.prioScanned.Load(); got != 3 {
		t.Errorf("clearPriority visited %d pieces, want the 3 raised ones", got)
	}
}

// Pieces still inside a reader window must keep their priority, otherwise the
// stream starves right after a seek.
func TestClearPriorityKeepsPiecesInReaderWindow(t *testing.T) {
	tt, c := passFixture(t, 64)
	files := tt.Files()
	if len(files) == 0 {
		t.Skip("torrent has no files")
	}
	r := c.NewReader(files[0])
	defer c.CloseReader(r)

	raisePieces(c, tt, 0)
	c.clearPriority()

	if p := tt.PieceState(0); p.Priority == torrent.PiecePriorityNone {
		t.Error("piece 0 lost its priority although it is inside the reader window")
	}
	if _, ok := c.prioritized[0]; !ok {
		t.Error("raised set dropped piece 0 although it is inside the reader window")
	}
}

// Scrubbing used to spawn one pass per seek and per reader close. Requests must
// collapse into at most one pass per minPassInterval, and the last request must
// still be served.
func TestRequestPassCoalescesBursts(t *testing.T) {
	const interval = 150 * time.Millisecond
	_, c := passFixtureInterval(t, 64, interval)
	base := c.passesRun.Load()

	for i := 0; i < 100; i++ {
		c.requestPass()
	}
	time.Sleep(500 * time.Millisecond)

	got := c.passesRun.Load() - base
	if got < 1 {
		t.Fatal("burst of 100 requests ran no pass at all")
	}
	if got > 2 {
		t.Fatalf("burst of 100 requests ran %d passes, want at most 2", got)
	}

	c.requestPass()
	time.Sleep(2*interval + 200*time.Millisecond)
	if n := c.passesRun.Load() - base; n != got+1 {
		t.Errorf("after an idle request: %d passes total, want %d", n, got+1)
	}
}

// The worker must stop with the cache: it used to keep retrying forever and held
// the whole Cache (and its LRU list) after the torrent was removed.
func TestPassWorkerStopsOnClose(t *testing.T) {
	_, c := passFixture(t, 32)
	if err := c.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}
	ran := c.passesRun.Load()

	c.requestPass()
	time.Sleep(3 * defaultPassInterval)

	if got := c.passesRun.Load(); got != ran {
		t.Fatalf("pass ran after Close (%d -> %d): the worker outlived the cache", ran, got)
	}
}

// WaitInfo calls SetTorrent on every stream request. Wiping the raised set on
// the same torrent left those priorities raised forever, because the clear pass
// then had nothing to undo.
func TestSetTorrentSameKeepsRaisedSet(t *testing.T) {
	tt, c := passFixture(t, 64)
	raisePieces(c, tt, 0, 1)

	c.SetTorrent(tt)

	if n := len(c.prioritized); n != 2 {
		t.Fatalf("raised set holds %d ids after SetTorrent(same), want 2", n)
	}
	c.getRemPieces()
	for _, id := range []int{0, 1} {
		if p := tt.PieceState(id); p.Priority != torrent.PiecePriorityNone {
			t.Errorf("piece %d priority = %v after the pass, want None", id, p.Priority)
		}
	}
}

// An evicted piece has no priority left to clear; leaving its id behind would
// grow the raised set for the lifetime of the torrent.
func TestRemovePieceDropsRaisedEntry(t *testing.T) {
	_, c := passFixture(t, 8)
	p := c.pieces[0]
	p.mPiece = NewMemPiece(p)

	c.muPrio.Lock()
	c.prioritized[0] = struct{}{}
	c.muPrio.Unlock()

	c.removePiece(p)

	if _, ok := c.prioritized[0]; ok {
		t.Fatal("removed piece kept its raised entry")
	}
}

// BenchmarkPass92245 records the cost of one pass on a remux-sized torrent.
func BenchmarkPass92245(b *testing.B) {
	tt, c := passFixture(b, 92245)
	raisePieces(c, tt, 0, 1, 2, 3, 4)
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		c.getRemPieces()
		// Keep a couple of pieces raised so the sweep has work to undo.
		raisePieces(c, tt, 0, 1)
	}
}
