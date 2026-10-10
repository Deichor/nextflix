package httpserver

import (
	"crypto/subtle"
	"net"
	"net/http"
	"net/url"
	"strings"

	"github.com/gin-gonic/gin"
)

// maxBodyBytes caps every request body; the JSON endpoints only need a few KB.
const maxBodyBytes = 1 << 20

// hostGuard blocks DNS rebinding: a public domain that resolves to this machine still sends its
// own name as Host, so only LAN names, the Docker service name and configured hosts get through.
// The web container's rewrites arrive as Host "server" with the browser's Host in X-Forwarded-Host
// (Next.js overwrites whatever the client sent there); its SSR fetches send no X-Forwarded-Host.
func hostGuard(allowedOrigins, allowedHosts []string) gin.HandlerFunc {
	extra := map[string]bool{}
	for _, o := range allowedOrigins {
		if u, err := url.Parse(o); err == nil && u.Hostname() != "" {
			extra[normHost(u.Hostname())] = true
		}
	}
	for _, h := range allowedHosts {
		extra[normHost(h)] = true
	}
	return func(c *gin.Context) {
		host := normHost(c.Request.Host)
		if host == "server" {
			fwd, _, _ := strings.Cut(c.GetHeader("X-Forwarded-Host"), ",")
			if host = normHost(strings.TrimSpace(fwd)); host == "" {
				c.Next()
				return
			}
		}
		if !extra[host] && !isLANHost(host) {
			c.AbortWithStatusJSON(http.StatusForbidden, gin.H{"error": "host not allowed"})
			return
		}
		c.Next()
	}
}

// normHost lowercases a Host value and drops the port, IPv6 brackets and a trailing dot.
func normHost(h string) string {
	return strings.TrimSuffix(strings.ToLower(hostOnly(h)), ".")
}

// hostOnly strips the port and IPv6 brackets: "[::1]:8252" -> "::1".
func hostOnly(h string) string {
	if host, _, err := net.SplitHostPort(h); err == nil {
		return host
	}
	return strings.TrimSuffix(strings.TrimPrefix(h, "["), "]")
}

// fetchSiteGuard rejects cross-site requests, e.g. an <img> or <video> on a foreign page pointing
// at /api/torrent-stream/play. Those carry no Origin, so the CORS check never sees them.
// Old clients without Sec-Fetch-Site and non-browser clients pass.
func fetchSiteGuard(allowedOrigins []string, tvAppKey string) gin.HandlerFunc {
	return func(c *gin.Context) {
		if c.GetHeader("Sec-Fetch-Site") == "cross-site" && !crossSiteAllowed(c.Request, allowedOrigins, tvAppKey) {
			c.AbortWithStatusJSON(http.StatusForbidden, gin.H{"error": "cross-site request blocked"})
			return
		}
		c.Next()
	}
}

func crossSiteAllowed(r *http.Request, allowedOrigins []string, tvAppKey string) bool {
	origin := r.Header.Get("Origin")
	switch {
	case origin == "null":
		// The packaged TV app runs from file://, but a sandboxed iframe on any site sends "null" too
		return hasTVAppKey(r, tvAppKey)
	case origin != "":
		// A CORS request from another LAN page (e.g. the web app on another host); CORS trusts it too
		return isAllowedOrigin(origin, allowedOrigins)
	case isWebOSApp(r) && (r.Method == http.MethodGet || r.Method == http.MethodHead):
		// The packaged TV app's images and video: no Origin, and the response stays unreadable
		return true
	case r.Header.Get("Sec-Fetch-Mode") == "navigate" && !strings.HasPrefix(r.URL.Path, "/api/"):
		// A link from another site to /tv/ only opens a page
		return r.Method == http.MethodGet || r.Method == http.MethodHead
	}
	return false
}

// tvAppKeyParam carries TV_APP_KEY on the packaged TV app's fetches. A query parameter, so CORS
// preflights (no custom headers) and keepalive / sendBeacon requests carry it too.
const tvAppKeyParam = "app_key"

func hasTVAppKey(r *http.Request, key string) bool {
	got := r.URL.Query().Get(tvAppKeyParam)
	return key != "" && subtle.ConstantTimeCompare([]byte(got), []byte(key)) == 1
}

// bodyLimit caps request bodies; handlers with a smaller cap (playback-log) still apply theirs.
func bodyLimit(n int64) gin.HandlerFunc {
	return func(c *gin.Context) {
		r := c.Request
		if r.ContentLength > n {
			c.AbortWithStatusJSON(http.StatusRequestEntityTooLarge, gin.H{"error": "request body too large"})
			return
		}
		if r.Body != nil && r.Body != http.NoBody {
			r.Body = http.MaxBytesReader(c.Writer, r.Body, n)
		}
		c.Next()
	}
}
