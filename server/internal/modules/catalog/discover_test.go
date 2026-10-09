package catalog

import (
	"net/url"
	"reflect"
	"testing"
	"time"

	"nextflix/internal/platform/tmdb"
)

func result(mediaType string, id int, lang string, genres ...int) tmdb.SearchResult {
	return tmdb.SearchResult{ID: id, MediaType: mediaType, Title: "T", PosterPath: "/p.jpg",
		OriginalLanguage: lang, GenreIDs: genres}
}

func TestParseFilter(t *testing.T) {
	f := ParseFilter(url.Values{"type": {"TV"}, "genres": {"16, 878,x,878,-1"}, "anime": {"1"}})
	if f.Type != "tv" || !reflect.DeepEqual(f.Genres, []int{16, 878}) || !f.Anime {
		t.Fatalf("filter = %+v", f)
	}
	if g := ParseFilter(url.Values{"type": {"person"}}); g.Type != "all" || !g.Empty() {
		t.Fatalf("default filter = %+v", g)
	}
}

func TestFilterMatchIsAnd(t *testing.T) {
	f := Filter{Type: "all", Genres: []int{878, 28}} // Sci-Fi AND Action
	cases := []struct {
		name string
		r    tmdb.SearchResult
		want bool
	}{
		{"movie with both", result("movie", 1, "en", 28, 878, 12), true},
		{"movie with one", result("movie", 2, "en", 878), false},
		{"tv: Sci-Fi & Fantasy + Action & Adventure", result("tv", 3, "en", 10765, 10759), true},
		{"tv: only Sci-Fi & Fantasy", result("tv", 4, "en", 10765, 18), false},
		{"movie ids on a show do not count", result("tv", 5, "en", 878, 28), false},
	}
	for _, c := range cases {
		if got := f.Match(c.r); got != c.want {
			t.Errorf("%s: Match = %v, want %v", c.name, got, c.want)
		}
	}

	// A genre without a TV counterpart (Horror) rules shows out
	horror := Filter{Type: "all", Genres: []int{27}}
	if horror.Match(result("tv", 6, "en", 27, 9648)) || !horror.Match(result("movie", 7, "en", 27)) {
		t.Error("horror filter")
	}
	// Type
	movies := Filter{Type: "movie"}
	if movies.Match(result("tv", 8, "en")) || !movies.Match(result("movie", 9, "en")) {
		t.Error("type filter")
	}
	// Adult titles never match
	adult := result("movie", 10, "en", 28)
	adult.Adult = true
	if (Filter{Type: "all"}).Match(adult) {
		t.Error("adult title matched")
	}
}

func TestFilterAnime(t *testing.T) {
	f := Filter{Type: "all", Anime: true}
	cases := []struct {
		name string
		r    tmdb.SearchResult
		want bool
	}{
		{"japanese animated show", result("tv", 1, "ja", 16, 10759), true},
		{"japanese animated movie", result("movie", 2, "ja", 16, 14), true},
		{"american cartoon", result("tv", 3, "en", 16), false},
		{"japanese live action", result("tv", 4, "ja", 18), false},
	}
	for _, c := range cases {
		if got := f.Match(c.r); got != c.want {
			t.Errorf("%s: Match = %v, want %v", c.name, got, c.want)
		}
	}
	// Anime AND Sci-Fi
	g := Filter{Type: "all", Anime: true, Genres: []int{878}}
	if !g.Match(result("tv", 5, "ja", 16, 10765)) || g.Match(result("tv", 6, "ja", 16, 35)) {
		t.Error("anime + sci-fi")
	}
}

func TestDiscoverParams(t *testing.T) {
	f := Filter{Type: "all", Genres: []int{28, 12, 878}, Anime: true}
	tv, ok := f.discoverParams("tv", 3)
	if !ok {
		t.Fatal("tv side skipped")
	}
	// Action and Adventure are one TV genre; Anime adds Animation and Japanese
	if tv.Get("with_genres") != "10759,10765,16" || tv.Get("with_original_language") != "ja" ||
		tv.Get("page") != "3" || tv.Get("sort_by") != "popularity.desc" {
		t.Errorf("tv params = %v", tv)
	}
	mv, ok := f.discoverParams("movie", 1)
	if !ok || mv.Get("with_genres") != "28,12,878,16" {
		t.Errorf("movie params = %v, %v", mv, ok)
	}
	if _, ok := (Filter{Type: "all", Genres: []int{10749}}).discoverParams("tv", 1); ok {
		t.Error("Romance has no TV genre, the TV side must be skipped")
	}
	if _, ok := (Filter{Type: "tv"}).discoverParams("movie", 1); ok {
		t.Error("type tv must skip movies")
	}
	if p, ok := (Filter{Type: "movie"}).discoverParams("movie", 1); !ok || p.Has("with_genres") {
		t.Errorf("no genres: %v", p)
	}
}

func TestSearchFiltered(t *testing.T) {
	s := NewService(tmdb.New(""))
	noArt := result("movie", 4, "ja", 16)
	noArt.PosterPath = ""
	s.searches["en|5|ghost"] = searchEntry{expires: time.Now().Add(time.Hour), results: []tmdb.SearchResult{
		result("tv", 1, "ja", 16, 10765),
		result("movie", 2, "ja", 16, 878),
		result("movie", 3, "en", 16, 878),
	}}
	res, err := s.RankedSearch(tmdb.New(""), "Ghost", Filter{Type: "all", Anime: true, Genres: []int{878}}, 0)
	if err != nil {
		t.Fatal(err)
	}
	got := []int{}
	for _, r := range res.Results {
		got = append(got, r.ID)
	}
	if !reflect.DeepEqual(got, []int{1, 2}) || res.TotalResults != 2 {
		t.Errorf("results = %v", got)
	}
	if usable(noArt) {
		t.Error("a title without artwork is usable")
	}
}

func TestMergeByPopularity(t *testing.T) {
	a := result("movie", 1, "en")
	a.Popularity = 5
	b := result("tv", 2, "en")
	b.Popularity = 9
	c := result("movie", 3, "en")
	c.Popularity = 1
	c.PosterPath = ""
	out := mergeByPopularity([]tmdb.SearchResult{a, c}, []tmdb.SearchResult{b})
	if len(out) != 2 || out[0].ID != 2 || out[1].ID != 1 {
		t.Errorf("merged = %+v", out)
	}
}
