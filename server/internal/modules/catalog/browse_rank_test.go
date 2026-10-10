package catalog

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"sync"
	"testing"
	"time"

	"nextflix/internal/platform/tmdb"
)

func br(id int, avg float64, votes int, pop float64) tmdb.SearchResult {
	return tmdb.SearchResult{ID: id, MediaType: "movie", Title: "T" + strconv.Itoa(id), PosterPath: "/p.jpg",
		VoteAverage: avg, VoteCount: votes, Popularity: pop, ReleaseDate: "2010-01-01", GenreIDs: []int{16}}
}

func browseIDs(rs []tmdb.SearchResult) []int {
	out := []int{}
	for _, r := range rs {
		out = append(out, r.ID)
	}
	return out
}

func TestBayesianOrdering(t *testing.T) {
	pool := []tmdb.SearchResult{
		br(1, 9.0, 12, 10),    // few votes
		br(2, 8.6, 30000, 10), // many votes
		br(3, 7.0, 800, 10),
		br(4, 6.0, 900, 10),
		br(5, 6.5, 500, 10),
	}
	b := newBayes(pool)
	if b.m != 900 {
		t.Errorf("m = %v, want the 75th percentile vote count 900", b.m)
	}
	if b.rating(pool[0]) >= b.rating(pool[1]) {
		t.Errorf("WR 9.0/12 votes %.2f >= 8.6/30k votes %.2f", b.rating(pool[0]), b.rating(pool[1]))
	}
	got := browseIDs(rankBrowse(browseInput{b: b, now: time.Now()}, pool))
	if got[0] != 2 {
		t.Errorf("order %v, want the 8.6 with 30k votes first", got)
	}
	// Popularity and votes still lift a title with the same rating
	same := []tmdb.SearchResult{br(10, 7.5, 1000, 5), br(11, 7.5, 1000, 400)}
	if got := browseIDs(rankBrowse(browseInput{b: newBayes(same), now: time.Now()}, same)); got[0] != 11 {
		t.Errorf("popularity tie-break: %v", got)
	}
}

func TestBrowseLocalSignals(t *testing.T) {
	pool := []tmdb.SearchResult{br(1, 7.5, 2000, 20), br(2, 7.5, 2000, 20), br(3, 7.5, 2000, 20)}
	local := aggregateLocal(map[uint][]LocalTitle{
		1: {{MediaType: "movie", TmdbID: 2, Watched: true}, {MediaType: "movie", TmdbID: 2, Rating: "love"},
			{MediaType: "movie", TmdbID: 3, Rating: "dislike"}},
		2: {{MediaType: "movie", TmdbID: 2, Watched: true, Finished: true}},
	})
	if st := local.titles["movie:2"]; st.watchers != 2 || st.positive != 1 {
		t.Errorf("stats of 2: %+v", st)
	}
	// Seen from profile 3 (no own history): watched + loved first, disliked by others last
	in := browseInput{b: newBayes(pool), local: local, profile: 3, now: time.Now()}
	if got := browseIDs(rankBrowse(in, pool)); got[0] != 2 || got[2] != 3 {
		t.Errorf("order %v, want [2 1 3]", got)
	}
}

func TestBrowseOwnHistoryAndDislikes(t *testing.T) {
	pool := []tmdb.SearchResult{br(1, 8.5, 20000, 50), br(2, 8.0, 15000, 40), br(3, 7.0, 3000, 20), br(4, 6.0, 1000, 5)}
	local := aggregateLocal(map[uint][]LocalTitle{
		7: {
			{MediaType: "movie", TmdbID: 1, Watched: true, Finished: true}, // finished: demoted, still listed
			{MediaType: "movie", TmdbID: 2, Rating: "dislike"},             // disliked: last
		},
	})
	in := browseInput{b: newBayes(pool), local: local, profile: 7, now: time.Now()}
	got := browseIDs(rankBrowse(in, pool))
	if len(got) != 4 || got[3] != 2 {
		t.Fatalf("order %v, want the disliked title last", got)
	}
	if got[0] == 1 {
		t.Errorf("order %v: the finished title should be demoted", got)
	}
	// Another profile sees the finished title first (one watcher lifts it further)
	other := browseInput{b: newBayes(pool), local: local, profile: 8, now: time.Now()}
	if got := browseIDs(rankBrowse(other, pool)); got[0] != 1 {
		t.Errorf("other profile: %v", got)
	}
	// Liked genres lift a title a little
	a, b := br(5, 7.0, 1000, 10), br(6, 7.0, 1000, 10)
	b.GenreIDs = []int{28}
	pair := []tmdb.SearchResult{a, b}
	taste := browseInput{b: newBayes(pair), taste: map[int]float64{28: 1}, now: time.Now()}
	if got := browseIDs(rankBrowse(taste, pair)); got[0] != 6 {
		t.Errorf("taste: %v", got)
	}
}

// fakeDiscover serves /discover/movie and /discover/tv: per sort order a list of movies (TV is
// empty); popularity has 10 pages in total.
func fakeDiscover(t *testing.T, bySort map[string][]map[string]any) (*tmdb.Client, map[string]int) {
	t.Helper()
	asked := map[string]int{}
	var mu sync.Mutex
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		mu.Lock()
		asked[r.URL.Path+" "+q.Get("sort_by")+" "+q.Get("with_genres")+" "+q.Get("vote_count.gte")]++
		mu.Unlock()
		page, _ := strconv.Atoi(q.Get("page"))
		results := []map[string]any{}
		if r.URL.Path == "/discover/movie" {
			if list := bySort[q.Get("sort_by")]; page >= 1 && page <= len(list) {
				results = append(results, list[page-1])
			}
		}
		json.NewEncoder(w).Encode(map[string]any{"page": page, "total_pages": 10, "total_results": 200, "results": results})
	}))
	t.Cleanup(srv.Close)
	return tmdb.New("test-key").WithBaseURL(srv.URL), asked
}

func movieJSON(id int, avg float64, votes int, pop float64) map[string]any {
	return map[string]any{"id": id, "title": "M" + strconv.Itoa(id), "original_title": "M" + strconv.Itoa(id),
		"poster_path": "/p.jpg", "vote_average": avg, "vote_count": votes, "popularity": pop,
		"release_date": "2015-01-01", "genre_ids": []int{16, 28}, "overview": "x", "original_language": "ja"}
}

func TestBrowseRankedPoolAndFallback(t *testing.T) {
	popular := []map[string]any{movieJSON(1, 6.0, 300, 900), movieJSON(2, 7.0, 500, 300)}
	voted := []map[string]any{movieJSON(3, 8.7, 40000, 60)}
	rated := []map[string]any{movieJSON(4, 9.1, 250, 15)}
	// Popularity page 6 (the first after the pool) repeats a pool title and adds a new one
	for len(popular) < browsePopPages {
		popular = append(popular, movieJSON(100+len(popular), 5, 50, 1))
	}
	popular = append(popular, movieJSON(3, 8.7, 40000, 60))
	tm, asked := fakeDiscover(t, map[string][]map[string]any{
		"popularity.desc": popular, "vote_count.desc": voted, "vote_average.desc": rated,
	})
	s := NewService(tm)
	f := Filter{Type: "movie", Anime: true, Genres: []int{28}}

	res, err := s.Browse(tm, f, 1, 0)
	if err != nil {
		t.Fatal(err)
	}
	got := browseIDs(res.Results)
	if len(got) == 0 || got[0] != 3 {
		t.Errorf("page 1 = %v, want the 8.7 / 40k votes title first", got)
	}
	// AND genres + anime reach every query; the rated list needs 200 votes
	if asked["/discover/movie vote_average.desc 28,16 200"] != browseRatedPages ||
		asked["/discover/movie popularity.desc 28,16 10"] != browsePopPages ||
		asked["/discover/tv popularity.desc 28,16 10"] != 0 {
		t.Errorf("queries: %v", asked)
	}
	// One ranked page (pool < 20), then TMDB popularity from page 6 without repeats
	if res.TotalPages != 1+10-browsePopPages {
		t.Errorf("total pages %d", res.TotalPages)
	}
	res2, err := s.Browse(tm, f, 2, 0)
	if err != nil {
		t.Fatal(err)
	}
	if got := browseIDs(res2.Results); len(got) != 0 {
		t.Errorf("page 2 = %v, want the pool title on page 6 dropped", got)
	}
}

func TestBrowsePagesKeepOneOrder(t *testing.T) {
	// 20 movies per popularity page: a pool of 100, five ranked pages
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		page, _ := strconv.Atoi(q.Get("page"))
		results := []map[string]any{}
		if r.URL.Path == "/discover/movie" && q.Get("sort_by") == "popularity.desc" {
			for i := range 20 {
				id := page*100 + i
				results = append(results, movieJSON(id, 5+float64(id%37)/10, 100+id, float64(id%13)))
			}
		}
		json.NewEncoder(w).Encode(map[string]any{"page": page, "total_pages": 5, "total_results": 100, "results": results})
	}))
	defer srv.Close()
	tm := tmdb.New("test-key").WithBaseURL(srv.URL)
	s := NewService(tm)
	var mu sync.Mutex
	signals := map[uint][]LocalTitle{}
	s.SetLocalSignals(func() (map[uint][]LocalTitle, error) {
		mu.Lock()
		defer mu.Unlock()
		return signals, nil
	})
	f := Filter{Type: "movie"}

	p1, err := s.Browse(tm, f, 1, 7)
	if err != nil {
		t.Fatal(err)
	}
	first := browseIDs(p1.Results)
	// The signals change mid-scroll (cache expiry, a rating elsewhere): 20 titles beyond page 1
	// are loved by ten profiles now, so a re-ranked page 2 would be the old page 1
	full := map[int]bool{}
	for _, id := range first {
		full[id] = true
	}
	var loved []LocalTitle
	for id := 100; len(loved) < 20; id++ {
		if !full[id] && id%100 < 20 {
			loved = append(loved, LocalTitle{MediaType: "movie", TmdbID: id, Rating: "love"})
		}
	}
	mu.Lock()
	for pid := uint(1); pid <= 10; pid++ {
		signals[pid] = loved
	}
	mu.Unlock()
	s.local.invalidate()

	p2, err := s.Browse(tm, f, 2, 7)
	if err != nil {
		t.Fatal(err)
	}
	for _, id := range browseIDs(p2.Results) {
		if full[id] {
			t.Fatalf("page 2 repeats %d from page 1: %v / %v", id, first, browseIDs(p2.Results))
		}
	}
	// A new scroll (page 1 again) ranks with the new signals
	p1, _ = s.Browse(tm, f, 1, 7)
	if id := p1.Results[0].ID; full[id] {
		t.Errorf("fresh page 1 starts with %d, want a loved title", id)
	}
}

func TestBrowseOrdersBounded(t *testing.T) {
	var c browseOrders
	now := time.Now()
	for i := range browseOrderMax + 10 {
		c.put(strconv.Itoa(i), browseOrder{}, now.Add(time.Duration(i)*time.Millisecond))
	}
	if len(c.items) != browseOrderMax {
		t.Fatalf("%d orders kept", len(c.items))
	}
	if _, ok := c.get("0", now); ok {
		t.Error("the oldest order was not evicted")
	}
	if _, ok := c.get(strconv.Itoa(browseOrderMax+9), now.Add(browseOrderTTL+time.Second)); ok {
		t.Error("expired order returned")
	}
}
