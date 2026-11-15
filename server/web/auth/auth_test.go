package auth

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/gin-gonic/gin"

	"server/settings"
)

func authRouter(accounts gin.Accounts) *gin.Engine {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.Use(BasicAuth(accounts))
	r.Use(CheckAuth())
	r.GET("/x", func(c *gin.Context) { c.String(http.StatusOK, "ok") })
	return r
}

func request(r *gin.Engine, authorization string) int {
	req := httptest.NewRequest(http.MethodGet, "/x", nil)
	if authorization != "" {
		req.Header.Set("Authorization", authorization)
	}
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	return w.Code
}

func TestBasicAuthAndCheckAuth(t *testing.T) {
	old := settings.HttpAuth
	settings.HttpAuth = true
	t.Cleanup(func() { settings.HttpAuth = old })

	r := authRouter(gin.Accounts{"admin": "secret"})
	if got := request(r, ""); got != http.StatusUnauthorized {
		t.Fatalf("no header: %d", got)
	}
	if got := request(r, "Basic invalid"); got != http.StatusUnauthorized {
		t.Fatalf("bad header: %d", got)
	}
	good := authorizationHeader("admin", "secret")
	if got := request(r, good); got != http.StatusOK {
		t.Fatalf("valid header: %d", got)
	}
}

func TestSetupAuthDisabledAndMissingFile(t *testing.T) {
	old := settings.HttpAuth
	oldPath := settings.Path
	settings.Path = t.TempDir()
	t.Cleanup(func() { settings.HttpAuth = old; settings.Path = oldPath })

	settings.HttpAuth = false
	r := gin.New()
	SetupAuth(r)
	r.GET("/x", func(c *gin.Context) { c.String(http.StatusOK, "ok") })
	if got := request(r, ""); got != http.StatusOK {
		t.Fatalf("disabled: %d", got)
	}

	settings.HttpAuth = true
	r = gin.New()
	SetupAuth(r) // accs.db does not exist: middleware is not installed
	r.GET("/x", func(c *gin.Context) { c.String(http.StatusOK, "ok") })
	if got := request(r, ""); got != http.StatusOK {
		t.Fatalf("missing accs.db: %d", got)
	}
}

func TestSetupAuthReadsAccountsFile(t *testing.T) {
	old := settings.HttpAuth
	oldPath := settings.Path
	settings.Path = t.TempDir()
	settings.HttpAuth = true
	t.Cleanup(func() { settings.HttpAuth = old; settings.Path = oldPath })

	if err := os.WriteFile(filepath.Join(settings.Path, "accs.db"), []byte(`{"admin":"secret"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	r := gin.New()
	SetupAuth(r)
	r.Use(CheckAuth())
	r.GET("/x", func(c *gin.Context) { c.String(http.StatusOK, "ok") })

	if got := request(r, ""); got != http.StatusUnauthorized {
		t.Fatalf("missing header: %d", got)
	}
	if got := request(r, authorizationHeader("admin", "secret")); got != http.StatusOK {
		t.Fatalf("valid: %d", got)
	}
}
