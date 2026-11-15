package api

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"

	sets "server/settings"
	"server/torr"
)

func apiContext(method, target, body string) (*gin.Context, *httptest.ResponseRecorder) {
	gin.SetMode(gin.TestMode)
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(method, target, strings.NewReader(body))
	if body != "" {
		c.Request.Header.Set("Content-Type", "application/json")
	}
	return c, w
}

// status reads gin's recorded status. A handler that only calls c.Status()
// does not flush the recorder, so w.Code stays 200 until a body is written.
func status(c *gin.Context, w *httptest.ResponseRecorder) int {
	if s := c.Writer.Status(); s != http.StatusOK {
		return s
	}
	return w.Code
}

func wantStatus(t *testing.T, c *gin.Context, w *httptest.ResponseRecorder, want int, label string) {
	t.Helper()
	if got := status(c, w); got != want {
		t.Fatalf("%s: status %d, want %d (body %q)", label, got, want, w.Body.String())
	}
}

func TestSearchEndpointsDisabled(t *testing.T) {
	old := sets.BTsets
	sets.BTsets = &sets.BTSets{EnableRutorSearch: false, EnableTorznabSearch: false}
	t.Cleanup(func() { sets.BTsets = old })

	c, w := apiContext(http.MethodGet, "/search?query=x", "")
	rutorSearch(c)
	wantStatus(t, c, w, http.StatusBadRequest, "rutor disabled")

	c, w = apiContext(http.MethodGet, "/torznab/search?query=x", "")
	torznabSearch(c)
	wantStatus(t, c, w, http.StatusBadRequest, "torznab disabled")
}

func TestTMDBAndGStreamerSettings(t *testing.T) {
	old := sets.BTsets
	sets.BTsets = nil
	c, w := apiContext(http.MethodGet, "/tmdb/settings", "")
	tmdbSettings(c)
	wantStatus(t, c, w, http.StatusInternalServerError, "tmdb nil settings")

	sets.BTsets = &sets.BTSets{TMDBSettings: sets.TMDBConfig{APIKey: "k"}}
	t.Cleanup(func() { sets.BTsets = old })
	c, w = apiContext(http.MethodGet, "/tmdb/settings", "")
	tmdbSettings(c)
	wantStatus(t, c, w, http.StatusOK, "tmdb get")
	if !strings.Contains(w.Body.String(), `"APIKey":"k"`) {
		t.Fatalf("tmdb body: %s", w.Body.String())
	}

	c, w = apiContext(http.MethodGet, "/gst/settings", "")
	GetGStreamerSettings(c)
	wantStatus(t, c, w, http.StatusOK, "gst get")
	if !strings.Contains(w.Body.String(), `"built_in":false`) {
		t.Fatalf("gst body: %s", w.Body.String())
	}
	c, w = apiContext(http.MethodPost, "/gst/settings", "{}")
	UpdateGStreamerSettings(c)
	wantStatus(t, c, w, http.StatusNotFound, "gst update")
}

func TestStorageSettingsReadOnlyAndValidation(t *testing.T) {
	oldReadOnly := sets.ReadOnly
	old := sets.BTsets
	sets.BTsets = &sets.BTSets{}
	sets.ReadOnly = true
	t.Cleanup(func() { sets.ReadOnly = oldReadOnly; sets.BTsets = old })

	c, w := apiContext(http.MethodGet, "/storage/settings", "")
	GetStorageSettings(c)
	wantStatus(t, c, w, http.StatusOK, "storage get")

	c, w = apiContext(http.MethodPost, "/storage/settings", `{"settings":"x"}`)
	UpdateStorageSettings(c)
	wantStatus(t, c, w, http.StatusForbidden, "read-only")

	sets.ReadOnly = false
	c, w = apiContext(http.MethodPost, "/storage/settings", `{"settings":"x"}`)
	UpdateStorageSettings(c)
	wantStatus(t, c, w, http.StatusBadRequest, "invalid value")

	c, w = apiContext(http.MethodPost, "/storage/settings", `{`)
	UpdateStorageSettings(c)
	wantStatus(t, c, w, http.StatusBadRequest, "invalid json")
}

func TestCacheAndTorrentsRejectMissingOrMalformedHash(t *testing.T) {
	old := sets.BTsets
	sets.BTsets = &sets.BTSets{}
	t.Cleanup(func() { sets.BTsets = old })

	if got := torr.GetTorrent("xyz"); got != nil {
		t.Fatalf("GetTorrent(xyz) = %v, want nil", got)
	}

	c, w := apiContext(http.MethodPost, "/cache", `{"action":"get"}`)
	cache(c)
	wantStatus(t, c, w, http.StatusBadRequest, "cache empty hash")

	c, w = apiContext(http.MethodPost, "/cache", `{"action":"get","hash":"xyz"}`)
	cache(c)
	wantStatus(t, c, w, http.StatusNotFound, "cache malformed hash")

	c, w = apiContext(http.MethodPost, "/torrents", `{"action":"get","hash":"xyz"}`)
	torrents(c)
	wantStatus(t, c, w, http.StatusNotFound, "torrents malformed hash")

	for _, body := range []string{
		`{"action":"add"}`,
		`{"action":"set"}`,
		`{"action":"rem"}`,
		`{"action":"drop"}`,
	} {
		c, w = apiContext(http.MethodPost, "/torrents", body)
		torrents(c)
		wantStatus(t, c, w, http.StatusBadRequest, body)
	}
}

func TestStreamAndPlayRejectMissingParams(t *testing.T) {
	c, w := apiContext(http.MethodGet, "/stream", "")
	stream(c)
	wantStatus(t, c, w, http.StatusBadRequest, "stream no link")

	c, w = apiContext(http.MethodGet, "/play//", "")
	c.Params = gin.Params{{Key: "hash", Value: ""}, {Key: "id", Value: ""}}
	play(c)
	wantStatus(t, c, w, http.StatusNotFound, "play empty")
}

func TestViewedRequiresHash(t *testing.T) {
	for _, body := range []string{`{"action":"set"}`, `{"action":"rem"}`} {
		c, w := apiContext(http.MethodPost, "/viewed", body)
		viewed(c)
		wantStatus(t, c, w, http.StatusBadRequest, body)
	}
}
