package torbox

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

func hashN(i int) string { return fmt.Sprintf("%040x", i+1) }

func TestCheckCachedBatchesAndHeaders(t *testing.T) {
	var mu sync.Mutex
	var batches []int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("User-Agent") != UserAgent || r.Header.Get("Authorization") != "Bearer k3y" {
			t.Errorf("headers: UA %q auth %q", r.Header.Get("User-Agent"), r.Header.Get("Authorization"))
		}
		if r.URL.Path != "/torrents/checkcached" || r.URL.Query().Get("format") != "list" || r.URL.Query().Get("list_files") != "true" {
			t.Errorf("request %s", r.URL)
		}
		hs := strings.Split(r.URL.Query().Get("hash"), ",")
		mu.Lock()
		batches = append(batches, len(hs))
		mu.Unlock()
		// Every 10th hash is cached
		var items []string
		for _, h := range hs {
			var i int
			fmt.Sscanf(h, "%x", &i)
			if (i-1)%10 == 0 {
				items = append(items, fmt.Sprintf(`{"name":"n","size":5,"hash":"%s","files":[{"name":"d/a.mkv","size":5}]}`, strings.ToUpper(h)))
			}
		}
		fmt.Fprintf(w, `{"success":true,"detail":"ok","data":[%s]}`, strings.Join(items, ","))
	}))
	defer srv.Close()
	var hashes []string
	for i := 0; i < 250; i++ {
		hashes = append(hashes, hashN(i))
	}
	cached, known, err := New(srv.URL, "k3y", nil).CheckCached(context.Background(), hashes)
	if err != nil {
		t.Fatal(err)
	}
	if len(batches) != 3 || batches[0] != 100 || batches[2] != 50 {
		t.Fatalf("batches %v", batches)
	}
	if len(cached) != 25 || len(known) != 250 {
		t.Fatalf("cached %d known %d", len(cached), len(known))
	}
	if it := cached[hashN(0)]; it.Hash != hashN(0) || len(it.Files) != 1 || it.Files[0].Name != "d/a.mkv" {
		t.Fatalf("item %+v", it)
	}
}

func TestCheckCachedMalformedIsUnknown(t *testing.T) {
	h1, h2 := hashN(1), hashN(2)
	cases := []struct {
		name, body string
		status     int
		cached     int
		known      bool
	}{
		{"none cached (null)", `{"success":true,"data":null}`, 200, 0, true},
		{"none cached (empty object)", `{"success":true,"data":{}}`, 200, 0, true},
		{"object format", `{"success":true,"data":{"` + h1 + `":{"name":"x","size":1,"hash":"` + h1 + `"}}}`, 200, 1, true},
		{"data is a string", `{"success":true,"data":"oops"}`, 200, 0, false},
		{"not json", `<html>cloudflare</html>`, 200, 0, false},
		{"success false", `{"success":false,"error":"DATABASE_ERROR","data":null}`, 200, 0, false},
		{"server error", `{"success":false}`, 502, 0, false},
		{"bad item keeps the good one", `{"success":true,"data":[{"hash":"` + h1 + `","name":"x","size":1},{"hash":"zz"}]}`, 200, 1, false},
		{"bad files", `{"success":true,"data":[{"hash":"` + h1 + `","files":"x"}]}`, 200, 0, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(tc.status)
				fmt.Fprint(w, tc.body)
			}))
			defer srv.Close()
			cached, known, err := New(srv.URL, "k", nil).CheckCached(context.Background(), []string{h1, h2})
			if len(cached) != tc.cached || known[h2] != tc.known {
				t.Fatalf("cached %d known[h2] %v (err %v)", len(cached), known[h2], err)
			}
			if !tc.known && err == nil {
				t.Fatal("no error for an unknown answer")
			}
		})
	}
}

func TestAPIErrorsAndKeySecrecy(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/user/me":
			w.WriteHeader(http.StatusForbidden)
			fmt.Fprint(w, `{"success":false,"error":"BAD_TOKEN","detail":"Invalid token"}`)
		case "/torrents/requestdl":
			if r.URL.Query().Get("token") != "s3cret" || r.Header.Get("Authorization") != "" {
				t.Errorf("requestdl auth: %s", r.URL)
			}
			fmt.Fprint(w, `{"success":true,"data":"https://cdn.example/dl/1?sig=x"}`)
		case "/torrents/createtorrent":
			if err := r.ParseMultipartForm(1 << 20); err != nil {
				t.Errorf("form: %v", err)
			}
			if r.FormValue("magnet") == "" || r.FormValue("add_only_if_cached") != "true" {
				t.Errorf("createtorrent form %v", r.MultipartForm.Value)
			}
			w.WriteHeader(http.StatusTooManyRequests)
			fmt.Fprint(w, `{"success":false,"error":"RATE_LIMIT"}`)
		}
	}))
	c := New(srv.URL, "s3cret", nil)
	_, err := c.Me(context.Background())
	var apiErr *APIError
	if !errors.As(err, &apiErr) || !apiErr.Unauthorized() || apiErr.Code != "BAD_TOKEN" {
		t.Fatalf("Me: %v", err)
	}
	link, err := c.RequestDL(context.Background(), 7, 0)
	if err != nil || link != "https://cdn.example/dl/1?sig=x" {
		t.Fatalf("RequestDL %q %v", link, err)
	}
	_, err = c.CreateTorrent(context.Background(), "magnet:?xt=urn:btih:"+hashN(1), true)
	if !errors.As(err, &apiErr) || !apiErr.RateLimited() {
		t.Fatalf("CreateTorrent: %v", err)
	}
	srv.Close()
	// Transport errors never carry the URL (requestdl has the key in its query)
	_, err = c.RequestDL(context.Background(), 7, 0)
	if err == nil || strings.Contains(err.Error(), "s3cret") {
		t.Fatalf("closed server: %v", err)
	}
}

func TestRequestDLRejectsOddLinks(t *testing.T) {
	for _, data := range []string{`"ftp://x/y"`, `"http://cdn.example/x"`, `123`, `null`} {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			fmt.Fprintf(w, `{"success":true,"data":%s}`, data)
		}))
		if _, err := New(srv.URL, "k", nil).RequestDL(context.Background(), 1, 1); !errors.Is(err, ErrMalformed) {
			t.Errorf("%s: %v", data, err)
		}
		srv.Close()
	}
}

func TestDialerFallsBackToTheLastGoodAddress(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			c.Close()
		}
	}()
	noDNS := &net.Resolver{PreferGo: true, Dial: func(ctx context.Context, network, address string) (net.Conn, error) {
		return nil, errors.New("dns down")
	}}
	l := &lastGoodDialer{d: &net.Dialer{Timeout: time.Second, Resolver: noDNS}, last: map[string]string{}}
	_, port, _ := net.SplitHostPort(ln.Addr().String())
	addr := net.JoinHostPort("cdn.example.invalid", port)
	if _, err := l.DialContext(context.Background(), "tcp", addr); err == nil {
		t.Fatal("dialed an unknown name without DNS")
	}
	l.last[addr] = ln.Addr().String() // an earlier connection succeeded
	c, err := l.DialContext(context.Background(), "tcp", addr)
	if err != nil {
		t.Fatalf("fallback: %v", err)
	}
	c.Close()
}

// The account's list is read page by page until the torrent turns up (or a short page ends it).
func TestFindTorrentPages(t *testing.T) {
	old := myListPage
	myListPage = 10
	t.Cleanup(func() { myListPage = old })
	var mu sync.Mutex
	var offsets []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		mu.Lock()
		offsets = append(offsets, q.Get("offset"))
		mu.Unlock()
		if r.URL.Path != "/torrents/mylist" || q.Get("limit") != "10" || q.Get("bypass_cache") != "true" {
			t.Errorf("request %s", r.URL)
		}
		var off int
		fmt.Sscanf(q.Get("offset"), "%d", &off)
		var items []string
		for i := off; i < min(off+10, 25); i++ {
			items = append(items, fmt.Sprintf(`{"id":%d,"hash":"%s"}`, 100+i, strings.ToUpper(hashN(i))))
		}
		fmt.Fprintf(w, `{"success":true,"data":[%s]}`, strings.Join(items, ","))
	}))
	defer srv.Close()
	c := New(srv.URL, "k", nil)
	id, err := c.FindTorrent(context.Background(), hashN(22), true)
	if err != nil || id != 122 {
		t.Fatalf("found %d, %v", id, err)
	}
	if strings.Join(offsets, ",") != "0,10,20" {
		t.Fatalf("offsets %v", offsets)
	}
	offsets = nil
	if id, err := c.FindTorrent(context.Background(), hashN(99), true); err != nil || id != 0 || len(offsets) != 3 {
		t.Fatalf("missing: %d %v after %d pages", id, err, len(offsets))
	}
}
