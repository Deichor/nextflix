package streaming

import (
	"fmt"
	"testing"
)

func TestResolutionExplicitTokenWins(t *testing.T) {
	q := tsResolveQuery{mediaType: "movie", title: "Charade", year: 1963, hevc: true}
	raw := fixture(
		40, "Charade (1963) Criterion 1080p UHD BluRay x265 HEVC FLAC-SARTRE", 9.0,
		30, "Charade 1963 2160p UHD BluRay x265 HDR", 25.0,
		20, "Charade 1963 UHD BluRay x265", 20.0,
		10, "Charade 1963 4K Remux", 30.0,
	)
	want := map[string]int{
		"Charade (1963) Criterion 1080p UHD BluRay x265 HEVC FLAC-SARTRE": 1080,
		"Charade 1963 2160p UHD BluRay x265 HDR":                          2160,
		"Charade 1963 UHD BluRay x265":                                    2160, // no explicit token: UHD still means 4K
		"Charade 1963 4K Remux":                                           2160,
	}
	for _, c := range tsRankAll(raw, q) {
		if c.quality != want[c.raw.Title] {
			t.Errorf("%q: %d, want %d", c.raw.Title, c.quality, want[c.raw.Title])
		}
	}
	// H.264 clients: an explicit 1080p still beats "UHD"
	q.hevc = false
	for _, c := range tsRankAll(raw[:1], q) {
		if c.quality != 1080 {
			t.Errorf("non-HEVC: %q is %d", c.raw.Title, c.quality)
		}
	}
}

func TestVideoQualityFromSize(t *testing.T) {
	for _, tc := range []struct{ w, h, want int }{
		{1920, 1040, 1080}, // Charade 1963 Criterion: "UHD" in the name, a 1080p file
		{3840, 1600, 2160}, // 2.40:1 4K
		{3840, 2160, 2160},
		{2880, 2160, 2160}, // 4:3 4K
		{1920, 800, 1080},
		{1440, 1080, 1080}, // 4:3 1080p
		{1280, 536, 720},
		{960, 720, 720},
		{720, 480, 480},
		{0, 0, 0},
	} {
		if got := tsVideoQuality(tc.w, tc.h); got != tc.want {
			t.Errorf("%dx%d: %d, want %d", tc.w, tc.h, got, tc.want)
		}
	}
}

func probed(s *Service, hash string, index, w, h int) {
	s.probeCache.Store(fmt.Sprintf("%s:%d", hash, index), &tsProbe{Streams: []tsProbeStream{{CodecType: "video", CodecName: "hevc", Width: w, Height: h}}})
}

func TestProbedQualityCorrectsResultAndCache(t *testing.T) {
	s := NewService(nil, Config{})
	hash := hx('c')
	r := &tsResolveResult{Hash: hash, Index: 1, Quality: "2160p"}
	if s.applyProbedQuality(r) || r.Quality != "2160p" {
		t.Fatal("changed without a probe")
	}
	probed(s, hash, 1, 1920, 1040)
	if !s.applyProbedQuality(r) || r.Quality != "1080p" || !r.demoted() {
		t.Fatalf("got %q (demoted %v), want the probed 1080p", r.Quality, r.demoted())
	}
	// A cached source stored with the name's label comes back with the probed one, so the
	// upgrade logic compares the real resolution
	q := tsResolveQuery{mediaType: "movie", title: "Charade", year: 1963, hevc: true}
	key := tsResolveKey(q)
	s.storeSource(key, tsResolveResult{Hash: hash, Index: 1, Quality: "2160p"})
	if c, ok := s.cachedSource(key); !ok || c.Quality != "1080p" {
		t.Fatalf("cached source: %+v", c)
	}
	// The client says 2160 (it trusted the old label): the server knows better and looks for a 4K
	s.flights.start(key, func() (*tsResolveResult, error) { select {} }) // no search from this test
	up := measured(hx('u'), "2160p", 9, 2)
	up.Quality = "2160p"
	s.upgrades.Store(key, &tsUpgrade{ready: up})
	if got := s.upgradeFor(key, q, hash, 2160); got == nil || got.Hash != up.Hash {
		t.Fatalf("upgrade for a probed 1080p: %v", got)
	}
}

func TestChooseDemotedGivesWayToRealResolution(t *testing.T) {
	fake4K := measured("fake", "1080p", 6, 1)
	fake4K.nameQ = 2160 // named 2160p, the file is 1080p
	real4K := measured("real", "2160p", 6, 2)
	hd := measured("hd", "1080p", 6, 1)
	if i, final := tsChoose([]*tsResolveResult{fake4K, real4K}, []bool{true, true}); i != 1 || !final {
		t.Fatalf("got %d final=%v, want the real 4K", i, final)
	}
	// Against a real 1080p it keeps its rank
	if i, _ := tsChoose([]*tsResolveResult{fake4K, hd}, []bool{true, true}); i != 0 {
		t.Fatalf("got %d, want the better-ranked 1080p", i)
	}
	// While another check runs it is not final
	if i, final := tsChoose([]*tsResolveResult{fake4K, nil}, []bool{true, false}); i != 0 || final {
		t.Fatalf("got %d final=%v, want to wait", i, final)
	}
}
