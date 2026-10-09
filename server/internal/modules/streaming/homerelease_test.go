package streaming

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/gin-gonic/gin"

	"nextflix/internal/platform/tmdb"
)

func odysseyRelease(i int, name string, seeders int, gb float64) JackettResult {
	return JackettResult{Title: name, Seeders: seeders, Size: int64(gb * float64(tsGB)),
		MagnetUri: fmt.Sprintf("magnet:?xt=urn:btih:%040x", 0x0d7553e0+i)}
}

// Live Jackett names for "The Odyssey 2026" on 2026-10-09 (TMDB still listed it as in cinemas)
var (
	odysseyHome = []string{
		"The Odyssey 2026 1080p AMZN WEB-DL DDP5 1 H 264-Kitsune",
		"The Odyssey (2026) (1080p AMZN WEB-DL x265 10bit EAC3 5.1 Celdra) [QxR]",
		"The Odyssey (2026) 1080p WEBRip 5.1-LAMA",
		"The Odyssey 2026 1080p web YTS",
		"The Odyssey 2026 720p web YTS",
	}
	odysseyCinema = []string{
		"The.Odyssey.2026.1080p.TELESYNC.HEVC.AAC2.0-SPLiCE",
		"The Odyssey (2026) 1080P HQ HDTS x265 AAC 2.0-MAZE",
	}
	odysseyTraps = []string{
		"The Odyssey The Making Of An Epic (2026) 1080p WEBRip 5.1-LAMA",
		"The Odyssey: The Making of an Epic 2026 1080p web YTS",
		"The.Odyssey.2026.720p.TUBI.WEB-DL.AAC2.0-NOtLAN (NOT The Chris Nolan FILM)",
	}
)

func odysseyResults() []JackettResult {
	sizes := map[string]float64{odysseyHome[0]: 4.7, odysseyHome[1]: 3.9, odysseyHome[2]: 3.4, odysseyHome[3]: 3.3, odysseyHome[4]: 1.6,
		odysseyCinema[0]: 3.1, odysseyCinema[1]: 2.2, odysseyTraps[0]: 1.2, odysseyTraps[1]: 1.6, odysseyTraps[2]: 1.4}
	seeders := map[string]int{odysseyHome[0]: 5093, odysseyHome[1]: 3679, odysseyHome[2]: 1919, odysseyHome[3]: 900, odysseyHome[4]: 600,
		odysseyCinema[0]: 16349, odysseyCinema[1]: 2400, odysseyTraps[0]: 800, odysseyTraps[1]: 700, odysseyTraps[2]: 300}
	var out []JackettResult
	for i, n := range append(append(append([]string{}, odysseyCinema...), odysseyTraps...), odysseyHome...) {
		out = append(out, odysseyRelease(i, n, seeders[n], sizes[n]))
	}
	return out
}

func odysseyQuery() tsResolveQuery {
	return tsResolveQuery{mediaType: "movie", title: "The Odyssey", year: 2026, runtime: 173, tmdbID: 1368337,
		directors: [][]string{tsNormalize("Christopher Nolan")}}
}

func TestOdysseyRankingSkipsTrapsAndCinemaRips(t *testing.T) {
	q := odysseyQuery()
	raw := odysseyResults()
	// An IMDb id search may return the making-of without naming its id: scored by its own name
	for _, n := range odysseyTraps[:2] {
		r := odysseyRelease(99, n, 500, 1.5)
		r.IMDbMatch = true
		raw = append(raw, r)
	}
	for _, hevc := range []bool{false, true} {
		q.hevc = hevc
		got := titles(tsRankAll(raw, q))
		if len(got) != len(odysseyHome) {
			t.Fatalf("hevc=%v: ranked %q", hevc, got)
		}
		for _, n := range got {
			if !strings.Contains(strings.Join(odysseyHome, "|"), n) {
				t.Fatalf("hevc=%v: ranked %q", hevc, n)
			}
		}
		if !strings.Contains(got[0], "AMZN WEB-DL") {
			t.Errorf("hevc=%v: best %q", hevc, got[0])
		}
		if home := tsHomeCandidates(raw, q); len(home) != len(odysseyHome) {
			t.Errorf("hevc=%v: home releases %q", hevc, titles(home))
		}
	}

	if !tsHomeFound(tsHomeCandidates(raw, q)) {
		t.Fatal("no home release found")
	}

	// Only the cinema recordings: no home release
	if home := tsHomeCandidates(odysseyResults()[:len(odysseyCinema)+len(odysseyTraps)], q); len(home) != 0 {
		t.Fatalf("cinema only: %q", titles(home))
	}
}

// Live names for "Resident Evil 2026" three weeks into its cinema run: one uploader's WEB-DL
// (a likely fake), a thin "AMZN WEB-DL" of 0.9 GB, and another film whose name ends in the
// title. One group alone is not a home release.
func TestHomeReleaseNeedsTwoGroups(t *testing.T) {
	q := tsResolveQuery{mediaType: "movie", title: "Resident Evil", year: 2026, tmdbID: 1423191}
	raw := []JackettResult{
		odysseyRelease(1, "Resident Evil 2026.1080p.WEB-DL.AAC.x264-rothebehap", 4581, 3.7),
		odysseyRelease(2, "Resident Evil (2026) 1080p AMZN WEB DL DDP5 1 H 264 FLUX", 5, 0.9),
		odysseyRelease(3, "George A. Romero's Resident Evil (2025) 1080p BRRip 5.1 x264 -YTS", 15, 2.1),
		odysseyRelease(4, "George.A.Romeros.Resident.Evil.2025.1080p.WEB.h264-EDITH", 10, 9.1),
		odysseyRelease(5, "Resident.Evil.2026.1080p.TELESYNC.x264-SPLiCE", 9000, 3.0),
	}
	home := tsHomeCandidates(raw, q)
	if len(home) != 1 || tsHomeFound(home) {
		t.Fatalf("home releases %q", titles(home))
	}
	// A second group makes it one
	raw = append(raw, odysseyRelease(6, "Resident Evil (2026) 1080p WEBRip 5.1-LAMA", 800, 3.0))
	if !tsHomeFound(tsHomeCandidates(raw, q)) {
		t.Fatal("two groups not found")
	}
}

func TestReleaseNameTraps(t *testing.T) {
	q := odysseyQuery()
	rejected := func(name string, q tsResolveQuery) bool {
		_, ok := tsScore(JackettResult{Title: name, Seeders: 50, Size: 3 * tsGB, MagnetUri: "magnet:?xt=urn:btih:" + instantHash(1)}, q, tsNormalize(q.title))
		return !ok
	}
	for _, n := range append(append([]string{}, odysseyTraps...), odysseyCinema...) {
		if !rejected(n, q) {
			t.Errorf("accepted %q", n)
		}
	}
	for _, n := range []string{
		"The Odyssey 2026 Making-of 1080p WEB-DL",
		"The Odyssey 2026 Behind the Scenes 1080p WEB",
		"The Odyssey 2026 Featurette 1080p WEB-DL",
		"The Odyssey 2026 1080p WEB-DL Extras",
		"The Odyssey 2026 1080p BluRay Bonus Disc",
		"The Odyssey 2026 Official Trailer 1080p",
		"The Odyssey 2026 1080p WEB-DL Sample",
		"The Odyssey 2026 Cast Interview 1080p WEB",
		"The Odyssey 2026 Documentary 1080p WEB",
		"The Odyssey 2026 1080p WEB-DL (not the film)",
		// Live, ranked first before (4K trailer of a Russian tracker, IMAX prologue)
		"Одиссея / The Odyssey [2026, фэнтези, боевик, приключения, ProRes encode, DTS-HD MA/AC-3 5.1 ENG] Трейлер #2 (4K/IMAX4K)",
		"THE ODYSSEY ( 2026) Official IMAX® Prologue (1.90:1) [4K Ultra HD]",
	} {
		if !rejected(n, q) {
			t.Errorf("accepted %q", n)
		}
	}
	for _, n := range odysseyHome {
		if rejected(n, q) {
			t.Errorf("rejected %q", n)
		}
	}
	// Directors unknown: a disclaimer is never taken
	noDir := q
	noDir.directors = nil
	if !rejected(odysseyTraps[2], noDir) {
		t.Error("disclaimer accepted without directors")
	}
	// The namesake (another director) keeps its own release
	other := tsResolveQuery{mediaType: "movie", title: "The Odyssey", year: 2026, tmdbID: 1698863,
		directors: [][]string{tsNormalize("Someone Else")}}
	if rejected(odysseyTraps[2], other) {
		t.Error("the namesake's own release was rejected")
	}
	// Words of the title itself are fine
	for _, c := range []struct{ title, name string }{
		{"The Interview", "The Interview 2014 1080p BluRay x264"},
		{"Trailer Park Boys", "Trailer Park Boys S01E01 1080p WEB"},
		{"This Is Not a Film", "This Is Not a Film 2011 1080p WEB-DL"},
		{"The Making of a Legend", "The Making of a Legend 1989 1080p WEB"},
	} {
		tq := tsResolveQuery{mediaType: "movie", title: c.title}
		if strings.Contains(c.name, " S01E01") {
			tq = tsResolveQuery{mediaType: "tv", title: c.title, season: 1, episode: 1}
		}
		if _, ok := tsScore(JackettResult{Title: c.name, Seeders: 50, Size: 2 * tsGB, MagnetUri: "magnet:?xt=urn:btih:" + instantHash(2)}, tq, tsNormalize(c.title)); !ok {
			t.Errorf("%q rejected for %q", c.name, c.title)
		}
	}
}

// odysseyTMDB answers like TMDB for the two films named "The Odyssey".
type odysseyTMDB struct{}

func (odysseyTMDB) GetMovieDetails(id int) (*tmdb.MovieDetails, error) {
	d := &tmdb.MovieDetails{Movie: tmdb.Movie{ID: id, Title: "The Odyssey", OriginalTitle: "The Odyssey", ReleaseDate: "2026-07-15",
		OriginalLanguage: "en"}, Runtime: 173}
	d.Credits.Crew = []tmdb.Crew{{Name: "Christopher Nolan", Job: "Director"}, {Name: "Hoyte van Hoytema", Job: "Director of Photography"}}
	return d, nil
}
func (odysseyTMDB) GetTVDetails(int) (*tmdb.TVDetails, error)       { return nil, fmt.Errorf("no tv") }
func (odysseyTMDB) GetSeasonDetails(int, int) (*tmdb.Season, error) { return nil, fmt.Errorf("no tv") }

// fakeIndexer is a Jackett with one indexer answering results (switchable).
func fakeIndexer(t *testing.T, results *atomic.Pointer[[]JackettResult], hits *atomic.Int32) *Service {
	t.Helper()
	jackett := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Query().Get("t") == "indexers":
			w.Write([]byte(`<indexers>
<indexer id="tpb" configured="true"><caps><searching><movie-search available="yes" supportedParams="q" /></searching><categories><category id="2000"/></categories></caps></indexer>
</indexers>`))
		case strings.HasSuffix(r.URL.Path, "/results"):
			hits.Add(1)
			json.NewEncoder(w).Encode(JackettResponse{Results: *results.Load()})
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(jackett.Close)
	db := memDB(t)
	if err := db.Create(&IndexerSettings{JackettURL: jackett.URL, JackettAPIKey: "k"}).Error; err != nil {
		t.Fatal(err)
	}
	return NewService(db, Config{JackettURL: jackett.URL, JackettAPIKey: "k", TMDB: odysseyTMDB{}, TorrServerURL: "http://127.0.0.1:1"})
}

func TestHomeReleaseOutSearchesAndCaches(t *testing.T) {
	var results atomic.Pointer[[]JackettResult]
	var hits atomic.Int32
	all := odysseyResults()
	results.Store(&all)
	s := fakeIndexer(t, &results, &hits)

	// Concurrent callers share one search
	var wg sync.WaitGroup
	found := make([]bool, 4)
	for i := range found {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			found[i] = s.HomeReleaseOut(1368337)
		}(i)
	}
	wg.Wait()
	for i, f := range found {
		if !f {
			t.Fatalf("caller %d: no home release", i)
		}
	}
	n := hits.Load()
	if !s.HomeReleaseOut(1368337) || hits.Load() != n {
		t.Fatalf("not answered from the cache (%d -> %d searches)", n, hits.Load())
	}

	// Another film with cinema recordings only
	cinema := all[:len(odysseyCinema)+len(odysseyTraps)]
	results.Store(&cinema)
	if s.HomeReleaseOut(7) {
		t.Fatal("cinema recordings counted as a home release")
	}
}

// With a home release found the catalog reports the film as available, so resolve searches
// and dry=1 lists the home releases; with cinema recordings only it stays 425.
func TestDryResolveFollowsTheVerdict(t *testing.T) {
	gin.SetMode(gin.TestMode)
	var results atomic.Pointer[[]JackettResult]
	var hits atomic.Int32
	all := odysseyResults()
	results.Store(&all)
	s := fakeIndexer(t, &results, &hits)
	f := &fakeAvailability{a: TitleAvailability{Status: "available"}}
	s.cfg.Availability = f
	r := gin.New()
	Register(r.Group("/api"), s)

	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest("GET", "/api/torrent-stream/resolve?type=movie&title=Odyssey&year=2026&tmdb_id=1368337&dry=1", nil))
	var body struct {
		Title      string `json:"title"`
		Candidates []struct {
			Title  string `json:"title"`
			Source string `json:"source"`
		} `json:"candidates"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &body); w.Code != 200 || err != nil {
		t.Fatalf("dry: %d %s", w.Code, w.Body.String())
	}
	if body.Title != "The Odyssey" || len(body.Candidates) != len(odysseyHome) || !strings.Contains(body.Candidates[0].Title, "AMZN WEB-DL") {
		t.Fatalf("dry: %+v", body)
	}
	if len(s.warms) != 0 {
		t.Fatal("a dry resolve held the title")
	}

	f.a = TitleAvailability{Status: "in_theaters", ReleaseDate: "2026-11-17", DigitalDate: "2026-11-17"}
	w = httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest("GET", "/api/torrent-stream/resolve?type=movie&title=X&year=2026&tmdb_id=7&dry=1", nil))
	if w.Code != 425 {
		t.Fatalf("in theaters: %d %s", w.Code, w.Body.String())
	}
}
