package torrstor

import (
	"testing"

	"github.com/anacrolix/torrent/metainfo"

	"server/settings"
)

// Guard: `filledSize` is NOT the sum of `p.Size`, and must never be substituted
// for GetState().Filled.
//
// The two counters answer different questions:
//
//	filledSize — what the cache is charged against `capacity`, i.e. what the
//	             memory/disk limit cares about. MemPiece adds the whole
//	             allocated buffer (mempiece.go), DiskPiece the file size.
//	sum(p.Size) — bytes of data actually written into those pieces, which is
//	             what "how much is preloaded" means.
//
// They coincide with UseDisk=true (production) and diverge with it off: a
// partially written piece is charged a full pieceLength to the limit but holds
// fewer bytes. This test exists because a "make it O(1)" optimisation swapped
// one for the other and measured 1024 against 100 in RAM mode.
func TestFilledIsNotTheFilledSize(t *testing.T) {
	initTestSettings(t)

	t.Run("ram mode diverges", func(t *testing.T) {
		settings.BTsets.UseDisk = false

		stor := NewStorage(1 << 20)
		var hash metainfo.Hash
		hash[0] = 1
		stor.OpenTorrent(testInfo(), hash)
		cache := stor.GetCache(hash)
		if cache == nil {
			t.Fatal("no cache")
		}
		defer cache.Close()

		pieces := cache.getPieces()
		if _, err := pieces[0].WriteAt(make([]byte, 100), 0); err != nil {
			t.Fatalf("WriteAt: %v", err)
		}

		charged := cache.filledSize.Load()
		data := int64(0)
		for _, p := range cache.getPieces() {
			data += p.Size
		}

		if charged <= data {
			t.Errorf("ram mode: charged %d should exceed data written %d", charged, data)
		}
		if st := cache.GetState(); st.Filled != data {
			t.Errorf("GetState().Filled = %d, want the data written %d", st.Filled, data)
		}
	})

	t.Run("disk mode agrees", func(t *testing.T) {
		settings.BTsets.UseDisk = true
		dir := t.TempDir()
		settings.BTsets.TorrentsSavePath = dir
		defer func() {
			settings.BTsets.UseDisk = false
			settings.BTsets.TorrentsSavePath = ""
		}()

		stor := NewStorage(1 << 20)
		var hash metainfo.Hash
		hash[0] = 2
		stor.OpenTorrent(testInfo(), hash)
		cache := stor.GetCache(hash)
		if cache == nil {
			t.Fatal("no cache")
		}
		defer cache.Close()

		pieces := cache.getPieces()
		if _, err := pieces[0].WriteAt(make([]byte, 100), 0); err != nil {
			t.Fatalf("WriteAt: %v", err)
		}

		charged := cache.filledSize.Load()
		data := int64(0)
		for _, p := range cache.getPieces() {
			data += p.Size
		}
		if charged != data {
			t.Errorf("disk mode: charged %d != data written %d, they should agree", charged, data)
		}
		if st := cache.GetState(); st.Filled != data {
			t.Errorf("GetState().Filled = %d, want %d", st.Filled, data)
		}
	})
}
