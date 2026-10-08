package httpserver

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
)

func TestLang(t *testing.T) {
	gin.SetMode(gin.TestMode)
	cases := map[string]string{
		"":       "en",
		"tr":     "tr",
		"TR":     "tr",
		" tr ":   "tr",
		"tr-TR":  "tr",
		"tr_TR":  "tr",
		"en":     "en",
		"en-US":  "en",
		"de":     "en",
		"turkey": "en",
	}
	for header, want := range cases {
		w := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(w)
		c.Request = httptest.NewRequest(http.MethodGet, "/", nil)
		if header != "" {
			c.Request.Header.Set("X-Lang", header)
		}
		if got := Lang(c); got != want {
			t.Errorf("X-Lang %q: got %q, want %q", header, got, want)
		}
		if w.Header().Get("Vary") != "X-Lang" {
			t.Errorf("X-Lang %q: Vary = %q", header, w.Header().Get("Vary"))
		}
	}
}

func TestCORSAllowsXLang(t *testing.T) {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.Use(corsMiddleware(nil))
	r.GET("/api/x", func(c *gin.Context) { c.Status(http.StatusOK) })

	req := httptest.NewRequest(http.MethodOptions, "/api/x", nil)
	req.Header.Set("Origin", "http://192.168.1.10:3008")
	req.Header.Set("Access-Control-Request-Method", "GET")
	req.Header.Set("Access-Control-Request-Headers", "x-lang")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code >= 300 {
		t.Fatalf("preflight status %d", w.Code)
	}
	if got := w.Header().Get("Access-Control-Allow-Headers"); got == "" {
		t.Fatalf("no Access-Control-Allow-Headers in preflight answer")
	}
}

func TestCORSNullOriginOnlyForWebOS(t *testing.T) {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.Use(corsMiddleware(nil))
	r.GET("/api/x", func(c *gin.Context) { c.Status(http.StatusOK) })

	do := func(method, ua string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(method, "/api/x", nil)
		req.Header.Set("Origin", "null")
		req.Header.Set("User-Agent", ua)
		if method == http.MethodOptions {
			req.Header.Set("Access-Control-Request-Method", "GET")
			req.Header.Set("Access-Control-Request-Headers", "x-user-id")
		}
		w := httptest.NewRecorder()
		r.ServeHTTP(w, req)
		return w
	}
	tv := "Mozilla/5.0 (Web0S; Linux/SmartTV) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/94.0 Safari/537.36 WebAppManager"
	if w := do(http.MethodGet, tv); w.Code != http.StatusOK || w.Header().Get("Access-Control-Allow-Origin") != "null" {
		t.Fatalf("webOS GET: %d %v", w.Code, w.Header())
	}
	if w := do(http.MethodOptions, tv); w.Code != http.StatusNoContent || !strings.Contains(w.Header().Get("Access-Control-Allow-Headers"), "X-User-ID") {
		t.Fatalf("webOS preflight: %d %v", w.Code, w.Header())
	}
	if w := do(http.MethodGet, "Mozilla/5.0 (Windows NT 10.0; Win64; x64) Chrome/131"); w.Header().Get("Access-Control-Allow-Origin") != "" || w.Code == http.StatusOK {
		t.Fatalf("null origin from a desktop browser allowed: %d %v", w.Code, w.Header())
	}
}
