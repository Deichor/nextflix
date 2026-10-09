package recommendations

import (
	"errors"
	"fmt"
	"math/rand/v2"
	"reflect"
	"testing"
	"time"
)

// Discover answers from the related map under keyOf("discover-<mediaType>", genre).
func (f *fakeCatalog) Discover(lang, mediaType string, genre int) ([]Item, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.related[keyOf("discover-"+mediaType, genre)].recs, nil
}

func (f *fakeCatalog) Logo(lang, mediaType string, id int) (string, error) {
	return fmt.Sprintf("/logo-%d.png", id), nil
}

// art is a candidate with the artwork and votes a surprise needs.
func art(id int, genres ...int) Item {
	it := movie(id, genres...)
	it.BackdropPath = fmt.Sprintf("/b%d.jpg", id)
	return it
}

func arts(from, n int, genres ...int) []Item {
	out := make([]Item, 0, n)
	for i := 0; i < n; i++ {
		out = append(out, art(from+i, genres...))
	}
	return out
}

func surpriseService(f *fakeCatalog, signals []Signal, history []Watched) *Service {
	s := newTestService(f, signals)
	s.SetHistory(HistoryFunc(func(uint) ([]Watched, error) { return history, nil }))
	s.surprise.rnd = rand.New(rand.NewPCG(1, 2))
	return s
}

func key(it Item) titleKey { return keyOf(it.MediaType, it.ID) }

func TestSurpriseExclusionsAndMemory(t *testing.T) {
	now := time.Now()
	f := &fakeCatalog{
		related: map[titleKey]relatedLists{
			keyOf("movie", 1): {recs: append([]Item{art(1), art(2)}, arts(100, 30, 878)...)},
		},
		genres: map[titleKey][]int{keyOf("movie", 1): {878}},
	}
	signals := []Signal{
		{MediaType: "movie", TmdbID: 1, Title: "Dune", Rating: "love", UpdatedAt: now},
		{MediaType: "movie", TmdbID: 2, Title: "Liked", Rating: "like", UpdatedAt: now},
	}
	history := []Watched{
		{MediaType: "movie", TmdbID: 100, Title: "Seen", Finished: true, UpdatedAt: now},
		{MediaType: "movie", TmdbID: 101, Title: "Half", UpdatedAt: now},
	}
	s := surpriseService(f, signals, history)
	exclude := ParseExclude("movie-102,tv:5")

	var picks []titleKey
	for i := 0; i < 25; i++ {
		got, err := s.Surprise(7, "en", exclude)
		if err != nil {
			t.Fatal(err)
		}
		k := keyOf(got.Title.MediaType, got.Title.ID)
		switch k.id {
		case 1, 2, 100, 101, 102:
			t.Fatalf("press %d picked excluded title %d", i, k.id)
		}
		if got.Reason.Kind != SurpriseBecauseLoved || got.Reason.Title != "Dune" || got.Reason.TmdbID != 1 {
			t.Fatalf("reason = %+v", got.Reason)
		}
		if got.Title.LogoPath != fmt.Sprintf("/logo-%d.png", k.id) || got.Title.BackdropPath == "" {
			t.Fatalf("title = %+v", got.Title)
		}
		picks = append(picks, k)
	}
	// No repeat within any 20 consecutive presses
	for i := range picks {
		for j := i + 1; j < len(picks) && j < i+surpriseMemory; j++ {
			if picks[i] == picks[j] {
				t.Fatalf("press %d and %d both picked %v", i, j, picks[i])
			}
		}
	}
	// Memory is per profile
	if len(s.surprise.recent[8]) != 0 || len(s.surprise.recent[7]) != surpriseMemory {
		t.Fatalf("memory: %d / %d", len(s.surprise.recent[7]), len(s.surprise.recent[8]))
	}
}

func TestSurpriseMemoryStartsOverWhenExhausted(t *testing.T) {
	f := &fakeCatalog{related: map[titleKey]relatedLists{keyOf("movie", 1): {recs: arts(100, 3, 878)}}}
	s := surpriseService(f, []Signal{{MediaType: "movie", TmdbID: 1, Title: "A", Rating: "love", UpdatedAt: time.Now()}}, nil)
	seen := map[int]bool{}
	for i := 0; i < 6; i++ {
		got, err := s.Surprise(1, "en", nil)
		if err != nil {
			t.Fatalf("press %d: %v", i, err)
		}
		seen[got.Title.ID] = true
	}
	if len(seen) != 3 {
		t.Fatalf("picked %v", seen)
	}
}

func TestSurpriseRespectsDislikes(t *testing.T) {
	now := time.Now()
	horror := art(310, 27)
	f := &fakeCatalog{
		related: map[titleKey]relatedLists{
			// 300 is weakly supported by the loved title but the disliked title's top recommendation;
			// 310 is a horror title in the loved title's list, and the profile dislikes horror
			keyOf("movie", 1): {recs: append(arts(100, 15, 878), horror), similar: []Item{art(300, 878)}},
			keyOf("movie", 3): {recs: []Item{art(300, 878), art(301, 27)}},
		},
		genres: map[titleKey][]int{keyOf("movie", 1): {878}, keyOf("movie", 3): {27}},
	}
	signals := []Signal{
		{MediaType: "movie", TmdbID: 1, Title: "Loved", Rating: "love", UpdatedAt: now},
		{MediaType: "movie", TmdbID: 3, Title: "Nope", Rating: "dislike", UpdatedAt: now},
	}
	s := surpriseService(f, signals, nil)
	for i := 0; i < 100; i++ {
		got, err := s.Surprise(1, "en", nil)
		if err != nil {
			t.Fatal(err)
		}
		switch got.Title.ID {
		case 3, 300, 301, 310:
			t.Fatalf("press %d picked %d", i, got.Title.ID)
		}
	}
}

func TestSurpriseVariety(t *testing.T) {
	f := &fakeCatalog{related: map[titleKey]relatedLists{keyOf("movie", 1): {recs: arts(100, 40, 878)}}}
	s := surpriseService(f, []Signal{{MediaType: "movie", TmdbID: 1, Title: "A", Rating: "love", UpdatedAt: time.Now()}}, nil)
	pool, err := s.surprisePool(1, "en", nil)
	if err != nil || len(pool.cands) != 40 {
		t.Fatalf("pool: %d, %v", len(pool.cands), err)
	}
	counts := map[int]int{}
	for i := 0; i < 2000; i++ {
		counts[s.sample(pool.cands, nil, nil).item.ID]++
	}
	if len(counts) < 30 {
		t.Errorf("only %d distinct picks of 40", len(counts))
	}
	best, worst := pool.cands[0].item.ID, pool.cands[len(pool.cands)-1].item.ID
	if counts[best] <= counts[worst] {
		t.Errorf("best %d picked %d times, worst %d %d times", best, counts[best], worst, counts[worst])
	}
	// Two services with different seeds start differently (presses are not deterministic)
	first := map[int]bool{}
	for seed := uint64(0); seed < 10; seed++ {
		s.surprise.rnd = rand.New(rand.NewPCG(seed, seed))
		first[s.sample(pool.cands, nil, nil).item.ID] = true
	}
	if len(first) < 3 {
		t.Errorf("first picks across seeds: %v", first)
	}
}

func TestSurpriseGenreReason(t *testing.T) {
	now := time.Now()
	f := &fakeCatalog{
		related: map[titleKey]relatedLists{
			keyOf("discover-movie", 878): {recs: arts(500, 10, 878)},
			keyOf("discover-tv", 878):    {recs: []Item{art(999, 878)}}, // 878 is no TV genre: never asked
		},
		genres: map[titleKey][]int{keyOf("tv", 9): {878}},
	}
	history := []Watched{{MediaType: "tv", TmdbID: 9, Title: "Show", Finished: true, UpdatedAt: now}}
	s := surpriseService(f, nil, history)
	got, err := s.Surprise(1, "en", nil)
	if err != nil {
		t.Fatal(err)
	}
	if got.Reason.Kind != SurpriseGenre || got.Reason.GenreID != 878 || got.Title.ID < 500 || got.Title.ID >= 510 {
		t.Fatalf("got %+v", got)
	}
}

func TestSurpriseFallsBackToTrending(t *testing.T) {
	rated := art(700)
	f := &fakeCatalog{trending: append([]Item{rated}, arts(701, 10)...)}
	signals := []Signal{{MediaType: "movie", TmdbID: 700, Rating: "dislike", UpdatedAt: time.Now()}}
	for _, sig := range [][]Signal{nil, signals} {
		s := surpriseService(f, sig, nil)
		got, err := s.Surprise(1, "en", nil)
		if err != nil {
			t.Fatal(err)
		}
		if got.Reason.Kind != SurpriseTrending || (sig != nil && got.Title.ID == 700) {
			t.Fatalf("got %+v", got)
		}
	}
	if _, err := surpriseService(&fakeCatalog{}, nil, nil).Surprise(1, "en", nil); !errors.Is(err, ErrNoSurprise) {
		t.Fatalf("empty catalog: %v", err)
	}
}

func TestSurprisePoolCacheAndInvalidate(t *testing.T) {
	f := &fakeCatalog{related: map[titleKey]relatedLists{keyOf("movie", 1): {recs: arts(100, 30, 878)}}}
	s := surpriseService(f, []Signal{{MediaType: "movie", TmdbID: 1, Title: "A", Rating: "love", UpdatedAt: time.Now()}}, nil)
	s.Surprise(1, "en", nil)
	calls := f.calls
	s.Surprise(1, "en", nil)
	if f.calls != calls {
		t.Fatal("second press should use the cached pool")
	}
	s.Invalidate(1)
	s.Surprise(1, "en", nil)
	if f.calls == calls {
		t.Fatal("invalidate should rebuild the pool")
	}
}

func TestTasteSeedsOrder(t *testing.T) {
	now := time.Now()
	seeds := tasteSeeds([]Signal{
		{MediaType: "movie", TmdbID: 1, Rating: "love", UpdatedAt: now},
		{MediaType: "movie", TmdbID: 2, Rating: "like", UpdatedAt: now},
		{MediaType: "movie", TmdbID: 3, Favorite: true, UpdatedAt: now},
		{MediaType: "movie", TmdbID: 4, Rating: "dislike", UpdatedAt: now},
	}, []Watched{
		{MediaType: "movie", TmdbID: 1, Finished: true, UpdatedAt: now}, // rated: history ignored
		{MediaType: "tv", TmdbID: 5, Finished: true, UpdatedAt: now},
		{MediaType: "tv", TmdbID: 6, UpdatedAt: now},
	}, now)
	got := map[int]float64{}
	kinds := map[int]string{}
	for _, sd := range seeds {
		got[sd.key.id] = sd.weight
		kinds[sd.key.id] = sd.kind
	}
	if len(seeds) != 6 || !(got[1] > got[2] && got[2] > got[3] && got[3] > got[5] && got[5] > got[6] && got[4] < 0) {
		t.Fatalf("weights %v", got)
	}
	want := map[int]string{1: SurpriseBecauseLoved, 2: SurpriseBecauseLiked, 3: SurpriseFavoriteLike, 4: "", 5: SurpriseBecauseWatched, 6: SurpriseBecauseWatched}
	if !reflect.DeepEqual(kinds, want) {
		t.Fatalf("kinds %v", kinds)
	}
}

func TestParseExclude(t *testing.T) {
	got := ParseExclude("movie-1, tv:2,person-3,movie-x,,tv-0")
	if !reflect.DeepEqual(got, []titleKey{keyOf("movie", 1), keyOf("tv", 2)}) {
		t.Fatalf("got %v", got)
	}
}
