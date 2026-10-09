package catalog

import (
	"bytes"
	"compress/gzip"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"nextflix/internal/platform/tmdb"
)

// fr is a fake TMDB search result.
type fr struct {
	mt     string
	id     int
	title  string
	orig   string
	pop    float64
	votes  int
	date   string
	genres []int
}

func (f fr) raw() map[string]any {
	orig := f.orig
	if orig == "" {
		orig = f.title
	}
	m := map[string]any{"id": f.id, "media_type": f.mt, "overview": "x", "poster_path": "/p.jpg",
		"popularity": f.pop, "vote_count": f.votes, "vote_average": 7.0, "genre_ids": f.genres, "original_language": "en"}
	if f.mt == "tv" {
		m["name"], m["original_name"], m["first_air_date"] = f.title, orig, f.date
	} else {
		m["title"], m["original_title"], m["release_date"] = f.title, orig, f.date
	}
	return m
}

var (
	mandalorian  = fr{"tv", 82856, "The Mandalorian", "", 63, 10500, "2019-11-12", []int{10765, 10759, 18}}
	mandoGallery = fr{"tv", 101978, "Disney Gallery / Star Wars: The Mandalorian", "", 13, 120, "2020-05-04",
		[]int{99}}
	mandoGrogu = fr{"movie", 1022789, "The Mandalorian and Grogu", "", 45, 900, "2026-05-22", []int{878, 12, 28}}
	mandoLook  = fr{"movie", 321564, "Star Wars: The Mandalorian and Grogu | A Special Look", "", 2, 3, "2025-04-19", nil}
	godfather  = fr{"movie", 238, "The Godfather", "", 95, 21000, "1972-03-14", []int{18, 80}}
	lotr1      = fr{"movie", 120, "The Lord of the Rings: The Fellowship of the Ring", "", 30, 26000, "2001-12-18", []int{12, 14, 28}}
	lotr2      = fr{"movie", 121, "The Lord of the Rings: The Two Towers", "", 25, 22000, "2002-12-18", []int{12, 14, 28}}
	lotr3      = fr{"movie", 122, "The Lord of the Rings: The Return of the King", "", 28, 24000, "2003-12-01", []int{12, 14, 28}}
	lotr78     = fr{"movie", 123, "The Lord of the Rings", "", 10, 1100, "1978-11-15", []int{16, 12, 14}}
	rings      = fr{"tv", 84773, "The Lord of the Rings: The Rings of Power", "", 40, 3000, "2022-09-01", []int{10765, 18}}
	it2017     = fr{"movie", 346364, "It", "", 30, 20000, "2017-09-06", []int{27}}
	it1990     = fr{"tv", 19614, "It", "", 15, 2500, "1990-11-18", []int{18, 9648}}
	itEnds     = fr{"movie", 1079091, "It Ends with Us", "", 90, 2000, "2024-08-07", []int{10749, 18}}
	itFollows  = fr{"movie", 270303, "It Follows", "", 20, 8000, "2014-05-17", []int{27}}
	itTwo      = fr{"movie", 474350, "It Chapter Two", "", 25, 9000, "2019-09-04", []int{27}}
	up         = fr{"movie", 14160, "Up", "", 25, 20000, "2009-05-28", []int{16, 35, 10751}}
	upAir      = fr{"movie", 22947, "Up in the Air", "", 12, 4000, "2009-09-05", []int{18}}
	upgrade    = fr{"movie", 500664, "Upgrade", "", 15, 4500, "2018-05-31", []int{28}}
	her        = fr{"movie", 152601, "Her", "", 25, 14000, "2013-12-18", []int{10749}}
	hereditary = fr{"movie", 493922, "Hereditary", "", 40, 8000, "2018-06-04", []int{27}}
	herStory   = fr{"movie", 1140535, "Her Story", "", 5, 40, "2024-03-01", []int{18}}
	dune21     = fr{"movie", 438631, "Dune", "", 80, 12000, "2021-09-15", []int{878, 12}}
	dune2      = fr{"movie", 693134, "Dune: Part Two", "", 150, 7000, "2024-02-27", []int{878, 12}}
	dune84     = fr{"movie", 841, "Dune", "", 12, 3000, "1984-12-14", []int{878, 12}}
	duneDoc    = fr{"movie", 251516, "Jodorowsky's Dune", "", 5, 600, "2013-07-10", []int{99}}
	prophecy   = fr{"tv", 90228, "Dune: Prophecy", "", 60, 600, "2024-11-17", []int{10765}}
	mandolin   = fr{"movie", 5001, "The Mandolin Man", "", 1, 3, "1999-01-01", []int{18}}
)

// fakeTMDB answers /search/multi from answers[lang][normalized query].
func fakeTMDB(t *testing.T, answers map[string]map[string][]fr) (*tmdb.Client, *atomic.Int32) {
	t.Helper()
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/search/multi" {
			http.NotFound(w, r)
			return
		}
		calls.Add(1)
		lang := "en"
		if strings.HasPrefix(r.URL.Query().Get("language"), "tr") {
			lang = "tr"
		}
		results := []map[string]any{}
		if r.URL.Query().Get("page") == "1" {
			for _, f := range answers[lang][normalize(r.URL.Query().Get("query"))] {
				results = append(results, f.raw())
			}
		}
		json.NewEncoder(w).Encode(map[string]any{"page": 1, "total_pages": 1, "results": results})
	}))
	t.Cleanup(srv.Close)
	return tmdb.New("test-key").WithBaseURL(srv.URL), &calls
}

func fixtureTitles() []IndexTitle {
	return []IndexTitle{
		{kindTV, 82856, 63, "The Mandalorian"},
		{kindTV, 101978, 13.6, "Disney Gallery / Star Wars: The Mandalorian"},
		{kindMovie, 1022789, 45, "The Mandalorian and Grogu"},
		{kindMovie, 5001, 1, "The Mandolin Man"},
		{kindMovie, 5002, 3, "Mandala"},
		{kindMovie, 5003, 2, "Mandalay"},
		{kindTV, 1396, 120, "Breaking Bad"},
		{kindMovie, 157336, 40, "Interstellar"},
		{kindMovie, 5004, 1.5, "Interstate 60"},
		{kindTV, 1399, 150, "Game of Thrones"},
		{kindTV, 5005, 2, "Thronecast"},
		{kindTV, 71446, 50, "La casa de papel"},
		{kindMovie, 438631, 80, "Dune"},
		{kindMovie, 346364, 30, "It"},
		{kindMovie, 14160, 25, "Up"},
		{kindMovie, 152601, 25, "Her"},
		{kindMovie, 238, 95, "The Godfather"},
		{kindMovie, 120, 30, "The Lord of the Rings: The Fellowship of the Ring"},
	}
}

func newSearchService(t *testing.T, answers map[string]map[string][]fr) (*Service, *tmdb.Client, *atomic.Int32) {
	tm, calls := fakeTMDB(t, answers)
	s := NewService(tm)
	s.SetSearchIndex(NewTitleIndex(fixtureTitles()))
	return s, tm, calls
}

func ids(res *DiscoverPage) []int {
	out := []int{}
	for _, r := range res.Results {
		out = append(out, r.ID)
	}
	return out
}

func TestNormalize(t *testing.T) {
	cases := map[string]string{
		"Yüzüklerin Efendisi: İki Kule": "yuzuklerin efendisi iki kule",
		"IŞIK ğÇö":                      "isik gco",
		"Schindler's List":              "schindlers list",
		"Amélie  (2001)":                "amelie 2001",
		"  Dune: Part Two! ":            "dune part two",
		"Ocean’s Eleven":                "oceans eleven",
	}
	for in, want := range cases {
		if got := normalize(in); got != want {
			t.Errorf("normalize(%q) = %q, want %q", in, got, want)
		}
	}
	if stripArticle("the mandalorian") != "mandalorian" || stripArticle("the") != "the" {
		t.Error("stripArticle")
	}
}

func TestTextScoreOrder(t *testing.T) {
	q := newQuery("mandalorian")
	exact := textScore(newQuery("the mandalorian"), "the mandalorian")
	noArticle := textScore(q, "the mandalorian")
	prefix := textScore(q, "the mandalorian and grogu")
	phrase := textScore(q, "disney gallery star wars the mandalorian")
	fuzzy := textScore(newQuery("mandalorien"), "the mandalorian")
	if !(exact > noArticle && noArticle > prefix && prefix > phrase && phrase > fuzzy && fuzzy > 0) {
		t.Errorf("order: exact %.2f noArticle %.2f prefix %.2f phrase %.2f fuzzy %.2f", exact, noArticle, prefix, phrase, fuzzy)
	}
	if textScore(q, "breaking bad") != 0 {
		t.Error("unrelated title matched")
	}
}

func TestPhonetic(t *testing.T) {
	same := [][2]string{{"mandoloarien", "mandalorian"}, {"brekin bad", "breaking bad"}, {"interstelar", "interstellar"},
		{"game of trones", "game of thrones"}}
	for _, p := range same {
		if phonetic(p[0]) != phonetic(p[1]) {
			t.Errorf("phonetic(%q) = %q, phonetic(%q) = %q", p[0], phonetic(p[0]), p[1], phonetic(p[1]))
		}
	}
	if d := damerauLevenshtein([]rune("mandolorian"), []rune("mandalorian")); d != 1 {
		t.Errorf("DL = %d", d)
	}
	if d := damerauLevenshtein([]rune("ab"), []rune("ba")); d != 1 {
		t.Errorf("DL swap = %d", d)
	}
}

func TestRankedSearchMandalorian(t *testing.T) {
	answers := map[string]map[string][]fr{"en": {
		// TMDB's own order puts the documentary and the film first
		"mandalorian":     {mandoGallery, mandoGrogu, mandoLook, mandalorian},
		"the mandalorian": {mandoGallery, mandoGrogu, mandoLook, mandalorian},
		"mandolorian":     {mandolin},
	}}
	s, tm, _ := newSearchService(t, answers)
	res, err := s.RankedSearch(tm, "mandalorian", Filter{Type: "all"}, 0)
	if err != nil {
		t.Fatal(err)
	}
	if got := ids(res); got[0] != mandalorian.id || res.DidYouMean != "" {
		t.Fatalf("mandalorian: %v, did_you_mean %q", got, res.DidYouMean)
	}
	if got := ids(res); got[len(got)-1] != mandoLook.id {
		t.Errorf("the special look should rank last: %v", got)
	}

	for _, typo := range []string{"mandoloarien", "mandalorien", "mandolorian", "the mandolorian", "Mandolorian"} {
		res, err := s.RankedSearch(tm, typo, Filter{Type: "all"}, 0)
		if err != nil {
			t.Fatalf("%s: %v", typo, err)
		}
		if got := ids(res); len(got) == 0 || got[0] != mandalorian.id {
			t.Errorf("%s: results %v", typo, got)
		}
		if res.DidYouMean != "The Mandalorian" {
			t.Errorf("%s: did_you_mean %q", typo, res.DidYouMean)
		}
	}

	// Filters apply to the corrected results too
	res, err = s.RankedSearch(tm, "mandoloarien", Filter{Type: "movie"}, 0)
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range res.Results {
		if r.MediaType != "movie" {
			t.Errorf("type filter let %s %d through", r.MediaType, r.ID)
		}
	}
}

func TestRankedSearchExactTitles(t *testing.T) {
	answers := map[string]map[string][]fr{"en": {
		"it":   {itEnds, itFollows, itTwo, it1990, it2017},
		"up":   {upAir, upgrade, up},
		"her":  {hereditary, herStory, her},
		"dune": {dune2, prophecy, duneDoc, dune84, dune21},
	}}
	s, tm, _ := newSearchService(t, answers)
	want := map[string]int{"it": it2017.id, "up": up.id, "her": her.id, "dune": dune21.id}
	for q, id := range want {
		res, err := s.RankedSearch(tm, q, Filter{Type: "all"}, 0)
		if err != nil {
			t.Fatal(err)
		}
		if got := ids(res); got[0] != id {
			t.Errorf("%s: %v, want %d first", q, got, id)
		}
		if res.DidYouMean != "" {
			t.Errorf("%s: unexpected did_you_mean %q", q, res.DidYouMean)
		}
	}
}

func TestRankedSearchTurkish(t *testing.T) {
	tr := func(f fr, title string) fr {
		f.orig, f.title = f.title, title
		return f
	}
	answers := map[string]map[string][]fr{"tr": {
		"yuzuklerin efendisi": {
			tr(rings, "Yüzüklerin Efendisi: Güç Yüzükleri"),
			tr(lotr78, "Yüzüklerin Efendisi"),
			tr(lotr2, "Yüzüklerin Efendisi: İki Kule"),
			tr(lotr1, "Yüzüklerin Efendisi: Yüzük Kardeşliği"),
			tr(lotr3, "Yüzüklerin Efendisi: Kralın Dönüşü"),
		},
		"baba": {
			{"tv", 200001, "Baba", "", 15, 50, "2022-01-11", []int{18}},
			{"movie", 200002, "Baba Yaga", "", 20, 300, "2020-01-01", []int{27}},
			{"movie", 200003, "Baba", "", 3, 150, "1971-05-01", []int{18}},
			tr(fr{"movie", 240, "The Godfather Part II", "", 60, 13000, "1974-12-20", []int{18, 80}}, "Baba II"),
			{"movie", 200004, "Babam ve Oğlum", "", 15, 1000, "2005-11-18", []int{18}},
			tr(godfather, "Baba"),
		},
	}}
	s, tm, _ := newSearchService(t, answers)
	trc := tm.WithLang("tr")

	res, err := s.RankedSearch(trc, "yüzüklerin efendisi", Filter{Type: "all"}, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Results) == 0 || !strings.HasPrefix(res.Results[0].OriginalTitle, "The Lord of the Rings") {
		t.Fatalf("yüzüklerin efendisi: %+v", res.Results)
	}
	if res.DidYouMean != "" {
		t.Errorf("did_you_mean %q", res.DidYouMean)
	}
	// Folded spelling ("yuzuklerin efendisi") finds the same titles
	res2, err := s.RankedSearch(trc, "YUZUKLERIN EFENDISI", Filter{Type: "all"}, 0)
	if err != nil || len(res2.Results) == 0 || res2.Results[0].ID != res.Results[0].ID {
		t.Errorf("folded query: %v %v", ids(res2), err)
	}

	res, err = s.RankedSearch(trc, "baba", Filter{Type: "all"}, 0)
	if err != nil {
		t.Fatal(err)
	}
	if got := ids(res); got[0] != godfather.id {
		t.Errorf("baba: %v, want The Godfather first", got)
	}

	// The Turkish titles were learned as aliases: an English search can match them now
	if a := s.index.aliasesOf("movie", 238); len(a) != 1 || a[0] != "Baba" {
		t.Errorf("aliases of The Godfather: %v", a)
	}
}

func TestSearchPoolCachedAndPeopleDropped(t *testing.T) {
	answers := map[string]map[string][]fr{"en": {"dune": {dune21, {mt: "person", id: 9, title: "Dune Person"}}}}
	s, tm, calls := newSearchService(t, answers)
	for range 2 {
		res, err := s.RankedSearch(tm, "Dune", Filter{Type: "all"}, 0)
		if err != nil {
			t.Fatal(err)
		}
		if got := ids(res); len(got) != 1 || got[0] != dune21.id {
			t.Errorf("results %v", got)
		}
	}
	if n := calls.Load(); n != rankPages {
		t.Errorf("TMDB calls = %d, want %d (second search cached)", n, rankPages)
	}
}

func TestRankPenalties(t *testing.T) {
	in := newRankInput(newQuery("star wars"), nil, nil)
	film := tmdb.SearchResult{ID: 1, MediaType: "movie", Title: "Star Wars", PosterPath: "/p", Popularity: 20, VoteCount: 1000, ReleaseDate: "1977-05-25"}
	doc := film
	doc.ID, doc.GenreIDs = 2, []int{genreDocumentary}
	noPoster := film
	noPoster.ID, noPoster.PosterPath = 3, ""
	few := film
	few.ID, few.VoteCount = 4, 2
	base := in.score(film, 1, 0, 1)
	for name, r := range map[string]tmdb.SearchResult{"documentary": doc, "no poster": noPoster, "few votes": few} {
		if s := in.score(r, 1, 0, 1); s >= base {
			t.Errorf("%s: %.2f >= %.2f", name, s, base)
		}
	}
	// A documentary query keeps documentaries
	docIn := newRankInput(newQuery("star wars documentary"), nil, nil)
	if docIn.score(doc, 1, 0, 1) != docIn.score(film, 1, 0, 1) {
		t.Error("documentary penalized although asked for")
	}
	// Taste: a loved genre lifts, a disliked one lowers
	loved := newRankInput(newQuery("star wars"), map[int]float64{878: 1}, nil)
	sf := film
	sf.GenreIDs = []int{878}
	if loved.score(sf, 1, 0, 1) <= in.score(sf, 1, 0, 1) {
		t.Error("taste did not lift")
	}
	// TV genres count as their movie genre
	if commonGenre("tv", 10765) != 878 || commonGenre("movie", 10765) != 10765 {
		t.Error("commonGenre")
	}
}

func TestSuggest(t *testing.T) {
	ix := NewTitleIndex(fixtureTitles())
	cases := map[string]string{
		"mand":        "The Mandalorian",
		"the manda":   "The Mandalorian",
		"mandolori":   "The Mandalorian", // typo while typing
		"interst":     "Interstellar",
		"thron":       "Game of Thrones", // more popular than "Thronecast"
		"breakin":     "Breaking Bad",
		"casa de pap": "La casa de papel",
	}
	for q, want := range cases {
		hits := ix.Suggest(q, "en", 5)
		if len(hits) == 0 || hits[0].Title != want {
			t.Errorf("Suggest(%q) = %+v, want %q first", q, hits, want)
		}
	}
	// Localized names are suggested in their language only
	ix.Learn("movie", 238, "tr", "Baba")
	ix.rebuildAliases()
	if hits := ix.Suggest("bab", "tr", 5); len(hits) == 0 || hits[0].Title != "Baba" || hits[0].Original != "The Godfather" {
		t.Errorf("tr suggest: %+v", hits)
	}
	if hits := ix.Suggest("bab", "en", 5); len(hits) != 0 {
		t.Errorf("en suggest got the Turkish alias: %+v", hits)
	}
}

func TestCorrect(t *testing.T) {
	ix := NewTitleIndex(fixtureTitles())
	cases := map[string]string{
		"mandoloarien":    "The Mandalorian",
		"mandalorien":     "The Mandalorian",
		"brekin bad":      "Breaking Bad",
		"interstelar":     "Interstellar",
		"game of trones":  "Game of Thrones",
		"la casa de papl": "La casa de papel",
	}
	for q, want := range cases {
		hits := ix.Correct(q, 3, correctMin)
		if len(hits) == 0 || hits[0].Title != want {
			t.Errorf("Correct(%q) = %+v, want %q", q, hits, want)
		}
	}
	if ix.KnownWords("mandoloarien") || !ix.KnownWords("the mandalorian") {
		t.Error("KnownWords")
	}
}

func TestParseExport(t *testing.T) {
	var buf bytes.Buffer
	zw := gzip.NewWriter(&buf)
	zw.Write([]byte(`{"adult":false,"id":1,"original_title":"Dune","popularity":80.5,"video":false}
{"adult":true,"id":2,"original_title":"X","popularity":50,"video":false}
{"adult":false,"id":3,"original_title":"Low","popularity":0.6,"video":false}
{"adult":false,"id":4,"original_title":"Clip","popularity":9,"video":true}
{"adult":false,"id":5,"original_title":"千と千尋の神隠し","popularity":60,"video":false}
`))
	zw.Close()
	got, err := parseExport(&buf, kindMovie, 1)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].ID != 1 || got[0].Title != "Dune" || got[0].Pop != 80.5 {
		t.Errorf("parsed %+v", got)
	}
}

func TestIndexLoaderSaveLoad(t *testing.T) {
	dir := t.TempDir()
	ix := NewTitleIndex(fixtureTitles())
	l := NewIndexLoader(ix, dir)
	if err := l.save(fixtureTitles()); err != nil {
		t.Fatal(err)
	}
	ix.Learn("movie", 238, "tr", "Baba")
	l.saveAliases()

	ix2 := newEmptyIndex()
	if _, err := NewIndexLoader(ix2, dir).load(); err != nil {
		t.Fatal(err)
	}
	if n, _ := ix2.Size(); n != len(fixtureTitles()) {
		t.Errorf("loaded %d titles", n)
	}
	if hits := ix2.Suggest("bab", "tr", 3); len(hits) == 0 || hits[0].Title != "Baba" {
		t.Errorf("alias not loaded: %+v", hits)
	}
}

func TestIndexRefreshFromExports(t *testing.T) {
	gz := func(lines string) []byte {
		var b bytes.Buffer
		zw := gzip.NewWriter(&b)
		zw.Write([]byte(lines))
		zw.Close()
		return b.Bytes()
	}
	movies := gz(`{"adult":false,"id":157336,"original_title":"Interstellar","popularity":40,"video":false}` + "\n")
	shows := gz(`{"id":82856,"original_name":"The Mandalorian","popularity":63}` + "\n")
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasPrefix(r.URL.Path, "/movie_ids_"):
			w.Write(movies)
		case strings.HasPrefix(r.URL.Path, "/tv_series_ids_"):
			w.Write(shows)
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()
	ix := newEmptyIndex()
	l := NewIndexLoader(ix, t.TempDir())
	l.base = srv.URL + "/"
	if _, err := l.Refresh(t.Context()); err != nil {
		t.Fatal(err)
	}
	if hits := ix.Correct("mandolorian", 1, correctMin); len(hits) != 1 || hits[0].MediaType != "tv" {
		t.Errorf("hits %+v", hits)
	}
}

func TestRankedSearchObscureExactMatch(t *testing.T) {
	interstelar := fr{"movie", 900001, "Interstelar", "", 0.8, 0, "2014-01-01", nil}
	interstellar := fr{"movie", 157336, "Interstellar", "", 40, 37000, "2014-11-05", []int{878, 18}}
	answers := map[string]map[string][]fr{"en": {
		"interstelar":  {interstelar},
		"interstellar": {interstellar},
	}}
	s, tm, _ := newSearchService(t, answers)
	res, err := s.RankedSearch(tm, "interstelar", Filter{Type: "all"}, 0)
	if err != nil {
		t.Fatal(err)
	}
	if got := ids(res); len(got) != 2 || got[0] != interstellar.id || res.DidYouMean != "Interstellar" {
		t.Errorf("results %v, did_you_mean %q", got, res.DidYouMean)
	}
}
