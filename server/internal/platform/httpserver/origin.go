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
		if strings.EqualFold(strings.TrimSuffix(o, "/"), origin) {
			return true
		}
	}

	u, err := url.Parse(origin)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") {
		return false
	}
	return isLANHost(u.Hostname())
}

// isLANHost reports whether a bare host name (no port) can only point at this machine or the LAN.
// Public DNS names are left out so a rebinding domain cannot pass.
func isLANHost(host string) bool {
	host = strings.TrimSuffix(strings.ToLower(host), ".")
	if host == "" {
		return false
	}
	if host == "localhost" || strings.HasSuffix(host, ".localhost") || strings.HasSuffix(host, ".local") {
		return true
	}
	if i := strings.IndexByte(host, '%'); i >= 0 { // IPv6 zone, e.g. fe80::1%eth0
		host = host[:i]
	}
	ip := net.ParseIP(host)
	return ip != nil && (ip.IsLoopback() || ip.IsPrivate() || ip.IsLinkLocalUnicast())
}
