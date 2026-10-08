package streaming

import (
	"fmt"
	"strings"
	"testing"
)

// fixture builds Jackett results from (seeders, title, size in GB) triples with a fake info hash
func fixture(rows ...any) []JackettResult {
	var out []JackettResult
	for i := 0; i+2 < len(rows); i += 3 {
		out = append(out, JackettResult{
			Title:    rows[i+1].(string),
			Seeders:  rows[i].(int),
			Size:     int64(rows[i+2].(float64) * float64(tsGB)),
			InfoHash: fmt.Sprintf("%040x", i+1),
		})
	}
	return out
}

func titles(cands []tsCandidate) []string {
	var out []string
	for _, c := range cands {
		out = append(out, c.raw.Title)
	}
	return out
}

// Live Jackett data for "Toy Story 5"
var toyStory5 = fixture(
	2406, "Toy Story 5 (2026) [1080p] [WEBRip] [5.1]", 2.2,
	1519, "Toy.Story.5.2026.1080p.DCPRip.10Bit.DDP5.1-NeoNoir", 5.0,
	1489, "Toy Story 5 (2026) [2160p h265 iT WEB-DL DV HDR10+ eng DDP 5.1 Atmos multisub] -BTM", 18.0,
	1454, "Toy.Story.5.2026.1080p.WEBRip.10Bit.DDP5.1.x265-NeoNoir", 4.0,
	1063, "Toy.Story.5.2026.1080p.DCPRIP.x264-FS", 3.0,
	562, "Toy Story 5 2026 1080p WEB-DL HEVC x265 5.1 BONE", 3.0,
	487, "Toy.Story.5.2026.1080p.DCPRIP.LTE.x264-SPLiCE", 6.0,
	471, "Toy.Story.5.2026.2160p.iT.WEB-DL.DV.HDR10+.DDP5.1.Atmos.H265.MP4-BTM", 17.0,
	366, "Toy.Story.5.2026.1080p.AMZN.WEB-DL.DDP5.1.H.264-KyoGo", 7.0,
	305, "Toy.Story.5.2026.2160p.BluRay.REMUX.DV.P7.HDR.MULTi.TrueHD.Atmos.H265-BTM", 60.0,
)

var toyQuery = tsResolveQuery{mediaType: "movie", title: "Toy Story 5", year: 2026}

func isCinemaRip(title string) bool {
	return strings.Contains(strings.ToLower(title), "dcprip")
}

func TestRankToyStoryHEVCPicks4KWebDL(t *testing.T) {
	for _, hdr := range []bool{true, false} {
		q := toyQuery
		q.hevc, q.hdr = true, hdr
		c := tsRankCandidates(toyStory5, q)
		if len(c) == 0 || c[0].quality != 2160 || !strings.Contains(c[0].raw.Title, "WEB-DL") || !c[0].hdr {
			t.Fatalf("hdr=%v: want a 2160p WEB-DL HDR first, got %q", hdr, titles(c))
		}
		for _, cand := range c {
			if isCinemaRip(cand.raw.Title) || strings.Contains(cand.raw.Title, "REMUX") {
				t.Fatalf("cinema rip or remux checked: %q", titles(c))
			}
		}
	}
}

func TestRankToyStoryNoHEVCPicksH264HomeRelease(t *testing.T) {
	c := tsRankCandidates(toyStory5, toyQuery)
	if len(c) == 0 || c[0].raw.Title != "Toy.Story.5.2026.1080p.AMZN.WEB-DL.DDP5.1.H.264-KyoGo" {
		t.Fatalf("want the AMZN H.264 WEB-DL first, got %q", titles(c))
	}
	for _, cand := range c {
		if isCinemaRip(cand.raw.Title) {
			t.Fatalf("cinema rip ranked in the top: %q", titles(c))
		}
	}
}

func TestRankUHDKeepsSmallerFallback(t *testing.T) {
	q := tsResolveQuery{mediaType: "movie", title: "Dune", year: 2021, hevc: true}
	raw := fixture(
		900, "Dune 2021 2160p WEB-DL HDR10 H265-A", 15.0,
		800, "Dune 2021 2160p WEB-DL HDR10 H265-B", 15.0,
		700, "Dune 2021 2160p BluRay HDR10 x265-C", 20.0,
		600, "Dune 2021 2160p WEB-DL HDR10 H265-D", 15.0,
		500, "Dune 2021 2160p WEB-DL HDR10 H265-E", 15.0,
		300, "Dune 2021 1080p WEB-DL H264", 6.0,
	)
	c := tsRankCandidates(raw, q)
	if len(c) != tsResolveMaxCheck || c[0].quality != 2160 || c[len(c)-1].quality != 1080 {
		t.Fatalf("no 1080p fallback among %q", titles(c))
	}
}

func TestRankCinemaRipsOnlyAsLastResort(t *testing.T) {
	raw := fixture(
		900, "Toy Story 5 2026 HDCAM x264", 1.5,
		800, "Toy.Story.5.2026.HD-TS.x264", 1.5,
		700, "Toy Story 5 2026 TELESYNC 720p", 1.5,
		600, "Toy.Story.5.2026.1080p.Tele-Sync.x264", 1.5,
		500, "Toy.Story.5.2026.1080p.WEBRip.x264.LiNE", 1.5,
		450, "Toy.Story.5.2026.PDVD.x264", 1.5,
		400, "Toy.Story.5.2026.1080p.DCP-Rip.x264", 3.0,
	)
	for _, hevc := range []bool{false, true} {
		q := toyQuery
		q.hevc = hevc
		c := tsRankCandidates(raw, q)
		if len(c) != 1 || !strings.Contains(c[0].raw.Title, "DCP-Rip") {
			t.Fatalf("hevc=%v: want only the DCPRip, got %q", hevc, titles(c))
		}
	}

	// A modestly seeded home release beats a big DCPRip swarm, hardcoded subs count as a leak too
	raw = append(raw, fixture(
		10, "Toy.Story.5.2026.720p.WEBRip.x264", 1.0,
		3000, "Toy.Story.5.2026.1080p.HC.HDRip.x264", 2.0,
		3000, "Toy.Story.5.2026.1080p.WEB-DL.KORSUB.x264", 2.0,
	)...)
	for _, hevc := range []bool{false, true} {
		q := toyQuery
		q.hevc = hevc
		if c := tsRankCandidates(raw, q); c[0].raw.Title != "Toy.Story.5.2026.720p.WEBRip.x264" {
			t.Fatalf("hevc=%v: leak beat a home release: %q", hevc, titles(c))
		}
	}
}

func TestRankKeepsDTSAndSimilarTokens(t *testing.T) {
	raw := fixture(50, "Heat 1995 1080p BluRay DTS x264-STS", 9.0)
	if c := tsRankCandidates(raw, tsResolveQuery{mediaType: "movie", title: "Heat", year: 1995}); len(c) != 1 {
		t.Fatal("DTS / STS release was rejected")
	}
}

func TestRankSourceTiers(t *testing.T) {
	q := tsResolveQuery{mediaType: "movie", title: "Heat", year: 1995}
	raw := fixture(
		100, "Heat 1995 1080p HDRip x264", 3.0,
		100, "Heat 1995 1080p WEBRip x264", 3.0,
		100, "Heat 1995 1080p AMZN WEB-DL x264", 3.0,
		100, "Heat 1995 1080p BluRay x264", 3.0,
	)
	c := tsRankCandidates(raw, q)
	want := "Heat 1995 1080p BluRay x264|Heat 1995 1080p AMZN WEB-DL x264|Heat 1995 1080p WEBRip x264|Heat 1995 1080p HDRip x264"
	if strings.Join(titles(c), "|") != want {
		t.Fatalf("got %q", titles(c))
	}
}

func TestRankUHDNeedsSeeders(t *testing.T) {
	q := tsResolveQuery{mediaType: "movie", title: "Inception", year: 2010, hevc: true, hdr: true}
	weak := fixture(
		2, "Inception 2010 2160p WEB-DL x265 HDR10", 20.0,
		200, "Inception 2010 1080p BluRay x264", 10.0,
	)
	if c := tsRankCandidates(weak, q); c[0].quality != 1080 {
		t.Fatalf("4K with almost no seeders beat 1080p: %q", titles(c))
	}
	// Stale seeder counts no longer push a 4K down; the measured swarm health decides
	modest := fixture(
		8, "Inception 2010 2160p WEB-DL x265 HDR10", 20.0,
		200, "Inception 2010 1080p BluRay x264", 10.0,
	)
	if c := tsRankCandidates(modest, q); c[0].quality != 2160 {
		t.Fatalf("4K with 8 seeders lost: %q", titles(c))
	}
	// Large 4K encodes are allowed, remux sizes are not
	big := fixture(100, "Inception 2010 2160p BluRay x265 10bit HDR", 38.0, 100, "Inception 2010 2160p BluRay REMUX HEVC", 70.0)
	if c := tsRankCandidates(big, q); len(c) != 1 || c[0].raw.Size != 38*tsGB {
		t.Fatalf("4K size limits wrong: %q", titles(c))
	}
}

func TestRankDolbyVisionOnly(t *testing.T) {
	q := tsResolveQuery{mediaType: "movie", title: "Dune", year: 2021, hevc: true, hdr: true}
	raw := fixture(
		900, "Dune 2021 2160p WEB-DL DV H265", 15.0,
		100, "Dune 2021 2160p WEB-DL DV HDR10 H265", 15.0,
		300, "Dune 2021 1080p WEB-DL H264", 6.0,
	)
	c := tsRankCandidates(raw, q)
	want := "Dune 2021 2160p WEB-DL DV HDR10 H265|Dune 2021 1080p WEB-DL H264|Dune 2021 2160p WEB-DL DV H265"
	if strings.Join(titles(c), "|") != want {
		t.Fatalf("DV-only not ranked last: %q", titles(c))
	}
}

func TestRankNoHEVCUnchanged(t *testing.T) {
	q := tsResolveQuery{mediaType: "movie", title: "Inception", year: 2010}
	raw := fixture(
		500, "Inception 2010 2160p BluRay x265 HDR10", 18.0,
		300, "Inception 2010 1080p BluRay x265 10bit", 5.0,
		200, "Inception 2010 1080p BluRay x264", 10.0,
		200, "Inception 2010 720p BluRay x264", 4.0,
	)
	c := tsRankCandidates(raw, q)
	want := "Inception 2010 1080p BluRay x264|Inception 2010 720p BluRay x264|Inception 2010 1080p BluRay x265 10bit|Inception 2010 2160p BluRay x265 HDR10"
	if strings.Join(titles(c), "|") != want {
		t.Fatalf("got %q", titles(c))
	}
	// Movies over 20 GB stay rejected without HEVC
	if c := tsRankCandidates(fixture(500, "Inception 2010 2160p WEB-DL x265", 30.0), q); len(c) != 0 {
		t.Fatal("30 GB release accepted for an H.264-only client")
	}
}

func TestResolveKeyCarriesCapability(t *testing.T) {
	q := tsResolveQuery{mediaType: "movie", title: "Inception", year: 2010}
	plain := tsResolveKey(q)
	q.hevc = true
	hevc := tsResolveKey(q)
	q.hdr = true
	hdr := tsResolveKey(q)
	if plain == hevc || hevc == hdr || !strings.HasPrefix(plain, "v10|") {
		t.Fatalf("keys: %q %q %q", plain, hevc, hdr)
	}
	// A TV (DV, Atmos passthrough) never shares a cached pick with a PC browser; play-only
	// codecs and premium codecs without atmos=1 rank the same, so they share one
	q.acodecs = tsParseAudioCodecs("aac,opus,flac,mp3,eac3", true)
	if tsResolveKey(q) != hdr {
		t.Fatalf("eac3 without atmos changed the key: %q", tsResolveKey(q))
	}
	q.dv = true
	dv := tsResolveKey(q)
	q.atmos = true
	tv := tsResolveKey(q)
	q.acodecs["truehd"] = true
	truehd := tsResolveKey(q)
	if dv == hdr || tv == dv || truehd == tv || !strings.HasSuffix(tv, "|hevc|hdr|dv|atmos") || !strings.HasSuffix(truehd, "|atmos+truehd") {
		t.Fatalf("keys: %q %q %q", dv, tv, truehd)
	}
	if tsResolveBaseKey(q) != plain { // release still finds every variant
		t.Fatalf("base key %q", tsResolveBaseKey(q))
	}
}

func TestPlayArgsVideoPlans(t *testing.T) {
	join := func(p tsVideoPlan) string {
		return strings.Join(playArgs("http://ts/file", 60, 0, p, (*tsProbe)(nil).audioPlan(0, nil), true), " ")
	}
	hevc := join(tsVideoPlan{copy: true, hevc: true})
	if !strings.Contains(hevc, "-c:v copy -tag:v hvc1") || !strings.Contains(hevc, "-noaccurate_seek") {
		t.Fatalf("hevc copy: %s", hevc)
	}
	h264 := join(tsVideoPlan{copy: true})
	if !strings.Contains(h264, "-c:v copy") || strings.Contains(h264, "hvc1") {
		t.Fatalf("h264 copy: %s", h264)
	}
	enc := join(tsVideoPlan{tonemap: "smpte2084"})
	if !strings.Contains(enc, "libx264") || !strings.Contains(enc, "tonemap=tonemap=hable") || strings.Contains(enc, "-noaccurate_seek") {
		t.Fatalf("hdr transcode: %s", enc)
	}
	for _, args := range []string{hevc, h264, enc} {
		if !strings.Contains(args, "-avoid_negative_ts make_zero") || !strings.Contains(args, "aresample=async=1") {
			t.Fatalf("A/V alignment flags missing: %s", args)
		}
	}
}

func TestVideoPlanFromProbe(t *testing.T) {
	probe := func(codec, pix, trc string) *tsProbe {
		return &tsProbe{Streams: []tsProbeStream{{CodecType: "video", CodecName: codec, PixFmt: pix, ColorTransfer: trc}}}
	}
	cases := []struct {
		p      *tsProbe
		hevcOK bool
		want   tsVideoPlan
	}{
		{probe("h264", "yuv420p", ""), false, tsVideoPlan{copy: true}},
		{probe("hevc", "yuv420p10le", "smpte2084"), true, tsVideoPlan{copy: true, hevc: true}},
		{probe("hevc", "yuv420p", ""), true, tsVideoPlan{copy: true, hevc: true}},
		{probe("hevc", "yuv420p10le", "smpte2084"), false, tsVideoPlan{tonemap: "smpte2084"}},
		{probe("hevc", "yuv422p10le", ""), true, tsVideoPlan{}},
		{probe("h264", "yuv420p10le", ""), true, tsVideoPlan{}},
	}
	for i, tc := range cases {
		if got := tc.p.videoPlan(tc.hevcOK); got != tc.want {
			t.Fatalf("case %d: got %+v want %+v", i, got, tc.want)
		}
	}
}

func TestRankHDRTags(t *testing.T) {
	q := tsResolveQuery{mediaType: "movie", title: "Heat", year: 1995, hevc: true}
	for title, want := range map[string]bool{
		"Heat 1995 1080p HDRip x264":                 false,
		"Heat 1995 2160p WEB-DL HDR10+ H265":         true,
		"Heat 1995 2160p WEB-DL HDR10Plus H265":      true,
		"Heat.1995.2160p.UHD.BluRay.x265.HDR.DTS-HD": true,
		"Heat 1995 2160p WEB-DL HLG H265":            true,
	} {
		c := tsRankCandidates(fixture(100, title, 15.0), q)
		if len(c) != 1 || c[0].hdr != want {
			t.Fatalf("%q: hdr want %v, got %v", title, want, c)
		}
	}
}
