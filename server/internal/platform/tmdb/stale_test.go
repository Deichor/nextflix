package tmdb

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// flakyTMDB answers like TMDB unless the path is in down, then it fails with a 404.
type flakyTMDB struct {
	mu   sync.Mutex
	down map[string]bool
	hits atomic.Int32
}

func newFlaky(t *testing.T) (*Client, *flakyTMDB) {
	t.Helper()
	f := &flakyTMDB{down: map[string]bool{}}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.hits.Add(1)
		f.mu.Lock()
		down := f.down[r.URL.Path] || f.down["*"]
		f.mu.Unlock()
		if down {
			http.NotFound(w, r)
			return
		}
		if strings.HasPrefix(r.URL.Path, "/movie/1") {
			json.NewEncoder(w).Encode(map[string]any{"id": 1, "title": "One", "overview": "x", "tagline": "t"})
			return
		}
		json.NewEncoder(w).Encode(map[string]any{"results": []map[string]any{{"id": 7, "title": "Seven", "overview": "x"}}})
	}))
	t.Cleanup(srv.Close)
	c := New("test-key")
	c.base = srv.URL
	return c, f
}

func (f *flakyTMDB) set(path string, down bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.down[path] = down
}

func expire(c *Client, key string) {
	c.cache.mu.Lock()
	defer c.cache.mu.Unlock()
	e := c.cache.items[c.cacheKey(key)]
	e.expires = time.Now().Add(-time.Second)
	c.cache.items[c.cacheKey(key)] = e
}

func TestStaleOnError(t *testing.T) {
	c, f := newFlaky(t)
	if _, err := c.GetMovieDetails(1); err != nil {
		t.Fatal(err)
	}
	expire(c, "movie_details_1")
	f.set("*", true)

	d, err := c.GetMovieDetails(1)
	if err != nil || d.Title != "One" {
		t.Fatalf("stale: %v %v", d, err)
	}
	waitIdle(t, c)
	// The stale value now counts as fresh for a minute: no new upstream request.
	hits := f.hits.Load()
	if _, err := c.GetMovieDetails(1); err != nil || f.hits.Load() != hits {
		t.Fatalf("err=%v hits %d -> %d", err, hits, f.hits.Load())
	}
	// Nothing cached: the error comes through.
	if _, err := c.GetMovieDetails(2); err == nil {
		t.Fatal("want error without stale data")
	}
}

func TestPartialListPrefersStale(t *testing.T) {
	c, f := newFlaky(t)
	full, err := c.GetUpcomingMovies()
	if err != nil || len(full.TrendingDaily) != 1 {
		t.Fatalf("%v %v", full, err)
	}
	expire(c, "upcoming_movies_combined")
	f.set("/trending/movie/day", true)

	got, err := c.GetUpcomingMovies()
	if err != nil || len(got.TrendingDaily) != 1 {
		t.Fatalf("want the stale complete lists, got %+v %v", got, err)
	}
	waitIdle(t, c)
}

func TestPartialListIsCachedBriefly(t *testing.T) {
	c, f := newFlaky(t)
	f.set("/trending/movie/day", true)
	got, err := c.GetUpcomingMovies()
	if err != nil || len(got.TrendingDaily) != 0 || len(got.Upcoming) != 1 {
		t.Fatalf("%+v %v", got, err)
	}
	c.cache.mu.Lock()
	e := c.cache.items[c.cacheKey("upcoming_movies_combined")]
	c.cache.mu.Unlock()
	if ttl := time.Until(e.expires); ttl > retryAfter {
		t.Fatalf("partial result cached for %s", ttl)
	}
}

func TestStaleEntriesAreDropped(t *testing.T) {
	m := newMemoryCache(time.Hour, 10)
	m.set("a", 1, 0)
	m.mu.Lock()
	e := m.items["a"]
	e.expires, e.stale = time.Now().Add(-2*time.Second), time.Now().Add(-time.Second)
	m.items["a"] = e
	m.mu.Unlock()
	if _, _, ok := m.get("a"); ok {
		t.Fatal("entry past its stale time returned")
	}
}

// waitIdle waits until no background refresh is queued or running.
func waitIdle(t *testing.T, c *Client) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		g := c.flight
		g.mu.Lock()
		idle := len(g.calls) == 0 && len(g.pending) == 0
		g.mu.Unlock()
		if idle {
			return
		}
		if time.Now().After(deadline) {
			t.Fatal("background refresh still running")
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// gatedTMDB answers /movie/1 and /movie/popular with the current title; while gated, requests
// wait for release.
type gatedTMDB struct {
	mu      sync.Mutex
	title   string
	gate    chan struct{}
	hits    atomic.Int32
	arrived chan struct{}
}

func newGated(t *testing.T) (*Client, *gatedTMDB) {
	t.Helper()
	g := &gatedTMDB{title: "Old", arrived: make(chan struct{}, 100)}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		g.hits.Add(1)
		g.mu.Lock()
		gate := g.gate
		g.mu.Unlock()
		g.arrived <- struct{}{}
		if gate != nil {
			<-gate
		}
		g.mu.Lock()
		title := g.title
		g.mu.Unlock()
		json.NewEncoder(w).Encode(map[string]any{"id": 1, "title": title, "overview": "x", "tagline": "t",
			"results": []map[string]any{{"id": 1, "title": title, "overview": "x"}}})
	}))
	t.Cleanup(srv.Close)
	c := New("test-key")
	c.base = srv.URL
	return c, g
}

func (g *gatedTMDB) hold(title string) chan struct{} {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.title = title
	g.gate = make(chan struct{})
	return g.gate
}

// popularTitle returns the first title of the cached popular list.
func popularTitle(c *Client) (string, error) {
	l, err := c.GetPopularMovies(1)
	if err != nil || len(l) == 0 {
		return "", err
	}
	return l[0].Title, nil
}

func TestStaleWhileRevalidate(t *testing.T) {
	c, g := newGated(t)
	if title, err := popularTitle(c); err != nil || title != "Old" {
		t.Fatalf("%q %v", title, err)
	}
	<-g.arrived
	expire(c, "popular_movies_page_1")
	release := g.hold("New")

	// Upstream is held: the stale value must come back without waiting for it
	done := make(chan string, 1)
	go func() {
		title, _ := popularTitle(c)
		done <- title
	}()
	select {
	case title := <-done:
		if title != "Old" {
			t.Fatalf("want the stale value, got %q", title)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("stale read waited for upstream")
	}
	select {
	case <-g.arrived:
	case <-time.After(3 * time.Second):
		t.Fatal("no background refresh")
	}
	close(release)
	waitIdle(t, c)
	if title, err := popularTitle(c); err != nil || title != "New" {
		t.Fatalf("after refresh: %q %v", title, err)
	}
}

func TestStaleRefreshOnce(t *testing.T) {
	c, g := newGated(t)
	if _, err := popularTitle(c); err != nil {
		t.Fatal(err)
	}
	<-g.arrived
	expire(c, "popular_movies_page_1")
	release := g.hold("New")
	before := g.hits.Load()

	var wg sync.WaitGroup
	for range 20 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if title, err := popularTitle(c); err != nil || title != "Old" {
				t.Errorf("stale read: %q %v", title, err)
			}
		}()
	}
	wg.Wait()
	close(release)
	waitIdle(t, c)
	if n := g.hits.Load() - before; n != 1 {
		t.Fatalf("upstream hit %d times, want 1", n)
	}
}

// Details (seasons, release dates) wait for the refresh instead of answering stale data.
func TestExpiredDetailsWaitForRefresh(t *testing.T) {
	c, g := newGated(t)
	if d, err := c.GetMovieDetails(1); err != nil || d.Title != "Old" {
		t.Fatalf("%v %v", d, err)
	}
	<-g.arrived
	expire(c, "movie_details_1")
	release := g.hold("New")

	done := make(chan *MovieDetails, 1)
	go func() {
		d, _ := c.GetMovieDetails(1)
		done <- d
	}()
	<-g.arrived
	select {
	case d := <-done:
		t.Fatalf("answered before the refresh: %+v", d)
	case <-time.After(100 * time.Millisecond):
	}
	close(release)
	if d := <-done; d == nil || d.Title != "New" {
		t.Fatalf("want the refreshed value, got %+v", d)
	}
}
