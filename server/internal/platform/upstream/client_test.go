package upstream

import (
	"context"
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
)

// reset closes the client connection with a TCP RST, like the ISP's DPI does.
func reset(t *testing.T, w http.ResponseWriter) {
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

// server runs handle for every request with its 1-based number and counts new connections.
func server(t *testing.T, handle func(n int32, w http.ResponseWriter, r *http.Request)) (*httptest.Server, *atomic.Int32, *atomic.Int32) {
	t.Helper()
	var reqs, conns atomic.Int32
	srv := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		handle(reqs.Add(1), w, r)
	}))
	srv.Config.ConnState = func(_ net.Conn, s http.ConnState) {
		if s == http.StateNew {
			conns.Add(1)
		}
	}
	srv.Start()
	t.Cleanup(srv.Close)
	return srv, &reqs, &conns
}

func ok(w http.ResponseWriter) {
	w.Header().Set("Content-Type", "text/plain")
	w.Write([]byte("ok"))
}

func TestRetriesResetOnNewConnection(t *testing.T) {
	srv, reqs, conns := server(t, func(n int32, w http.ResponseWriter, r *http.Request) {
		if n <= 2 {
			reset(t, w)
			return
		}
		ok(w)
	})
	c := New(Options{})
	resp, err := c.Get(context.Background(), srv.URL, nil)
	if err != nil {
		t.Fatal(err)
	}
	if resp.Status != 200 || string(resp.Body) != "ok" {
		t.Fatalf("got %d %q", resp.Status, resp.Body)
	}
	if reqs.Load() != 3 || conns.Load() != 3 {
		t.Fatalf("requests=%d conns=%d, want 3 and 3", reqs.Load(), conns.Load())
	}
	if s := c.Stats(); s.Retries != 2 || s.Failures != 0 {
		t.Fatalf("stats %+v", s)
	}
}

func TestGivesUpAfterAttempts(t *testing.T) {
	srv, reqs, _ := server(t, func(_ int32, w http.ResponseWriter, _ *http.Request) { reset(t, w) })
	c := New(Options{Attempts: 3})
	start := time.Now()
	if _, err := c.Get(context.Background(), srv.URL, nil); err == nil {
		t.Fatal("want error")
	}
	if d := time.Since(start); d > 2*time.Second {
		t.Fatalf("took %s", d)
	}
	if reqs.Load() != 3 {
		t.Fatalf("requests=%d, want 3", reqs.Load())
	}
}

func TestRetriesTruncatedBody(t *testing.T) {
	srv, reqs, _ := server(t, func(n int32, w http.ResponseWriter, _ *http.Request) {
		if n == 1 {
			w.Header().Set("Content-Length", "100")
			w.WriteHeader(200)
			w.Write([]byte("partial"))
			w.(http.Flusher).Flush()
			reset(t, w)
			return
		}
		ok(w)
	})
	resp, err := New(Options{}).Get(context.Background(), srv.URL, nil)
	if err != nil || string(resp.Body) != "ok" || reqs.Load() != 2 {
		t.Fatalf("resp=%v err=%v requests=%d", resp, err, reqs.Load())
	}
}

func TestStatuses(t *testing.T) {
	srv, reqs, _ := server(t, func(n int32, w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasPrefix(r.URL.Path, "/missing"):
			http.NotFound(w, r)
		case strings.HasPrefix(r.URL.Path, "/flaky") && n%2 == 1:
			w.WriteHeader(http.StatusServiceUnavailable)
		default:
			ok(w)
		}
	})
	c := New(Options{})

	resp, err := c.Get(context.Background(), srv.URL+"/missing", nil)
	if err != nil || resp.Status != 404 || reqs.Load() != 1 {
		t.Fatalf("404: resp=%v err=%v requests=%d", resp, err, reqs.Load())
	}
	reqs.Store(0)
	resp, err = c.Get(context.Background(), srv.URL+"/flaky", nil)
	if err != nil || resp.Status != 200 || reqs.Load() != 2 {
		t.Fatalf("503 then 200: resp=%v err=%v requests=%d", resp, err, reqs.Load())
	}
}

func TestHedgesSlowFirstTry(t *testing.T) {
	srv, reqs, conns := server(t, func(n int32, w http.ResponseWriter, r *http.Request) {
		if n == 1 {
			select {
			case <-time.After(5 * time.Second):
			case <-r.Context().Done():
			}
			return
		}
		ok(w)
	})
	c := New(Options{HedgeAfter: 200 * time.Millisecond})
	start := time.Now()
	resp, err := c.Get(context.Background(), srv.URL, nil)
	d := time.Since(start)
	if err != nil || string(resp.Body) != "ok" {
		t.Fatalf("resp=%v err=%v", resp, err)
	}
	if d < 200*time.Millisecond || d > time.Second {
		t.Fatalf("took %s, want about the hedge delay", d)
	}
	if reqs.Load() != 2 || conns.Load() != 2 {
		t.Fatalf("requests=%d conns=%d, want 2 and 2", reqs.Load(), conns.Load())
	}
	if s := c.Stats(); s.Hedges != 1 {
		t.Fatalf("stats %+v", s)
	}
}

func TestNoHedgeOnceHeadersArrived(t *testing.T) {
	srv, reqs, _ := server(t, func(_ int32, w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(200)
		w.(http.Flusher).Flush()
		time.Sleep(400 * time.Millisecond) // slow body, healthy connection
		w.Write([]byte("ok"))
	})
	c := New(Options{HedgeAfter: 100 * time.Millisecond})
	if resp, err := c.Get(context.Background(), srv.URL, nil); err != nil || string(resp.Body) != "ok" {
		t.Fatalf("resp=%v err=%v", resp, err)
	}
	if reqs.Load() != 1 {
		t.Fatalf("requests=%d, want 1", reqs.Load())
	}
}

func TestBudget(t *testing.T) {
	srv, _, _ := server(t, func(_ int32, _ http.ResponseWriter, r *http.Request) {
		select {
		case <-time.After(5 * time.Second):
		case <-r.Context().Done():
		}
	})
	c := New(Options{HedgeAfter: 100 * time.Millisecond, Budget: 500 * time.Millisecond})
	start := time.Now()
	_, err := c.Get(context.Background(), srv.URL, nil)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("err=%v, want deadline exceeded", err)
	}
	if d := time.Since(start); d > 800*time.Millisecond {
		t.Fatalf("took %s, budget 500ms", d)
	}
}

func TestBodyLimitIsNotRetried(t *testing.T) {
	srv, reqs, _ := server(t, func(_ int32, w http.ResponseWriter, _ *http.Request) {
		w.Write([]byte(strings.Repeat("x", 100)))
	})
	_, err := New(Options{MaxBody: 10}).Get(context.Background(), srv.URL, nil)
	if !errors.Is(err, ErrTooLarge) || reqs.Load() != 1 {
		t.Fatalf("err=%v requests=%d", err, reqs.Load())
	}
}

func TestSendsHeaders(t *testing.T) {
	srv, _, _ := server(t, func(_ int32, w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(r.Header.Get("Authorization") + "|" + r.Header.Get("User-Agent")))
	})
	c := New(Options{UserAgent: "test-agent"})
	resp, err := c.Get(context.Background(), srv.URL, http.Header{"Authorization": {"Bearer x"}})
	if err != nil || string(resp.Body) != "Bearer x|test-agent" {
		t.Fatalf("resp=%q err=%v", resp.Body, err)
	}
}

func TestRetriesStalledBody(t *testing.T) {
	srv, reqs, _ := server(t, func(n int32, w http.ResponseWriter, r *http.Request) {
		if n == 1 {
			w.Header().Set("Content-Length", "100")
			w.WriteHeader(200)
			w.Write([]byte("partial"))
			w.(http.Flusher).Flush()
			select {
			case <-time.After(5 * time.Second):
			case <-r.Context().Done():
			}
			return
		}
		ok(w)
	})
	c := New(Options{HedgeAfter: 100 * time.Millisecond, ReadIdleTimeout: 200 * time.Millisecond})
	start := time.Now()
	resp, err := c.Get(context.Background(), srv.URL, nil)
	if err != nil || string(resp.Body) != "ok" || reqs.Load() != 2 {
		t.Fatalf("resp=%v err=%v requests=%d", resp, err, reqs.Load())
	}
	if d := time.Since(start); d > time.Second {
		t.Fatalf("took %s", d)
	}
}

func TestGivesUpWithErrTransport(t *testing.T) {
	srv, _, _ := server(t, func(_ int32, w http.ResponseWriter, _ *http.Request) { reset(t, w) })
	_, err := New(Options{Attempts: 2}).Get(context.Background(), srv.URL, nil)
	if !errors.Is(err, ErrTransport) {
		t.Fatalf("err = %v, want ErrTransport", err)
	}
	slow, _, _ := server(t, func(_ int32, w http.ResponseWriter, r *http.Request) {
		select {
		case <-time.After(2 * time.Second):
		case <-r.Context().Done():
		}
	})
	_, err = New(Options{Budget: 100 * time.Millisecond}).Get(context.Background(), slow.URL, nil)
	if !errors.Is(err, ErrTransport) || !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("budget err = %v, want ErrTransport and DeadlineExceeded", err)
	}
}

func TestPostRetriesResetWithBody(t *testing.T) {
	var mu sync.Mutex
	var bodies []string
	srv, reqs, conns := server(t, func(n int32, w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		mu.Lock()
		bodies = append(bodies, r.Method+" "+string(b)+" "+r.Header.Get("Content-Type"))
		mu.Unlock()
		if n <= 2 {
			reset(t, w)
			return
		}
		ok(w)
	})
	// HedgeAfter is ignored for POSTs: a slow answer must not be sent twice
	c := New(Options{HedgeAfter: time.Millisecond})
	resp, err := c.Post(context.Background(), srv.URL, http.Header{"Content-Type": {"application/json"}}, []byte(`{"a":1}`))
	if err != nil || string(resp.Body) != "ok" {
		t.Fatalf("resp=%v err=%v", resp, err)
	}
	if reqs.Load() != 3 || conns.Load() != 3 || c.Stats().Hedges != 0 {
		t.Fatalf("requests=%d conns=%d stats=%+v", reqs.Load(), conns.Load(), c.Stats())
	}
	mu.Lock()
	defer mu.Unlock()
	for _, b := range bodies {
		if b != `POST {"a":1} application/json` {
			t.Fatalf("request %q", b)
		}
	}
}

func TestRetryStatusOption(t *testing.T) {
	srv, reqs, _ := server(t, func(_ int32, w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusTooManyRequests)
	})
	c := New(Options{RetryStatus: func(code int) bool { return code == http.StatusBadGateway }})
	resp, err := c.Get(context.Background(), srv.URL, nil)
	if err != nil || resp.Status != 429 || reqs.Load() != 1 {
		t.Fatalf("resp=%v err=%v requests=%d", resp, err, reqs.Load())
	}
}

func TestCheckRedirectOption(t *testing.T) {
	srv, _, _ := server(t, func(_ int32, w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/a" {
			http.Redirect(w, r, "http://example.invalid/b", http.StatusFound)
			return
		}
		ok(w)
	})
	refuse := errors.New("off site")
	c := New(Options{CheckRedirect: func(req *http.Request, _ []*http.Request) error {
		if req.URL.Host != strings.TrimPrefix(srv.URL, "http://") {
			return refuse
		}
		return nil
	}})
	if _, err := c.Get(context.Background(), srv.URL+"/a", nil); !errors.Is(err, refuse) {
		t.Fatalf("err = %v, want the CheckRedirect error", err)
	}
}

// A real 429 / 503 must not be hidden by a later reset or the budget running out.
func TestKeepsLastStatusOverLaterFailures(t *testing.T) {
	srv, _, _ := server(t, func(n int32, w http.ResponseWriter, _ *http.Request) {
		if n == 1 {
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		reset(t, w)
	})
	resp, err := New(Options{Attempts: 2}).Get(context.Background(), srv.URL, nil)
	if err != nil || resp.Status != http.StatusServiceUnavailable {
		t.Fatalf("503 then reset: resp=%v err=%v", resp, err)
	}

	slow, _, _ := server(t, func(n int32, w http.ResponseWriter, r *http.Request) {
		if n == 1 {
			w.Header().Set("Retry-After", "0")
			w.WriteHeader(http.StatusTooManyRequests)
			return
		}
		select {
		case <-time.After(2 * time.Second):
		case <-r.Context().Done():
		}
	})
	resp, err = New(Options{Budget: 500 * time.Millisecond}).Get(context.Background(), slow.URL, nil)
	if err != nil || resp.Status != http.StatusTooManyRequests || resp.Header.Get("Retry-After") != "0" {
		t.Fatalf("429 then budget: resp=%v err=%v", resp, err)
	}
}

func TestHonorsRetryAfter(t *testing.T) {
	srv, reqs, _ := server(t, func(n int32, w http.ResponseWriter, r *http.Request) {
		if n == 1 {
			w.Header().Set("Retry-After", r.URL.Query().Get("ra"))
			w.WriteHeader(http.StatusTooManyRequests)
			return
		}
		ok(w)
	})
	c := New(Options{Budget: 3 * time.Second})
	start := time.Now()
	resp, err := c.Get(context.Background(), srv.URL+"?ra=1", nil)
	if err != nil || resp.Status != 200 || reqs.Load() != 2 {
		t.Fatalf("resp=%v err=%v requests=%d", resp, err, reqs.Load())
	}
	if d := time.Since(start); d < time.Second {
		t.Fatalf("retried after %s, Retry-After was 1s", d)
	}

	// Longer than the budget: the 429 comes back at once instead of a doomed wait
	reqs.Store(0)
	start = time.Now()
	resp, err = c.Get(context.Background(), srv.URL+"?ra=30", nil)
	if err != nil || resp.Status != http.StatusTooManyRequests || reqs.Load() != 1 {
		t.Fatalf("resp=%v err=%v requests=%d", resp, err, reqs.Load())
	}
	if d := time.Since(start); d > 500*time.Millisecond {
		t.Fatalf("took %s", d)
	}
}
