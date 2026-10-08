package httpserver

import (
	"net"
	"net/url"
	"strings"
)

// isAllowedOrigin allows browser origins on the local network only:
// localhost, private IPs, *.local hosts, plus the extra origins from ALLOWED_ORIGINS.
func isAllowedOrigin(origin string, extra []string) bool {
	for _, o := range extra {
		if strings.EqualFold(o, origin) {
			return true
		}
	}

	u, err := url.Parse(origin)
	if err != nil || u.Hostname() == "" {
		return false
	}
	host := u.Hostname()
	if host == "localhost" || strings.HasSuffix(host, ".local") {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && (ip.IsLoopback() || ip.IsPrivate() || ip.IsLinkLocalUnicast())
}
