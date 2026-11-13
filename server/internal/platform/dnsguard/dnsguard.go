// Package dnsguard keeps outgoing connections working when the DNS server lies: a public name
// that resolves only to loopback / 0.0.0.0 (an ISP block, a DPI tool's resolver) or does not
// resolve at all is looked up again over DNS-over-HTTPS, and the dial goes to those addresses.
// TLS still verifies the certificate against the host name.
package dnsguard

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"
)

// DialFunc is the shape of net.Dialer.DialContext and http.Transport.DialContext.
type DialFunc func(ctx context.Context, network, addr string) (net.Conn, error)

const (
	goodFor    = time.Minute      // a correct system answer is not checked again for this long
	failedFor  = 10 * time.Second // nor is a host whose DoH lookup failed
	minTTL     = time.Minute
	maxTTL     = 10 * time.Minute
	dohTimeout = 4 * time.Second
	maxHosts   = 512
)

// Addressed by IP so the lookup itself needs no DNS; HTTPS so a resolver on port 53 cannot rewrite it.
var dohServers = []string{"https://1.1.1.1/dns-query", "https://8.8.8.8/resolve"}

var dohClient = &http.Client{Transport: &http.Transport{
	DialContext:         (&net.Dialer{Timeout: 3 * time.Second}).DialContext,
	TLSHandshakeTimeout: 3 * time.Second,
	ForceAttemptHTTP2:   true,
	MaxIdleConnsPerHost: 2,
	IdleConnTimeout:     90 * time.Second,
}}

type entry struct {
	ips     []string // DoH answers; empty: dial the name as usual
	expires time.Time
}

type guard struct {
	lookup func(ctx context.Context, host string) ([]net.IPAddr, error)
	doh    func(ctx context.Context, host string) ([]string, time.Duration, error)

	mu    sync.Mutex
	hosts map[string]entry
}

var std = &guard{lookup: net.DefaultResolver.LookupIPAddr, doh: dohLookup, hosts: map[string]entry{}}

// Wrap puts the check in front of dial. All wrapped dialers share one host cache.
func Wrap(dial DialFunc) DialFunc { return std.wrap(dial) }

// InstallDefault wraps http.DefaultTransport (http.DefaultClient and clients without a Transport).
func InstallDefault() {
	if t, ok := http.DefaultTransport.(*http.Transport); ok && t.DialContext != nil {
		t.DialContext = Wrap(t.DialContext)
	}
}

func (g *guard) wrap(dial DialFunc) DialFunc {
	return func(ctx context.Context, network, addr string) (net.Conn, error) {
		host, port, err := net.SplitHostPort(addr)
		if err != nil || !strings.HasPrefix(network, "tcp") || network == "tcp6" || !public(host) {
			return dial(ctx, network, addr)
		}
		ips := g.resolve(ctx, host)
		if len(ips) == 0 {
			return dial(ctx, network, addr)
		}
		var last error
		for _, ip := range ips {
			conn, err := dial(ctx, network, net.JoinHostPort(ip, port))
			if err == nil {
				return conn, nil
			}
			last = err
			if ctx.Err() != nil {
				break
			}
		}
		return nil, last
	}
}

// public: a name the system resolver should answer with a public address. IP literals,
// compose services ("redis"), localhost and LAN suffixes are dialed as they are.
func public(host string) bool {
	h := strings.ToLower(strings.TrimSuffix(host, "."))
	if net.ParseIP(h) != nil || !strings.Contains(h, ".") {
		return false
	}
	for _, s := range []string{".local", ".lan", ".internal", ".localhost", ".home.arpa"} {
		if strings.HasSuffix(h, s) {
			return false
		}
	}
	return true
}

// resolve returns the addresses to dial for host, or nil when the system resolver is fine
// (or DoH has nothing better).
func (g *guard) resolve(ctx context.Context, host string) []string {
	now := time.Now()
	g.mu.Lock()
	prev, ok := g.hosts[host]
	g.mu.Unlock()
	if ok && now.Before(prev.expires) {
		return prev.ips
	}

	addrs, err := g.lookup(ctx, host)
	if err == nil && !blocked(addrs) {
		g.store(host, entry{expires: now.Add(goodFor)})
		return nil
	}
	if ctx.Err() != nil {
		return nil
	}
	ips, ttl, derr := g.doh(ctx, host)
	if derr != nil || len(ips) == 0 {
		g.store(host, entry{expires: now.Add(failedFor)})
		return nil
	}
	if len(prev.ips) == 0 {
		why := "did not resolve"
		if err == nil {
			why = "resolved to " + addrs[0].IP.String()
		}
		log.Printf("dnsguard: %s %s on the system DNS; using DNS-over-HTTPS: %s", host, why, strings.Join(ips, ", "))
	}
	g.store(host, entry{ips: ips, expires: now.Add(ttl)})
	return ips
}

func (g *guard) store(host string, e entry) {
	g.mu.Lock()
	if len(g.hosts) >= maxHosts {
		g.hosts = map[string]entry{}
	}
	g.hosts[host] = e
	g.mu.Unlock()
}

// blocked: no address, or only loopback / unspecified ones, which no public name really has.
func blocked(addrs []net.IPAddr) bool {
	for _, a := range addrs {
		if !a.IP.IsLoopback() && !a.IP.IsUnspecified() {
			return false
		}
	}
	return true
}

func dohLookup(ctx context.Context, host string) ([]string, time.Duration, error) {
	var last error
	for _, srv := range dohServers {
		ips, ttl, err := dohQuery(ctx, srv, host)
		if err == nil && len(ips) > 0 {
			return ips, ttl, nil
		}
		if err == nil {
			err = fmt.Errorf("dnsguard: no A record for %s from %s", host, srv)
		}
		last = err
		if ctx.Err() != nil {
			break
		}
	}
	return nil, 0, last
}

// dohQuery asks one server for A records (JSON API, which Cloudflare and Google both speak).
func dohQuery(ctx context.Context, srv, host string) ([]string, time.Duration, error) {
	ctx, cancel := context.WithTimeout(ctx, dohTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, srv+"?type=A&name="+url.QueryEscape(host), nil)
	if err != nil {
		return nil, 0, err
	}
	req.Header.Set("Accept", "application/dns-json")
	resp, err := dohClient.Do(req)
	if err != nil {
		return nil, 0, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, 0, fmt.Errorf("dnsguard: %s answered %d", srv, resp.StatusCode)
	}
	var body struct {
		Answer []answer `json:"Answer"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 64<<10)).Decode(&body); err != nil {
		return nil, 0, err
	}
	ips, ttl := parseAnswer(body.Answer)
	return ips, ttl, nil
}

type answer struct {
	Type int    `json:"type"`
	TTL  int    `json:"TTL"`
	Data string `json:"data"`
}

// parseAnswer keeps the public IPv4 addresses and the shortest TTL (within minTTL..maxTTL).
func parseAnswer(answers []answer) ([]string, time.Duration) {
	var ips []string
	ttl := maxTTL
	for _, a := range answers {
		if a.Type != 1 { // A; a CNAME chain comes first
			continue
		}
		ip := net.ParseIP(a.Data)
		if ip == nil || ip.To4() == nil || ip.IsLoopback() || ip.IsUnspecified() {
			continue
		}
		ips = append(ips, ip.String())
		if d := time.Duration(a.TTL) * time.Second; d < ttl {
			ttl = d
		}
	}
	return ips, max(ttl, minTTL)
}
