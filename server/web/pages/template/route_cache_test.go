package template

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
)

// TestIndexIsRevalidated is the #908 regression: the fixed-name index.html must
// not be cached for a year, and a matching If-None-Match must get 304.
func TestIndexIsRevalidated(t *testing.T) {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	RouteWebPages(r)

	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/", nil))
	if got := w.Header().Get("Cache-Control"); got != "no-cache" {
		t.Fatalf("Cache-Control = %q, want no-cache", got)
	}
	etag := w.Header().Get("ETag")
	if etag == "" || etag[0] != '"' {
		t.Fatalf("ETag = %q, want a quoted strong ETag", etag)
	}

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Header.Set("If-None-Match", etag)
	w = httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code != http.StatusNotModified {
		t.Fatalf("status = %d, want 304", w.Code)
	}
}
