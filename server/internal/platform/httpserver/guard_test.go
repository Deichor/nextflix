package httpserver

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
)

const (
	webOSUA    = "Mozilla/5.0 (Web0S; Linux/SmartTV) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/79.0.3945.79 Safari/537.36 WebAppManager"
	testAppKey = "0123456789abcdef"
)

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
	r := guardEngine(hostGuard([]string{"https://flix.example.com"}, []string{"mypc", "box.lan:8252", "NAS.lan."}))
	for host, want := range map[string]int{
		"nas.lan":              http.StatusOK,
		"nas.lan.:8252":        http.StatusOK,
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

// Requests the web container proxies (Host "server") are judged by the browser's Host, which
// Next.js passes in X-Forwarded-Host; its own SSR fetches have none.
func TestHostGuardBehindWebProxy(t *testing.T) {
	r := guardEngine(hostGuard(nil, []string{"mypc"}))
	for fwd, want := range map[string]int{
		"":                    http.StatusOK, // SSR fetch
		"nextflix.local":      http.StatusOK,
		"192.168.1.5:3008":    http.StatusOK,
		"tv.nextflix.local":   http.StatusOK,
		"localhost:3008":      http.StatusOK,
		"mypc":                http.StatusOK,
		"rebind.evil.com":     http.StatusForbidden,
		"rebind.evil.com:80":  http.StatusForbidden,
		"evil.com, 10.0.0.1":  http.StatusForbidden,
		"8.8.8.8:3008":        http.StatusForbidden,
		"server.evil.com:80":  http.StatusForbidden,
		"nextflix.local.evil": http.StatusForbidden,
	} {
		req := httptest.NewRequest(http.MethodGet, "/api/x", nil)
		req.Host = "server:8252"
		if fwd != "" {
			req.Header.Set("X-Forwarded-Host", fwd)
		}
		w := httptest.NewRecorder()
		r.ServeHTTP(w, req)
		if w.Code != want {
			t.Errorf("X-Forwarded-Host %q: got %d, want %d", fwd, w.Code, want)
		}
	}
}

func TestFetchSiteGuard(t *testing.T) {
	r := guardEngine(fetchSiteGuard(nil, testAppKey))
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
		{"packaged TV app fetch", "GET", "/api/x?app_key=" + testAppKey, "cross-site", "cors", "null", webOSUA, 200},
		{"packaged TV app beacon", "POST", "/api/x?a=1&app_key=" + testAppKey, "cross-site", "no-cors", "null", webOSUA, 200},
		{"packaged TV app video", "GET", "/api/x", "cross-site", "no-cors", "", webOSUA, 200},
		{"foreign page on TV", "GET", "/api/x", "cross-site", "cors", "http://evil.com", webOSUA, 403},
		{"sandboxed iframe on TV", "GET", "/api/x", "cross-site", "cors", "null", webOSUA, 403},
		{"sandboxed iframe on TV, POST", "POST", "/api/x", "cross-site", "no-cors", "null", webOSUA, 403},
		{"sandboxed iframe on TV, wrong key", "GET", "/api/x?app_key=guess", "cross-site", "cors", "null", webOSUA, 403},
		{"TV user agent, POST without Origin", "POST", "/api/x", "cross-site", "no-cors", "", webOSUA, 403},
		{"key without TV user agent", "GET", "/api/x?app_key=" + testAppKey, "cross-site", "cors", "null", "", 200},
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
// A page with Origin null (sandboxed iframe) in the TV's own browser has the same user agent but
// not the key, so it gets neither through the guards nor a CORS grant.
func TestWebOSAppThroughChain(t *testing.T) {
	cases := []struct {
		name, method, path, site string
		wantCode                 int
		wantACAO                 string
	}{
		{"app fetch", "GET", "/api/x?app_key=" + testAppKey, "cross-site", 200, "null"},
		{"app preflight", "OPTIONS", "/api/x?app_key=" + testAppKey, "cross-site", 204, "null"},
		{"iframe fetch", "GET", "/api/x", "cross-site", 403, ""},
		{"iframe preflight", "OPTIONS", "/api/x", "cross-site", 403, ""},
		// webOS 4/5 send no Sec-Fetch-Site: CORS alone must refuse
		{"iframe fetch, old webOS", "GET", "/api/x", "", 403, ""},
		{"app fetch, old webOS", "GET", "/api/x?app_key=" + testAppKey, "", 200, "null"},
	}
	for _, key := range []string{testAppKey, ""} {
		r := guardEngine(hostGuard(nil, nil), fetchSiteGuard(nil, key), bodyLimit(maxBodyBytes), corsMiddleware(nil, key))
		for _, tc := range cases {
			req := httptest.NewRequest(tc.method, tc.path, nil)
			req.Host = "nextflix.local:8252"
			req.Header.Set("Origin", "null")
			req.Header.Set("User-Agent", webOSUA)
			if tc.site != "" {
				req.Header.Set("Sec-Fetch-Site", tc.site)
			}
			if tc.method == http.MethodOptions {
				req.Header.Set("Access-Control-Request-Method", "PUT")
			}
			w := httptest.NewRecorder()
			r.ServeHTTP(w, req)
			wantCode, wantACAO := tc.wantCode, tc.wantACAO
			if key == "" {
				// No TV_APP_KEY configured: Origin null is never trusted
				wantCode, wantACAO = 403, ""
			}
			if w.Code != wantCode || w.Header().Get("Access-Control-Allow-Origin") != wantACAO {
				t.Errorf("key %q, %s: got %d, ACAO %q", key, tc.name, w.Code, w.Header().Get("Access-Control-Allow-Origin"))
			}
		}
	}
}
