package images

import (
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gin-gonic/gin"

	"nextflix/internal/platform/upstream"
)

func TestParseKey(t *testing.T) {
	valid := []struct{ size, file string }{
		{"w342", "/kqjL17yufvn9OVLyXYpvtyrFfak.jpg"},
		{"original", "/a-b_C.png"},
		{"w92", "/x.svg"},
	}
	for _, c := range valid {
		if _, err := parseKey(c.size, c.file); err != nil {
			t.Errorf("parseKey(%q, %q) = %v, want ok", c.size, c.file, err)
		}
	}

	invalid := []struct{ size, file string }{
		{"w999", "/a.jpg"},
		{"", "/a.jpg"},
		{"../w342", "/a.jpg"},
		{"w342", "/../../etc/passwd"},
		{"w342", "/..%2fa.jpg"},
		{"w342", "/sub/a.jpg"},
		{"w342", "/a.jpg/"},
		{"w342", "/.jpg"},
		{"w342", "/a.exe"},
		{"w342", "/a.jpg?x=1"},
		{"w342", "/"},
		{"w342", "/" + strings.Repeat("a", 200) + ".jpg"},
	}
	for _, c := range invalid {
		if _, err := parseKey(c.size, c.file); err == nil {
			t.Errorf("parseKey(%q, %q) accepted, want error", c.size, c.file)
		}
	}
}

func TestKeyFromTMDBPath(t *testing.T) {
	if _, ok := keyFromTMDBPath("w300", ""); ok {
		t.Error("empty path accepted")
	}
	if _, ok := keyFromTMDBPath("w300", "https://image.tmdb.org/t/p/w300/a.jpg"); ok {
		t.Error("absolute URL accepted")
	}
	if k, ok := keyFromTMDBPath("w300", "/a.jpg"); !ok || k.String() != "w300/a.jpg" {
		t.Errorf("got %v %v", k, ok)
	}
}

// fakeTMDB serves a fixed body for /t/p/<size>/ok*.jpg and 404 for anything else, counting hits.
// The first resets requests get a TCP reset; hang makes every request wait for the client.
type fakeTMDB struct {
	hits   atomic.Int32
	delay  time.Duration
	resets atomic.Int32
	hang   atomic.Bool
}

func (f *fakeTMDB) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.hits.Add(1)
	if f.resets.Add(-1) >= 0 {
		if conn, _, err := w.(http.Hijacker).Hijack(); err == nil {
			conn.(*net.TCPConn).SetLinger(0)
			conn.Close()
		}
		return
	}
	if f.hang.Load() {
		<-r.Context().Done()
		return
	}
	time.Sleep(f.delay)
	if !strings.Contains(r.URL.Path, "/ok") {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Content-Type", "image/jpeg")
	w.Write([]byte("jpeg-bytes:" + r.URL.Path))
}

func newTestService(t *testing.T, up *fakeTMDB, maxBytes int64) (*Service, *gin.Engine) {
	t.Helper()
	srv := httptest.NewServer(up)
	t.Cleanup(srv.Close)
	s := NewService(Config{Dir: t.TempDir(), Upstream: srv.URL, MaxBytes: maxBytes}, nil)
	gin.SetMode(gin.TestMode)
	r := gin.New()
	Register(r, s)
	return s, r
}

func get(r http.Handler, url string, hdr map[string]string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodGet, url, nil)
	for k, v := range hdr {
		req.Header.Set(k, v)
	}
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	return w
}

func TestServeCachesOnDisk(t *testing.T) {
	up := &fakeTMDB{}
	s, r := newTestService(t, up, 1<<20)

	w := get(r, "/tmdb-img/t/p/w342/ok1.jpg", nil)
	if w.Code != http.StatusOK || w.Header().Get("X-Cache") != "MISS" {
		t.Fatalf("first: %d %q", w.Code, w.Header().Get("X-Cache"))
	}
	if got := w.Body.String(); got != "jpeg-bytes:/t/p/w342/ok1.jpg" {
		t.Fatalf("body %q", got)
	}
	if cc := w.Header().Get("Cache-Control"); cc != immutable {
		t.Errorf("cache-control %q", cc)
	}
	if w.Header().Get("Content-Type") != "image/jpeg" || w.Header().Get("ETag") == "" || w.Header().Get("Last-Modified") == "" {
		t.Errorf("headers %v", w.Header())
	}
	if _, err := os.Stat(filepath.Join(s.store.dir, "w342", "ok1.jpg")); err != nil {
		t.Fatalf("not cached on disk: %v", err)
	}

	w = get(r, "/tmdb-img/t/p/w342/ok1.jpg", nil)
	if w.Code != http.StatusOK || w.Header().Get("X-Cache") != "HIT" {
		t.Fatalf("second: %d %q", w.Code, w.Header().Get("X-Cache"))
	}
	if n := up.hits.Load(); n != 1 {
		t.Fatalf("upstream hits = %d, want 1", n)
	}

	w = get(r, "/tmdb-img/t/p/w342/ok1.jpg", map[string]string{"If-None-Match": w.Header().Get("ETag")})
	if w.Code != http.StatusNotModified {
		t.Fatalf("conditional: %d", w.Code)
	}
}

func TestServeRejectsBadPaths(t *testing.T) {
	up := &fakeTMDB{}
	_, r := newTestService(t, up, 1<<20)
	for _, u := range []string{
		"/tmdb-img/t/p/w999/ok.jpg",
		"/tmdb-img/t/p/w342/..%2f..%2fetc%2fpasswd",
		"/tmdb-img/t/p/w342/a/b.jpg",
	} {
		if w := get(r, u, nil); w.Code != http.StatusNotFound {
			t.Errorf("%s: %d", u, w.Code)
		}
	}
	if n := up.hits.Load(); n != 0 {
		t.Errorf("upstream hit %d times for invalid paths", n)
	}
}

func TestNotFoundIsRemembered(t *testing.T) {
	up := &fakeTMDB{}
	_, r := newTestService(t, up, 1<<20)
	for i := 0; i < 3; i++ {
		if w := get(r, "/tmdb-img/t/p/w342/missing.jpg", nil); w.Code != http.StatusNotFound {
			t.Fatalf("got %d", w.Code)
		}
	}
	if n := up.hits.Load(); n != 1 {
		t.Fatalf("upstream hits = %d, want 1", n)
	}
}

func TestConcurrentRequestsShareOneDownload(t *testing.T) {
	up := &fakeTMDB{delay: 100 * time.Millisecond}
	_, r := newTestService(t, up, 1<<20)

	var wg sync.WaitGroup
	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if w := get(r, "/tmdb-img/t/p/w500/ok2.jpg", nil); w.Code != http.StatusOK {
				t.Errorf("got %d", w.Code)
			}
		}()
	}
	wg.Wait()
	if n := up.hits.Load(); n != 1 {
		t.Fatalf("upstream hits = %d, want 1", n)
	}
}

func TestCleanupEvictsOldestFirst(t *testing.T) {
	st, err := newStore(t.TempDir(), 100)
	if err != nil {
		t.Fatal(err)
	}
	body := strings.Repeat("x", 30)
	names := []string{"a.jpg", "b.jpg", "c.jpg", "d.jpg"}
	for i, n := range names {
		k := key{size: "w92", file: n}
		if err := st.write(k, strings.NewReader(body), -1); err != nil {
			t.Fatal(err)
		}
		old := time.Now().Add(-time.Duration(len(names)-i) * time.Hour)
		os.Chtimes(st.path(k), old, old)
	}
	// A stale temp file from an interrupted download.
	tmp := filepath.Join(st.dir, "w92", tempPrefix+"123")
	os.WriteFile(tmp, []byte("partial"), 0o644)
	stale := time.Now().Add(-2 * staleTemp)
	os.Chtimes(tmp, stale, stale)

	removed, total, err := st.cleanup()
	if err != nil {
		t.Fatal(err)
	}
	// 120 bytes > 100: drop the oldest until at most 90% (90 bytes) remain.
	if removed != 1 || total != 90 {
		t.Fatalf("removed=%d total=%d", removed, total)
	}
	for i, n := range names {
		_, err := os.Stat(st.path(key{size: "w92", file: n}))
		if exists := err == nil; exists != (i >= 1) {
			t.Errorf("%s exists=%v", n, exists)
		}
	}
	if _, err := os.Stat(tmp); err == nil {
		t.Error("stale temp file kept")
	}
}

func TestWriteRejectsTruncatedBody(t *testing.T) {
	st, err := newStore(t.TempDir(), 1<<20)
	if err != nil {
		t.Fatal(err)
	}
	k := key{size: "w92", file: "a.jpg"}
	if err := st.write(k, strings.NewReader("short"), 100); err == nil {
		t.Fatal("truncated body accepted")
	}
	if _, err := os.Stat(st.path(k)); err == nil {
		t.Fatal("partial file left in cache")
	}
	entries, _ := os.ReadDir(filepath.Join(st.dir, "w92"))
	if len(entries) != 0 {
		t.Fatalf("temp files left: %d", len(entries))
	}
}

func TestServeRetriesResets(t *testing.T) {
	up := &fakeTMDB{}
	up.resets.Store(2)
	_, r := newTestService(t, up, 1<<20)
	if w := get(r, "/tmdb-img/t/p/w92/ok3.jpg", nil); w.Code != http.StatusOK {
		t.Fatalf("got %d", w.Code)
	}
	if n := up.hits.Load(); n != 3 {
		t.Fatalf("upstream hits = %d, want 3", n)
	}
}

func TestServeFailsFastAndBriefly(t *testing.T) {
	up := &fakeTMDB{}
	up.hang.Store(true)
	s, r := newTestService(t, up, 1<<20)
	s.client = upstream.New(upstream.Options{HedgeAfter: 50 * time.Millisecond, Budget: 300 * time.Millisecond})

	start := time.Now()
	w := get(r, "/tmdb-img/t/p/w92/ok4.jpg", nil)
	if w.Code != http.StatusGatewayTimeout || w.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("got %d %q", w.Code, w.Header().Get("Cache-Control"))
	}
	if d := time.Since(start); d > time.Second {
		t.Fatalf("took %s", d)
	}

	// Remembered briefly: answered at once without asking upstream again.
	hits := up.hits.Load()
	if w := get(r, "/tmdb-img/t/p/w92/ok4.jpg", nil); w.Code != http.StatusGatewayTimeout || up.hits.Load() != hits {
		t.Fatalf("second: %d, hits %d -> %d", w.Code, hits, up.hits.Load())
	}
	s.negMu.Lock()
	ttl := time.Until(s.neg[key{size: "w92", file: "ok4.jpg"}].until)
	s.negMu.Unlock()
	if ttl > failureTTL {
		t.Fatalf("failure remembered for %s", ttl)
	}

	// Once the failure expires and upstream recovers, the image loads.
	up.hang.Store(false)
	s.negMu.Lock()
	delete(s.neg, key{size: "w92", file: "ok4.jpg"})
	s.negMu.Unlock()
	if w := get(r, "/tmdb-img/t/p/w92/ok4.jpg", nil); w.Code != http.StatusOK {
		t.Fatalf("after recovery: %d", w.Code)
	}
}
