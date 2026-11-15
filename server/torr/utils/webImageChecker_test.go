package utils

import (
	"bytes"
	"image"
	"image/color"
	"image/png"
	"net/http"
	"net/http/httptest"
	"testing"
)

// Several image hosts answer the default Go-http-client agent with 404, so the
// poster was reported as broken even though the URL served a valid image.
func TestCheckImgUrlSendsBrowserUserAgent(t *testing.T) {
	var pngBuf bytes.Buffer
	img := image.NewRGBA(image.Rect(0, 0, 2, 2))
	img.Set(0, 0, color.RGBA{R: 255, A: 255})
	if err := png.Encode(&pngBuf, img); err != nil {
		t.Fatalf("encode png: %v", err)
	}

	var gotAgent string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAgent = r.UserAgent()
		if gotAgent == "" || gotAgent == "Go-http-client/1.1" {
			http.Error(w, "no browser agent", http.StatusNotFound)
			return
		}
		w.Header().Set("Content-Type", "image/png")
		_, _ = w.Write(pngBuf.Bytes())
	}))
	defer srv.Close()

	if !CheckImgUrl(srv.URL + "/poster.png") {
		t.Fatalf("valid image rejected, User-Agent seen by server: %q", gotAgent)
	}
	if gotAgent == "Go-http-client/1.1" {
		t.Errorf("User-Agent = %q, want a browser-like agent", gotAgent)
	}
}

// A non-image answer must still be rejected, whatever the agent is: the fix
// must not turn the check into "any 200 means ok".
func TestCheckImgUrlRejectsNonImage(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		_, _ = w.Write([]byte("<html>not an image</html>"))
	}))
	defer srv.Close()

	if CheckImgUrl(srv.URL + "/poster.png") {
		t.Error("HTML body accepted as an image")
	}
}
