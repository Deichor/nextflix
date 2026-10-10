package streaming

import (
	"context"
	"errors"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

func ok(name string) *tsResolveResult { return &tsResolveResult{Hash: name} }

// check returns a fake candidate check: candidate i waits delays[i], then succeeds when good[i]
func fakeCheck(delays []time.Duration, good []bool) func(context.Context, int) (*tsResolveResult, error) {
	return func(ctx context.Context, i int) (*tsResolveResult, error) {
		select {
		case <-time.After(delays[i]):
		case <-ctx.Done():
			return nil, ctx.Err()
		}
		if good[i] {
			return ok(string(rune('a' + i))), nil
		}
		return nil, errors.New("bad")
	}
}

func TestPickBestPrefersRankAndExitsEarly(t *testing.T) {
	ms := time.Millisecond
	start := time.Now()
	// #0 fails fast, #1 works at 30ms, #2 works at 5ms, #3 is very slow
	i, res, _ := tsPickBest(context.Background(), 4, time.Second,
		fakeCheck([]time.Duration{5 * ms, 30 * ms, 5 * ms, 5 * time.Second}, []bool{false, true, true, true}))
	if i != 1 || res.Hash != "b" {
		t.Fatalf("got %d %v, want candidate 1", i, res)
	}
	if d := time.Since(start); d > 500*ms {
		t.Fatalf("waited %s for slower, worse candidates", d)
	}
}

func TestPickBestGraceForSlowBetterCandidate(t *testing.T) {
	ms := time.Millisecond
	start := time.Now()
	// #0 hangs, #1 works quickly: return #1 after the grace period, not after #0's timeout
	i, _, _ := tsPickBest(context.Background(), 2, 50*ms,
		fakeCheck([]time.Duration{5 * time.Second, 5 * ms}, []bool{true, true}))
	if i != 1 {
		t.Fatalf("got %d, want 1", i)
	}
	if d := time.Since(start); d > time.Second {
		t.Fatalf("grace did not cut the wait: %s", d)
	}

	// #0 finishes inside the grace period and wins
	i, _, _ = tsPickBest(context.Background(), 2, time.Second,
		fakeCheck([]time.Duration{40 * ms, 5 * ms}, []bool{true, true}))
	if i != 0 {
		t.Fatalf("got %d, want 0", i)
	}
}

func TestPickBestAllFail(t *testing.T) {
	i, res, errs := tsPickBest(context.Background(), 3, time.Second,
		fakeCheck([]time.Duration{0, 0, 0}, []bool{false, false, false}))
	if i != -1 || res != nil {
		t.Fatalf("got %d %v, want none", i, res)
	}
	for j, err := range errs {
		if err == nil {
			t.Fatalf("missing error for %d", j)
		}
	}
}

func TestFlightGroupDedupes(t *testing.T) {
	var g tsFlightGroup
	var runs int32
	release := make(chan struct{})
	fn := func() (*tsResolveResult, error) {
		atomic.AddInt32(&runs, 1)
		<-release
		return ok("x"), nil
	}
	f1, s1 := g.start("k", fn)
	f2, s2 := g.start("k", fn)
	if !s1 || s2 || f1 != f2 || !g.running("k") {
		t.Fatalf("second start should join the first (started %v %v)", s1, s2)
	}
	close(release)
	<-f2.done
	if f2.res == nil || f2.res.Hash != "x" || atomic.LoadInt32(&runs) != 1 {
		t.Fatalf("runs=%d res=%v", runs, f2.res)
	}
	if g.running("k") {
		t.Fatal("job still registered after it finished")
	}
}

func TestSourceCachePersists(t *testing.T) {
	db, err := gorm.Open(sqlite.Open("file::memory:"), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	if sqlDB, err := db.DB(); err == nil {
		sqlDB.SetMaxOpenConns(1) // one in-memory database
	}
	if err := db.AutoMigrate(Models()...); err != nil {
		t.Fatal(err)
	}

	hash := "0123456789abcdef0123456789abcdef01234567"
	key := tsResolveKey(tsResolveQuery{mediaType: "movie", title: "The Matrix", year: 1999})
	s1 := NewService(db, Config{})
	s1.storeSource(key, tsResolveResult{Hash: hash, Index: 2, TorrentTitle: "The.Matrix.1999.1080p"})
	s1.storeSource(key, tsResolveResult{Hash: hash, Index: 3}) // upsert

	// A new service (restart) reads it back from the table
	s2 := NewService(db, Config{})
	res, found := s2.cachedSource(key)
	if !found || res.Hash != hash || res.Index != 3 {
		t.Fatalf("cached source not restored: %v %v", found, res)
	}

	// Expired rows are ignored and removed
	db.Model(&StreamSource{}).Where("source_key = ?", key).Update("resolved_at", time.Now().Add(-8*24*time.Hour))
	s3 := NewService(db, Config{})
	if _, found := s3.cachedSource(key); found {
		t.Fatal("expired source was returned")
	}
	var n int64
	db.Model(&StreamSource{}).Count(&n)
	if n != 0 {
		t.Fatalf("expired row kept, %d rows", n)
	}

	s3.storeSource(key, tsResolveResult{Hash: hash})
	s3.forgetSource(key)
	if _, found := NewService(db, Config{}).cachedSource(key); found {
		t.Fatal("forgotten source still cached")
	}
}

func TestRankCandidatesAvoidsFailedHash(t *testing.T) {
	good := "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	other := "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
	raw := []JackettResult{
		{Title: "Inception 2010 1080p BluRay x264", Seeders: 500, Size: 2 * tsGB, InfoHash: good},
		{Title: "Inception 2010 720p BluRay x264", Seeders: 50, Size: tsGB, InfoHash: other},
	}
	q := tsResolveQuery{mediaType: "movie", title: "Inception", year: 2010}
	if c := tsRankCandidates(raw, q); len(c) != 2 || c[0].hash != good {
		t.Fatalf("unexpected ranking %v", c)
	}
	q.avoid = []string{good}
	if c := tsRankCandidates(raw, q); c[0].hash != other || c[1].hash != good {
		t.Fatalf("failed hash not ranked last: %s %s", c[0].hash, c[1].hash)
	}
}

func TestRankCandidatesAvoidList(t *testing.T) {
	// The Odyssey: two wrong-length picks in a row are both skipped
	first, second, third := strings.Repeat("a", 40), strings.Repeat("b", 40), strings.Repeat("c", 40)
	raw := []JackettResult{
		{Title: "The Odyssey 2026 1080p WEBRip x265 10bit LAMA", Seeders: 900, Size: 3 * tsGB, InfoHash: first},
		{Title: "The Odyssey 2026 1080p WEB-DL x264", Seeders: 600, Size: 4 * tsGB, InfoHash: second},
		{Title: "The Odyssey 2026 720p WEBRip x264", Seeders: 100, Size: 2 * tsGB, InfoHash: strings.ToUpper(third)},
	}
	q := tsResolveQuery{mediaType: "movie", title: "The Odyssey", year: 2026, avoid: []string{first, strings.ToUpper(second)}}
	c := tsRankCandidates(raw, q)
	if len(c) != 3 || c[0].hash != third {
		t.Fatalf("avoided hashes not ranked last: %q", titles(c))
	}
	for _, cand := range c[1:] {
		if cand.hash != first && cand.hash != second {
			t.Fatalf("unexpected order: %q", titles(c))
		}
	}
}

func TestParseResolveAvoid(t *testing.T) {
	gin.SetMode(gin.TestMode)
	a, b := strings.Repeat("a", 40), strings.Repeat("B", 40)
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest("GET", "/?type=movie&title=Dune&avoid="+a+",nothex,"+b, nil)
	q, ok := parseResolveQuery(c)
	if !ok || len(q.avoid) != 2 || q.avoid[0] != a || q.avoid[1] != strings.ToLower(b) {
		t.Fatalf("avoid list %v (ok %v)", q.avoid, ok)
	}
}

func TestPickBestSurvivesPanickingCheck(t *testing.T) {
	cases := []struct {
		name   string
		panics []bool
		want   int
	}{
		{"best panics", []bool{true, false}, 1},
		{"all panic", []bool{true, true}, -1},
		{"none panics", []bool{false, false}, 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			i, _, errs := tsPickBest(context.Background(), len(tc.panics), 50*time.Millisecond,
				func(_ context.Context, i int) (*tsResolveResult, error) {
					if tc.panics[i] {
						panic("boom")
					}
					return ok(string(rune('a' + i))), nil
				})
			if i != tc.want {
				t.Fatalf("picked %d, want %d", i, tc.want)
			}
			for j, p := range tc.panics {
				if p && (errs[j] == nil || !strings.Contains(errs[j].Error(), "panicked")) {
					t.Fatalf("check %d: error %v, want the panic", j, errs[j])
				}
			}
		})
	}
}

func TestScoreTVYearAfterName(t *testing.T) {
	cases := []struct {
		title, release string
		firstAir       int
		want           bool
	}{
		{"Doctor Who", "Doctor.Who.2023.S01E01.1080p.WEB-DL.x264", 2005, false},
		{"Doctor Who", "Doctor.Who.2005.S01E01.1080p.WEB-DL.x264", 2005, true},
		{"Doctor Who", "Doctor.Who.S01E01.1080p.WEB-DL.x264", 2005, true},   // undated
		{"Doctor Who", "Doctor.Who.2023.S01E01.1080p.WEB-DL.x264", 0, true}, // first-air year unknown
		{"Shogun", "Shogun.1980.S01E01.1080p.BluRay.x264", 2024, false},
		{"Shogun", "Shogun.2024.S01E01.1080p.WEB-DL.x264", 2024, true},
		{"Shogun", "Shogun.2023.S01E01.1080p.WEB-DL.x264", 2024, true}, // +-1
		{"The Office", "The.Office.US.2005.S01E01.1080p.WEB-DL.x264", 2005, true},
		{"The Office", "The.Office.UK.2001.S01E01.1080p.WEB-DL.x264", 2005, false},
	}
	for _, tc := range cases {
		q := tsResolveQuery{mediaType: "tv", title: tc.title, firstAir: tc.firstAir, season: 1, episode: 1}
		_, got := tsScore(JackettResult{Title: tc.release, Seeders: 50, Size: tsGB, InfoHash: strings.Repeat("a", 40)}, q, tsNormalize(tc.title))
		if got != tc.want {
			t.Errorf("%q (first aired %d): accepted %v, want %v", tc.release, tc.firstAir, got, tc.want)
		}
	}
	// A daily show's date is an air date, not the show's year
	q := tsResolveQuery{mediaType: "tv", title: "The Daily Show", firstAir: 1996, season: 29, episode: 120}
	r := JackettResult{Title: "The.Daily.Show.2024.10.15.S29E120.1080p.WEB.x264", Seeders: 50, Size: tsGB, InfoHash: strings.Repeat("a", 40)}
	if _, ok := tsScore(r, q, tsNormalize(q.title)); !ok {
		t.Error("dated daily show rejected by its air year")
	}
}

func TestScoreTVEpisodeTitleWords(t *testing.T) {
	cases := []struct {
		release string
		want    bool
	}{
		{"Show.S02E05.The.Line.1080p.WEB-DL.x264", true},
		{"Show.S02E05.Prologue.1080p.WEB-DL.x264", true},
		{"Show.S02E05.The.Interview.720p.HDTV.x264", true},
		{"Show.S02E05.Bonus.Documentary.1080p.WEB-DL.x264", true},
		{"Show.S02E05.Recap.1080p.WEB-DL.x264", false}, // never an episode title
		{"Show.S02E05.Behind.the.Scenes.1080p.WEB-DL.x264", false},
		{"Show.S02E05.1080p.HDTS.x264", false},                  // source after the release tag
		{"Show.S02E05.HD-TS.720p.x264", false},                  // the words are only a source tag
		{"Show.S02E05.TS.1080p.x264", false},                    // ...
		{"Show.S02E05.CAM", false},                              // no release tag: every word counts
		{"Show.S02E05.The.Line.1080p.WEB-DL.Line.Audio", false}, // after the release tag
		{"Show.S02.Bonus.Features.1080p.WEB-DL.x264", false},    // a season's bonus material
		{"Show.S02E05.1080p.WEB-DL.x264.Featurette", false},
	}
	q := tsResolveQuery{mediaType: "tv", title: "Show", season: 2, episode: 5}
	for _, tc := range cases {
		_, got := tsScore(JackettResult{Title: tc.release, Seeders: 50, Size: tsGB, InfoHash: strings.Repeat("a", 40)}, q, tsNormalize(q.title))
		if got != tc.want {
			t.Errorf("%q: accepted %v, want %v", tc.release, got, tc.want)
		}
	}
	// Movies are unchanged: a cam stays rejected
	m := tsResolveQuery{mediaType: "movie", title: "Dune", year: 2021}
	if _, ok := tsScore(JackettResult{Title: "Dune.2021.HDCAM.x264", Seeders: 50, Size: tsGB, InfoHash: strings.Repeat("a", 40)}, m, tsNormalize("Dune")); ok {
		t.Error("movie cam accepted")
	}
}

func TestPickMovieFileInCollections(t *testing.T) {
	gb := tsGB
	cases := []struct {
		name  string
		q     tsResolveQuery
		files []tsFileStat
		want  string // "" = rejected
	}{
		{"single film with extras",
			tsResolveQuery{mediaType: "movie", title: "The Matrix", year: 1999},
			[]tsFileStat{{ID: 1, Path: "M/xyz-1080p.mkv", Length: 8 * gb}, {ID: 2, Path: "M/Featurettes/making.mkv", Length: gb}},
			"M/xyz-1080p.mkv"},
		{"trilogy by file name",
			tsResolveQuery{mediaType: "movie", title: "The Matrix", year: 1999},
			[]tsFileStat{
				{ID: 1, Path: "The Matrix 1999 2003 Trilogy/The.Matrix.Reloaded.2003.1080p.mkv", Length: 9 * gb},
				{ID: 2, Path: "The Matrix 1999 2003 Trilogy/The.Matrix.1999.1080p.mkv", Length: 8 * gb},
				{ID: 3, Path: "The Matrix 1999 2003 Trilogy/The.Matrix.Revolutions.2003.1080p.mkv", Length: 7 * gb},
			},
			"The Matrix 1999 2003 Trilogy/The.Matrix.1999.1080p.mkv"},
		{"trilogy sequel by file name",
			tsResolveQuery{mediaType: "movie", title: "The Matrix Revolutions", year: 2003},
			[]tsFileStat{
				{ID: 1, Path: "Trilogy/The.Matrix.Reloaded.2003.1080p.mkv", Length: 9 * gb},
				{ID: 2, Path: "Trilogy/The.Matrix.1999.1080p.mkv", Length: 8 * gb},
				{ID: 3, Path: "Trilogy/The.Matrix.Revolutions.2003.1080p.mkv", Length: 7 * gb},
			},
			"Trilogy/The.Matrix.Revolutions.2003.1080p.mkv"},
		{"trilogy by folder",
			tsResolveQuery{mediaType: "movie", title: "The Matrix", year: 1999},
			[]tsFileStat{
				{ID: 1, Path: "Pack/The.Matrix.Reloaded.2003/a.mkv", Length: 9 * gb},
				{ID: 2, Path: "Pack/The.Matrix.1999.1080p/b.mkv", Length: 8 * gb},
			},
			"Pack/The.Matrix.1999.1080p/b.mkv"},
		{"pack folder names the collection only",
			tsResolveQuery{mediaType: "movie", title: "The Matrix", year: 1999},
			[]tsFileStat{
				{ID: 1, Path: "The Matrix 1999 2003 Trilogy/Part 2.mkv", Length: 9 * gb},
				{ID: 2, Path: "The Matrix 1999 2003 Trilogy/Part 1.mkv", Length: 8 * gb},
			},
			""},
		{"wrong year",
			tsResolveQuery{mediaType: "movie", title: "Dune", year: 2021},
			[]tsFileStat{{ID: 1, Path: "Dune.1984.mkv", Length: 9 * gb}, {ID: 2, Path: "Dune.2000.mkv", Length: 8 * gb}},
			""},
	}
	for _, tc := range cases {
		f, ok := tsPickFile(tc.files, tc.q, tsCandidate{})
		got := ""
		if ok {
			got = f.Path
		}
		if got != tc.want {
			t.Errorf("%s: picked %q, want %q", tc.name, got, tc.want)
		}
	}
}

// "Documentary" names bonus material of a film, but is a plain tag of a documentary.
func TestScoreDocumentaryTag(t *testing.T) {
	r := JackettResult{Title: "Free Solo 2018 Documentary 1080p WEBRip x264", Seeders: 50, Size: 2 * tsGB, InfoHash: strings.Repeat("a", 40)}
	q := tsResolveQuery{mediaType: "movie", title: "Free Solo", year: 2018, documentary: true}
	if _, ok := tsScore(r, q, tsNormalize(q.title)); !ok {
		t.Error("documentary rejected as an extra")
	}
	making := r
	making.Title = "Free Solo 2018 Documentary Making of 1080p WEBRip x264"
	if _, ok := tsScore(making, q, tsNormalize(q.title)); ok {
		t.Error("making-of of a documentary accepted")
	}
	q.documentary = false
	if _, ok := tsScore(r, q, tsNormalize(q.title)); ok {
		t.Error("documentary about a film accepted")
	}
}

// Late batches of slower indexers drop the releases a length check rejected, like the first one.
func TestAddLateSkipsRejectedHome(t *testing.T) {
	s := NewService(nil, Config{})
	q := tsResolveQuery{mediaType: "movie", title: "The Odyssey", year: 2026, tmdbID: 1368337}
	bad, good := strings.Repeat("a", 40), strings.Repeat("b", 40)
	s.demoteHome(q.tmdbID, q.title, []string{bad}, "wrong length")
	raw := []JackettResult{{Title: "first", InfoHash: good}}
	raw = s.addLate(q, raw, []JackettResult{{Title: "rejected", InfoHash: strings.ToUpper(bad)}, {Title: "late", InfoHash: good}})
	if len(raw) != 2 || raw[0].Title != "first" || raw[1].Title != "late" {
		t.Fatalf("raw %+v, want the rejected release dropped", raw)
	}
}
