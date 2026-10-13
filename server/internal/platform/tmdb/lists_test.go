package tmdb

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"
)

// barrierTMDB holds every request until 4 are in flight (or a timeout passes) and records the peak.
func barrierTMDB(t *testing.T) (*Client, func() int) {
	t.Helper()
	var mu sync.Mutex
	inFlight, peak := 0, 0
	all := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		inFlight++
		peak = max(peak, inFlight)
		if inFlight == 4 {
			close(all)
		}
		mu.Unlock()
		select {
		case <-all:
		case <-time.After(time.Second):
		}
		mu.Lock()
		inFlight--
		mu.Unlock()
		json.NewEncoder(w).Encode(map[string]any{"results": []map[string]any{{"id": 1, "title": "One", "name": "One", "overview": "x"}}})
	}))
	t.Cleanup(srv.Close)
	c := New("test-key")
	c.base = srv.URL
	return c, func() int {
		mu.Lock()
		defer mu.Unlock()
		return peak
	}
}

func TestCombinedFeedsFetchInParallel(t *testing.T) {
	c, peak := barrierTMDB(t)
	mv, err := c.GetUpcomingMovies()
	if err != nil || len(mv.TrendingDaily) != 1 || len(mv.Upcoming) != 1 {
		t.Fatalf("%+v %v", mv, err)
	}
	if p := peak(); p < 4 {
		t.Fatalf("movie lists fetched one after another (peak %d in flight)", p)
	}

	c, peak = barrierTMDB(t)
	tv, err := c.GetUpcomingTVSeries()
	if err != nil || len(tv.AiringToday) != 1 || len(tv.TrendingWeekly) != 1 {
		t.Fatalf("%+v %v", tv, err)
	}
	if p := peak(); p < 4 {
		t.Fatalf("TV lists fetched one after another (peak %d in flight)", p)
	}
}
