package providers

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"

	"nextflix/internal/platform/tmdb"
)

// fakeTMDB answers /discover/tv with one show per region; regions in down get a 404.
func fakeTMDB(t *testing.T) (*Service, func(region string, down bool), *atomic.Int32) {
	t.Helper()
	var mu sync.Mutex
	down := map[string]bool{}
	var hits atomic.Int32
	ids := map[string]int{"US": 1, "IN": 2, "BD": 3}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		region := r.URL.Query().Get("watch_region")
		mu.Lock()
		d := down[region]
		mu.Unlock()
		if d {
			http.NotFound(w, r)
			return
		}
		json.NewEncoder(w).Encode(map[string]any{"total_pages": 1, "total_results": 1,
			"results": []map[string]any{{"id": ids[region], "name": region, "overview": "x", "popularity": ids[region]}}})
	}))
	t.Cleanup(srv.Close)
	set := func(region string, d bool) {
		mu.Lock()
		down[region] = d
		mu.Unlock()
	}
	return NewService(tmdb.New("k").WithBaseURL(srv.URL)), set, &hits
}

func TestDiscoverTVAllRegionsFail(t *testing.T) {
	s, set, _ := fakeTMDB(t)
	for _, r := range SupportedRegions {
		set(r, true)
	}
	if res, err := s.DiscoverTV("en", 8, 1, ""); err == nil {
		t.Fatalf("want an error, got %+v", res)
	}
	// The failure is not cached: the next call succeeds once TMDB is back
	for _, r := range SupportedRegions {
		set(r, false)
	}
	res, err := s.DiscoverTV("en", 8, 1, "")
	if err != nil || len(res.Results) != 3 {
		t.Fatalf("%+v %v", res, err)
	}
}

func TestDiscoverTVPartialIsNotCachedLong(t *testing.T) {
	s, set, hits := fakeTMDB(t)
	set("IN", true)
	res, err := s.DiscoverTV("en", 8, 1, "")
	if err != nil || len(res.Results) != 2 {
		t.Fatalf("partial: %+v %v", res, err)
	}
	// Within the retry window the partial copy is served from the cache
	n := hits.Load()
	if _, err := s.DiscoverTV("en", 8, 1, ""); err != nil || hits.Load() != n {
		t.Fatalf("err=%v hits %d -> %d", err, n, hits.Load())
	}
}
