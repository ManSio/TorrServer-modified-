package torr

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// The stream response used to force "Connection: close", so every small Range
// request - MKV Cues and index reads alone - paid a fresh TCP handshake. With
// dozens per second that is latency paid continuously, and it is what upstream
// TorrServer PR #739 removed (commit 8f897c4e).
func TestStreamHeadersDoNotForceConnectionClose(t *testing.T) {
	h := http.Header{}
	applyStreamHeaders(h)

	if v := h.Get("Connection"); strings.EqualFold(v, "close") {
		t.Errorf("stream still forces Connection: close; keep-alive is what removes the per-request handshake")
	}
}

// Removing that header is only safe if the response still tells the player it
// may issue Range requests. Dropping it silently would break seeking outright.
func TestStreamHeadersStillAdvertiseRanges(t *testing.T) {
	h := http.Header{}
	applyStreamHeaders(h)

	if got := h.Get("Accept-Ranges"); got != "bytes" {
		t.Errorf("Accept-Ranges is %q, want %q - without it players cannot seek", got, "bytes")
	}
}

// The guard against the obvious way to break seeking while touching these
// headers: a range request must still be answered as a partial response.
func TestRangeResponseIsPartial(t *testing.T) {
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/stream/movie.mkv?link=abc&index=1&play", nil)
	req.Header.Set("Range", "bytes=100-199")

	h := rec.Header()
	applyStreamHeaders(h)
	h.Set("Content-Range", "bytes 100-199/1000")
	rec.WriteHeader(http.StatusPartialContent)

	res := rec.Result()
	defer res.Body.Close()

	if res.StatusCode != http.StatusPartialContent {
		t.Errorf("range request answered with %d, want 206", res.StatusCode)
	}
	if cr := res.Header.Get("Content-Range"); !strings.HasPrefix(cr, "bytes 100-199/") {
		t.Errorf("Content-Range is %q, want it to describe the requested slice", cr)
	}
	if ar := res.Header.Get("Accept-Ranges"); ar != "bytes" {
		t.Errorf("Accept-Ranges is %q, want %q", ar, "bytes")
	}
}
