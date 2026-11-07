package settings

import (
	"encoding/json"
	"fmt"
	"testing"

	"github.com/anacrolix/torrent"
	"github.com/anacrolix/torrent/metainfo"
)

func seedTorrents(b *testing.B, n int) {
	b.Helper()
	old := tdb
	db := newMemDB()
	for i := 0; i < n; i++ {
		var h metainfo.Hash
		h[0] = byte(i)
		torr := &TorrentDB{
			TorrentSpec: &torrent.TorrentSpec{InfoHash: h},
			Title:       fmt.Sprintf("Torrent %d %s", i, "title-padding-xxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxx"),
			Data:        `{"prisma":true,"overview":"` + "overview-padding-" + `"}`,
			Timestamp:   int64(i),
		}
		buf, err := json.Marshal(torr)
		if err != nil {
			b.Fatal(err)
		}
		db.Set("Torrents", h.HexString(), buf)
	}
	tdb = db
	b.Cleanup(func() { tdb = old })
}

func BenchmarkListTorrent(b *testing.B) {
	seedTorrents(b, 50)
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if got := ListTorrent(); len(got) != 50 {
			b.Fatalf("got %d torrents", len(got))
		}
	}
}
