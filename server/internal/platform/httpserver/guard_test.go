package httpserver

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
)

const webOSUA = "Mozilla/5.0 (Web0S; Linux/SmartTV) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/79.0.3945.79 Safari/537.36 WebAppManager"

func TestIsLANHost(t *testing.T) {
	for host, want := range map[string]bool{
		"localhost":          true,
		"app.localhost":      true,
		"nextflix.local":     true,
		"NEXTFLIX.LOCAL.":    true,
		"tv.nextflix.local":  true,
		"127.0.0.1":          true,
		"192.168.1.5":        true,
		"10.0.0.2":           true,
		"172.20.0.3":         true,
		"169.254.1.1":        true,
		"::1":                true,
		"fd00::1":            true,
		"fe80::1%eth0":       true,
		"":                   false,
		"8.8.8.8":            false,
		"100.64.0.1":         false,
		"evil.com":           false,
		"local.evil.com":     false,
		"localhost.evil.com": false,
		"2001:db8::1":        false,
	} {
		if got := isLANHost(host); got != want {
			t.Errorf("isLANHost(%q) = %v, want %v", host, got, want)
		}
	}
}

func TestIsAllowedOrigin(t *testing.T) {
	extra := []string{"https://flix.example.com/"}
	for origin, want := range map[string]bool{
		"http://192.168.1.5:3008":      true,
		"http://nextflix.local":        true,
		"https://flix.example.com":     true,
		"http://evil.com":              false,
		"null":                         false,
		"chrome-extension://localhost": false,
		"file://":                      false,
	} {
		if got := isAllowedOrigin(origin, extra); got != want {
			t.Errorf("isAllowedOrigin(%q) = %v, want %v", origin, got, want)
		}
	}
}

func guardEngine(mw ...gin.HandlerFunc) *gin.Engine {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.Use(mw...)
	ok := func(c *gin.Context) { c.Status(http.StatusOK) }
	r.GET("/api/x", ok)
	r.POST("/api/x", ok)
	r.GET("/tv/", ok)
	return r
}

func TestHostGuard(t *testing.T) {
	r := guardEngine(hostGuard([]string{"https://flix.example.com"}, []string{"mypc", "box.lan:8252"}))
	for host, want := range map[string]int{
		"192.168.1.5:8252":     http.StatusOK,
		"nextflix.local":       http.StatusOK,
		"nextflix.local:8252":  http.StatusOK,
		"localhost:8252":       http.StatusOK,
		"127.0.0.1:8252":       http.StatusOK,
		"[::1]:8252":           http.StatusOK,
		"server:8252":          http.StatusOK,
		"flix.example.com":     http.StatusOK,
		"MyPC:8252":            http.StatusOK,
		"box.lan":              http.StatusOK,
		"rebind.evil.com:8252": http.StatusForbidden,
		"8.8.8.8":              http.StatusForbidden,
		"":                     http.StatusForbidden,
	} {
		req := httptest.NewRequest(http.MethodGet, "/api/x", nil)
		req.Host = host
		w := httptest.NewRecorder()
		r.ServeHTTP(w, req)
		if w.Code != want {
			t.Errorf("Host %q: got %d, want %d", host, w.Code, want)
		}
	}
}

func TestFetchSiteGuard(t *testing.T) {
	r := guardEngine(fetchSiteGuard(nil))
	cases := []struct {
		name, method, path, site, mode, origin, ua string
		want                                       int
	}{
		{"no header (old client, curl)", "GET", "/api/x", "", "", "", "", 200},
		{"same-origin", "GET", "/api/x", "same-origin", "cors", "", "", 200},
		{"same-site web app on :3008", "POST", "/api/x", "same-site", "cors", "http://192.168.1.5:3008", "", 200},
		{"typed URL", "GET", "/api/x", "none", "navigate", "", "", 200},
		{"foreign <img>", "GET", "/api/x", "cross-site", "no-cors", "", "", 403},
		{"foreign fetch", "POST", "/api/x", "cross-site", "cors", "http://evil.com", "", 403},
		{"sandboxed iframe", "GET", "/api/x", "cross-site", "cors", "null", "", 403},
		{"foreign link to api", "GET", "/api/x", "cross-site", "navigate", "", "", 403},
		{"foreign link to /tv/", "GET", "/tv/", "cross-site", "navigate", "", "", 200},
		{"other LAN page", "GET", "/api/x", "cross-site", "cors", "http://10.0.0.7:5173", "", 200},
		{"packaged TV app fetch", "GET", "/api/x", "cross-site", "cors", "null", webOSUA, 200},
		{"packaged TV app video", "GET", "/api/x", "cross-site", "no-cors", "", webOSUA, 200},
		{"foreign page on TV", "GET", "/api/x", "cross-site", "cors", "http://evil.com", webOSUA, 403},
	}
	for _, tc := range cases {
		req := httptest.NewRequest(tc.method, tc.path, nil)
		for k, v := range map[string]string{"Sec-Fetch-Site": tc.site, "Sec-Fetch-Mode": tc.mode, "Origin": tc.origin, "User-Agent": tc.ua} {
			if v != "" {
				req.Header.Set(k, v)
			}
		}
		w := httptest.NewRecorder()
		r.ServeHTTP(w, req)
		if w.Code != tc.want {
			t.Errorf("%s: got %d, want %d", tc.name, w.Code, tc.want)
		}
	}
}

func TestBodyLimit(t *testing.T) {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.Use(bodyLimit(1024))
	r.POST("/read", func(c *gin.Context) {
		if _, err := io.ReadAll(c.Request.Body); err != nil {
			c.Status(http.StatusRequestEntityTooLarge)
			return
		}
		c.Status(http.StatusOK)
	})
	// A handler with its own smaller cap (like playback-log) keeps it
	r.POST("/small", func(c *gin.Context) {
		if _, err := io.ReadAll(http.MaxBytesReader(c.Writer, c.Request.Body, 100)); err != nil {
			c.Status(http.StatusRequestEntityTooLarge)
			return
		}
		c.Status(http.StatusOK)
	})

	post := func(path string, n int, chunked bool) int {
		req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(strings.Repeat("a", n)))
		if chunked {
			req.ContentLength = -1
		}
		w := httptest.NewRecorder()
		r.ServeHTTP(w, req)
		return w.Code
	}
	if c := post("/read", 1000, false); c != http.StatusOK {
		t.Errorf("small body: %d", c)
	}
	if c := post("/read", 2000, false); c != http.StatusRequestEntityTooLarge {
		t.Errorf("Content-Length over limit: %d", c)
	}
	if c := post("/read", 2000, true); c != http.StatusRequestEntityTooLarge {
		t.Errorf("chunked over limit: %d", c)
	}
	if c := post("/small", 500, false); c != http.StatusRequestEntityTooLarge {
		t.Errorf("handler cap ignored: %d", c)
	}
}

// The packaged TV app (Origin: null, cross-site) must pass the whole chain including CORS.
func TestWebOSAppThroughChain(t *testing.T) {
	r := guardEngine(hostGuard(nil, nil), fetchSiteGuard(nil), bodyLimit(maxBodyBytes), corsMiddleware(nil))
	req := httptest.NewRequest(http.MethodGet, "/api/x", nil)
	req.Host = "nextflix.local:8252"
	req.Header.Set("Origin", "null")
	req.Header.Set("Sec-Fetch-Site", "cross-site")
	req.Header.Set("User-Agent", webOSUA)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code != http.StatusOK || w.Header().Get("Access-Control-Allow-Origin") != "null" {
		t.Fatalf("got %d, ACAO %q", w.Code, w.Header().Get("Access-Control-Allow-Origin"))
	}
}
