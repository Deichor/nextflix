package recommendations

import (
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"
)

type fakeCatalog struct {
	mu       sync.Mutex
	related  map[titleKey]relatedLists
	genres   map[titleKey][]int
	trending []Item
	explore  func(q ExploreQuery) []Item
	calls    int
}

func (f *fakeCatalog) Related(lang, mediaType string, id int) ([]Item, []Item, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls++
	r, ok := f.related[keyOf(mediaType, id)]
	if !ok {
		return nil, nil, errors.New("no data")
	}
	return r.recs, r.similar, nil
}

func (f *fakeCatalog) Genres(mediaType string, id int) ([]int, error) {
	return f.genres[keyOf(mediaType, id)], nil
}

func (f *fakeCatalog) Trending(lang string) ([]Item, error) { return f.trending, nil }

func movie(id int, genres ...int) Item {
	return Item{ID: id, MediaType: "movie", Title: fmt.Sprintf("M%d", id), PosterPath: fmt.Sprintf("/%d.jpg", id),
		GenreIDs: genres, VoteAverage: 7, VoteCount: 500, Popularity: 50}
}

func movies(from, n int, genres ...int) []Item {
	out := make([]Item, 0, n)
	for i := 0; i < n; i++ {
		out = append(out, movie(from+i, genres...))
	}
	return out
}

func ids(items []Item) []int {
	out := make([]int, 0, len(items))
	for _, it := range items {
		out = append(out, it.ID)
	}
	return out
}

func contains(items []Item, id int) bool {
	for _, it := range items {
		if it.ID == id {
			return true
		}
	}
	return false
}

const (
	loved    = 1
	liked    = 2
	disliked = 3
	favOnly  = 4
)

// scenario: a loved title (action), a liked title, a disliked title (horror) and a favorite.
func scenario() (*fakeCatalog, []Signal) {
	now := time.Now()
	adult := movie(150)
	adult.Adult = true
	noPoster := movie(151)
	noPoster.PosterPath = ""

	f := &fakeCatalog{
		related: map[titleKey]relatedLists{
			// 100 is the loved title's first recommendation; liked (2) and disliked (3) titles show up too
			keyOf("movie", loved): {
				recs:    append([]Item{movie(100, 28), movie(liked), movie(disliked), adult, noPoster}, movies(101, 15, 28)...),
				similar: movies(120, 10, 28),
			},
			// 300 is only weakly supported by the liked title (last position, similar list) ...
			keyOf("movie", liked): {
				recs:    movies(200, 12, 35),
				similar: append(movies(220, 9, 35), movie(300, 27)),
			},
			// ... but it is the disliked title's top recommendation, so it must be dropped
			keyOf("movie", disliked): {
				recs:    append([]Item{movie(300, 27)}, movies(400, 5, 27)...),
				similar: movies(410, 5, 27),
			},
			keyOf("movie", favOnly): {
				recs: movies(500, 10, 18),
			},
		},
		genres: map[titleKey][]int{
			keyOf("movie", loved):    {28},
			keyOf("movie", liked):    {35},
			keyOf("movie", disliked): {27},
			keyOf("movie", favOnly):  {18},
		},
	}
	signals := []Signal{
		{MediaType: "movie", TmdbID: loved, Title: "Loved", Rating: "love", UpdatedAt: now},
		{MediaType: "movie", TmdbID: liked, Title: "Liked", Rating: "like", UpdatedAt: now.Add(-time.Hour)},
		{MediaType: "movie", TmdbID: disliked, Title: "Nope", Rating: "dislike", UpdatedAt: now},
		{MediaType: "movie", TmdbID: favOnly, Title: "Fav", Favorite: true, UpdatedAt: now.Add(-2 * time.Hour)},
	}
	return f, signals
}

func newTestService(f *fakeCatalog, signals []Signal) *Service {
	return NewService(RatingsFunc(func(uint) ([]Signal, error) { return signals, nil }), f)
}

func TestSignalWeights(t *testing.T) {
	cases := []struct {
		s    Signal
		want float64
	}{
		{Signal{Rating: "love"}, 3},
		{Signal{Rating: "like"}, 1.5},
		{Signal{Rating: "love", Favorite: true}, 5},
		{Signal{Favorite: true}, 2},
		{Signal{Rating: "dislike"}, -3},
		{Signal{}, 0},
	}
	for _, c := range cases {
		if got := signalWeight(c.s); got != c.want {
			t.Errorf("%+v: got %v want %v", c.s, got, c.want)
		}
	}
}

func TestSeedsOrderedByStrength(t *testing.T) {
	_, signals := scenario()
	pos, neg := pickSeeds(signals, time.Now())
	if len(pos) != 3 || pos[0].TmdbID != loved || pos[1].TmdbID != favOnly || pos[2].TmdbID != liked {
		t.Fatalf("seeds: %+v", pos)
	}
	if len(neg) != 1 || neg[0].TmdbID != disliked {
		t.Fatalf("dislikes: %+v", neg)
	}
}

func TestRecommendationsScoringAndExclusion(t *testing.T) {
	f, signals := scenario()
	resp, err := newTestService(f, signals).For(1, "en")
	if err != nil {
		t.Fatal(err)
	}
	if len(resp.Rows) == 0 || resp.Rows[0].ID != "top-picks" || resp.Rows[0].Title != "Top Picks for You" {
		t.Fatalf("rows: %+v", resp.Rows)
	}
	top := resp.Rows[0].Items
	if len(top) != topPicksSize {
		t.Fatalf("top picks size %d", len(top))
	}
	if top[0].ID != 100 {
		t.Fatalf("loved title's first recommendation should lead, got %v", ids(top))
	}
	for _, r := range resp.Rows {
		for _, bad := range []int{loved, liked, disliked, favOnly, 150, 151, 300} {
			if contains(r.Items, bad) {
				t.Errorf("row %s contains excluded title %d", r.ID, bad)
			}
		}
	}
	// Genre affinity: loved action titles outrank the liked comedy titles in Top Picks
	if !contains(top, 101) || contains(top, 211) {
		t.Errorf("unexpected top picks %v", ids(top))
	}
}

func TestBecauseRows(t *testing.T) {
	f, signals := scenario()
	resp, _ := newTestService(f, signals).For(1, "en")
	var titles []string
	seen := map[int]string{}
	for _, r := range resp.Rows[1:] {
		titles = append(titles, r.Title)
		if len(r.Items) < becauseMinSize {
			t.Errorf("%s has %d items", r.Title, len(r.Items))
		}
		for _, it := range r.Items {
			if prev, ok := seen[it.ID]; ok {
				t.Errorf("item %d in %s and %s", it.ID, prev, r.Title)
			}
			seen[it.ID] = r.Title
		}
	}
	want := []string{"Because you loved Loved", "Because you added Fav to Favorites", "Because you liked Liked"}
	if strings.Join(titles, "|") != strings.Join(want, "|") {
		t.Fatalf("row titles %q", titles)
	}
	if resp.Rows[1].Reason != ReasonBecauseLoved || resp.Rows[1].ID != "because-movie-1" {
		t.Fatalf("row 1: %+v", resp.Rows[1])
	}
}

func TestNoRatingsFallsBackToTrending(t *testing.T) {
	f := &fakeCatalog{trending: append([]Item{movie(disliked)}, movies(700, 25)...)}
	dislikeOnly := []Signal{{MediaType: "movie", TmdbID: disliked, Rating: "dislike", UpdatedAt: time.Now()}}
	for _, signals := range [][]Signal{nil, dislikeOnly} {
		resp, err := newTestService(f, signals).For(1, "en")
		if err != nil {
			t.Fatal(err)
		}
		if len(resp.Rows) != 1 || resp.Rows[0].Title != "Trending Now" || resp.Rows[0].Reason != ReasonPopular {
			t.Fatalf("rows: %+v", resp.Rows)
		}
		if len(resp.Rows[0].Items) != topPicksSize || (signals != nil && contains(resp.Rows[0].Items, disliked)) {
			t.Fatalf("trending items: %v", ids(resp.Rows[0].Items))
		}
	}
}

func TestCacheAndInvalidate(t *testing.T) {
	f, signals := scenario()
	s := newTestService(f, signals)
	s.For(1, "en")
	first := f.calls
	s.For(1, "en")
	if f.calls != first {
		t.Fatalf("second call should be cached (%d -> %d)", first, f.calls)
	}
	s.Invalidate(1)
	s.For(1, "en")
	if f.calls == first {
		t.Fatal("invalidate should force a recompute")
	}
	now := time.Now()
	s.now = func() time.Time { return now.Add(cacheTTL + time.Minute) }
	before := f.calls
	s.For(1, "en")
	if f.calls == before {
		t.Fatal("expired entry should be recomputed")
	}
}

func TestGenreIDsNeverNull(t *testing.T) {
	f := &fakeCatalog{trending: []Item{{ID: 1, MediaType: "tv", Title: "x", PosterPath: "/x.jpg"}}}
	resp, _ := newTestService(f, nil).For(1, "en")
	if resp.Rows[0].Items[0].GenreIDs == nil {
		t.Fatal("genre_ids must be [] not null")
	}
}

func TestCacheIsPerLanguage(t *testing.T) {
	f, signals := scenario()
	s := newTestService(f, signals)
	s.For(1, "en")
	afterEN := f.calls
	s.For(1, "tr")
	afterTR := f.calls
	if afterTR == afterEN {
		t.Fatal("tr rows should not come from the en cache")
	}
	s.For(1, "tr")
	s.For(1, "en")
	if f.calls != afterTR {
		t.Fatalf("both languages should be cached (%d -> %d)", afterTR, f.calls)
	}
	s.Invalidate(1)
	s.For(1, "tr")
	if f.calls == afterTR {
		t.Fatal("invalidate should drop every language")
	}
}

func TestSeedRowsUseTheMatcher(t *testing.T) {
	f, signals := scenario()
	s := newTestService(f, signals)
	var asked []int
	var mu sync.Mutex
	s.SetSimilar(func(lang, mediaType string, id int) ([]Item, bool) {
		mu.Lock()
		asked = append(asked, id)
		mu.Unlock()
		if id == loved {
			return movies(700, 10, 28), true
		}
		return nil, false // not ready: TMDB's lists
	})
	resp, err := s.For(1, "en")
	if err != nil {
		t.Fatal(err)
	}
	var lovedRow *Row
	for i := range resp.Rows {
		if resp.Rows[i].ID == "because-movie-1" {
			lovedRow = &resp.Rows[i]
		}
	}
	if lovedRow == nil || lovedRow.Items[0].ID != 700 || contains(lovedRow.Items, 100) {
		t.Fatalf("loved row should be the matcher's list: %+v", lovedRow)
	}
	for _, id := range asked {
		if id == disliked {
			t.Error("the matcher was asked for a disliked title")
		}
	}
	// The liked title fell back to TMDB's lists, so the rows are kept only briefly
	for _, r := range resp.Rows {
		if r.ID == "because-movie-2" && !contains(r.Items, 201) {
			t.Errorf("liked row without TMDB fallback: %v", ids(r.Items))
		}
	}
	s.mu.Lock()
	c := s.cache[cacheKey{1, "en"}]
	s.mu.Unlock()
	if ttl := time.Until(c.expires); ttl <= 0 || ttl > partialTTL {
		t.Errorf("partial rows cached for %v, want up to %v", ttl, partialTTL)
	}
}
