package torznab

import (
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"server/version"
)

// Behavioral test for the Torznab backport (#919).
//
// Two defects at once, both observable here:
//   - the old code appended "api" to the configured host verbatim, so a host
//     stored as ".../api" was requested as ".../api/api";
//   - it used http.Get, so indexers behind a WAF answered 403 (no User-Agent).
//
// The fake indexer is given a host that ALREADY carries "/api" — the exact
// configuration that broke — and asserts the path it was actually asked for and
// the User-Agent it received.
//
// Verified to fail before the fix (path /api/api, UA Go-http-client/1.1) and
// pass after.
func TestSearchOneHitsAPIOnceAndSendsUserAgent(t *testing.T) {
	var gotPath, gotUA string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotUA = r.Header.Get("User-Agent")
		w.Header().Set("Content-Type", "application/xml")
		_, _ = io.WriteString(w, `<rss><channel><item><title>Some.Movie.2024</title>`+
			`<link>magnet:?xt=urn:btih:0123456789abcdef0123456789abcdef01234567</link>`+
			`</item></channel></rss>`)
	}))
	defer srv.Close()

	// Host already carries the "/api" segment.
	res := searchOne(srv.URL+"/api", "key", "query")
	if len(res) != 1 {
		t.Fatalf("results = %d, want 1", len(res))
	}
	if gotPath != "/api" {
		t.Errorf("indexer path = %q, want /api (old code requested /api/api)", gotPath)
	}
	wantUA := "TorrServer-Torznab/" + version.Version
	if gotUA != wantUA {
		t.Errorf("User-Agent = %q, want %q", gotUA, wantUA)
	}
}

// Negative control: the harness must be able to observe a failure. An indexer
// that refuses the request must yield no results, not a silent success.
func TestSearchOneReturnsNilOnForbidden(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusForbidden)
	}))
	defer srv.Close()

	if res := searchOne(srv.URL, "key", "query"); res != nil {
		t.Fatalf("expected nil on 403, got %d results", len(res))
	}
}
