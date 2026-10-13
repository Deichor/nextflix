package httpserver

import (
	"compress/gzip"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
)

func gzipEngine() *gin.Engine {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.Use(corsMiddleware(nil, ""), gzipJSON())
	big := strings.Repeat("nextflix ", 200)
	r.GET("/api/json", func(c *gin.Context) { c.JSON(http.StatusOK, gin.H{"text": big}) })
	r.GET("/api/empty", func(c *gin.Context) { c.JSON(http.StatusNoContent, gin.H{"text": big}) })
	r.GET("/api/jpeg", func(c *gin.Context) { c.Data(http.StatusOK, "image/jpeg", []byte(big)) })
	r.GET("/api/mp4", func(c *gin.Context) { c.Data(http.StatusOK, "video/mp4", []byte(big)) })
	r.GET("/api/events", func(c *gin.Context) {
		c.Header("Content-Type", "text/event-stream")
		c.Status(http.StatusOK)
		c.Writer.WriteString("data: " + big + "\n\n")
		c.Writer.Flush()
	})
	r.GET("/api/torrent-stream/x", func(c *gin.Context) { c.JSON(http.StatusOK, gin.H{"text": big}) })
	r.GET("/other", func(c *gin.Context) { c.JSON(http.StatusOK, gin.H{"text": big}) })
	return r
}

func gzipDo(r http.Handler, method, path string, hdr map[string]string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, path, nil)
	for k, v := range hdr {
		req.Header.Set(k, v)
	}
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	return w
}

func TestGzipJSON(t *testing.T) {
	r := gzipEngine()
	w := gzipDo(r, http.MethodGet, "/api/json", map[string]string{"Accept-Encoding": "br, gzip;q=0.8"})
	if w.Header().Get("Content-Encoding") != "gzip" || !strings.Contains(w.Header().Get("Vary"), "Accept-Encoding") {
		t.Fatalf("not compressed: %v", w.Header())
	}
	zr, err := gzip.NewReader(w.Body)
	if err != nil {
		t.Fatal(err)
	}
	var body struct{ Text string }
	if err := json.NewDecoder(zr).Decode(&body); err != nil || !strings.HasPrefix(body.Text, "nextflix") {
		t.Fatalf("decode: %v %q", err, body.Text)
	}

	plain := gzipDo(r, http.MethodGet, "/api/json", nil)
	if plain.Header().Get("Content-Encoding") != "" || !json.Valid(plain.Body.Bytes()) {
		t.Fatalf("no Accept-Encoding: %v", plain.Header())
	}
	if w := gzipDo(r, http.MethodGet, "/api/json", map[string]string{"Accept-Encoding": "gzip;q=0"}); w.Header().Get("Content-Encoding") != "" {
		t.Fatal("gzip;q=0 compressed")
	}
}

func TestGzipSkips(t *testing.T) {
	r := gzipEngine()
	gz := map[string]string{"Accept-Encoding": "gzip"}
	cases := []struct {
		name, method, path string
		hdr                map[string]string
	}{
		{"jpeg", "GET", "/api/jpeg", gz},
		{"mp4", "GET", "/api/mp4", gz},
		{"event stream", "GET", "/api/events", gz},
		{"range", "GET", "/api/json", map[string]string{"Accept-Encoding": "gzip", "Range": "bytes=0-10"}},
		{"torrent stream", "GET", "/api/torrent-stream/x", gz},
		{"outside /api", "GET", "/other", gz},
		{"head", "HEAD", "/api/json", gz},
	}
	for _, tc := range cases {
		w := gzipDo(r, tc.method, tc.path, tc.hdr)
		if w.Header().Get("Content-Encoding") != "" {
			t.Errorf("%s: compressed", tc.name)
		}
		if tc.method == "GET" && !strings.Contains(w.Body.String(), "nextflix nextflix") {
			t.Errorf("%s: body changed: %q", tc.name, w.Body.String()[:min(40, w.Body.Len())])
		}
	}

	pre := gzipDo(r, http.MethodOptions, "/api/json", map[string]string{"Accept-Encoding": "gzip",
		"Origin": "http://192.168.1.10:3008", "Access-Control-Request-Method": "GET"})
	if pre.Code != http.StatusNoContent || pre.Header().Get("Content-Encoding") != "" || pre.Body.Len() != 0 {
		t.Errorf("preflight: %d %v", pre.Code, pre.Header())
	}

	empty := gzipDo(r, http.MethodGet, "/api/empty", gz)
	if empty.Code != http.StatusNoContent || empty.Header().Get("Content-Encoding") != "" || empty.Body.Len() != 0 {
		t.Errorf("204: %d %v %q", empty.Code, empty.Header(), empty.Body.String())
	}
}

// The writers are pooled: a second compressed answer must not carry data of the first.
func TestGzipPoolReuse(t *testing.T) {
	r := gzipEngine()
	for range 3 {
		w := gzipDo(r, http.MethodGet, "/api/json", map[string]string{"Accept-Encoding": "gzip"})
		zr, err := gzip.NewReader(w.Body)
		if err != nil {
			t.Fatal(err)
		}
		b, err := io.ReadAll(zr)
		if err != nil || !json.Valid(b) {
			t.Fatalf("%v %q", err, b)
		}
	}
}
