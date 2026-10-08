package subtitles

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"nextflix/internal/platform/upstream"
)

func testClient() *upstream.Client { return newProviderClient(true, true, nil) }

// resetConn closes the client connection with a TCP RST, like the ISP's DPI does.
func resetConn(t *testing.T, w http.ResponseWriter) {
	conn, _, err := w.(http.Hijacker).Hijack()
	if err != nil {
		t.Errorf("hijack: %v", err)
		return
	}
	if tc, ok := conn.(*net.TCPConn); ok {
		tc.SetLinger(0)
	}
	conn.Close()
}

// flaky resets the first request of every "METHOD path" and answers the next ones with handle.
type flaky struct {
	t      *testing.T
	mu     sync.Mutex
	seen   map[string]int
	handle http.HandlerFunc
}

func (f *flaky) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	key := r.Method + " " + r.URL.Path
	f.mu.Lock()
	if f.seen == nil {
		f.seen = map[string]int{}
	}
	f.seen[key]++
	n := f.seen[key]
	f.mu.Unlock()
	if n == 1 {
		io.Copy(io.Discard, r.Body)
		resetConn(f.t, w)
		return
	}
	f.handle(w, r)
}

func (f *flaky) count(key string) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.seen[key]
}

func TestOpenSubtitlesRetriesResets(t *testing.T) {
	var srv *httptest.Server
	f := &flaky{t: t}
	f.handle = func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/login":
			var body map[string]string
			json.NewDecoder(r.Body).Decode(&body)
			if body["username"] != "user" {
				t.Errorf("login body %v", body)
			}
			io.WriteString(w, `{"token":"JWT"}`)
		case "/subtitles":
			io.WriteString(w, `{"data":[{"id":"1","attributes":{"language":"tr","release":"X","files":[{"file_id":7,"file_name":"x.srt"}]}}]}`)
		case "/download":
			var body map[string]int
			json.NewDecoder(r.Body).Decode(&body)
			if body["file_id"] != 7 {
				t.Errorf("download body %v", body)
			}
			json.NewEncoder(w).Encode(map[string]string{"link": srv.URL + "/file/7", "file_name": "x.srt"})
		case "/file/7":
			io.WriteString(w, sampleSRT)
		}
	}
	srv = httptest.NewServer(f)
	defer srv.Close()

	o := newOpenSubtitles(srv.URL, testClient())
	st := Settings{OpenSubtitlesAPIKey: "K", OpenSubtitlesUsername: "user", OpenSubtitlesPassword: "pw"}
	items, err := o.search(context.Background(), Query{TmdbID: 1, MediaType: "movie", Langs: []string{"tr"}}, st, "")
	if err != nil || len(items) != 1 {
		t.Fatalf("search: %v %d", err, len(items))
	}
	_, _, ref, _ := decodeID(items[0].ID)
	raw, _, err := o.fetch(context.Background(), ref, st)
	if err != nil || !strings.Contains(string(raw), "Merhaba") {
		t.Fatalf("fetch: %v", err)
	}
	for _, k := range []string{"POST /login", "GET /subtitles", "POST /download", "GET /file/7"} {
		if n := f.count(k); n != 2 {
			t.Errorf("%s: %d requests, want 2 (reset, then retried)", k, n)
		}
	}
}

func TestOpenSubtitlesCutAnswerIsTemporary(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, `{"data":[{"id":"1",`)
	}))
	defer srv.Close()
	o := newOpenSubtitles(srv.URL, testClient())
	_, err := o.search(context.Background(), Query{TmdbID: 1, MediaType: "movie", Langs: []string{"tr"}}, Settings{OpenSubtitlesAPIKey: "K"}, "")
	if err == nil || !isTemporary(err) {
		t.Fatalf("err = %v, want a temporary error", err)
	}
}

func TestSubDLRetriesResetsAndHidesKey(t *testing.T) {
	zipped := makeZip(t, map[string]string{"x.srt": sampleSRT})
	f := &flaky{t: t}
	f.handle = func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/api/v1/subtitles":
			io.WriteString(w, `{"status":true,"subtitles":[{"release_name":"X","lang":"TR","language":"TR","url":"/subtitle/1-2.zip"}]}`)
		case r.URL.Path == "/subtitle/1-2.zip":
			w.Write(zipped)
		}
	}
	srv := httptest.NewServer(f)
	defer srv.Close()
	d := newSubDL(srv.URL+"/api/v1", srv.URL, testClient())
	items, err := d.search(context.Background(), Query{TmdbID: 1, MediaType: "movie", Langs: []string{"tr"}}, Settings{SubDLAPIKey: "SECRETKEY"})
	if err != nil || len(items) != 1 {
		t.Fatalf("search: %v %d", err, len(items))
	}
	_, lang, ref, _ := decodeID(items[0].ID)
	raw, _, err := d.fetch(context.Background(), ref, lang)
	if err != nil || !strings.Contains(string(raw), "Merhaba") {
		t.Fatalf("fetch: %v", err)
	}
	if f.count("GET /api/v1/subtitles") != 2 || f.count("GET /subtitle/1-2.zip") != 2 {
		t.Fatalf("requests: search %d, file %d", f.count("GET /api/v1/subtitles"), f.count("GET /subtitle/1-2.zip"))
	}

	// A server that resets every time: the error is temporary and never shows the key
	dead := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { resetConn(t, w) }))
	defer dead.Close()
	d = newSubDL(dead.URL+"/api/v1", dead.URL, testClient())
	_, err = d.search(context.Background(), Query{TmdbID: 1, MediaType: "movie", Langs: []string{"tr"}}, Settings{SubDLAPIKey: "SECRETKEY"})
	if err == nil || !isTemporary(err) || strings.Contains(err.Error(), "SECRETKEY") {
		t.Fatalf("err = %v", err)
	}
}

func TestTurkceAltyaziRetriesResets(t *testing.T) {
	f := &flaky{t: t}
	f.handle = func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/tt1375666/":
			http.Redirect(w, r, "/mov/1375666/inception1.html", http.StatusMovedPermanently)
		case r.URL.Path == "/mov/1375666/inception1.html":
			io.WriteString(w, taMoviePage)
		case strings.HasPrefix(r.URL.Path, "/sub/"):
			io.WriteString(w, strings.Replace(taDetailPage, "ALTID", strings.Split(r.URL.Path, "/")[2], 1))
		case r.URL.Path == "/ind" && r.Method == http.MethodPost:
			r.ParseForm()
			if r.Form.Get("idid") != "746874" {
				t.Errorf("form %v", r.Form)
			}
			w.Write(makeZip(t, map[string]string{"x.srt": sampleSRT}))
		}
	}
	srv := httptest.NewServer(f)
	defer srv.Close()
	ta := newTurkceAltyazi(srv.URL, "", diskCache{dir: t.TempDir()})
	ta.gap = 0
	items, err := ta.search(context.Background(), Query{TmdbID: 27205, MediaType: "movie", Langs: []string{"tr"}}, "tt1375666")
	if err != nil || len(items) == 0 {
		t.Fatalf("search: %v %d", err, len(items))
	}
	_, lang, ref, _ := decodeID(items[0].ID)
	raw, _, err := ta.fetch(context.Background(), ref, lang)
	if err != nil || !strings.Contains(string(raw), "Merhaba") {
		t.Fatalf("fetch: %v", err)
	}
	// The redirect target was reset once too, so the whole GET (from /tt.../) ran 3 times
	for k, want := range map[string]int{"GET /tt1375666/": 3, "GET /mov/1375666/inception1.html": 2, "POST /ind": 2} {
		if n := f.count(k); n != want {
			t.Errorf("%s: %d requests, want %d", k, n, want)
		}
	}
	// An off-site redirect is refused at once, not retried
	var evilCalls atomic.Int32
	evil := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		evilCalls.Add(1)
		http.Redirect(w, r, "http://example.invalid/x", http.StatusFound)
	}))
	defer evil.Close()
	ta = newTurkceAltyazi(evil.URL, "", diskCache{})
	ta.gap = 0
	if _, err := ta.get(context.Background(), "/tt1/"); err == nil || isTemporary(err) || evilCalls.Load() != 1 {
		t.Fatalf("off-site redirect: %v (%d requests)", err, evilCalls.Load())
	}
}

func TestFlareSolverrNetworkErrorRetried(t *testing.T) {
	site := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("cf-mitigated", "challenge")
		w.WriteHeader(http.StatusForbidden)
	}))
	defer site.Close()
	var calls atomic.Int32
	fs := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if calls.Add(1) == 1 {
			json.NewEncoder(w).Encode(map[string]any{"status": "error", "message": "Error: net::ERR_CONNECTION_RESET at https://x"})
			return
		}
		json.NewEncoder(w).Encode(map[string]any{"status": "ok", "solution": map[string]any{"status": 200, "response": "solved"}})
	}))
	defer fs.Close()
	ta := newTurkceAltyazi(site.URL, fs.URL, diskCache{})
	ta.gap = 0
	b, err := ta.get(context.Background(), "/x")
	if err != nil || string(b) != "solved" || calls.Load() != 2 {
		t.Fatalf("%v %q calls=%d", err, b, calls.Load())
	}
}

// A provider that fails on the network is asked again on the next List; one that found
// nothing is not (its empty answer is cached).
func TestTransportFailuresAreNotCached(t *testing.T) {
	var osCalls, sdlCalls atomic.Int32
	osSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		osCalls.Add(1)
		resetConn(t, w)
	}))
	defer osSrv.Close()
	sdlSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		sdlCalls.Add(1)
		io.WriteString(w, `{"status":false,"error":"can't find subtitle"}`)
	}))
	defer sdlSrv.Close()
	svc := NewService(nil, Config{CacheDir: t.TempDir(), OpenSubtitlesURL: osSrv.URL, SubDLAPIURL: sdlSrv.URL})
	svc.wait = 5 * time.Second
	if err := svc.settings.save(Settings{OpenSubtitlesAPIKey: "K", SubDLAPIKey: "S", DefaultLangs: "tr"}); err != nil {
		t.Fatal(err)
	}
	q := Query{TmdbID: 1, MediaType: "movie"}
	for i := 0; i < 2; i++ {
		if items := svc.List(context.Background(), q); len(items) != 0 {
			t.Fatalf("items %v", items)
		}
	}
	if n := osCalls.Load(); n != 6 {
		t.Errorf("OpenSubtitles requests = %d, want 6 (3 tries per List, failure not cached)", n)
	}
	if n := sdlCalls.Load(); n != 1 {
		t.Errorf("SubDL requests = %d, want 1 (no results cached)", n)
	}
}

func TestIsTemporary(t *testing.T) {
	for _, c := range []struct {
		err  error
		want bool
	}{
		{statusError{code: 503}, true},
		{statusError{code: 401}, false},
		{errors.New("turkcealtyazi: title not found"), false},
		{ErrRateLimited, false},
		{context.DeadlineExceeded, true},
		{redact(upstream.ErrTransport, "x"), true},
	} {
		if got := isTemporary(c.err); got != c.want {
			t.Errorf("isTemporary(%v) = %v", c.err, got)
		}
	}
}
