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
