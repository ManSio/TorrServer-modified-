package api

import (
	"net/http"
	"os"
	"strings"
	"testing"

	"github.com/anacrolix/torrent"
	"github.com/anacrolix/torrent/metainfo"

	sets "server/settings"
	"server/torr"
)

func TestMain(m *testing.M) {
	dir, err := os.MkdirTemp("", "torrserver-api-test-")
	if err != nil {
		panic(err)
	}
	sets.Path = dir
	sets.InitSets(false, false)
	torr.InitApiHelper(torr.NewBTS())

	code := m.Run()

	sets.CloseDB()
	_ = os.RemoveAll(dir)
	os.Exit(code)
}

func TestTorrentsListEmpty(t *testing.T) {
	c, w := apiContext(http.MethodPost, "/torrents", `{"action":"list"}`)
	torrents(c)
	wantStatus(t, c, w, http.StatusOK, "torrents list")
	if strings.TrimSpace(w.Body.String()) != "[]" {
		t.Fatalf("empty list body %q", w.Body.String())
	}
}

func TestTorrentsListIncludesDBTorrent(t *testing.T) {
	var h metainfo.Hash
	h[0] = 0x42
	sets.AddTorrent(&sets.TorrentDB{
		TorrentSpec: &torrent.TorrentSpec{InfoHash: h},
		Title:       "db-only",
	})
	t.Cleanup(func() { sets.RemTorrent(h) })

	c, w := apiContext(http.MethodPost, "/torrents", `{"action":"list"}`)
	torrents(c)
	wantStatus(t, c, w, http.StatusOK, "torrents list with db")
	if !strings.Contains(w.Body.String(), h.HexString()) {
		t.Fatalf("db torrent missing from %s", w.Body.String())
	}
}

func TestSettingsGetReturnsCurrent(t *testing.T) {
	c, w := apiContext(http.MethodPost, "/settings", `{"action":"get"}`)
	settings(c)
	wantStatus(t, c, w, http.StatusOK, "settings get")
	if !strings.Contains(w.Body.String(), `"CacheSize"`) {
		t.Fatalf("settings body %q", w.Body.String())
	}
}

func TestViewedLifecycle(t *testing.T) {
	body := `{"action":"set","hash":"abc","file_index":2,"timecode":12.5}`
	c, w := apiContext(http.MethodPost, "/viewed", body)
	viewed(c)
	wantStatus(t, c, w, http.StatusOK, "viewed set")

	c, w = apiContext(http.MethodPost, "/viewed", `{"action":"list","hash":"abc"}`)
	viewed(c)
	wantStatus(t, c, w, http.StatusOK, "viewed list")
	if !strings.Contains(w.Body.String(), `"file_index":2`) {
		t.Fatalf("viewed list %q", w.Body.String())
	}

	c, w = apiContext(http.MethodPost, "/viewed", `{"action":"rem","hash":"abc","file_index":2}`)
	viewed(c)
	wantStatus(t, c, w, http.StatusOK, "viewed rem")

	c, w = apiContext(http.MethodPost, "/viewed", `{"action":"list","hash":"abc"}`)
	viewed(c)
	wantStatus(t, c, w, http.StatusOK, "viewed list after rem")
	afterBody := strings.TrimSpace(w.Body.String())
	if afterBody != "[]" && afterBody != "null" {
		t.Fatalf("viewed not removed: %q", afterBody)
	}
}
