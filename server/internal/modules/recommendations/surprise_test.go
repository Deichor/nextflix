package recommendations

import (
	"errors"
	"fmt"
	"math/rand/v2"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"
)

func (f *fakeCatalog) Explore(lang string, q ExploreQuery) ([]Item, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.explore == nil {
		return nil, nil
	}
	return f.explore(q), nil
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

func TestSurpriseExclusionsAndMemory(t *testing.T) {
	now := time.Now()
	f := &fakeCatalog{
		related: map[titleKey]relatedLists{
			keyOf("movie", 1): {recs: append([]Item{art(1), art(2)}, arts(100, 40, 878)...)},
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
	for i := 0; i < 36; i++ {
		got, err := s.Surprise(7, "en", exclude)
		if err != nil {
			t.Fatal(err)
		}
		k := keyOf(got.Title.MediaType, got.Title.ID)
		switch k.id {
		case 1, 2, 100, 101, 102:
			t.Fatalf("press %d picked excluded title %d", i, k.id)
		}
		if got.Reason.Kind != SurpriseBecauseLoved || got.Reason.Title != "Dune" || got.Reason.TmdbID != 1 || got.Reason.Bucket != BucketTaste {
			t.Fatalf("reason = %+v", got.Reason)
		}
		if got.Title.LogoPath != fmt.Sprintf("/logo-%d.png", k.id) || got.Title.BackdropPath == "" {
			t.Fatalf("title = %+v", got.Title)
		}
		picks = append(picks, k)
	}
	// 37 titles are left: no repeat within any 30 consecutive presses
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
		// Discover lists offer a horror title and the disliked title's recommendation too
		explore: func(q ExploreQuery) []Item { return []Item{art(310, 27), art(301, 27), art(300, 878)} },
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

func TestSurpriseSampleLeansToBest(t *testing.T) {
	var cands []*surpriseCand
	for i := 0; i < surpriseTop; i++ {
		cands = append(cands, &surpriseCand{item: art(100 + i), score: float64(surpriseTop - i)})
	}
	s := surpriseService(&fakeCatalog{}, nil, nil)
	counts := map[int]int{}
	for i := 0; i < 3000; i++ {
		counts[s.sample(cands, tasteTemperature, nil).item.ID]++
	}
	if len(counts) < surpriseTop-2 {
		t.Errorf("only %d distinct picks of %d", len(counts), surpriseTop)
	}
	if counts[100] <= counts[100+surpriseTop-1] {
		t.Errorf("best picked %d times, worst %d", counts[100], counts[100+surpriseTop-1])
	}
}

func TestSurpriseGenreReason(t *testing.T) {
	now := time.Now()
	f := &fakeCatalog{
		genres: map[titleKey][]int{keyOf("tv", 9): {10765}}, // Sci-Fi & Fantasy counts as Sci-Fi
		explore: func(q ExploreQuery) []Item {
			if q.MediaType == "movie" && reflect.DeepEqual(q.Genres, []int{878}) && q.Sort == "popularity.desc" {
				return arts(500, 10, 878)
			}
			return nil
		},
	}
	history := []Watched{{MediaType: "tv", TmdbID: 9, Title: "Show", Finished: true, UpdatedAt: now}}
	s := surpriseService(f, nil, history)
	got, err := s.Surprise(1, "en", nil)
	if err != nil {
		t.Fatal(err)
	}
	if got.Reason.Kind != SurpriseGenre || !reflect.DeepEqual(got.Reason.GenreIDs, []int{878}) || got.Title.ID < 500 || got.Title.ID >= 510 {
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

// mixProfile: a loved crime drama next to liked, favorite and watched titles of other genres,
// one disliked horror film, related lists per title and generated discover lists.
func mixProfile() (*fakeCatalog, []Signal, []Watched) {
	now := time.Now()
	type seedDef struct {
		key    titleKey
		title  string
		rating string
		fav    bool
		genres []int
	}
	defs := []seedDef{
		{keyOf("movie", 240), "The Godfather Part II", "love", false, []int{18, 80}},
		{keyOf("tv", 1396), "Breaking Bad", "like", false, []int{18, 80}},
		{keyOf("movie", 27205), "Inception", "like", false, []int{28, 878, 12}},
		{keyOf("movie", 8363), "Superbad", "", true, []int{35}},
		{keyOf("movie", 157336), "Interstellar", "", true, []int{12, 18, 878}},
		{keyOf("tv", 2316), "The Office", "", true, []int{35}},
		{keyOf("movie", 210577), "Gone Girl", "", true, []int{9648, 53, 18}},
		{keyOf("movie", 9999), "Bad Horror", "dislike", false, []int{27}},
	}
	f := &fakeCatalog{related: map[titleKey]relatedLists{}, genres: map[titleKey][]int{}}
	var signals []Signal
	for i, d := range defs {
		f.genres[d.key] = d.genres
		var recs, similar []Item
		for j := 0; j < 30; j++ {
			it := art(10000*(i+1)+j, d.genres...)
			it.MediaType = d.key.mediaType
			it.OriginalLanguage = "en"
			if j < 20 {
				recs = append(recs, it)
			} else {
				similar = append(similar, it)
			}
		}
		f.related[d.key] = relatedLists{recs: recs, similar: similar}
		signals = append(signals, Signal{MediaType: d.key.mediaType, TmdbID: d.key.id, Title: d.title, Rating: d.rating,
			Favorite: d.fav, UpdatedAt: now.Add(-time.Duration(i) * time.Hour)})
	}
	dark := keyOf("tv", 70523)
	f.genres[dark] = []int{80, 18, 9648, 10765}
	f.related[dark] = relatedLists{recs: func() []Item {
		out := arts(90000, 20, 80, 18, 9648)
		for i := range out {
			out[i].MediaType, out[i].OriginalLanguage = "tv", "en"
		}
		return out
	}()}
	history := []Watched{{MediaType: "tv", TmdbID: dark.id, Title: "Dark", Finished: true, UpdatedAt: now}}

	// Every discover query gets its own 20 titles shaped by the query
	lists := map[string]int{}
	fillers := []int{18, 35, 28, 99, 10749, 37}
	f.explore = func(q ExploreQuery) []Item {
		sig := fmt.Sprintf("%+v", q)
		n, ok := lists[sig]
		if !ok {
			n = len(lists)
			lists[sig] = n
		}
		year := 2012
		switch {
		case q.ToYear > 0:
			year = q.ToYear - 15
		case q.FromYear > 0:
			year = q.FromYear + 8
		}
		lang := q.Language
		if lang == "" {
			lang = "en"
		}
		out := make([]Item, 0, 20)
		for i := 0; i < 20; i++ {
			genres := q.Genres
			if len(genres) == 0 {
				genres = []int{fillers[(n+i)%len(fillers)]}
			}
			it := art(200000+1000*n+i, genres...)
			it.MediaType = q.MediaType
			it.OriginalLanguage = lang
			it.VoteAverage = 8.6 - 0.05*float64(i)
			it.VoteCount = 3000
			it.ReleaseDate = fmt.Sprintf("%d-05-01", year)
			out = append(out, it)
		}
		return out
	}
	return f, signals, history
}

type pressLog struct {
	key      titleKey
	bucket   string
	group    string
	genre    int
	genres   []int
	reason   Reason
	language string
}

func press(t *testing.T, s *Service, profile uint, n int) []pressLog {
	t.Helper()
	var out []pressLog
	for i := 0; i < n; i++ {
		got, err := s.Surprise(profile, "en", nil)
		if err != nil {
			t.Fatalf("press %d: %v", i, err)
		}
		mem := s.surprise.recent[profile]
		last := mem[len(mem)-1]
		out = append(out, pressLog{key: last.key, bucket: got.Reason.Bucket, group: last.group, genre: last.genre,
			reason: got.Reason, language: got.Title.OriginalLanguage, genres: last.genres})
	}
	return out
}

func TestSurpriseMixOverThirtyPresses(t *testing.T) {
	f, signals, history := mixProfile()
	s := surpriseService(f, signals, history)
	picks := press(t, s, 1, 30)

	seen := map[titleKey]bool{}
	seeds := map[string]bool{}
	buckets := map[string]int{}
	genres := map[int]bool{}
	types := map[string]int{}
	foreign := 0
	var lines []string
	for i, p := range picks {
		if seen[p.key] {
			t.Errorf("press %d repeats %v", i, p.key)
		}
		seen[p.key] = true
		if strings.HasPrefix(p.group, "seed:") {
			seeds[p.group] = true
		}
		buckets[p.bucket]++
		genres[p.genre] = true
		types[p.key.mediaType]++
		if p.language != "en" {
			foreign++
		}
		if i > 0 {
			prev := picks[i-1]
			if p.group == prev.group {
				t.Errorf("press %d: group %s twice in a row", i, p.group)
			}
			if slices.Contains(prev.genres, p.genre) {
				t.Errorf("press %d: genre %d was already in the last pick %v", i, p.genre, prev.genres)
			}
			if p.bucket == prev.bucket && p.bucket != BucketTaste {
				t.Errorf("press %d: bucket %s twice in a row", i, p.bucket)
			}
		}
		lines = append(lines, fmt.Sprintf("%2d %-8s %-22s genre %-5d %s %s", i+1, p.bucket, p.group, p.genre, p.language, p.reason.Kind))
	}
	t.Logf("30 presses:\n%s", strings.Join(lines, "\n"))
	t.Logf("buckets %v, seeds %d, genres %d, non-English %d, types %v", buckets, len(seeds), len(genres), foreign, types)

	if len(seeds) < 3 {
		t.Errorf("seeds used: %v", seeds)
	}
	if buckets[BucketTaste] < 10 || buckets[BucketTaste] > 20 || buckets[BucketAdjacent] < 4 || buckets[BucketWild] < 4 {
		t.Errorf("buckets %v", buckets)
	}
	if len(genres) < 4 {
		t.Errorf("genres %v", genres)
	}
	if foreign < 1 {
		t.Error("no non-English pick")
	}
	if types["movie"] == 0 || types["tv"] == 0 {
		t.Errorf("media types %v", types)
	}
}

func TestSurpriseBucketShares(t *testing.T) {
	f, signals, history := mixProfile()
	s := surpriseService(f, signals, history)
	buckets := map[string]int{}
	godfather, taste, total := 0, 0, 0
	for profile := uint(1); profile <= 40; profile++ {
		for _, p := range press(t, s, profile, 30) {
			buckets[p.bucket]++
			total++
			if p.bucket == BucketTaste {
				taste++
				if p.reason.TmdbID == 240 {
					godfather++
				}
			}
			switch p.reason.Kind {
			case SurpriseAdjacent:
				if p.reason.GenreID == 0 {
					t.Fatalf("adjacent without genre: %+v", p.reason)
				}
			case SurpriseWild:
				if (p.language != "en") != (p.reason.Language != "") {
					t.Fatalf("wild language %q, reason %+v", p.language, p.reason)
				}
			}
		}
	}
	share := func(b string) float64 { return float64(buckets[b]) / float64(total) }
	t.Logf("%d presses: taste %.2f, adjacent %.2f, wild %.2f; The Godfather Part II seeds %d of %d taste picks",
		total, share(BucketTaste), share(BucketAdjacent), share(BucketWild), godfather, taste)
	for _, b := range bucketShare {
		if d := share(b.name) - b.share; d < -0.05 || d > 0.05 {
			t.Errorf("bucket %s: share %.3f, want %.2f", b.name, share(b.name), b.share)
		}
	}
	// One loved title must not dominate the taste blend
	if float64(godfather) > float64(taste)/3 {
		t.Errorf("The Godfather Part II seeded %d of %d taste picks", godfather, taste)
	}
}

func TestGenreStrengthDiminishes(t *testing.T) {
	now := time.Now()
	pos := tasteSeeds([]Signal{
		{MediaType: "movie", TmdbID: 1, Rating: "love", Favorite: true, UpdatedAt: now},
		{MediaType: "movie", TmdbID: 2, Favorite: true, UpdatedAt: now},
		{MediaType: "movie", TmdbID: 3, Favorite: true, UpdatedAt: now},
		{MediaType: "movie", TmdbID: 4, Favorite: true, UpdatedAt: now},
	}, nil, now)
	genres := map[titleKey][]int{
		keyOf("movie", 1): {18, 80}, keyOf("movie", 2): {35}, keyOf("movie", 3): {35}, keyOf("movie", 4): {35},
	}
	strength := genreStrength(pos, genres, tasteAffinity(pos, genres))
	// One loved title (capped at 1) is weaker than three favorites of another genre
	if !(strength[35] > strength[18] && strength[18] == strength[80]) {
		t.Fatalf("strength %v", strength)
	}
	if got := topGenres(strength, 2); !reflect.DeepEqual(got, []int{35, 18}) {
		t.Fatalf("top %v", got)
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

func TestEveryTVGenreKnown(t *testing.T) {
	// TMDB's TV genres: Action & Adventure ... Talk, War & Politics
	for _, g := range []int{10759, 16, 35, 80, 99, 18, 10751, 10762, 9648, 10763, 10764, 10765, 10766, 10767, 10768, 37} {
		if tvGenre(canonGenre(g)) == 0 {
			t.Errorf("TV genre %d has no TV discover genre", g)
		}
	}
}

func TestParseExclude(t *testing.T) {
	got := ParseExclude("movie-1, tv:2,person-3,movie-x,,tv-0")
	if !reflect.DeepEqual(got, []titleKey{keyOf("movie", 1), keyOf("tv", 2)}) {
		t.Fatalf("got %v", got)
	}
}
