package httpserver

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
)

func TestServeTVApp(t *testing.T) {
	gin.SetMode(gin.TestMode)
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "index.html"), []byte("<html>tv</html>"), 0o644); err != nil {
		t.Fatal(err)
	}
	r := gin.New()
	ServeTVApp(r, dir)
	r.GET("/api/health", func(c *gin.Context) { c.Status(http.StatusOK) })

	get := func(path string) *httptest.ResponseRecorder {
		w := httptest.NewRecorder()
		r.ServeHTTP(w, httptest.NewRequest(http.MethodGet, path, nil))
		return w
	}
	if w := get("/tv/"); w.Code != http.StatusOK || !strings.Contains(w.Body.String(), "tv") {
		t.Fatalf("/tv/: %d %q", w.Code, w.Body.String())
	}
	if w := get("/tv"); w.Code != http.StatusMovedPermanently && w.Code != http.StatusOK {
		t.Fatalf("/tv: %d", w.Code)
	}
	if w := get("/api/health"); w.Code != http.StatusOK {
		t.Fatalf("api: %d", w.Code)
	}

	empty := gin.New()
	ServeTVApp(empty, t.TempDir())
	w := httptest.NewRecorder()
	empty.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/tv/", nil))
	if w.Code != http.StatusNotFound {
		t.Fatalf("empty dir served: %d", w.Code)
	}
}
