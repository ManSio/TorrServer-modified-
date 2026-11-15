package api

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
)

// TestSettingsSetWithoutSets is the #942 regression: a set request without a
// "sets" object passed nil to SetSettings and panicked.
func TestSettingsSetWithoutSets(t *testing.T) {
	gin.SetMode(gin.TestMode)
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodPost, "/settings",
		strings.NewReader(`{"action":"set"}`))
	c.Request.Header.Set("Content-Type", "application/json")

	settings(c)

	if w.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", w.Code)
	}
}
