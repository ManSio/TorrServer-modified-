package torrstor

import (
	"testing"

	"github.com/anacrolix/torrent/metainfo"

	"server/settings"
)

const benchPieceLen = 256 << 10

func benchCache(b *testing.B, useDisk bool, pieces int) (*Cache, []byte) {
	b.Helper()
	settings.BTsets = &settings.BTSets{
		UseDisk:           useDisk,
		TorrentsSavePath:  b.TempDir(),
		ReaderReadAheadMB: 16,
		ConnectionsLimit:  25,
		CacheHysteresis:   0.9,
	}
	stor := NewStorage(int64(pieces+2) * benchPieceLen)
	stor.WaitInitialScan()
	b.Cleanup(func() { _ = stor.Close() })

	c := NewCache(stor.capacity, stor)
	var h metainfo.Hash
	h[0] = 1
	info := &metainfo.Info{
		Name:        "bench",
		PieceLength: benchPieceLen,
		Length:      int64(pieces) * benchPieceLen,
		Pieces:      make([]byte, 20*pieces),
	}
	c.Init(info, h)
	b.Cleanup(func() { _ = c.Close() })

	buf := make([]byte, benchPieceLen)
	for i := range buf {
		buf[i] = byte(i)
	}
	return c, buf
}

func BenchmarkMemPieceWrite(b *testing.B) {
	c, buf := benchCache(b, false, 8)
	p := c.pieces[0]
	b.SetBytes(int64(len(buf)))
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, err := p.WriteAt(buf, 0); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkMemPieceRead64K(b *testing.B) {
	c, full := benchCache(b, false, 8)
	p := c.pieces[0]
	if _, err := p.WriteAt(full, 0); err != nil {
		b.Fatal(err)
	}
	chunk := make([]byte, 64<<10)
	b.SetBytes(int64(len(chunk)))
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		off := int64((i * len(chunk)) % len(full))
		if _, err := p.ReadAt(chunk, off); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkDiskPieceWrite(b *testing.B) {
	c, buf := benchCache(b, true, 8)
	p := c.pieces[0]
	b.SetBytes(int64(len(buf)))
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, err := p.WriteAt(buf, 0); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkDiskPieceRead64K(b *testing.B) {
	c, full := benchCache(b, true, 8)
	p := c.pieces[0]
	if _, err := p.WriteAt(full, 0); err != nil {
		b.Fatal(err)
	}
	chunk := make([]byte, 64<<10)
	b.SetBytes(int64(len(chunk)))
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		off := int64((i * len(chunk)) % len(full))
		if _, err := p.ReadAt(chunk, off); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkCacheGetRemPieces64(b *testing.B) {
	c, buf := benchCache(b, false, 64)
	for i := 0; i < 64; i++ {
		if _, err := c.pieces[i].WriteAt(buf, 0); err != nil {
			b.Fatal(err)
		}
		c.pieces[i].Accessed = 1
	}
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if got := c.getRemPieces(); len(got) == 0 {
			b.Fatal("nothing to evict")
		}
	}
}

func BenchmarkCacheGetState64(b *testing.B) {
	c, buf := benchCache(b, false, 64)
	for i := 0; i < 64; i++ {
		if _, err := c.pieces[i].WriteAt(buf, 0); err != nil {
			b.Fatal(err)
		}
	}
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = c.GetState()
	}
}

func BenchmarkCacheFilledBytes64(b *testing.B) {
	c, buf := benchCache(b, false, 64)
	for i := 0; i < 64; i++ {
		if _, err := c.pieces[i].WriteAt(buf, 0); err != nil {
			b.Fatal(err)
		}
	}
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = c.FilledBytes()
	}
}
