package providers

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"slices"
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

func TestDiscoverTVKeepsTheRequestedSort(t *testing.T) {
	var mu sync.Mutex
	minVotes := map[string]string{}
	shows := map[string][]map[string]any{
		"US": {{"id": 1, "vote_average": 7.1, "popularity": 900, "first_air_date": "2020-01-01"}},
		"IN": {{"id": 2, "vote_average": 8.9, "popularity": 10, "first_air_date": "2024-05-01"}},
		"BD": {{"id": 3, "vote_average": 8.2, "popularity": 300, "first_air_date": "2010-03-01"}},
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		mu.Lock()
		minVotes[q.Get("sort_by")] = q.Get("vote_count.gte")
		mu.Unlock()
		json.NewEncoder(w).Encode(map[string]any{"total_pages": 1, "total_results": 1, "results": shows[q.Get("watch_region")]})
	}))
	t.Cleanup(srv.Close)
	s := NewService(tmdb.New("k").WithBaseURL(srv.URL))

	order := func(sortBy string) []int {
		t.Helper()
		res, err := s.DiscoverTV("en", 8, 1, sortBy)
		if err != nil {
			t.Fatal(err)
		}
		var out []int
		for _, r := range res.Results {
			out = append(out, r.ID)
		}
		return out
	}
	for sortBy, want := range map[string][]int{
		"vote_average.desc":   {2, 3, 1},
		"popularity.desc":     {1, 3, 2},
		"first_air_date.desc": {2, 1, 3},
		"first_air_date.asc":  {3, 1, 2},
	} {
		if got := order(sortBy); !slices.Equal(got, want) {
			t.Errorf("%s: got %v, want %v", sortBy, got, want)
		}
	}
	if minVotes["vote_average.desc"] != minRatedVotes || minVotes["popularity.desc"] != "" {
		t.Errorf("vote_count.gte sent: %v", minVotes)
	}
}
