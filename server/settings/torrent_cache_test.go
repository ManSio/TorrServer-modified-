package settings

import (
	"testing"

	"github.com/anacrolix/torrent"
	"github.com/anacrolix/torrent/metainfo"
)

func TestListTorrentCacheInvalidated(t *testing.T) {
	old := tdb
	tdb = newMemDB()
	invalidateListTorrentCache()
	t.Cleanup(func() {
		tdb = old
		invalidateListTorrentCache()
	})

	var h1, h2 metainfo.Hash
	h1[0] = 1
	h2[0] = 2

	AddTorrent(&TorrentDB{TorrentSpec: &torrent.TorrentSpec{InfoHash: h1}, Title: "one", Timestamp: 1})
	if got := ListTorrent(); len(got) != 1 {
		t.Fatalf("after add: %d torrents", len(got))
	}

	// A second read is served from the cache.
	if got := ListTorrent(); len(got) != 1 {
		t.Fatalf("cached read: %d torrents", len(got))
	}

	AddTorrent(&TorrentDB{TorrentSpec: &torrent.TorrentSpec{InfoHash: h2}, Title: "two", Timestamp: 2})
	if got := ListTorrent(); len(got) != 2 {
		t.Fatalf("add did not invalidate the cache: %d torrents", len(got))
	}

	RemTorrent(h1)
	got := ListTorrent()
	if len(got) != 1 || got[0].InfoHash != h2 {
		t.Fatalf("rem did not invalidate the cache: %+v", got)
	}
}
