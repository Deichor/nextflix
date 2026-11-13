package dnsguard

import (
	"context"
	"errors"
	"net"
	"testing"
	"time"
)

type fake struct {
	system  []net.IPAddr
	sysErr  error
	doh     []string
	dohErr  error
	lookups int
	dohs    int
	dialed  []string
}

func (f *fake) guard() *guard {
	return &guard{
		lookup: func(context.Context, string) ([]net.IPAddr, error) { f.lookups++; return f.system, f.sysErr },
		doh: func(context.Context, string) ([]string, time.Duration, error) {
			f.dohs++
			return f.doh, time.Minute, f.dohErr
		},
		hosts: map[string]entry{},
	}
}

func (f *fake) dial(_ context.Context, _, addr string) (net.Conn, error) {
	f.dialed = append(f.dialed, addr)
	return nil, nil
}

func ips(s ...string) []net.IPAddr {
	out := make([]net.IPAddr, len(s))
	for i, v := range s {
		out[i] = net.IPAddr{IP: net.ParseIP(v)}
	}
	return out
}

func TestHijackedNameDialsDoHAddress(t *testing.T) {
	f := &fake{system: ips("127.0.0.1", "::1"), doh: []string{"3.160.57.82"}}
	dial := f.guard().wrap(f.dial)
	dial(context.Background(), "tcp", "api.themoviedb.org:443")
	dial(context.Background(), "tcp", "api.themoviedb.org:443")
	if want := "3.160.57.82:443"; len(f.dialed) != 2 || f.dialed[0] != want || f.dialed[1] != want {
		t.Fatalf("dialed %v, want %s twice", f.dialed, want)
	}
	if f.lookups != 1 || f.dohs != 1 {
		t.Fatalf("lookups=%d dohs=%d, want the second dial from the cache", f.lookups, f.dohs)
	}
}

func TestCorrectAnswerDialsName(t *testing.T) {
	f := &fake{system: ips("3.160.57.82")}
	f.guard().wrap(f.dial)(context.Background(), "tcp", "api.themoviedb.org:443")
	if len(f.dialed) != 1 || f.dialed[0] != "api.themoviedb.org:443" || f.dohs != 0 {
		t.Fatalf("dialed %v dohs=%d", f.dialed, f.dohs)
	}
}

func TestLookupErrorFallsBackToDoH(t *testing.T) {
	f := &fake{sysErr: errors.New("server misbehaving"), doh: []string{"1.2.3.4"}}
	f.guard().wrap(f.dial)(context.Background(), "tcp", "api.torbox.app:443")
	if len(f.dialed) != 1 || f.dialed[0] != "1.2.3.4:443" {
		t.Fatalf("dialed %v", f.dialed)
	}
}

func TestDoHFailureDialsName(t *testing.T) {
	f := &fake{system: ips("127.0.0.1"), dohErr: errors.New("offline")}
	f.guard().wrap(f.dial)(context.Background(), "tcp", "api.themoviedb.org:443")
	if len(f.dialed) != 1 || f.dialed[0] != "api.themoviedb.org:443" {
		t.Fatalf("dialed %v", f.dialed)
	}
}

func TestLocalNamesSkipCheck(t *testing.T) {
	f := &fake{}
	dial := f.guard().wrap(f.dial)
	for _, addr := range []string{"redis:6379", "jackett:9117", "127.0.0.1:8090", "nextflix.local:80", "[::1]:80"} {
		dial(context.Background(), "tcp", addr)
	}
	if f.lookups != 0 || len(f.dialed) != 5 {
		t.Fatalf("lookups=%d dialed=%v", f.lookups, f.dialed)
	}
}

func TestParseAnswer(t *testing.T) {
	got, ttl := parseAnswer([]answer{
		{Type: 5, TTL: 30, Data: "cdn.example.net."},
		{Type: 1, TTL: 300, Data: "3.160.57.82"},
		{Type: 1, TTL: 20, Data: "127.0.0.1"},
		{Type: 1, TTL: 120, Data: "3.160.57.51"},
	})
	if len(got) != 2 || got[0] != "3.160.57.82" || got[1] != "3.160.57.51" || ttl != 2*time.Minute {
		t.Fatalf("got %v ttl %v", got, ttl)
	}
}
