package api

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
)

// TestSendM3UETagFollowsBody is the #938 regression: the ETag must change with
// the playlist body, otherwise clients keep a stale list.
func TestSendM3UETagFollowsBody(t *testing.T) {
	gin.SetMode(gin.TestMode)
	send := func(m3u, ifNoneMatch string) (int, http.Header, string) {
		w := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(w)
		c.Request = httptest.NewRequest(http.MethodGet, "/playlist", nil)
		if ifNoneMatch != "" {
			c.Request.Header.Set("If-None-Match", ifNoneMatch)
		}
		sendM3U(c, "movie.m3u", "08ada5a7", m3u)
		return c.Writer.Status(), w.Header(), w.Body.String()
	}

	_, header, _ := send("#EXTM3U\nhttp://h/stream/a.mkv\n", "")
	tag := header.Get("ETag")
	if tag == "" {
		t.Fatal("no ETag")
	}
	if status, _, _ := send("#EXTM3U\nhttp://h/stream/a.mkv\n", tag); status != http.StatusNotModified {
		t.Fatalf("same playlist: status = %d, want 304", status)
	}
	status, _, body := send("#EXTM3U\nhttp://h/stream/b.mkv\n", tag)
	if status != http.StatusOK || !strings.Contains(body, "b.mkv") {
		t.Fatalf("changed playlist with old ETag: status = %d, body %q", status, body)
	}
}
