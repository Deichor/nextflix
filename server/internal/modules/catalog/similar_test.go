package catalog

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gin-gonic/gin"

	"nextflix/internal/platform/tmdb"
)

const simTestToday = "2026-10-01"

// fakeSim is an in-memory simFetcher.
type fakeSim struct {
	mu      sync.Mutex
	src     simSource
	byKey   map[string]*simFacts
	lists   map[string][]tmdb.SearchResult // "recommendations:1", "similar:1", "collection", "kw:<id>", "lang:<l>:<page>"
	works   map[int]*simPersonWorks
	df      map[int]int
	failSrc bool
	builds  int
}

func newFakeSim(src simFacts) *fakeSim {
	return &fakeSim{src: simSource{simFacts: src, KeywordNames: map[int]string{}},
		byKey: map[string]*simFacts{}, lists: map[string][]tmdb.SearchResult{}, works: map[int]*simPersonWorks{},
		df: map[int]int{}}
}

// add registers a title's facts and returns its list entry.
func (f *fakeSim) add(fa simFacts) tmdb.SearchResult {
	if fa.PosterPath == "" {
		fa.PosterPath = "/p.jpg"
	}
	if fa.Title == "" {
		fa.Title = fmt.Sprintf("Title %d", fa.ID)
	}
	f.byKey[resultKey(fa.MediaType, fa.ID)] = &fa
	return tmdb.SearchResult{ID: fa.ID, MediaType: fa.MediaType, Title: fa.Title, OriginalTitle: fa.OriginalTitle,
		ReleaseDate: fa.Date, PosterPath: fa.PosterPath, VoteAverage: fa.VoteAverage, VoteCount: fa.VoteCount,
		Popularity: fa.Popularity, Adult: fa.Adult, GenreIDs: fa.Genres, OriginalLanguage: fa.Language}
}

func (f *fakeSim) list(key string, rs ...tmdb.SearchResult) {
	f.lists[key] = append(f.lists[key], rs...)
}

func (f *fakeSim) source(mediaType string, id int) (*simSource, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.builds++
	if f.failSrc {
		return nil, fmt.Errorf("tmdb down")
	}
	s := f.src
	return &s, nil
}

func (f *fakeSim) facts(mediaType string, id int) (*simFacts, error) {
	if fa, ok := f.byKey[resultKey(mediaType, id)]; ok {
		return fa, nil
	}
	return nil, fmt.Errorf("no facts")
}

func (f *fakeSim) related(lang, mediaType, kind string, id, page int) ([]tmdb.SearchResult, error) {
	return f.lists[fmt.Sprintf("%s:%d", kind, page)], nil
}

func (f *fakeSim) collection(lang string, id int) ([]tmdb.SearchResult, error) {
	return f.lists["collection"], nil
}

func (f *fakeSim) personWorks(lang string, personID int) (*simPersonWorks, error) {
	if w, ok := f.works[personID]; ok {
		return w, nil
	}
	return &simPersonWorks{}, nil
}

func (f *fakeSim) keywordDF(mediaType string, kw int) (int, error) {
	if n, ok := f.df[kw]; ok {
		return n, nil
	}
	return 0, fmt.Errorf("unknown keyword")
}

func (f *fakeSim) discover(lang, mediaType string, q simQuery) ([]tmdb.SearchResult, error) {
	if q.Keyword > 0 {
		return f.lists[fmt.Sprintf("kw:%d", q.Keyword)], nil
	}
	return f.lists[fmt.Sprintf("lang:%s:%d", q.Language, q.Page)], nil
}

func movie(id int, title, lang, date string, votes int, avg float64, genres ...int) simFacts {
	return simFacts{MediaType: "movie", ID: id, Title: title, OriginalTitle: title, Language: lang, Date: date,
		VoteCount: votes, VoteAverage: avg, Genres: genres, Runtime: 110}
}

func show(id int, title, lang, date string, votes int, avg float64, genres ...int) simFacts {
	return simFacts{MediaType: "tv", ID: id, Title: title, OriginalTitle: title, Language: lang, Date: date,
		VoteCount: votes, VoteAverage: avg, Genres: genres, TVType: "Scripted", Runtime: 50}
}

func titles(items []SimilarItem) []string {
	out := make([]string, len(items))
	for i, it := range items {
		out[i] = it.Title
	}
	return out
}

func indexOf(items []SimilarItem, title string) int {
	for i, it := range items {
		if it.Title == title {
			return i
		}
	}
	return -1
}

func build(t *testing.T, f *fakeSim) []SimilarItem {
	t.Helper()
	items, err := buildSimilar(f, "en", f.src.MediaType, f.src.ID, simTestToday)
	if err != nil {
		t.Fatal(err)
	}
	return items
}

func TestSimilarCollectionFirstInReleaseOrder(t *testing.T) {
	src := movie(240, "The Godfather Part II", "en", "1974-12-20", 14000, 8.6, gDrama, gCrime)
	src.Collection = 230
	f := newFakeSim(src)
	part3 := f.add(movie(242, "The Godfather Part III", "en", "1990-12-25", 7000, 7.4, gCrime, gDrama))
	part1 := f.add(movie(238, "The Godfather", "en", "1972-03-14", 23000, 8.7, gDrama, gCrime))
	f.byKey["movie:242"].Collection, f.byKey["movie:238"].Collection = 230, 230
	f.list("collection", part3, part1, f.add(movie(240, "The Godfather Part II", "en", "1974-12-20", 14000, 8.6, gDrama, gCrime)))
	// Strong matches from TMDB's lists come after the collection
	f.list("recommendations:1",
		f.add(movie(769, "GoodFellas", "en", "1990-09-12", 14000, 8.4, gDrama, gCrime)),
		f.add(movie(524, "Casino", "en", "1995-11-22", 6700, 8.0, gCrime, gDrama)),
		part3)

	items := build(t, f)
	got := titles(items)
	if len(got) < 4 || got[0] != "The Godfather" || got[1] != "The Godfather Part III" {
		t.Fatalf("collection is not first in release order: %v", got)
	}
	if items[0].Reason != "collection" || items[2].Reason != "match" {
		t.Errorf("reasons = %q, %q", items[0].Reason, items[2].Reason)
	}
	if indexOf(items, "The Godfather Part II") >= 0 {
		t.Error("the source itself is listed")
	}
}

func TestSimilarAnimationAndLiveActionStayApart(t *testing.T) {
	// Live-action crime drama: kids' animation never, adult anime far below live action
	src := movie(1, "Crime Drama", "en", "2010-01-01", 5000, 8.0, gCrime, gDrama)
	f := newFakeSim(src)
	f.list("recommendations:1",
		f.add(movie(2, "Kids Cartoon", "en", "2010-01-01", 9000, 8.0, gAnimation, gFamily, gCrime)),
		f.add(movie(3, "Adult Anime", "en", "2010-01-01", 9000, 8.0, gAnimation, gCrime, gDrama)),
		f.add(movie(4, "Live Crime", "en", "2010-01-01", 3000, 7.5, gCrime, gDrama)),
		f.add(movie(5, "Family Comedy", "en", "2010-01-01", 3000, 7.5, gFamily, gCrime, 35)))
	items := build(t, f)
	if indexOf(items, "Kids Cartoon") >= 0 {
		t.Errorf("kids' animation for a live-action crime drama: %v", titles(items))
	}
	if a, l := indexOf(items, "Adult Anime"), indexOf(items, "Live Crime"); l < 0 || (a >= 0 && a < l) {
		t.Errorf("animation ranks above live action: %v", titles(items))
	}
	if fam, l := indexOf(items, "Family Comedy"), indexOf(items, "Live Crime"); fam >= 0 && fam < l {
		t.Errorf("family title ranks above live action: %v", titles(items))
	}

	// Kids' animation: live-action thrillers are dropped, other animation leads
	src2 := movie(10, "Toy Movie", "en", "1995-11-22", 18000, 8.0, gAnimation, 35, gFamily)
	g := newFakeSim(src2)
	g.list("recommendations:1",
		g.add(movie(11, "Live Thriller", "en", "1996-01-01", 9000, 8.0, gThriller, gCrime, 35)),
		g.add(movie(12, "Bug Movie", "en", "1998-01-01", 9000, 7.2, gAnimation, gFamily, 35)),
		g.add(movie(13, "Live Family", "en", "1996-01-01", 9000, 7.2, gFamily, 35)))
	items = build(t, g)
	if indexOf(items, "Live Thriller") >= 0 {
		t.Errorf("live-action thriller for kids' animation: %v", titles(items))
	}
	if b, l := indexOf(items, "Bug Movie"), indexOf(items, "Live Family"); b != 0 || l < b {
		t.Errorf("animation should lead: %v", titles(items))
	}
}

func TestSimilarKeywordIDF(t *testing.T) {
	src := movie(1, "Heist Film", "en", "2010-01-01", 5000, 7.5, gCrime, gThriller)
	src.Keywords = []int{100, 200, 300, 400}
	f := newFakeSim(src)
	f.src.KeywordNames = map[int]string{100: "safecracker", 200: "new york city", 300: "based on novel or book", 400: "sequel"}
	f.df = map[int]int{100: 120, 200: 15000, 300: 120, 400: 50}

	rare := movie(2, "Rare Keyword", "en", "2010-01-01", 3000, 7.0, gCrime, gThriller)
	rare.Keywords = []int{100}
	common := movie(3, "Common Keywords", "en", "2010-01-01", 3000, 7.0, gCrime, gThriller)
	common.Keywords = []int{200, 300, 400}
	f.list("similar:1", f.add(common), f.add(rare))
	f.list("kw:100", f.add(rare))

	s := f.src
	s.prepare(func(k int) int { return f.df[k] })
	if s.kwWeight[300] != 0 || s.kwWeight[400] != 0 {
		t.Errorf("generic keywords weigh %v / %v", s.kwWeight[300], s.kwWeight[400])
	}
	if !(s.kwWeight[100] > 3*s.kwWeight[200]) {
		t.Errorf("rare keyword %.2f vs common %.2f", s.kwWeight[100], s.kwWeight[200])
	}
	if got := s.topKeywords(4, simKwMinW); len(got) != 1 || got[0] != 100 {
		t.Errorf("discover keywords = %v", got)
	}

	items := build(t, f)
	if r, c := indexOf(items, "Rare Keyword"), indexOf(items, "Common Keywords"); r < 0 || (c >= 0 && c < r) {
		t.Errorf("the rare shared keyword should win: %v", titles(items))
	}
}

func TestSimilarExclusions(t *testing.T) {
	src := movie(1, "Thriller", "en", "2015-01-01", 5000, 7.5, gThriller, gCrime)
	f := newFakeSim(src)
	ok := movie(2, "Fine Thriller", "en", "2016-01-01", 3000, 7.2, gThriller, gCrime)
	adult := movie(3, "Adult", "en", "2016-01-01", 3000, 7.2, gThriller)
	adult.Adult = true
	doc := movie(4, "Documentary", "en", "2016-01-01", 3000, 7.2, gDocumentary, gCrime)
	short := movie(5, "Short", "en", "2016-01-01", 3000, 7.2, gThriller, gCrime)
	short.Runtime = 20
	unreleased := movie(6, "Unreleased", "en", "2027-01-01", 3000, 7.2, gThriller, gCrime)
	video := movie(7, "Video", "en", "2016-01-01", 3000, 7.2, gThriller, gCrime)
	video.Video = true
	makingOf := movie(8, "Thriller: The Making of", "en", "2016-01-01", 3000, 7.2, gThriller, gCrime)
	few := movie(9, "Few Votes", "en", "2016-01-01", 40, 9.0, gThriller, gCrime)
	noPoster := movie(10, "No Poster", "en", "2016-01-01", 3000, 7.2, gThriller, gCrime)
	talk := show(11, "Talk Show", "en", "2016-01-01", 3000, 7.2, gCrime)
	talk.TVType = "Talk Show"
	for _, fa := range []simFacts{ok, adult, doc, short, unreleased, video, makingOf, few, talk} {
		f.list("recommendations:1", f.add(fa))
	}
	r := f.add(noPoster)
	f.byKey["movie:10"].PosterPath, r.PosterPath = "", ""
	f.list("recommendations:1", r)

	items := build(t, f)
	if got := titles(items); len(got) != 1 || got[0] != "Fine Thriller" {
		t.Errorf("only the fine thriller should remain: %v", got)
	}
}

func TestSimilarDiversityCaps(t *testing.T) {
	src := movie(1, "Space Opera", "en", "2015-01-01", 5000, 7.5, 878, 12)
	f := newFakeSim(src)
	f.src.Creators = []int{77}
	var made []tmdb.SearchResult
	for i := 0; i < 5; i++ {
		// Another collection: at most 2
		fa := movie(100+i, fmt.Sprintf("Saga Part %d", i+1), "en", "2016-01-01", 9000, 7.5, 878, 12)
		fa.Collection = 999
		f.list("recommendations:1", f.add(fa))
		// One franchise prefix: at most 2
		f.list("recommendations:1", f.add(movie(200+i, fmt.Sprintf("Star Voyage: Chapter %d", i+1), "en", "2016-01-01", 9000, 7.5, 878, 12)))
		// The same director: at most 4
		made = append(made, f.add(movie(300+i, fmt.Sprintf("Director Film %d", i+1), "en", "2016-01-01", 9000, 7.5, 878, 12)))
		// Shows: at most 3, and only with a strong link (the director here)
		made = append(made, f.add(show(400+i, fmt.Sprintf("Director Show %d", i+1), "en", "2016-01-01", 9000, 7.5, 10765)))
	}
	f.works[77] = &simPersonWorks{Made: made}
	f.list("recommendations:2", f.add(show(500, "Unlinked Show", "en", "2016-01-01", 9000, 8.5, 10765)))

	items := build(t, f)
	count := func(prefix string) int {
		n := 0
		for _, it := range items {
			if strings.HasPrefix(it.Title, prefix) {
				n++
			}
		}
		return n
	}
	if n := count("Saga Part"); n != simPerColl {
		t.Errorf("same collection: %d, want %d (%v)", n, simPerColl, titles(items))
	}
	if n := count("Star Voyage"); n != simPerFran {
		t.Errorf("same franchise: %d, want %d", n, simPerFran)
	}
	if n := count("Director "); n != simPerCreator {
		t.Errorf("same director: %d, want %d", n, simPerCreator)
	}
	if indexOf(items, "Unlinked Show") >= 0 {
		t.Error("a show without a strong link was listed for a movie")
	}
	cross := 0
	for _, it := range items {
		if it.MediaType == "tv" {
			cross++
		}
	}
	if cross > simCrossMax {
		t.Errorf("%d shows, want at most %d", cross, simCrossMax)
	}
}

func TestSimilarSpinOffAndLanguage(t *testing.T) {
	src := show(1396, "Breaking Bad", "en", "2008-01-20", 18000, 8.9, gDrama, gCrime)
	f := newFakeSim(src)
	f.src.Creators = []int{66633}
	camino := f.add(movie(559969, "El Camino: A Breaking Bad Movie", "en", "2019-10-11", 5900, 7.1, gCrime, gDrama, gThriller))
	saul := f.add(show(60059, "Better Call Saul", "en", "2015-02-08", 6900, 8.7, gCrime, gDrama))
	f.works[66633] = &simPersonWorks{Made: []tmdb.SearchResult{camino, saul}}
	f.list("recommendations:1", saul, f.add(show(2, "Some Crime Show", "en", "2016-01-01", 900, 7.5, gCrime, gDrama)))

	items := build(t, f)
	if len(items) < 3 || items[0].Title != "El Camino: A Breaking Bad Movie" || items[0].Reason != "spin_off" {
		t.Fatalf("spin-off is not first: %v", titles(items))
	}
	if items[1].Title != "Better Call Saul" {
		t.Errorf("the creator's show should follow: %v", titles(items))
	}

	// Turkish source: same-language titles beat English ones of the same quality
	tr := show(74823, "Çukur", "tr", "2017-10-23", 94, 8.0, gCrime, gDrama)
	g := newFakeSim(tr)
	g.list("recommendations:1", g.add(show(3, "English Crime", "en", "2017-01-01", 800, 7.8, gCrime, gDrama)))
	g.list("lang:tr:1", g.add(show(4, "Turkish Crime", "tr", "2016-01-01", 60, 7.8, gCrime, gDrama)))
	items = build(t, g)
	if tc, ec := indexOf(items, "Turkish Crime"), indexOf(items, "English Crime"); tc != 0 || (ec >= 0 && ec < tc) {
		t.Errorf("Turkish title should lead: %v", titles(items))
	}
}

func TestSpinOffTitles(t *testing.T) {
	cases := []struct {
		a, b string
		want bool
	}{
		{"Breaking Bad", "El Camino: A Breaking Bad Movie", true},
		{"The Mandalorian", "The Mandalorian and Grogu", true},
		{"Thriller", "Fine Thriller", false},
		{"Dune", "Dune: Part Two", false}, // one short word: collections cover these
		{"Breaking Bad", "Breaking Badly", false},
		{"Breaking Bad", "Breaking Bad", false},
	}
	for _, c := range cases {
		if got := spinOff(c.a, c.b); got != c.want {
			t.Errorf("spinOff(%q, %q) = %v", c.a, c.b, got)
		}
	}
}

func TestMainGenreSkipsBroadGenres(t *testing.T) {
	cases := []struct {
		mediaType string
		genres    []int
		want      int
	}{
		{"movie", []int{12, gDrama, 878}, 878},        // Interstellar
		{"movie", []int{35, gThriller, gDrama}, 53},   // Parasite
		{"movie", []int{gDrama}, gDrama},              // only broad
		{"tv", []int{10759, gDrama, gCrime}, gCrime},  // Action & Adventure is broad
		{"movie", []int{gAnimation, 35, gFamily}, 16}, // first one is specific
	}
	for _, c := range cases {
		s := simSource{simFacts: simFacts{MediaType: c.mediaType, Genres: c.genres}}
		if got := s.mainGenreRaw(); got != c.want {
			t.Errorf("%v: main = %d, want %d", c.genres, got, c.want)
		}
	}
}

func TestSimilarCacheAndStaleFallback(t *testing.T) {
	src := movie(1, "Thriller", "en", "2015-01-01", 5000, 7.5, gThriller, gCrime)
	f := newFakeSim(src)
	f.list("recommendations:1", f.add(movie(2, "Fine Thriller", "en", "2016-01-01", 3000, 7.2, gThriller, gCrime)))
	s := &Service{sim: newSimState(f)}

	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if items, err := s.Similar("en", "movie", 1); err != nil || len(items) != 1 {
				t.Errorf("Similar = %v, %v", items, err)
			}
		}()
	}
	wg.Wait()
	if f.builds != 1 {
		t.Errorf("%d builds for concurrent and cached calls, want 1", f.builds)
	}

	// Expired and TMDB down: the old list is served
	s.sim.mu.Lock()
	for k, e := range s.sim.results {
		e.expires = time.Now().Add(-time.Hour)
		s.sim.results[k] = e
	}
	s.sim.mu.Unlock()
	f.failSrc = true
	if items, err := s.Similar("en", "movie", 1); err != nil || len(items) != 1 {
		t.Errorf("stale fallback = %v, %v", items, err)
	}
	if _, ok := s.SimilarWithin("en", "movie", 1, time.Second); !ok {
		t.Error("SimilarWithin missed a cached list")
	}
}

func TestPersonalizeSimilar(t *testing.T) {
	s := &Service{}
	s.SetLocalSignals(func() (map[uint][]LocalTitle, error) {
		return map[uint][]LocalTitle{7: {
			{MediaType: "movie", TmdbID: 2, Rating: "dislike"},
			{MediaType: "movie", TmdbID: 3, Watched: true, Finished: true},
			{MediaType: "movie", TmdbID: 10, Watched: true, Finished: true},
		}}, nil
	})
	mk := func(id int, reason string, score float64) SimilarItem {
		return SimilarItem{SearchResult: tmdb.SearchResult{ID: id, MediaType: "movie", Title: fmt.Sprint(id)},
			Reason: reason, score: score}
	}
	items := []SimilarItem{mk(10, "collection", 9), mk(2, "match", 8), mk(3, "match", 7), mk(4, "match", 6)}
	got := s.personalizeSimilar(items, 7)
	ids := []int{}
	for _, it := range got {
		ids = append(ids, it.ID)
	}
	// Disliked 2 removed, finished 3 moves below 4, the collection part keeps its place
	if fmt.Sprint(ids) != "[10 4 3]" {
		t.Errorf("personalized = %v", ids)
	}
	if fmt.Sprint(titles(s.personalizeSimilar(items, 0))) != fmt.Sprint(titles(items)) {
		t.Error("no profile must not change the list")
	}
	if items[1].ID != 2 {
		t.Error("the cached list was modified")
	}
}

func TestSimilarRoute(t *testing.T) {
	gin.SetMode(gin.TestMode)
	src := movie(1, "Thriller", "en", "2015-01-01", 5000, 7.5, gThriller, gCrime)
	f := newFakeSim(src)
	f.list("recommendations:1", f.add(movie(2, "Fine Thriller", "en", "2016-01-01", 3000, 7.2, gThriller, gCrime)))
	s := NewService(nil)
	s.sim = newSimState(f)
	r := gin.New()
	Register(r.Group("/api"), s) // must not conflict with /catalog/discover and /catalog/suggest

	for _, c := range []struct {
		path string
		code int
	}{
		{"/api/catalog/movie/1/similar?limit=5", http.StatusOK},
		{"/api/tmdb/1/related?type=movie&limit=12", http.StatusOK},
		{"/api/catalog/person/1/similar", http.StatusBadRequest},
		{"/api/catalog/movie/x/similar", http.StatusBadRequest},
	} {
		w := httptest.NewRecorder()
		r.ServeHTTP(w, httptest.NewRequest(http.MethodGet, c.path, nil))
		if w.Code != c.code {
			t.Errorf("%s: %d, want %d (%s)", c.path, w.Code, c.code, w.Body.String())
			continue
		}
		if c.code != http.StatusOK {
			continue
		}
		var body struct {
			Results []map[string]any `json:"results"`
		}
		if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil || len(body.Results) != 1 {
			t.Errorf("%s: body %s", c.path, w.Body.String())
			continue
		}
		if body.Results[0]["title"] != "Fine Thriller" || body.Results[0]["media_type"] != "movie" || body.Results[0]["reason"] != "match" {
			t.Errorf("%s: result %v", c.path, body.Results[0])
		}
	}
}
