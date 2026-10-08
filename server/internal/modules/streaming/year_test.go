package streaming

import (
	"errors"
	"testing"

	"nextflix/internal/platform/tmdb"
)

const (
	notld2026   = "Night.Of.The.Living.Dead.2026.XviD.WEB-DLRip.avi"
	notld1990   = "Night of the Living Dead 1990 Uncut 2160p UHD BluRay x265 10bit DV HDR TrueHD Atmos 7 1 r00t-QxR"
	notldRemast = "Night.of.the.Living.Dead.1968.REMASTERED.1080p.BluRay.X264-AMIABLE"
	notldYTS    = "Night Of The Living Dead (1968) [BluRay] [1080p] [YTS.AM]"
)

// yearTMDB knows Night of the Living Dead (1968).
type yearTMDB struct{}

func (yearTMDB) GetMovieDetails(id int) (*tmdb.MovieDetails, error) {
	if id != 10331 {
		return nil, errors.New("not found")
	}
	return &tmdb.MovieDetails{Movie: tmdb.Movie{ID: id, Title: "Night of the Living Dead", ReleaseDate: "1968-10-04"}, Runtime: 96}, nil
}
func (yearTMDB) GetTVDetails(int) (*tmdb.TVDetails, error)       { return nil, errors.New("not found") }
func (yearTMDB) GetSeasonDetails(int, int) (*tmdb.Season, error) { return nil, errors.New("not found") }

func notldResults() []JackettResult {
	r := func(title string, size int64) JackettResult {
		return JackettResult{Title: title, Seeders: 20, Size: size, Link: "http://jackett/dl/" + title}
	}
	return []JackettResult{
		r(notld2026, 1400<<20),
		r(notld1990, 20*tsGB),
		r(notldRemast, 10*tsGB),
		r(notldYTS, 2*tsGB),
		r("Night of the Living Dead 1969 1080p BluRay x264", 3*tsGB),
		r("Night of the Living Dead 1080p BluRay x264-GRP", 3*tsGB),
	}
}

func TestScoreRejectsAnotherYear(t *testing.T) {
	q := tsResolveQuery{mediaType: "movie", title: "Night of the Living Dead", year: 1968, hevc: true}
	tokens := tsNormalize(q.title)
	want := map[string]bool{notld2026: false, notld1990: false, notldRemast: true, notldYTS: true,
		"Night of the Living Dead 1969 1080p BluRay x264": true, "Night of the Living Dead 1080p BluRay x264-GRP": true}
	for _, r := range notldResults() {
		if _, ok := tsScore(r, q, tokens); ok != want[r.Title] {
			t.Errorf("%q: accepted %v, want %v", r.Title, ok, want[r.Title])
		}
	}
}

// Clients send no year (resumes, upgrade polls) or a wrong one: TMDB's decides
func TestTMDBYearRejectsRemakesOnEveryPath(t *testing.T) {
	s := NewService(nil, Config{TMDB: yearTMDB{}})
	for _, clientYear := range []int{0, 2026} {
		q := tsResolveQuery{mediaType: "movie", title: "Night of the Living Dead", year: clientYear, tmdbID: 10331, hevc: true}
		s.englishTitle(&q)
		if q.year != 1968 {
			t.Fatalf("client year %d: query year %d, want 1968", clientYear, q.year)
		}
		for _, c := range tsRankAll(notldResults(), q) {
			if c.raw.Title == notld2026 || c.raw.Title == notld1990 {
				t.Errorf("client year %d: ranked %q", clientYear, c.raw.Title)
			}
		}
		// Upgrade search (above 1080p) and late results: the 1990 4K remake is not an upgrade
		up := q
		up.upgradeFrom = 1080
		if got := tsRankAll(notldResults(), up); len(got) != 0 {
			t.Errorf("client year %d: upgrade candidates %q", clientYear, got[0].raw.Title)
		}
		pick := tsCandidate{raw: JackettResult{Title: notldYTS}, score: -1000}
		for _, c := range tsLateBetter(notldResults(), q, nil, pick) {
			if c.raw.Title == notld2026 || c.raw.Title == notld1990 {
				t.Errorf("client year %d: late candidate %q", clientYear, c.raw.Title)
			}
		}
	}
	// A title TMDB does not date keeps the client's year
	other := tsResolveQuery{mediaType: "movie", title: "Something", year: 2001, tmdbID: 1}
	s.englishTitle(&other)
	if other.year != 2001 {
		t.Fatalf("unknown tmdb id: year %d", other.year)
	}
}

func TestDateYear(t *testing.T) {
	for in, want := range map[string]int{"1968-10-04": 1968, "2026": 2026, "": 0, "abcd-01-01": 0, "0001-01-01": 0} {
		if got := tsDateYear(in); got != want {
			t.Errorf("%q: %d, want %d", in, got, want)
		}
	}
}
