package httpserver

import (
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
func hostGuard(allowedOrigins, allowedHosts []string) gin.HandlerFunc {
	extra := map[string]bool{"server": true} // the web container's rewrites and SSR fetches
	for _, o := range allowedOrigins {
		if u, err := url.Parse(o); err == nil && u.Hostname() != "" {
			extra[strings.ToLower(u.Hostname())] = true
		}
	}
	for _, h := range allowedHosts {
		extra[strings.ToLower(hostOnly(h))] = true
	}
	return func(c *gin.Context) {
		host := strings.TrimSuffix(strings.ToLower(hostOnly(c.Request.Host)), ".")
		if !extra[host] && !isLANHost(host) {
			c.AbortWithStatusJSON(http.StatusForbidden, gin.H{"error": "host not allowed"})
			return
		}
		c.Next()
	}
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
func fetchSiteGuard(allowedOrigins []string) gin.HandlerFunc {
	return func(c *gin.Context) {
		if c.GetHeader("Sec-Fetch-Site") == "cross-site" && !crossSiteAllowed(c.Request, allowedOrigins) {
			c.AbortWithStatusJSON(http.StatusForbidden, gin.H{"error": "cross-site request blocked"})
			return
		}
		c.Next()
	}
}

func crossSiteAllowed(r *http.Request, allowedOrigins []string) bool {
	origin := r.Header.Get("Origin")
	switch {
	case origin != "" && origin != "null":
		// A CORS request from another LAN page (e.g. the web app on another host); CORS trusts it too
		return isAllowedOrigin(origin, allowedOrigins)
	case isWebOSApp(r):
		// The packaged TV app runs from file://, so its fetches, images and video are cross-site
		return true
	case r.Header.Get("Sec-Fetch-Mode") == "navigate" && !strings.HasPrefix(r.URL.Path, "/api/"):
		// A link from another site to /tv/ only opens a page
		return r.Method == http.MethodGet || r.Method == http.MethodHead
	}
	return false
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
