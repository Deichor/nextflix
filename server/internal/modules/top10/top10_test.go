package top10

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

const countriesTSV = "country_name\tcountry_iso2\tweek\tcategory\tweekly_rank\tshow_title\tseason_title\tcumulative_weeks_in_top_10\r\n" +
	"Argentina\tAR\t2026-10-04\tFilms\t1\tUNABOMBER\tN/A\t2\r\n" +
	"Turkey\tTR\t2026-09-27\tFilms\t1\tOld Week\tN/A\t1\r\n" +
	"Turkey\tTR\t2026-10-04\tFilms\t2\tSonic the Hedgehog 3\tN/A\t1\r\n" +
	"Turkey\tTR\t2026-10-04\tFilms\t1\tUNABOMBER\tN/A\t2\r\n" +
	"Turkey\tTR\t2026-10-04\tTV\t1\tNot a Stranger\tNot a Stranger: Season 1\t3\r\n" +
	"Turkey\tTR\t2026-10-04\tTV\t2\tThe Mentalist\tThe Mentalist: Season 1\t12\r\n" +
	"Turkey\tTR\t2026-10-04\tTV\t3\tMonster: The Lizzie Borden Story\tMonster: The Lizzie Borden Story: Season 1\t3\r\n" +
	"Turkey\tTR\t2026-10-04\tTV\t4\tThe Mentalist\tThe Mentalist: Season 2\t10\r\n" +
	"Turkey\tTR\t2026-10-04\tFilms\t3\tUnknown Film\tN/A\t1\r\n" +
	"Turkey\tTR\t2026-10-04\tFilms\t4\tThe Mummy\tN/A\t1\r\n" +
	"Tunisia\tTN\t2026-10-11\tFilms\t1\tNewer Elsewhere\tN/A\t1\r\n" +
	"Turkey\tTR\t2026-09-20\tTV\t1\tOlder\tOlder: Season 1\t1\r\n"

const globalTSV = "week\tcategory\tweekly_rank\tshow_title\tseason_title\tweekly_hours_viewed\truntime\tweekly_views\tcumulative_weeks_in_top_10\n" +
	"2026-10-04\tFilms (English)\t1\tEN One\tN/A\t17900000\t1.85\t9700000\t2\n" +
	"2026-10-04\tFilms (English)\t2\tEN Two\tN/A\t11800000\t1.9\t6200000\t4\n" +
	"2026-10-04\tFilms (Non-English)\t1\tNON One\tN/A\t15000000\t2\t7500000\t1\n" +
	"2026-10-04\tFilms (Non-English)\t2\tNON Two\tN/A\t3000000\t1\t3000000\t1\n" +
	"2026-10-04\tTV (English)\t1\tShow EN\tShow EN: Season 1\t50000000\t5\t10000000\t1\n" +
	"2026-10-04\tTV (Non-English)\t1\tShow NON\tShow NON: Season 1\t40000000\t4\t10000001\t1\n" +
	"2026-09-27\tFilms (English)\t1\tLast Week\tN/A\t99999999\t1\t99999999\t1\n"

func TestParseCountriesLatestWeekTROnly(t *testing.T) {
	got, err := parseCountries(strings.NewReader(countriesTSV), []string{"TR"})
	if err != nil {
		t.Fatal(err)
	}
	tr := got["TR"]
	if tr.Week != "2026-10-04" {
		t.Fatalf("week %q", tr.Week)
	}
	var films []string
	for _, e := range tr.Films {
		films = append(films, fmt.Sprintf("%d:%s", e.Rank, e.Title))
	}
	if strings.Join(films, ",") != "1:UNABOMBER,2:Sonic the Hedgehog 3,3:Unknown Film,4:The Mummy" {
		t.Fatalf("films %v", films)
	}
	if len(tr.TV) != 4 || tr.TV[0].Title != "Not a Stranger" || tr.TV[3].SeasonTitle != "The Mentalist: Season 2" {
		t.Fatalf("tv %+v", tr.TV)
	}
	if len(got) != 1 {
		t.Fatalf("only TR wanted, got %v", got)
	}
}

func TestParseGlobalAndMerge(t *testing.T) {
	g, err := parseGlobal(strings.NewReader(globalTSV))
	if err != nil {
		t.Fatal(err)
	}
	if g.Week != "2026-10-04" || len(g.FilmsEN) != 2 || len(g.FilmsNonEN) != 2 {
		t.Fatalf("global %+v", g)
	}
	films := mergeGlobal(g.FilmsEN, g.FilmsNonEN)
	var order []string
	for _, e := range films {
		order = append(order, fmt.Sprintf("%d:%s", e.Rank, e.Title))
	}
	if strings.Join(order, ",") != "1:EN One,2:NON One,3:EN Two,4:NON Two" {
		t.Fatalf("merged by views: %v", order)
	}
	tv := mergeGlobal(g.TVEN, g.TVNonEN)
	if tv[0].Title != "Show NON" {
		t.Fatalf("tv merged %+v", tv)
	}
	// Without views or hours the English list is kept as is
	noMetric := mergeGlobal([]entry{{Rank: 2, Title: "b"}, {Rank: 1, Title: "a"}}, []entry{{Rank: 1, Title: "x"}})
	if len(noMetric) != 2 || noMetric[0].Title != "a" {
		t.Fatalf("english fallback %+v", noMetric)
	}
}

func TestQueriesAndPickMatch(t *testing.T) {
	q := queries(entry{Title: "Monster: The Lizzie Borden Story", SeasonTitle: "Monster: The Lizzie Borden Story: Season 1"}, "tv")
	if strings.Join(q, "|") != "Monster: The Lizzie Borden Story|Monster" {
		t.Fatalf("queries %q", q)
	}
	q = queries(entry{Title: "Gupi", SeasonTitle: "Gupi: Season 1"}, "tv")
	if len(q) != 1 {
		t.Fatalf("queries %q", q)
	}
	hits := []Match{
		{TmdbID: 1, Title: "The Mummy Returns", Popularity: 90},
		{TmdbID: 2, Title: "The Mummy", Popularity: 30},
		{TmdbID: 3, Title: "The Mummy", Popularity: 60},
		{TmdbID: 4, Title: "The Mummy", Popularity: 99, Adult: true},
	}
	if m, ok, exact := pickMatch("the mummy", hits); !ok || !exact || m.TmdbID != 3 {
		t.Fatalf("exact pick %+v", m)
	}
	if m, ok, exact := pickMatch("Mummy 4", hits); !ok || exact || m.TmdbID != 1 {
		t.Fatalf("loose pick %+v", m)
	}
	if normTitle("Kill Bill: Vol. 1") != "kill bill vol 1" || normTitle("Tom & Jerry") != "tom and jerry" {
		t.Fatal("normTitle")
	}
}

type fakeSearcher struct {
	movies, tv map[string][]Match
	calls      int
}

func (f *fakeSearcher) SearchMovies(q string) ([]Match, error) { f.calls++; return f.movies[q], nil }
func (f *fakeSearcher) SearchTV(q string) ([]Match, error)     { f.calls++; return f.tv[q], nil }

func testDB(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(filepath.Join(t.TempDir(), "t.db")), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.AutoMigrate(Models()...); err != nil {
		t.Fatal(err)
	}
	return db
}

func fakeTMDB() *fakeSearcher {
	return &fakeSearcher{
		movies: map[string][]Match{
			"UNABOMBER":            {{TmdbID: 11, MediaType: "movie", Title: "Unabomber", PosterPath: "/u.jpg", Date: "2026-09-01"}},
			"Sonic the Hedgehog 3": {{TmdbID: 12, MediaType: "movie", Title: "Sonic the Hedgehog 3", PosterPath: "/s.jpg"}},
			"The Mummy":            {{TmdbID: 13, MediaType: "movie", Title: "The Mummy"}},
		},
		tv: map[string][]Match{
			"Not a Stranger": {{TmdbID: 21, MediaType: "tv", Title: "Not a Stranger", Date: "2026-09-10"}},
			"The Mentalist":  {{TmdbID: 22, MediaType: "tv", Title: "The Mentalist"}},
			"Monster":        {{TmdbID: 23, MediaType: "tv", Title: "Monster", Popularity: 80}},
		},
	}
}

func TestRefreshMapsAndFallsBack(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/down/countries.tsv", "/down/global.tsv":
			http.Error(w, "nope", http.StatusBadGateway)
		case "/mirror/countries.tsv":
			fmt.Fprint(w, countriesTSV)
		case "/mirror/global.tsv":
			fmt.Fprint(w, globalTSV)
		}
	}))
	defer srv.Close()

	search := fakeTMDB()
	db := testDB(t)
	s := NewService(db, search, Config{
		CountriesURLs: []string{srv.URL + "/down/countries.tsv", srv.URL + "/mirror/countries.tsv"},
		GlobalURLs:    []string{srv.URL + "/down/global.tsv", srv.URL + "/mirror/global.tsv"},
	})
	// The global titles are unknown to the fake TMDB, so only that part may fail
	if err := s.Refresh(t.Context()); err != nil && !strings.Contains(err.Error(), "global") {
		t.Fatal(err)
	}

	films, ok := s.Get("TR", "movie")
	if !ok || films.Week != "2026-10-04" || films.Region != "TR" || films.Type != "movie" {
		t.Fatalf("films %+v", films)
	}
	var got []string
	for _, it := range films.Items {
		got = append(got, fmt.Sprintf("%d:%d", it.Rank, it.TmdbID))
	}
	// "Unknown Film" (rank 3) has no TMDB match: skipped, the ranks stay as Netflix published them
	if strings.Join(got, ",") != "1:11,2:12,4:13" || films.Items[0].ReleaseDate != "2026-09-01" {
		t.Fatalf("films %v %+v", got, films.Items[0])
	}

	tv, _ := s.Get("TR", "tv")
	got = nil
	for _, it := range tv.Items {
		got = append(got, fmt.Sprintf("%d:%d", it.Rank, it.TmdbID))
	}
	// The Mentalist season 2 (rank 4) collapses into season 1 (rank 2); Monster found via the colon prefix
	if strings.Join(got, ",") != "1:21,2:22,3:23" || tv.Items[0].FirstAirDate != "2026-09-10" || tv.Items[0].ReleaseDate != "" {
		t.Fatalf("tv %v %+v", got, tv.Items)
	}

	// Matches (and misses) are cached in the database: a second refresh does not search again
	calls := search.calls
	s.Refresh(t.Context())
	if search.calls != calls {
		t.Fatalf("second refresh searched %d more times", search.calls-calls)
	}

	// A restart serves the stored lists without downloading
	s2 := NewService(db, nil, Config{})
	s2.load()
	if l, ok := s2.Get("TR", "tv"); !ok || len(l.Items) != 3 {
		t.Fatalf("stored list %+v", l)
	}
}

func TestSearchPrefersFullTitle(t *testing.T) {
	search := &fakeSearcher{movies: map[string][]Match{
		"Demon Slayer: Infinity Castle I": {{TmdbID: 1, Title: "Demon Slayer: Infinity Castle", Popularity: 50}},
		"Demon Slayer":                    {{TmdbID: 2, Title: "Demon Slayer", Popularity: 5}},
		"Solo":                            {{TmdbID: 3, Title: "Solo"}},
	}}
	s := NewService(nil, search, Config{})
	m, ok, _ := s.searchTitle("movie", entry{Title: "Demon Slayer: Infinity Castle I"})
	if !ok || m.TmdbID != 1 {
		t.Fatalf("got %+v", m)
	}
	// The prefix is used only when the full title finds nothing
	m, ok, _ = s.searchTitle("movie", entry{Title: "Solo: Extended"})
	if !ok || m.TmdbID != 3 {
		t.Fatalf("prefix fallback %+v", m)
	}
}

func TestRegion(t *testing.T) {
	s := NewService(nil, nil, Config{})
	for in, want := range map[string]string{"tr": "TR", "TR": "TR", "Global": "global"} {
		if got, ok := s.Region(in); !ok || got != want {
			t.Errorf("Region(%q) = %q, %v", in, got, ok)
		}
	}
	if _, ok := s.Region("US"); ok {
		t.Error("US is not configured")
	}
}

type fakeLocalizer map[int]Localized

func (f fakeLocalizer) Localize(lang, mediaType string, id int) (Localized, bool) {
	l, ok := f[id]
	return l, ok && lang == "tr"
}

func TestGetInLocalizesWithoutChangingRanks(t *testing.T) {
	s := NewService(nil, nil, Config{Localizer: fakeLocalizer{
		11: {Title: "Başlangıç", Overview: "Türkçe", PosterPath: "/tr.jpg"},
	}})
	en := List{Region: Global, Week: "2026-10-04", Type: "movie", Items: []Item{
		{Rank: 1, TmdbID: 11, MediaType: "movie", Title: "Inception", Overview: "English", PosterPath: "/en.jpg", BackdropPath: "/b.jpg"},
		{Rank: 3, TmdbID: 12, MediaType: "movie", Title: "Unknown", Overview: "Kept"},
	}}
	s.lists[listKey{Global, "movie"}] = en

	tr, ok := s.GetIn(Global, "movie", "tr")
	if !ok || len(tr.Items) != 2 {
		t.Fatalf("GetIn: ok=%v items=%d", ok, len(tr.Items))
	}
	if it := tr.Items[0]; it.Rank != 1 || it.Title != "Başlangıç" || it.Overview != "Türkçe" || it.PosterPath != "/tr.jpg" || it.BackdropPath != "/b.jpg" {
		t.Errorf("localized item: %+v", it)
	}
	if it := tr.Items[1]; it.Rank != 3 || it.Title != "Unknown" || it.Overview != "Kept" {
		t.Errorf("item without translation: %+v", it)
	}
	// The stored English list is untouched
	if got, _ := s.GetIn(Global, "movie", "en"); got.Items[0].Title != "Inception" {
		t.Errorf("english list changed: %+v", got.Items[0])
	}
}
