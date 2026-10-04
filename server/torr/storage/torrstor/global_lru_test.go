package torrstor

import (
	"testing"
	"time"

	"server/settings"
)

// TestGlobalLRUOldestFirst: two caches, old pieces must go first,
// total must converge to target, counter must match reality.
func TestGlobalLRUOldestFirst(t *testing.T) {
	initTestSettings(t)
	settings.BTsets.UseDisk = false
	// 100MB limit, target 90MB
	stor := NewStorage(100 * 1024 * 1024)
	defer stor.Close()
	stor.lastCleanupTime.Store(0)

	now := time.Now().Unix()
	mkCache := func(nPieces int, sizeEach int64, accessed int64) *Cache {
		c := NewCache(stor.capacity, stor)
		c.pieceLength = 8 * 1024 * 1024
		c.pieces = make(map[int]*Piece)
		for i := 0; i < nPieces; i++ {
			p := &Piece{Id: i, Size: sizeEach, Accessed: accessed, cache: c}
			p.mPiece = NewMemPiece(p)
			// pre-fill mem buffer so Release accounting is consistent
			p.mPiece.buffer = make([]byte, sizeEach)
			c.pieces[i] = p
		}
		return c
	}

	// Old cache: 10 x 8MB = 80MB, accessed 1h ago
	old := mkCache(10, 8*1024*1024, now-3600)
	// New cache: 10 x 8MB = 80MB, accessed now
	fresh := mkCache(10, 8*1024*1024, now)
	stor.caches[[20]byte{1}] = old
	stor.caches[[20]byte{2}] = fresh
	stor.filledSize.Store(160 * 1024 * 1024)

	stor.triggerGlobalCleanup(90 * 1024 * 1024)

	// Old pieces must be mostly gone (cleanup stops once under target),
	// fresh must survive untouched.
	evictedOld := 0
	for _, p := range old.getPieces() {
		if p.Size == 0 {
			evictedOld++
		}
	}
	if evictedOld == 0 {
		t.Error("no old pieces evicted — LRU order broken")
	}
	alive := 0
	for _, p := range fresh.getPieces() {
		if p.Size > 0 {
			alive++
		}
	}
	if alive == 0 {
		t.Error("fresh pieces were evicted — active data protection broken")
	}
	// Counter must equal real sum of remaining pieces
	var real int64
	for _, c := range []*Cache{old, fresh} {
		for _, p := range c.getPieces() {
			real += p.Size
		}
	}
	if got := stor.getFilledSize(); got != real {
		t.Errorf("counter desync: counter=%d real=%d", got, real)
	}
	if real > 90*1024*1024 {
		t.Errorf("did not converge to target: %d > 90MB", real)
	}
	t.Logf("✅ Global LRU: old evicted, fresh kept=%d, total=%dMB", alive, real/1024/1024)
}

// TestIdlePauseProtection: pieces touched 2 min ago (paused video)
// must survive — the old 60s guard wiped them live (E03 @ 0/605).
func TestIdlePauseProtection(t *testing.T) {
	initTestSettings(t)
	settings.BTsets.UseDisk = false
	stor := NewStorage(100 * 1024 * 1024)
	defer stor.Close()
	stor.lastCleanupTime.Store(0)

	now := time.Now().Unix()
	mkCache := func(nPieces int, sizeEach int64, accessed int64) *Cache {
		c := NewCache(stor.capacity, stor)
		c.pieceLength = 8 * 1024 * 1024
		c.pieces = make(map[int]*Piece)
		for i := 0; i < nPieces; i++ {
			p := &Piece{Id: i, Size: sizeEach, Accessed: accessed, cache: c}
			p.mPiece = NewMemPiece(p)
			p.mPiece.buffer = make([]byte, sizeEach)
			c.pieces[i] = p
		}
		return c
	}

	// 12 x 8MB = 96MB old (1h) + 4 x 8MB = 32MB paused 2 min ago = 128MB > 90MB target
	old := mkCache(12, 8*1024*1024, now-3600)
	paused := mkCache(4, 8*1024*1024, now-120)
	stor.caches[[20]byte{1}] = old
	stor.caches[[20]byte{2}] = paused
	stor.filledSize.Store(128 * 1024 * 1024)

	stor.triggerGlobalCleanup(90 * 1024 * 1024)

	kept := 0
	for _, p := range paused.getPieces() {
		if p.Size > 0 {
			kept++
		}
	}
	if kept != 4 {
		t.Errorf("paused pieces evicted: kept %d/4 (120s idle must survive 300s guard)", kept)
	}
	t.Log("✅ paused-video pieces protected")
}

// TestEvictsCompleteFirst: partially downloaded pieces survive while
// complete candidates exist (upstream PR #875 port). Evicting a partial
// deletes bytes the client still counts -> zeros on re-read + hash fail.
func TestEvictsCompleteFirst(t *testing.T) {
	initTestSettings(t)
	settings.BTsets.UseDisk = false
	stor := NewStorage(100 * 1024 * 1024)
	defer stor.Close()
	stor.lastCleanupTime.Store(0)

	now := time.Now().Unix()
	mkPiece := func(c *Cache, id int, complete bool) *Piece {
		p := &Piece{Id: id, Size: 8 * 1024 * 1024, Accessed: now - 3600,
			Complete: complete, cache: c}
		p.mPiece = NewMemPiece(p)
		p.mPiece.buffer = make([]byte, 8*1024*1024)
		return p
	}

	// 6 complete + 6 partial, all same age: 96MB total, 90MB target
	// -> exactly 1 piece must go, and it must be a complete one.
	c := NewCache(stor.capacity, stor)
	c.pieceLength = 8 * 1024 * 1024
	c.pieces = make(map[int]*Piece)
	for i := 0; i < 6; i++ {
		c.pieces[i] = mkPiece(c, i, true)
		c.pieces[6+i] = mkPiece(c, 6+i, false)
	}
	stor.caches[[20]byte{9}] = c
	stor.filledSize.Store(96 * 1024 * 1024)

	stor.triggerGlobalCleanup(90 * 1024 * 1024)

	zeroed := []int{}
	for id, p := range c.getPieces() {
		if p.Size == 0 {
			zeroed = append(zeroed, id)
		}
	}
	if len(zeroed) != 1 {
		t.Fatalf("expected exactly 1 eviction, got %d", len(zeroed))
	}
	// The evicted piece was complete (ids 0-5); partials 6-11 survive.
	if zeroed[0] > 5 {
		t.Fatalf("evicted partial piece %d while complete candidates existed", zeroed[0])
	}
	partialAlive := 0
	for id := 6; id < 12; id++ {
		if c.getPieces()[id].Size > 0 {
			partialAlive++
		}
	}
	if partialAlive != 6 {
		t.Fatalf("partial pieces lost: %d/6 alive", partialAlive)
	}
	t.Log("✅ complete evicted first, partials kept")
}

// TestRequestCleanupNonBlocking: hot path signal must never block.
func TestRequestCleanupNonBlocking(t *testing.T) {
	stor := NewStorage(100 * 1024 * 1024)
	defer stor.Close()
	done := make(chan struct{})
	go func() {
		for i := 0; i < 10000; i++ {
			stor.requestCleanup()
		}
		close(done)
	}()
	select {
	case <-done:
		t.Log("✅ requestCleanup non-blocking under 10k signals")
	case <-time.After(5 * time.Second):
		t.Fatal("requestCleanup blocked the hot path")
	}
}

// TestReleaseNilTorrent: Release must not panic without torrent (Aug 29 panics).
func TestReleaseNilTorrent(t *testing.T) {
	initTestSettings(t)
	settings.BTsets.UseDisk = false
	stor := NewStorage(100 * 1024 * 1024)
	defer stor.Close()
	c := NewCache(stor.capacity, stor)
	c.pieceLength = 8 * 1024 * 1024
	c.evictChan = make(chan string, 4096)
	p := &Piece{Id: 0, Size: 1024, Accessed: time.Now().Unix(), cache: c}
	p.mPiece = NewMemPiece(p)
	p.mPiece.buffer = make([]byte, 1024)
	defer func() {
		if r := recover(); r != nil {
			t.Fatalf("Release panicked with nil torrent: %v", r)
		}
	}()
	p.Release()
	if p.Size != 0 {
		t.Error("Release did not zero size")
	}
	t.Log("✅ Release nil-torrent safe")
}
