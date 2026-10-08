package streaming

import (
	"errors"
	"fmt"
	"strings"
	"testing"

	"nextflix/internal/platform/tmdb"
)

// TMDB's season list of Çukur (tv 74823): 33, 34, 25 and 39 episodes plus 2 specials. One
// uploader counts a special as S3E26 ("Cukur 94 S4 E1"; TMDB and the season packs: 93).
var cukurSeasons = []int{33, 34, 25, 39}

func cukurQuery(season, episode int) tsResolveQuery {
	q := tsResolveQuery{mediaType: "tv", title: "The Pit", names: []string{"Çukur"}, season: season, episode: episode,
		seasonEps: cukurSeasons}
	q.absEp = tsAbsoluteEpisode(q.seasonEps, season, episode)
	return q
}

// tsTestEpisode parses a release name the way tsScore does: title and a year after it dropped.
func tsTestEpisode(t *testing.T, title, show string) tsEpInfo {
	t.Helper()
	rest, ok := tsMatchTitle(tsNormalize(title), tsNormalize(show))
	if !ok {
		t.Fatalf("%q does not start with %q", title, show)
	}
	for len(rest) > 0 && tsYearPattern.MatchString(rest[0]) {
		rest = rest[1:]
	}
	return tsParseEpisode(rest)
}

func TestParseEpisodeNotations(t *testing.T) {
	cases := []struct{ title, want string }{
		// Live Jackett / Bitsearch names of Çukur
		{"Cukur 94 S4 E1", "S4E1 #94"},
		{"Cukur 96 S4E3", "S4E3 #96"},
		{"Cukur.BL.90.720p.WEB-DL.x264.mkv", "#90"},
		{"Cukur 103 S4E10.mp4", "S4E10 #103"},
		{"Cukur 66 S2E33.mp4", "S2E33 #66"},
		{"Cukur 69 S3 E02.mp4", "S3E2 #69"},
		{"Cukur ep.1 (GR subs).mkv", "#1"},
		{"Cukur Episode 1 with English Subtitles Silkroad", "#1"},
		{"Cukur / Season: 1 / Episodes: 01-107 of 107 [2020, Turkey, crime, drama, WEBRip …]", "S1E1-107"},
		{"Cukur / Season: 1 / Episodes: 1-20 (33) [2017, Turkey, WEBRip]", "S1E1-20"},
		{"Çukur / Cukur (Season: 4 / Episodes: 093-131 / (131) [2020, Turkey, crime, drama, WEBRip 720p]", "S4E93-131"},
		{"Çukur / Cukur (Season: 2 / Series: 108-138 (01-31) / (108/215) [2020, Turkey, WEBRip 720p]", "S2E108-138"},
		{"Cukur.01.sez.rus.profes..WEB-DLRip720p", "S1"},
		// Other notations
		{"Dizi.Adi.S01.Bolum.05.1080p", "S1E5"},
		{"Dizi Adi 5. Bölüm 1080p", "#5"},
		{"Dizi Adi Bölüm 5", "#5"},
		{"Dizi.Adi.B05.720p", "#5"},
		{"Dizi.Adi.BLM.12", "#12"},
		{"Dizi Adi E05 1080p WEB", "#5"},
		{"Dizi.Adi.1x05.720p.HDTV", "S1E5"},
		{"Dizi Adi S01 E05 1080p", "S1E5"},
		{"Dizi Adi S01E05 1080p", "S1E5"},
		{"Dizi Adi Season 1 Episode 5", "S1E5"},
		{"Dizi Adi Sezon 1 Bölüm 5", "S1E5"},
		{"Dizi Adi 2. Sezon 5. Bölüm", "S2E5"},
		{"Dizi Adi Bölüm 5 Sezon 2", "S2E5"},
		{"Dizi Adi 2021 S01E05 1080p", "S1E5"},
		{"Dizi Adi 05 1080p WEB-DL", "#5"},
		{"Dizi Adi 2021 12 720p", "#12"},
		{"Dizi Adi S01E01-E03 1080p", "S1E1-3"},
		{"Dizi Adi S01E1050 720p", "S1E1050"},
		{"Dizi Adi Seasons 1-3 1080p", "S1-S3"},
		{"Dizi Adi S01-S03 Complete", "S1-S3 complete"},
		{"Dizi Adi S02 1080p", "S2"},
		{"Dizi Adi Complete Series 720p", "complete"},
		{"Dizi Adi Series 2 720p", "S2"},
		{"Dizi Adi Series 1080p", ""},
		// Years, resolutions, codecs and channels are not episodes
		{"Dizi Adi 1080p WEB-DL DDP5.1 x264", ""},
		{"Dizi Adi 2160p HDR AAC 2.0 H.264", ""},
		{"Dizi Adi 720 HDTV", ""},
		{"Dizi Adi 1080 WEB", ""},
		{"Dizi Adi 2019 1080p", ""},
		{"Dizi Adi H 264 5 1", ""},
		{"Dizi Adi The Movie 2019", ""},
	}
	for _, c := range cases {
		show := "Dizi Adi"
		if strings.HasPrefix(tsFold(c.title), "Cukur") {
			show = "Cukur"
		}
		if got := tsTestEpisode(t, c.title, show).String(); got != c.want {
			t.Errorf("%q: got %q, want %q", c.title, got, c.want)
		}
	}
	// A title that goes on is another show; the episode info has to come first
	if e := tsTestEpisode(t, "Dizi Adi The Movie 2019", "Dizi Adi"); e.marked {
		t.Error("title words marked as episode info")
	}
	if e := tsTestEpisode(t, "Cukur 94 S4 E1", "Cukur"); !e.marked || !e.lead {
		t.Errorf("leading absolute number: %+v", e)
	}
	// A dated name is not an episode range
	daily := tsResolveQuery{mediaType: "tv", title: "The Daily Show", season: 1, episode: 10}
	if c, ok := tsScore(JackettResult{Title: "The Daily Show 2024 10 15 1080p WEB", Seeders: 50, Size: tsGB,
		InfoHash: strings.Repeat("a", 40)}, daily, tsNormalize(daily.title)); ok {
		t.Errorf("a date read as episodes: kind %d [%s]", c.kind, c.ep)
	}
}

func TestEpisodeKindCukur(t *testing.T) {
	const pack107 = "Cukur / Season: 1 / Episodes: 01-107 of 107 [2020, Turkey, crime, drama, WEBRip …]"
	const pack20 = "Cukur / Season: 1 / Episodes: 1-20 (33) [2017, Turkey, WEBRip]"
	cases := []struct {
		title           string
		season, episode int
		abs             int // 0 = TMDB's
		kind            int
	}{
		{"Cukur 94 S4 E1", 4, 1, 94, 1},
		{"Cukur 94 S4 E1", 4, 2, 95, 0},
		{"Cukur 96 S4E3", 4, 1, 94, 0},
		{"Cukur 96 S4E3", 4, 3, 96, 1},
		{"Cukur 103 S4E10.mp4", 4, 10, 103, 1},
		{"Cukur 66 S2E33.mp4", 2, 33, 0, 1},
		{"Cukur 69 S3 E02.mp4", 3, 2, 0, 1},
		{"Cukur.BL.90.720p.WEB-DL.x264.mkv", 3, 23, 0, 1}, // TMDB: 33 + 34 + 23
		{"Cukur.BL.90.720p.WEB-DL.x264.mkv", 4, 1, 94, 0},
		{"Cukur ep.1 (GR subs).mkv", 1, 1, 0, 1},
		{"Cukur ep.1 (GR subs).mkv", 4, 1, 94, 0},
		{"Cukur Episode 1 with English Subtitles Silkroad", 1, 1, 0, 1},
		{"Cukur Episode 1 with English Subtitles Silkroad", 2, 1, 0, 0},
		{pack107, 4, 1, 94, 3}, // the whole show as one season, numbered absolutely
		{pack107, 1, 1, 0, 3},
		{pack20, 1, 5, 0, 2},
		{pack20, 1, 25, 0, 0},
		{pack20, 4, 1, 94, 0},
		{"Cukur S01E94 720p", 4, 1, 94, 1},
		{"Cukur S04E01 720p", 4, 1, 94, 1},
		{"Cukur S04 720p", 4, 1, 94, 2},
		{"Cukur 2. Sezon 5. Bölüm", 2, 5, 0, 1},
		{"Cukur Season 2 Episodes 34-67", 2, 1, 0, 2}, // a season pack numbered absolutely
		{"Cukur Season 2 Episodes 34-67", 3, 1, 0, 0},
	}
	for _, c := range cases {
		q := cukurQuery(c.season, c.episode)
		if c.abs > 0 {
			q.absEp = c.abs
		}
		if got := tsEpisodeKind(tsTestEpisode(t, c.title, "Cukur"), q.epTarget()); got != c.kind {
			t.Errorf("%q for S%dE%d (abs %d): kind %d, want %d", c.title, c.season, c.episode, q.absEp, got, c.kind)
		}
	}

	// A stale count (a running season) does not make a regular name absolute
	q := tsResolveQuery{mediaType: "tv", season: 5, episode: 10, seasonEps: []int{10, 10, 10, 10, 8}}
	q.absEp = tsAbsoluteEpisode(q.seasonEps, 5, 10)
	if got := tsEpisodeKind(tsTestEpisode(t, "Show S05E10 1080p", "Show"), q.epTarget()); got != 1 {
		t.Errorf("S05E10 past a stale count: kind %d", got)
	}
	s1 := tsResolveQuery{mediaType: "tv", season: 1, episode: 10, seasonEps: []int{10, 10, 10, 10, 8}}
	if got := tsEpisodeKind(tsTestEpisode(t, "Show S05E10 1080p", "Show"), s1.epTarget()); got != 0 {
		t.Errorf("S05E10 taken for S01E10: kind %d", got)
	}
	// Without TMDB counts season 1 still matches absolute numbers, later seasons do not
	noCounts := tsResolveQuery{mediaType: "tv", season: 1, episode: 5}
	if got := tsEpisodeKind(tsTestEpisode(t, "Show Bölüm 5", "Show"), noCounts.epTarget()); got != 1 {
		t.Errorf("Bölüm 5 for S01E05 without counts: kind %d", got)
	}
	noCounts.season = 2
	if got := tsEpisodeKind(tsTestEpisode(t, "Show Bölüm 5", "Show"), noCounts.epTarget()); got != 0 {
		t.Errorf("Bölüm 5 for S02E05 without counts: kind %d", got)
	}
}

// The live results of Çukur go through the whole ranking: the absolute and space-separated
// names, the whole-show pack, Russian tracker packs ("Çukur / Cukur (Season: 4 / Episodes:
// 093-131", "Series: 108-138 (01-31) / (108/215)" counts split parts) and "01 sez" packs.
func TestRankCukurReleases(t *testing.T) {
	raw := []JackettResult{
		{Title: "Cukur 94 S4 E1", Seeders: 42, Size: gbSize(1.4), InfoHash: strings.Repeat("1", 40)},
		{Title: "Cukur 96 S4E3", Seeders: 30, Size: gbSize(1.4), InfoHash: strings.Repeat("2", 40)},
		{Title: "Cukur.BL.90.720p.WEB-DL.x264.mkv", Seeders: 12, Size: gbSize(1.6), InfoHash: strings.Repeat("3", 40)},
		{Title: "Cukur 103 S4E10.mp4", Seeders: 9, Size: gbSize(1.3), InfoHash: strings.Repeat("4", 40)},
		{Title: "Cukur ep.1 (GR subs).mkv", Seeders: 5, Size: gbSize(1.1), InfoHash: strings.Repeat("5", 40)},
		{Title: "Cukur / Season: 1 / Episodes: 01-107 of 107 [2020, Turkey, crime, drama, WEBRip …]", Seeders: 8,
			Size: 102 * tsGB, InfoHash: strings.Repeat("6", 40)},
		{Title: "Cukur / Season: 1 / Episodes: 1-20 (33) [2017, Turkey, WEBRip]", Seeders: 6, Size: 20 * tsGB,
			InfoHash: strings.Repeat("7", 40)},
		{Title: "Çukur / Cukur (Season: 4 / Episodes: 093-131 / (131) [2020, Turkey, crime, drama, WEBRip 720p] VO (I.Kotova)",
			Seeders: 7, Size: 60 * tsGB, InfoHash: strings.Repeat("8", 40)},
		{Title: "Çukur / Cukur (Season: 2 / Series: 108-138 (01-31) / (108/215) [2020, Turkey, crime, drama, WEBRip 720p] MVO",
			Seeders: 7, Size: 40 * tsGB, InfoHash: strings.Repeat("9", 40)},
		{Title: "Cukur.01.sez.rus.profes..WEB-DLRip720p", Seeders: 4, Size: 50 * tsGB, InfoHash: strings.Repeat("b", 40)},
	}
	accepted := func(q tsResolveQuery) map[string]int {
		tsApplyAbsolute(&q, raw)
		got := map[string]int{}
		for _, c := range tsRankAll(raw, q) {
			got[c.raw.Title] = c.kind
		}
		return got
	}
	check := func(name string, got, want map[string]int) {
		t.Helper()
		if len(got) != len(want) {
			t.Errorf("%s: accepted %v, want %v", name, got, want)
			return
		}
		for title, kind := range want {
			if got[title] != kind {
				t.Errorf("%s: %q kind %d, want %d", name, title, got[title], kind)
			}
		}
	}
	check("S04E01", accepted(cukurQuery(4, 1)), map[string]int{"Cukur 94 S4 E1": 1, raw[5].Title: 3, raw[7].Title: 2})
	check("S01E01", accepted(cukurQuery(1, 1)), map[string]int{"Cukur ep.1 (GR subs).mkv": 1, raw[5].Title: 3, raw[6].Title: 2,
		raw[9].Title: 2})
	check("S02E05", accepted(cukurQuery(2, 5)), map[string]int{raw[5].Title: 3})
	check("S03E23", accepted(cukurQuery(3, 23)), map[string]int{"Cukur.BL.90.720p.WEB-DL.x264.mkv": 1, raw[5].Title: 3})
}

type seasonTMDB struct {
	seasons []tmdb.Season
	err     error
	calls   int
}

func (f *seasonTMDB) GetMovieDetails(int) (*tmdb.MovieDetails, error) {
	return nil, errors.New("no movies")
}

func (f *seasonTMDB) GetSeasonDetails(int, int) (*tmdb.Season, error) { return &tmdb.Season{}, nil }

func (f *seasonTMDB) GetTVDetails(id int) (*tmdb.TVDetails, error) {
	f.calls++
	if f.err != nil {
		return nil, f.err
	}
	return &tmdb.TVDetails{TV: tmdb.TV{ID: id}, Seasons: f.seasons}, nil
}

func TestAbsoluteEpisodeMapping(t *testing.T) {
	f := &seasonTMDB{seasons: []tmdb.Season{
		{SeasonNumber: 0, Name: "Specials", EpisodeCount: 2},
		{SeasonNumber: 1, EpisodeCount: 33}, {SeasonNumber: 2, EpisodeCount: 34},
		{SeasonNumber: 3, EpisodeCount: 25}, {SeasonNumber: 4, EpisodeCount: 39},
	}}
	s := NewService(nil, Config{TMDB: f})
	counts := s.seasonEpisodes(74823)
	if len(counts) != 4 || counts[0] != 33 || counts[3] != 39 {
		t.Fatalf("season counts %v (specials must be left out)", counts)
	}
	s.seasonEpisodes(74823)
	if f.calls != 1 {
		t.Fatalf("season list not cached: %d calls", f.calls)
	}
	for _, c := range []struct{ season, episode, want int }{
		{1, 1, 1}, {1, 33, 33}, {2, 1, 34}, {2, 33, 66}, {3, 2, 69}, {4, 1, 93}, {4, 10, 102}, {5, 1, 132}, {0, 1, 0},
	} {
		if got := tsAbsoluteEpisode(counts, c.season, c.episode); got != c.want {
			t.Errorf("S%dE%d: %d, want %d", c.season, c.episode, got, c.want)
		}
	}
	// A season without a count leaves the later ones unknown
	if got := tsAbsoluteEpisode([]int{10, 0, 12}, 3, 1); got != 0 {
		t.Errorf("gap: %d", got)
	}
	// TMDB (33, 34, 25, 39) and the season packs ("Season: 4 / Episodes: 093-131") start
	// season 4 at 93; one uploader counts 26 in season 3 ("94 S4 E1", "96 S4E3", "103
	// S4E10"). TMDB wins; those names still match by season and episode.
	q := cukurQuery(4, 1)
	names := []JackettResult{{Title: "Cukur 94 S4 E1"}, {Title: "Cukur 96 S4E3"}, {Title: "Cukur 103 S4E10.mp4"},
		{Title: "Cukur 69 S3 E02.mp4"}}
	if abs, votes := tsAbsoluteFromNames(names, q); abs != 94 || votes != 3 {
		t.Errorf("names count S4E1 as %d (%d votes), want 94 (3)", abs, votes)
	}
	tsApplyAbsolute(&q, names)
	if q.absEp != 93 {
		t.Errorf("S4E1: %d, want TMDB's 93", q.absEp)
	}
	// Without TMDB counts the names decide, for any episode of the season
	q = cukurQuery(4, 5)
	q.seasonEps = nil
	tsApplyAbsolute(&q, names)
	if q.absEp != 98 {
		t.Errorf("S4E5 by the names: %d, want 98", q.absEp)
	}
	q = cukurQuery(4, 1)
	q.seasonEps = nil
	tsApplyAbsolute(&q, names[3:])
	if q.absEp != 0 {
		t.Errorf("a name of another season decided: %d", q.absEp)
	}
	// A failed lookup is retried later, not cached for a day
	bad := NewService(nil, Config{TMDB: &seasonTMDB{err: errors.New("down")}})
	if c := bad.seasonEpisodes(1); c != nil {
		t.Fatalf("failed lookup: %v", c)
	}
}

func files(paths ...string) []tsFileStat {
	var out []tsFileStat
	for i, p := range paths {
		out = append(out, tsFileStat{ID: i + 1, Path: p, Length: int64(1000 + i)})
	}
	return out
}

func TestPickEpisodeFileInPacks(t *testing.T) {
	pick := func(t *testing.T, fs []tsFileStat, q tsResolveQuery, release string) string {
		t.Helper()
		c, ok := tsScore(JackettResult{Title: release, Seeders: 10, InfoHash: strings.Repeat("a", 40)}, q,
			tsNormalize("Cukur"))
		if !ok {
			t.Fatalf("%q rejected for S%dE%d", release, q.season, q.episode)
		}
		f, ok := tsPickFile(fs, q, c)
		if !ok {
			return ""
		}
		return f.Path
	}

	// The whole show as one season, files numbered absolutely
	const whole = "Cukur / Season: 1 / Episodes: 01-107 of 107 [2020, Turkey, crime, drama, WEBRip]"
	wholeFiles := files(
		"Cukur Season 1 Episodes 01-107/Cukur.001.720p.WEBRip.mkv",
		"Cukur Season 1 Episodes 01-107/Cukur.033.720p.WEBRip.mkv",
		"Cukur Season 1 Episodes 01-107/Cukur.093.720p.WEBRip.mkv",
		"Cukur Season 1 Episodes 01-107/Cukur.094.720p.WEBRip.mkv",
		"Cukur Season 1 Episodes 01-107/Cukur.107.720p.WEBRip.mkv",
		"Cukur Season 1 Episodes 01-107/Sample/Cukur.094.sample.mkv",
	)
	q := cukurQuery(4, 1)
	q.absEp = 94
	if got := pick(t, wholeFiles, q, whole); !strings.HasSuffix(got, "Cukur.094.720p.WEBRip.mkv") {
		t.Errorf("S4E1 in the whole-show pack: %q", got)
	}
	if got := pick(t, wholeFiles, cukurQuery(1, 1), whole); !strings.HasSuffix(got, "Cukur.001.720p.WEBRip.mkv") {
		t.Errorf("S1E1 in the whole-show pack: %q", got)
	}
	// ... the same with S01Exxx names
	sFiles := files("Cukur/Cukur.S01E093.mkv", "Cukur/Cukur.S01E094.mkv", "Cukur/Cukur.S01E001.mkv")
	if got := pick(t, sFiles, q, whole); got != "Cukur/Cukur.S01E094.mkv" {
		t.Errorf("S01E094 for S4E1: %q", got)
	}

	// ... and with a season label on every file (live: "Cukur.001.ser.01.sez...", 107 files)
	const ser = "Cukur.01.sez.rus.profes..WEB-DLRip720p/Cukur.%03d.ser.01.sez.rus.profes..WEB-DLRip720p.mp4"
	serFiles := files(fmt.Sprintf(ser, 1), fmt.Sprintf(ser, 2), fmt.Sprintf(ser, 93), fmt.Sprintf(ser, 94), fmt.Sprintf(ser, 107))
	if got := pick(t, serFiles, cukurQuery(4, 1), whole); got != fmt.Sprintf(ser, 93) {
		t.Errorf("S4E1 (TMDB 93) in the ser/sez pack: %q", got)
	}
	if got := pick(t, serFiles, cukurQuery(1, 2), whole); got != fmt.Sprintf(ser, 2) {
		t.Errorf("S1E2 in the ser/sez pack: %q", got)
	}

	// Season packs numbered per season, in folders or by name
	q2 := cukurQuery(2, 7)
	relFiles := files("Cukur S02/Cukur S02E06.mkv", "Cukur S02/Cukur S02E07.mkv", "Cukur S02/Cukur S02E08.mkv")
	if got := pick(t, relFiles, q2, "Cukur S02 720p WEBRip"); got != "Cukur S02/Cukur S02E07.mkv" {
		t.Errorf("S02E07 by name: %q", got)
	}
	bolumFiles := files("Cukur 2. Sezon/Bölüm 06.mp4", "Cukur 2. Sezon/Bölüm 07.mp4", "Cukur 2. Sezon/Bölüm 08.mp4")
	if got := pick(t, bolumFiles, q2, "Cukur 2. Sezon 720p"); got != "Cukur 2. Sezon/Bölüm 07.mp4" {
		t.Errorf("Bölüm 07 in a season folder: %q", got)
	}
	multi := files("Cukur/Season 1/E07.mkv", "Cukur/Season 2/E07.mkv", "Cukur/Season 3/E07.mkv")
	if got := pick(t, multi, q2, "Cukur Seasons 1-3 720p"); got != "Cukur/Season 2/E07.mkv" {
		t.Errorf("E07 of season 2 in a multi-season pack: %q", got)
	}

	// A season pack numbered absolutely: S2 runs 34-67, so "34. Bölüm" is S2E1 and S2E34 is 67
	absFiles := files("Cukur Sezon 2/Cukur 34. Bölüm.mp4", "Cukur Sezon 2/Cukur 35. Bölüm.mp4", "Cukur Sezon 2/Cukur 40. Bölüm.mp4",
		"Cukur Sezon 2/Cukur 67. Bölüm.mp4")
	if got := pick(t, absFiles, cukurQuery(2, 1), "Cukur Sezon 2 720p"); got != "Cukur Sezon 2/Cukur 34. Bölüm.mp4" {
		t.Errorf("S2E1 in an absolutely numbered season pack: %q", got)
	}
	if got := pick(t, absFiles, cukurQuery(2, 34), "Cukur Sezon 2 720p"); got != "Cukur Sezon 2/Cukur 67. Bölüm.mp4" {
		t.Errorf("S2E34 in an absolutely numbered season pack: %q", got)
	}
	if got := pick(t, absFiles, cukurQuery(2, 7), "Cukur Sezon 2 720p"); got != "Cukur Sezon 2/Cukur 40. Bölüm.mp4" {
		t.Errorf("S2E7 in an absolutely numbered season pack: %q", got)
	}

	// Years and resolutions in file names are not episodes
	bl := files("Cukur.2017.BL.89.1080p.x264.mkv", "Cukur.2017.BL.90.1080p.x264.mkv", "Cukur.2017.BL.91.1080p.x264.mkv")
	q3 := cukurQuery(3, 23) // TMDB: episode 90
	if got := pick(t, bl, q3, "Cukur BL 89-91 1080p"); got != "Cukur.2017.BL.90.1080p.x264.mkv" {
		t.Errorf("BL.90: %q", got)
	}
	// Files without the title: "[Group] Show - 05 [1080p]"
	anime := files("[Grp] Some Show - 04 [1080p].mkv", "[Grp] Some Show - 05 [1080p].mkv")
	if got := pick(t, anime, cukurQuery(1, 5), "Cukur 01-20 1080p"); got != "[Grp] Some Show - 05 [1080p].mkv" {
		t.Errorf("number without the title: %q", got)
	}
	// A wanted episode missing from a pack: no file
	if got := pick(t, relFiles, cukurQuery(2, 9), "Cukur S02 720p WEBRip"); got != "" {
		t.Errorf("missing episode picked %q", got)
	}
	// A single episode whose file has no number: the largest video
	single := files("Cukur 94 S4 E1/video.mkv", "Cukur 94 S4 E1/extra.mkv")
	if got := pick(t, single, q, "Cukur 94 S4 E1"); got != "Cukur 94 S4 E1/extra.mkv" {
		t.Errorf("single episode: %q", got)
	}
}

func TestBroadEpisodeQueries(t *testing.T) {
	q := tsResolveQuery{mediaType: "tv", title: "The Pit", season: 4, episode: 1}
	n := tsTitleNames{names: []string{"Çukur"}, queries: []string{"Çukur", "Cukur"}, eager: true, abs: 94}
	initial, _ := tsSearchQueries(q)
	later := tsLaterQueries(q, n, initial)
	want := "Çukur S04E01|Çukur S04|Cukur S04E01|Cukur S04|Cukur|Cukur 94"
	if got := strings.Join(later, "|"); got != want {
		t.Fatalf("later queries %q, want %q", got, want)
	}
	// Bitsearch: episode queries, then the absolute and bare ones, season ones last (and left
	// out next to their bare name)
	bs := tsBitsearchQueries(q, append(initial, later...))
	if got := strings.Join(bs, "|"); got != "the pit s04e01|cukur s04e01|cukur|cukur 94" {
		t.Fatalf("bitsearch queries %q", got)
	}
	// An English original adds nothing eagerly; season 1 has no absolute query
	if got := tsLaterQueries(q, tsTitleNames{names: []string{"Pit"}, queries: []string{"Pit"}}, initial); len(got) != 0 {
		t.Fatalf("English original: %q", got)
	}
	q.season = 1
	if got := strings.Join(tsBroadQueries(q, []string{"Çukur"}, 1), "|"); got != "Cukur" {
		t.Fatalf("season 1 broad queries %q", got)
	}
	if got := tsBroadQueries(tsResolveQuery{mediaType: "movie", title: "X"}, nil, 0); got != nil {
		t.Fatalf("movie: %q", got)
	}
}
