package streaming

import (
	"encoding/json"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/gin-gonic/gin"

	"nextflix/internal/platform/tmdb"
)

func TestFoldAndNormalize(t *testing.T) {
	for in, want := range map[string]string{
		"Organize İşler":         "Organize Isler",
		"Organize İşler: Karun":  "Organize Isler: Karun",
		"ŞAHSİYET ığdır çÖü":     "SAHSIYET igdir cOu",
		"Amélie":                 "Amelie",
		"La Casa de Papel":       "La Casa de Papel",
		"Crème Brûlée à Ærøskøb": "Creme Brulee a AEroskob",
		"Łódź":                   "Lodz",
		"Straße":                 "Strasse",
	} {
		if got := tsFold(in); got != want {
			t.Errorf("tsFold(%q) = %q, want %q", in, got, want)
		}
	}
	if got := tsNormalize("Organize İşler"); !reflect.DeepEqual(got, []string{"organize", "isler"}) {
		t.Errorf("normalize: %q", got)
	}
	if got, want := tsNormalize("Organize İşler"), tsNormalize("Organize.Isler.S01E01.1080p")[:2]; !reflect.DeepEqual(got, want) {
		t.Errorf("%q != %q", got, want)
	}
	if got := tsNormalize("Bir Başkadır"); !reflect.DeepEqual(got, []string{"bir", "baskadir"}) {
		t.Errorf("normalize: %q", got)
	}
}

func TestOriginalTitleMatching(t *testing.T) {
	raw := []JackettResult{
		{Title: "Organize.Isler.S01E01.1080p.NF.WEB-DL.DDP5.1.x264-GRP", Seeders: 40, Size: 2 * tsGB, InfoHash: strings.Repeat("1", 40)},
		{Title: "Organize Isler Karun Hazinesi S01E01 720p WEB-DL x264", Seeders: 20, Size: tsGB, InfoHash: strings.Repeat("2", 40)},
		{Title: "Money Trap The Karun Treasure S01 1080p NF WEB-DL x264", Seeders: 15, Size: 12 * tsGB, InfoHash: strings.Repeat("3", 40)},
		{Title: "Money.Trap.S01E01.1080p.WEB.h264-GRP", Seeders: 10, Size: 2 * tsGB, InfoHash: strings.Repeat("4", 40)},
		// The 2005 film of the same name is not an episode
		{Title: "Organize Isler 2005 1080p NF WEB DL DDP5 1 H 264 SomniWare", Seeders: 6, Size: 6 * tsGB, InfoHash: strings.Repeat("5", 40)},
		{Title: "Organize Isler S01E02 1080p WEB-DL x264", Seeders: 30, Size: 2 * tsGB, InfoHash: strings.Repeat("6", 40)},
	}
	q := tsResolveQuery{mediaType: "tv", title: "Money Trap", season: 1, episode: 1}
	if got := tsRankAll(raw, q); len(got) != 1 || !strings.HasPrefix(got[0].raw.Title, "Money.Trap") {
		t.Fatalf("English title alone: %q", titles(got))
	}

	names := tsBuildNames("tv", "Money Trap", "Organize İşler: Karun Hazinesi", "tr", "",
		[]tmdb.AlternativeTitle{{Country: "DE", Title: "Geldfalle"}, {Country: "US", Title: "Money Trap: The Karun Treasure"}})
	q.names = names.names
	got := tsRankAll(raw, q)
	want := map[string]bool{strings.Repeat("1", 40): true, strings.Repeat("2", 40): true, strings.Repeat("3", 40): true, strings.Repeat("4", 40): true}
	if len(got) != len(want) {
		t.Fatalf("with other names: %q", titles(got))
	}
	for _, c := range got {
		if !want[c.hash] {
			t.Fatalf("unexpected candidate %q", c.raw.Title)
		}
	}
}

func TestBuildNames(t *testing.T) {
	n := tsBuildNames("tv", "Money Trap", "Organize İşler: Karun Hazinesi", "tr", "",
		[]tmdb.AlternativeTitle{{Country: "DE", Title: "Geldfalle"}, {Country: "US", Title: "Money Trap: The Karun Treasure"},
			{Country: "CN", Title: "有组织的工作"}, {Country: "GB", Title: "money trap"}})
	if !n.eager {
		t.Fatal("a Turkish original is searched right away")
	}
	wantNames := []string{"Organize İşler: Karun Hazinesi", "Organize İşler", "Money Trap: The Karun Treasure", "Geldfalle"}
	if !reflect.DeepEqual(n.names, wantNames) {
		t.Errorf("names %q, want %q", n.names, wantNames)
	}
	// The US title starts with the English title, so keyword search already finds it
	wantQueries := []string{"Organize İşler: Karun Hazinesi", "Organize Isler: Karun Hazinesi", "Organize Isler", "Geldfalle"}
	if !reflect.DeepEqual(n.queries, wantQueries) {
		t.Errorf("queries %q, want %q", n.queries, wantQueries)
	}
	q := tsResolveQuery{mediaType: "tv", title: "Money Trap", season: 1, episode: 1, extraQueries: n.queries[:3]}
	queries, cat := tsSearchQueries(q)
	if cat != "5000" || len(queries) != 8 || queries[2] != "Organize İşler: Karun Hazinesi S01E01" || queries[6] != "Organize Isler S01E01" {
		t.Errorf("search queries %q", queries)
	}

	// An English original only gets alternative titles, searched as a fallback
	n = tsBuildNames("movie", "Harry Potter and the Sorcerer's Stone", "Harry Potter and the Sorcerer's Stone", "en", "",
		[]tmdb.AlternativeTitle{{Country: "GB", Title: "Harry Potter and the Philosopher's Stone"}})
	if n.eager || !reflect.DeepEqual(n.queries, []string{"Harry Potter and the Philosopher's Stone"}) {
		t.Errorf("english original: %+v", n)
	}
	// Spider-Man keeps its hyphen; nothing new to search for
	if n = tsBuildNames("tv", "Spider-Man", "Spider-Man", "en", "", nil); len(n.names) != 0 || len(n.queries) != 0 {
		t.Errorf("same title: %+v", n)
	}
	if got := tsMainTitle("Spider-Man: Across the Spider-Verse"); got != "Spider-Man" {
		t.Errorf("main title %q", got)
	}
}

type fakeAvailability struct {
	a     TitleAvailability
	calls atomic.Int32
	args  [4]any
}

func (f *fakeAvailability) Availability(mediaType string, id, season, episode int) TitleAvailability {
	f.calls.Add(1)
	f.args = [4]any{mediaType, id, season, episode}
	return f.a
}

func TestResolveNotReleased(t *testing.T) {
	gin.SetMode(gin.TestMode)
	f := &fakeAvailability{a: TitleAvailability{Status: "not_released", ReleaseDate: "2026-10-16"}}
	s := NewService(nil, Config{Availability: f})
	r := gin.New()
	Register(r.Group("/api"), s)

	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest("GET", "/api/torrent-stream/resolve?type=tv&title=Money+Trap&season=1&episode=1&tmdb_id=320754", nil))
	if w.Code != 425 {
		t.Fatalf("resolve: %d %s", w.Code, w.Body.String())
	}
	var body map[string]any
	json.Unmarshal(w.Body.Bytes(), &body)
	if body["error"] != "not_released" || body["status"] != "not_released" || body["release_date"] != "2026-10-16" || body["digital_date"] != nil {
		t.Fatalf("body %v", body)
	}
	if f.args != [4]any{"tv", 320754, 1, 1} {
		t.Fatalf("checked %v", f.args)
	}

	// A film only in cinemas is not searched either
	f.a = TitleAvailability{Status: "in_theaters", DigitalDate: "2026-11-04", ReleaseDate: "2026-11-04"}
	w = httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest("GET", "/api/torrent-stream/resolve?type=movie&title=Dune&year=2026&tmdb_id=7", nil))
	json.Unmarshal(w.Body.Bytes(), &body)
	if w.Code != 425 || body["status"] != "in_theaters" || body["digital_date"] != "2026-11-04" {
		t.Fatalf("in theaters: %d %v", w.Code, body)
	}

	// Prefetch answers without starting a job or holding the title
	w = httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest("POST", "/api/torrent-stream/prefetch?type=movie&title=Dune&year=2026&tmdb_id=7", nil))
	json.Unmarshal(w.Body.Bytes(), &body)
	if w.Code != 202 || body["status"] != "not_released" || body["availability"] != "in_theaters" {
		t.Fatalf("prefetch: %d %v", w.Code, body)
	}
	key := tsResolveKey(tsResolveQuery{mediaType: "movie", title: "Dune", year: 2026})
	if s.flights.running(key) || len(s.warms) != 0 {
		t.Fatal("prefetch of an unreleased title started work")
	}
}

func TestNotOutNeverBlocksUnknown(t *testing.T) {
	f := &fakeAvailability{a: TitleAvailability{Status: "unknown"}}
	s := NewService(nil, Config{Availability: f})
	if _, blocked := s.notOut(tsResolveQuery{mediaType: "movie", title: "X", tmdbID: 1}); blocked {
		t.Fatal("unknown blocked")
	}
	f.a.Status = "available"
	if _, blocked := s.notOut(tsResolveQuery{mediaType: "movie", title: "X", tmdbID: 1}); blocked {
		t.Fatal("available blocked")
	}
	// Without a tmdb_id nothing is asked
	f.a.Status = "not_released"
	calls := f.calls.Load()
	if _, blocked := s.notOut(tsResolveQuery{mediaType: "movie", title: "X"}); blocked || f.calls.Load() != calls {
		t.Fatal("checked a title without tmdb_id")
	}
	if _, blocked := NewService(nil, Config{}).notOut(tsResolveQuery{mediaType: "movie", title: "X", tmdbID: 1}); blocked {
		t.Fatal("blocked without a checker")
	}
}

// titleTMDB answers English titles like the shared (en-US) TMDB client.
type titleTMDB struct{ calls int }

func (f *titleTMDB) GetMovieDetails(id int) (*tmdb.MovieDetails, error) {
	f.calls++
	return &tmdb.MovieDetails{Movie: tmdb.Movie{ID: id, Title: "Inception", OriginalTitle: "Inception"}}, nil
}

func (f *titleTMDB) GetTVDetails(id int) (*tmdb.TVDetails, error) {
	f.calls++
	return &tmdb.TVDetails{TV: tmdb.TV{ID: id, Name: "Money Heist", OriginalName: "La casa de papel"}}, nil
}

func (f *titleTMDB) GetSeasonDetails(int, int) (*tmdb.Season, error) { return &tmdb.Season{}, nil }

func TestEnglishTitleReplacesLocalizedTitle(t *testing.T) {
	f := &titleTMDB{}
	s := NewService(nil, Config{TMDB: f})

	q := tsResolveQuery{mediaType: "movie", title: "Başlangıç", year: 2010, tmdbID: 27205}
	s.englishTitle(&q)
	if q.title != "Inception" {
		t.Fatalf("movie title: %q", q.title)
	}
	tv := tsResolveQuery{mediaType: "tv", title: "La Casa De Papel", season: 1, episode: 1, tmdbID: 71446}
	s.englishTitle(&tv)
	if tv.title != "Money Heist" {
		t.Fatalf("tv title: %q", tv.title)
	}
	// Remembered: the same key for later prefetch/release calls without another lookup
	calls := f.calls
	again := tsResolveQuery{mediaType: "movie", title: "Başlangıç", year: 2010, tmdbID: 27205}
	s.englishTitle(&again)
	if f.calls != calls || tsResolveKey(again) != tsResolveKey(q) {
		t.Fatalf("second lookup: calls %d -> %d, keys %q / %q", calls, f.calls, tsResolveKey(again), tsResolveKey(q))
	}
	// Without a tmdb_id the client's title stays
	plain := tsResolveQuery{mediaType: "movie", title: "Başlangıç"}
	s.englishTitle(&plain)
	if plain.title != "Başlangıç" {
		t.Fatalf("title without tmdb_id changed: %q", plain.title)
	}
}

// airTMDB answers shows with a first-air date.
type airTMDB struct{ dates map[int]string }

func (f airTMDB) GetMovieDetails(id int) (*tmdb.MovieDetails, error) {
	return &tmdb.MovieDetails{Movie: tmdb.Movie{ID: id, Title: "Film"}}, nil
}

func (f airTMDB) GetTVDetails(id int) (*tmdb.TVDetails, error) {
	return &tmdb.TVDetails{TV: tmdb.TV{ID: id, Name: "Doctor Who", FirstAirDate: f.dates[id]}}, nil
}

func (f airTMDB) GetSeasonDetails(int, int) (*tmdb.Season, error) { return &tmdb.Season{}, nil }

func TestEnglishTitleSetsFirstAirYear(t *testing.T) {
	s := NewService(nil, Config{TMDB: airTMDB{dates: map[int]string{57243: "2005-03-26", 239770: "2023-11-25", 1: ""}}})
	cases := []struct {
		id, year, want int
	}{
		{57243, 0, 2005},
		{239770, 0, 2023},
		{1, 0, 0},           // no date: unknown
		{57243, 2010, 2005}, // the client's year stays (cache key), firstAir is TMDB's
	}
	for _, tc := range cases {
		q := tsResolveQuery{mediaType: "tv", title: "Doctor Who", year: tc.year, season: 1, episode: 1, tmdbID: tc.id}
		s.englishTitle(&q)
		if q.firstAir != tc.want || q.year != tc.year {
			t.Errorf("tmdb %d: firstAir %d year %d, want %d / %d", tc.id, q.firstAir, q.year, tc.want, tc.year)
		}
	}
	// Movies do not get one
	m := tsResolveQuery{mediaType: "movie", title: "Film", tmdbID: 5}
	s.englishTitle(&m)
	if m.firstAir != 0 {
		t.Errorf("movie firstAir %d", m.firstAir)
	}
}
