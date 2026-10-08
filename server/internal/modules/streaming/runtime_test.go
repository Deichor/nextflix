package streaming

import (
	"errors"
	"testing"

	"nextflix/internal/platform/tmdb"
)

func TestDurationMismatch(t *testing.T) {
	mins := func(m float64) float64 { return m * 60 }
	cases := []struct {
		name      string
		dur       float64
		runtime   int
		mediaType string
		longerCut bool
		want      bool
	}{
		{"half-length movie (The Odyssey 86 vs 172)", 5163, 172, "movie", false, true},
		{"movie a few minutes short", mins(165), 172, "movie", false, false},
		{"short movie within 10 minutes", mins(80), 90, "movie", false, false},
		{"short movie 15 minutes off", mins(75), 90, "movie", false, true},
		{"episode 41 vs 50", mins(41), 50, "tv", false, false},
		{"episode 22 vs 50", mins(22), 50, "tv", false, true},
		{"double episode file", mins(100), 50, "tv", false, true},
		{"unknown duration", 0, 172, "movie", false, false},
		{"unknown runtime", 5163, 0, "movie", false, false},
		{"extended cut longer", mins(228), 179, "movie", true, false},
		{"untagged longer cut", mins(228), 179, "movie", false, true},
		{"extended tag does not excuse a short file", mins(86), 172, "movie", true, true},
	}
	for _, tc := range cases {
		if got := tsDurationMismatch(tc.dur, tc.runtime, tc.mediaType, tc.longerCut); got != tc.want {
			t.Errorf("%s: got %v, want %v", tc.name, got, tc.want)
		}
	}
}

func TestLongerCut(t *testing.T) {
	for title, want := range map[string]bool{
		"The Lord of the Rings 2001 EXTENDED 1080p BluRay x264":  true,
		"Kingdom of Heaven 2005 Director's Cut 1080p BluRay":     true,
		"The Odyssey (2026) (1080p AMZN WEB-DL x265 10bit EAC3)": false,
	} {
		if got := tsLongerCut(title); got != want {
			t.Errorf("%q: got %v, want %v", title, got, want)
		}
	}
}

func TestParseClock(t *testing.T) {
	p := &tsProbe{Streams: []tsProbeStream{
		{CodecType: "video", Tags: map[string]string{"DURATION": "01:26:03.000000000"}},
		{CodecType: "subtitle", Tags: map[string]string{"DURATION-eng": "01:22:16.500000000"}},
	}}
	if d := p.duration(); d != 5163 {
		t.Fatalf("tag duration: got %v", d)
	}
	p.Format.Duration = "5163.5"
	if d := p.duration(); d != 5163.5 {
		t.Fatalf("format duration: got %v", d)
	}
	if d := tsParseClock("bad"); d != 0 {
		t.Fatalf("invalid clock: got %v", d)
	}
}

type fakeTMDB struct {
	movie   map[int]int
	season  map[int][]tmdb.Episode
	showRun []int
	calls   int
}

func (f *fakeTMDB) GetMovieDetails(id int) (*tmdb.MovieDetails, error) {
	f.calls++
	if rt, ok := f.movie[id]; ok {
		return &tmdb.MovieDetails{Runtime: rt}, nil
	}
	return nil, errors.New("not found")
}

func (f *fakeTMDB) GetTVDetails(int) (*tmdb.TVDetails, error) {
	f.calls++
	return &tmdb.TVDetails{EpisodeRunTime: f.showRun}, nil
}

func (f *fakeTMDB) GetSeasonDetails(_, season int) (*tmdb.Season, error) {
	f.calls++
	return &tmdb.Season{Episodes: f.season[season]}, nil
}

func TestExpectedRuntime(t *testing.T) {
	f := &fakeTMDB{
		movie:   map[int]int{1: 172},
		season:  map[int][]tmdb.Episode{1: {{EpisodeNumber: 1, Runtime: 58}, {EpisodeNumber: 2, Runtime: 0}}},
		showRun: []int{0, 45},
	}
	s := NewService(nil, Config{TMDB: f})

	// TMDB wins over the client's runtime and is cached
	q := tsResolveQuery{mediaType: "movie", title: "The Odyssey", tmdbID: 1, runtime: 90}
	if rt, from := s.expectedRuntime(q); rt != 172 || from != "tmdb" {
		t.Fatalf("movie: %d %s", rt, from)
	}
	calls := f.calls
	s.expectedRuntime(q)
	if f.calls != calls {
		t.Fatal("movie runtime was not cached")
	}

	// Unknown movie falls back to the client runtime, then to unknown
	if rt, from := s.expectedRuntime(tsResolveQuery{mediaType: "movie", tmdbID: 9, runtime: 100}); rt != 100 || from != "client" {
		t.Fatalf("fallback: %d %s", rt, from)
	}
	if rt, _ := s.expectedRuntime(tsResolveQuery{mediaType: "movie"}); rt != 0 {
		t.Fatalf("unknown: %d", rt)
	}

	// Episode runtime from the season, then the show's episode_run_time
	tv := tsResolveQuery{mediaType: "tv", tmdbID: 5, season: 1, episode: 1}
	if rt, _ := s.expectedRuntime(tv); rt != 58 {
		t.Fatalf("episode: %d", rt)
	}
	tv.episode = 2
	if rt, _ := s.expectedRuntime(tv); rt != 45 {
		t.Fatalf("show fallback: %d", rt)
	}

	// No TMDB client: only the client runtime
	if rt, _ := NewService(nil, Config{}).expectedRuntime(tsResolveQuery{mediaType: "movie", tmdbID: 1, runtime: 120}); rt != 120 {
		t.Fatalf("no tmdb: %d", rt)
	}
}
