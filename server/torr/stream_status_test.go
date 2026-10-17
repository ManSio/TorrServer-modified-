package torr

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	sets "server/settings"
	"server/torr/state"
)

// Regression test for the empty-200 stream response (upstream YouROK/TorrServer
// PR #845 / commit ef4e461d).
//
// Stream used to return an error without writing anything after its checks, so
// gin emitted an empty 200 OK and players read that as a zero-length file.
//
// Stat = TorrentPreload makes GotInfo() report true without touching the
// torrent, and Status() then lists no files — exactly the state left behind
// when a torrent is dropped while a preload is still in flight. Stream must
// answer 404, not 200.
//
// Verified to fail before the fix (status 200, empty body) and pass after.
func TestStreamNotFoundAnswers404(t *testing.T) {
	sets.BTsets = &sets.BTSets{}
	defer func() { sets.BTsets = nil }()

	tr := &Torrent{}
	tr.Stat = state.TorrentPreload

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/stream?index=1&play", nil)

	err := tr.Stream(1, req, rec)
	if err == nil {
		t.Fatalf("Stream returned nil error for a missing file")
	}
	res := rec.Result()
	defer res.Body.Close()
	if res.StatusCode != http.StatusNotFound {
		t.Fatalf("status = %d, want 404 (an empty 200 is read as a zero-length file)", res.StatusCode)
	}
	if !strings.Contains(rec.Body.String(), "not found") {
		t.Errorf("body = %q, want it to name the missing file", rec.Body.String())
	}
}
