package catalog

import (
	"encoding/json"
	"fmt"
	"math"
	"net/http"
	"net/http/httptest"
	"net/url"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"time"
	"unicode/utf8"

	"nextflix/internal/platform/tmdb"
)

func TestAffinityAllZeroIsEmpty(t *testing.T) {
	genres := func(string, int) []int { return []int{28} }
	rated := []RatedTitle{{MediaType: "movie", TmdbID: 1, Rating: "love"}, {MediaType: "movie", TmdbID: 2, Rating: "dislike"}}
	aff := affinityOf(rated, genres)
	if len(aff) != 0 {
		t.Fatalf("affinity = %v, want empty", aff)
	}
	aff = affinityOf([]RatedTitle{{MediaType: "movie", TmdbID: 1, Rating: "like"}}, genres)
	if aff[28] != 1 {
		t.Fatalf("affinity = %v", aff)
	}
	for _, v := range aff {
		if math.IsNaN(v) {
			t.Fatal("NaN affinity")
		}
	}
}

// genreTMDB answers /movie/{id} with the genre {id}.
func genreTMDB(t *testing.T) *tmdb.Client {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var id int
		fmt.Sscanf(strings.TrimPrefix(r.URL.Path, "/movie/"), "%d", &id)
		json.NewEncoder(w).Encode(map[string]any{"id": id, "title": "T", "genres": []map[string]any{{"id": id}}})
	}))
	t.Cleanup(srv.Close)
	return tmdb.New("k").WithBaseURL(srv.URL)
}

func TestInvalidateTasteDropsRunningCompute(t *testing.T) {
	s := NewService(genreTMDB(t))
	release := make(chan struct{})
	calls := make(chan int32, 4)
	var n atomic.Int32
	s.SetRatings(func(uint) ([]RatedTitle, error) {
		call := n.Add(1)
		calls <- call
		if call == 1 {
			<-release // the old ratings: genre 28
			return []RatedTitle{{MediaType: "movie", TmdbID: 28, Rating: "love"}}, nil
		}
		return []RatedTitle{{MediaType: "movie", TmdbID: 35, Rating: "love"}}, nil
	})
	s.taste.affinity(7)
	<-calls
	s.InvalidateTaste(7)
	s.taste.affinity(7) // a new compute starts at once
	<-calls
	waitTaste(t, s, 7, 35)
	close(release)
	// The old compute finishes and must not overwrite the new result
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		s.taste.mu.Lock()
		_, busy := s.taste.running[7]
		aff := s.taste.entries[7].affinity
		s.taste.mu.Unlock()
		if aff[28] != 0 {
			t.Fatalf("stale taste stored: %v", aff)
		}
		if !busy {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	time.Sleep(50 * time.Millisecond)
	s.taste.mu.Lock()
	aff := s.taste.entries[7].affinity
	s.taste.mu.Unlock()
	if aff[35] != 1 || aff[28] != 0 {
		t.Fatalf("affinity = %v, want genre 35 only", aff)
	}
}

func waitTaste(t *testing.T, s *Service, profile uint, genre int) {
	t.Helper()
	for range 400 {
		s.taste.mu.Lock()
		v := s.taste.entries[profile].affinity[genre]
		s.taste.mu.Unlock()
		if v == 1 {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("taste for genre %d not computed", genre)
}

func TestClampQuery(t *testing.T) {
	long := strings.Repeat("ğ", 500)
	got := clampQuery("  " + long + "  ")
	if utf8.RuneCountInString(got) != maxQueryRunes || !utf8.ValidString(got) {
		t.Fatalf("clamped to %d runes, valid=%v", utf8.RuneCountInString(got), utf8.ValidString(got))
	}
	if clampQuery("  dune ") != "dune" {
		t.Error("short query changed")
	}
}

func TestDiscoverParamsWhitelist(t *testing.T) {
	q := url.Values{
		"sort_by": {"popularity.desc"}, "with_genres": {"28|12"}, "vote_count.gte": {"0300"},
		"primary_release_date.gte": {"2025-01-01"}, "page": {"2"},
		"include_adult": {"true"}, "api_key": {"x"}, "junk": {"1"}, "language": {"de-DE"},
		"vote_average.gte": {"NaN"}, "with_original_language": {"en'"},
	}
	got := discoverParams(q)
	want := map[string]string{"sort_by": "popularity.desc", "with_genres": "28|12", "vote_count.gte": "300",
		"primary_release_date.gte": "2025-01-01", "page": "2", "include_adult": "false"}
	if fmt.Sprint(got) != fmt.Sprint(want) {
		t.Fatalf("got %v\nwant %v", got, want)
	}
	if p := discoverParams(url.Values{"sort_by": {"x"}, "page": {"9999"}, "with_genres": {"1,2;3"}}); len(p) != 1 {
		t.Errorf("invalid values kept: %v", p)
	}
}

func TestPersonalizeSimilarOnlyLeadingBlockFixed(t *testing.T) {
	s := &Service{}
	s.SetLocalSignals(func() (map[uint][]LocalTitle, error) {
		return map[uint][]LocalTitle{7: {{MediaType: "movie", TmdbID: 2, Watched: true, Finished: true}}}, nil
	})
	mk := func(id int, reason string, score float64) SimilarItem {
		return SimilarItem{SearchResult: tmdb.SearchResult{ID: id, MediaType: "movie", Title: fmt.Sprint(id)},
			Reason: reason, score: score}
	}
	// A spin-off further down must not freeze the matches before it
	items := []SimilarItem{mk(1, "collection", 9), mk(2, "match", 8), mk(3, "match", 7), mk(4, "spin_off", 1)}
	var got []int
	for _, it := range s.personalizeSimilar(items, 7) {
		got = append(got, it.ID)
	}
	if fmt.Sprint(got) != "[1 3 2 4]" {
		t.Errorf("personalized = %v, want [1 3 2 4]", got)
	}
}

func TestSimilarOverflowSpinOffsAreMatches(t *testing.T) {
	src := show(1396, "Breaking Bad", "en", "2008-01-20", 18000, 8.9, gDrama, gCrime)
	f := newFakeSim(src)
	f.src.Creators = []int{66633}
	var made []tmdb.SearchResult
	for i := range simRelatedMax + 2 {
		made = append(made, f.add(show(500+i, fmt.Sprintf("Breaking Bad Story %d", i+1), "en", "2019-10-11",
			5000-100*i, 7.1, gCrime, gDrama)))
	}
	f.works[66633] = &simPersonWorks{Made: made}
	f.list("recommendations:1", made...)

	items := build(t, f)
	spin := 0
	for i, it := range items {
		if it.Reason == "spin_off" {
			spin++
			if i >= simRelatedMax {
				t.Errorf("spin-off at %d: %v", i, titles(items))
			}
		}
	}
	if spin != simRelatedMax {
		t.Errorf("%d spin-offs, want %d: %v", spin, simRelatedMax, titles(items))
	}
}

// seasonsTMDB answers /tv/1 with the given episode counts per season (season 0 = specials);
// every episode has aired.
func seasonsTMDB(t *testing.T, counts map[int]int) *tmdb.Client {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var n int
		if _, err := fmt.Sscanf(r.URL.Path, "/tv/1/season/%d", &n); err == nil {
			var eps []map[string]any
			for e := 1; e <= counts[n]; e++ {
				eps = append(eps, map[string]any{"episode_number": e, "air_date": "2001-01-01"})
			}
			json.NewEncoder(w).Encode(map[string]any{"season_number": n, "air_date": "2001-01-01", "episodes": eps})
			return
		}
		var seasons []map[string]any
		for n, c := range counts {
			seasons = append(seasons, map[string]any{"season_number": n, "episode_count": c})
		}
		json.NewEncoder(w).Encode(map[string]any{"id": 1, "name": "Show", "seasons": seasons})
	}))
	t.Cleanup(srv.Close)
	return tmdb.New("k").WithBaseURL(srv.URL)
}

func TestNextEpisodeAfterSpecial(t *testing.T) {
	s := NewService(seasonsTMDB(t, map[int]int{0: 5, 1: 10, 2: 8}))
	for _, c := range []struct{ season, episode, ws, we int }{
		{0, 2, 0, 3}, // the next special
		{0, 5, 1, 1}, // after the last special, the first season
		{1, 10, 2, 1},
		{1, 4, 1, 5},
	} {
		ns, ne, ok := s.NextEpisode(1, c.season, c.episode)
		if !ok || ns != c.ws || ne != c.we {
			t.Errorf("after S%dE%d: S%dE%d %v, want S%dE%d", c.season, c.episode, ns, ne, ok, c.ws, c.we)
		}
	}
	if _, _, ok := s.NextEpisode(1, 2, 8); ok {
		t.Error("the last episode has no next one")
	}
}

func TestAliasesAtCapAndPruned(t *testing.T) {
	ix := NewTitleIndex(fixtureTitles())
	godfather := aliasKey{kindMovie, 238, "tr"}
	ix.Learn("movie", 238, "tr", "Baba")
	ix.rebuildAliases()
	for i := 0; len(ix.aliases) < aliasMax; i++ {
		ix.aliases[aliasKey{kindMovie, int32(5_000_000 + i), "tr"}] = "x" // titles not in the index
	}
	// At the cap an existing alias can still change, a new one cannot
	ix.Learn("movie", 238, "tr", "Vaftiz Baba")
	ix.Learn("tv", 1399, "tr", "Taht Oyunları")
	if ix.aliases[godfather] != "Vaftiz Baba" || len(ix.aliases) != aliasMax {
		t.Fatalf("at cap: %q, %d aliases", ix.aliases[godfather], len(ix.aliases))
	}
	// The rebuild drops the aliases of titles that are not indexed
	ix.rebuildAliases()
	if len(ix.aliases) != 1 || !ix.dirty {
		t.Fatalf("%d aliases after the rebuild, dirty %v", len(ix.aliases), ix.dirty)
	}
	for _, h := range ix.Suggest("baba", "tr", 5) {
		if h.Title == "Baba" {
			t.Errorf("the old alias is still searchable: %+v", h)
		}
	}
	if hits := ix.Suggest("vaftiz", "tr", 5); len(hits) == 0 || hits[0].ID != 238 {
		t.Errorf("changed alias: %+v", hits)
	}
	ix.Learn("tv", 1399, "tr", "Taht Oyunları")
	if _, ok := ix.aliases[aliasKey{kindTV, 1399, "tr"}]; !ok {
		t.Error("new alias rejected below the cap")
	}
	var rest []IndexTitle
	for _, ti := range fixtureTitles() {
		if ti.ID != 238 {
			rest = append(rest, ti)
		}
	}
	ix.replace(rest)
	if _, ok := ix.aliases[godfather]; ok {
		t.Error("alias of a title that left the index kept")
	}
}

// indexShape describes index data independent of the order its names were added in.
func indexShape(d *indexData) string {
	id := func(ni int32) string {
		n := d.names[ni]
		return fmt.Sprintf("%d|%s|%s|%s|%d|%d", n.entry, n.lang, n.text, n.bare, n.phon, n.grams)
	}
	var names, sorted []string
	var phons []uint32
	for i := range d.names {
		names = append(names, id(int32(i)))
	}
	slices.Sort(names)
	for _, ni := range d.sorted {
		sorted = append(sorted, d.names[ni].bare)
	}
	for _, ni := range d.byPhon {
		phons = append(phons, d.names[ni].phon)
	}
	grams := map[uint64][]string{}
	for g, p := range d.grams {
		for _, ni := range p {
			grams[g] = append(grams[g], id(ni))
		}
		slices.Sort(grams[g])
	}
	return fmt.Sprint(names, sorted, phons, grams, d.vocab, d.dropped)
}

func TestAliasRebuildAddsWithoutFullBuild(t *testing.T) {
	ix := NewTitleIndex(fixtureTitles())
	built := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	ix.data.Load().built = built
	learned := map[aliasKey]string{
		{kindMovie, 238, "tr"}:    "Baba",
		{kindTV, 1399, "tr"}:      "Taht Oyunları",
		{kindTV, 71446, "en"}:     "Money Heist",
		{kindTV, 82856, "tr"}:     "Mandalorian",
		{kindMovie, 157336, "tr"}: "Yıldızlararası",
	}
	for k, v := range learned {
		ix.Learn(mediaTypeOf(k.kind), int(k.id), k.lang, v)
	}
	ix.Learn("movie", 999, "tr", "Not Indexed")
	ix.Learn("movie", 438631, "tr", "DUNE")
	ix.rebuildAliases()

	d := ix.data.Load()
	if !d.built.Equal(built) {
		t.Error("new aliases rebuilt the whole index")
	}
	full := &TitleIndex{aliases: learned}
	full.data.Store(buildIndexData(fixtureTitles(), learned))
	if got, want := indexShape(d), indexShape(full.data.Load()); got != want {
		t.Fatalf("incremental data differs from a full build:\n%s\n%s", got, want)
	}
	for _, c := range []struct{ q, lang string }{{"bab", "tr"}, {"money", "en"}, {"mand", "tr"}, {"taht", "tr"}, {"yildiz", "tr"}} {
		if got, want := fmt.Sprint(ix.Suggest(c.q, c.lang, 5)), fmt.Sprint(full.Suggest(c.q, c.lang, 5)); got != want {
			t.Errorf("Suggest(%q, %s) = %s, full build %s", c.q, c.lang, got, want)
		}
	}
	for _, q := range []string{"mony heist", "mandalorain", "yildizlarasi"} {
		if got, want := fmt.Sprint(ix.Correct(q, 3, correctMin)), fmt.Sprint(full.Correct(q, 3, correctMin)); got != want {
			t.Errorf("Correct(%q) = %s, full build %s", q, got, want)
		}
	}
	if hits := ix.Suggest("money", "en", 1); len(hits) == 0 || hits[0].ID != 71446 {
		t.Errorf("learned alias not found: %+v", hits)
	}
	if !ix.KnownWords("money heist") {
		t.Error("alias words unknown")
	}
}
