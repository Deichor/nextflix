package streaming

import (
	"strings"
	"testing"
)

// Live Jackett results for Game of Thrones S01E01: one 2160p torrent listed by three
// indexers (two without a hash), plus a real alternative
var gotS01E01 = []JackettResult{
	{Title: "Game of Thrones S01E01 2160p UHD BluRay x265-SCOTLUHD", Seeders: 9, Size: 5_100_000_000, Link: "http://jackett/dl/1337x/1"},
	{Title: "Game of Thrones S01E01 2160p UHD BluRay x265 SCOTLUHD [eztv]", Seeders: 14, Size: 5_120_000_000, Link: "http://jackett/dl/limetorrents/2"},
	{Title: "Game.of.Thrones.S01E01.2160p.UHD.BluRay.x265-SCOTLUHD[eztv.io].mkv[eztvx.to]", Seeders: 4, Size: 5_101_000_000,
		MagnetUri: "magnet:?xt=urn:btih:E4AAE367B822794D37D8CAFD5A628DB985D65FB4&dn=x"},
	{Title: "Game of Thrones S01E01 2160p UHD BluRay x265 SCOTLUHD[eztv io]", Seeders: 3, Size: 5_100_000_000,
		MagnetUri: "magnet:?xt=urn:btih:e4aae367b822794d37d8cafd5a628db985d65fb4"},
	{Title: "Game of Thrones S01E01 Winter Is Coming 2160p MAX WEB-DL TrueHD 7 1 Atmos DV HDR H 265-FLUX", Seeders: 14, Size: 9_000_000_000,
		InfoHash: strings.Repeat("1", 40)},
	{Title: "Game of Thrones S01E01 Winter Is Coming 1080p BluRay x264-OFT", Seeders: 30, Size: 2_800_000_000, InfoHash: strings.Repeat("2", 40)},
	{Title: "www.UIndex.org    -    Game of Thrones S01E01 Winter Is Coming 1080p BluRay x264-OFT", Seeders: 6, Size: 2_810_000_000,
		InfoHash: strings.Repeat("3", 40)}, // a different hash: a different torrent, kept
}

var gotQuery = tsResolveQuery{mediaType: "tv", title: "Game of Thrones", season: 1, episode: 1, hevc: true}

func TestDedupeSameTorrentAcrossIndexers(t *testing.T) {
	all := tsRankAll(gotS01E01, gotQuery)
	scot := 0
	for _, c := range all {
		if strings.Contains(strings.ToLower(c.raw.Title), "scotluhd") {
			scot++
			if c.hash != "e4aae367b822794d37d8cafd5a628db985d65fb4" {
				t.Fatalf("kept the entry without a hash: %q", c.raw.Title)
			}
		}
	}
	if scot != 1 {
		t.Fatalf("SCOTLUHD listed %d times, want once: %q", scot, titles(all))
	}
	if len(all) != 4 {
		t.Fatalf("got %d candidates, want 4 distinct torrents: %q", len(all), titles(all))
	}
	// The top 4 now hold both distinct 2160p releases
	uhd := 0
	for _, c := range tsRankCandidates(gotS01E01, gotQuery) {
		if c.quality == 2160 {
			uhd++
		}
	}
	if uhd != 2 {
		t.Fatalf("%d distinct 4K candidates checked, want 2", uhd)
	}
}

func TestDedupeHelpers(t *testing.T) {
	stems := []string{
		"Game of Thrones S01E01 2160p UHD BluRay x265-SCOTLUHD",
		"Game of Thrones S01E01 2160p UHD BluRay x265 SCOTLUHD [eztv]",
		"Game.of.Thrones.S01E01.2160p.UHD.BluRay.x265-SCOTLUHD[eztv.io].mkv[eztvx.to]",
		"www.Site.org - Game of Thrones S01E01 2160p UHD BluRay x265 SCOTLUHD",
	}
	for _, s := range stems[1:] {
		if tsStem(s) != tsStem(stems[0]) {
			t.Errorf("stem %q = %q, want %q", s, tsStem(s), tsStem(stems[0]))
		}
	}
	if !tsSameSize(5_100_000_000, 5_190_000_000) || tsSameSize(5_100_000_000, 5_300_000_000) || tsSameSize(0, 0) {
		t.Fatal("size tolerance is not 2 %")
	}
	// Same name and size but different hashes are different torrents
	a := tsCandidate{raw: JackettResult{Title: "X 1080p", Size: 100, Seeders: 5}, hash: strings.Repeat("a", 40)}
	b := tsCandidate{raw: JackettResult{Title: "X 1080p", Size: 100, Seeders: 9}, hash: strings.Repeat("b", 40)}
	if got := tsDedupe([]tsCandidate{a, b}); len(got) != 2 {
		t.Fatalf("merged two hashes: %d", len(got))
	}
	// Without hashes the best-seeded entry stays and seeders are not summed
	c := tsCandidate{raw: JackettResult{Title: "X 1080p [eztv]", Size: 101, Seeders: 7}}
	d := tsCandidate{raw: JackettResult{Title: "X 1080p", Size: 100, Seeders: 3}}
	if got := tsDedupe([]tsCandidate{d, c}); len(got) != 1 || got[0].raw.Seeders != 7 {
		t.Fatalf("dedupe kept %+v", got)
	}
}

func TestTVRejectsSpecialsAndCompilations(t *testing.T) {
	titleTokens := tsNormalize(gotQuery.title)
	reject := []string{
		"Game of Thrones Greatest Moments 2018 HDTV x264-PLUTONiUM[rarbg]",
		"Game of Thrones Greatest Moments 2018 720p HDTV x264-PLUTONiUM[rarbg]",
		"Game of Thrones S01E01 Inside the Episode 1080p WEB-DL",
		"Game of Thrones Season 1 Making of 1080p BluRay x264",
		"Game of Thrones S01 Behind the Scenes 720p",
		"Game of Thrones S01E01 Recap 1080p",
		"Game of Thrones S01E01 Sample 1080p",
		"Game of Thrones The Last Watch 2019 1080p WEB",
		"Game of Thrones S02E01 1080p BluRay x264", // another episode
		"Game of Thrones S02 1080p BluRay x264",    // another season
		"Game of Thrones S01E02-E04 1080p BluRay",  // range without E01
		"Game of Thrones Seasons 2-4 1080p BluRay", // complete range without season 1
	}
	for _, title := range reject {
		if c, ok := tsScore(JackettResult{Title: title, Seeders: 50, Size: 2 * tsGB, InfoHash: strings.Repeat("a", 40)}, gotQuery, titleTokens); ok {
			t.Errorf("accepted %q (kind %d)", title, c.kind)
		}
	}
	accept := map[string]int{
		"Game of Thrones S01E01 Winter Is Coming 1080p BluRay x264-OFT": 1,
		"Game.of.Thrones.1x01.720p.HDTV":                                1,
		"Game of Thrones S01E01-E03 1080p BluRay":                       2,
		"Game of Thrones S01 1080p BluRay x264":                         2,
		"Game of Thrones Season 1 Complete 1080p":                       2,
		"Game of Thrones S01 Complete 1080p BluRay":                     2,
		"Game of Thrones Seasons 1-8 1080p BluRay":                      3,
		"Game of Thrones S01-S08 Complete Series 1080p":                 3,
	}
	for title, kind := range accept {
		c, ok := tsScore(JackettResult{Title: title, Seeders: 50, Size: 3 * tsGB, InfoHash: strings.Repeat("a", 40)}, gotQuery, titleTokens)
		if !ok || c.kind != kind {
			t.Errorf("%q: ok=%v kind=%d, want kind %d", title, ok, c.kind, kind)
		}
	}
}

func TestTruncatedTitleIsNotDolbyVisionOnly(t *testing.T) {
	q := tsResolveQuery{mediaType: "movie", title: "Dune", year: 2021, hevc: true, hdr: true}
	tok := tsNormalize(q.title)
	full, _ := tsScore(JackettResult{Title: "Dune 2021 2160p WEB-DL DV HDR10 H265-FLUX", Seeders: 50, Size: 15 * tsGB, InfoHash: strings.Repeat("a", 40)}, q, tok)
	cut, _ := tsScore(JackettResult{Title: "Dune 2021 2160p WEB-DL TrueHD 7 1 Atmos DV", Seeders: 50, Size: 15 * tsGB, InfoHash: strings.Repeat("b", 40)}, q, tok)
	dots, _ := tsScore(JackettResult{Title: "Dune 2021 2160p WEB-DL DV H265 Atmos D...", Seeders: 50, Size: 15 * tsGB, InfoHash: strings.Repeat("c", 40)}, q, tok)
	dvOnly, _ := tsScore(JackettResult{Title: "Dune 2021 2160p WEB-DL DV H265-GRP", Seeders: 50, Size: 15 * tsGB, InfoHash: strings.Repeat("d", 40)}, q, tok)
	if cut.score < full.score-30 || dots.score < full.score-30 {
		t.Fatalf("cut-off names penalised as DV-only: full %.0f cut %.0f dots %.0f", full.score, cut.score, dots.score)
	}
	if dvOnly.score > full.score-60 {
		t.Fatalf("a complete DV-only name is no longer penalised: %.0f vs %.0f", dvOnly.score, full.score)
	}
}

func TestLateBetterCandidates(t *testing.T) {
	q := tsResolveQuery{mediaType: "movie", title: "Night of the Living Dead", year: 1968}
	early := fixture(
		20, "Night of the Living Dead 1968 720p BluRay x264", 1.2,
		10, "Night of the Living Dead 1968 480p DVDRip", 0.8,
	)
	checked := tsRankCandidates(early, q)
	late := []JackettResult{
		{Title: "Night of the Living Dead 1968 1080p BluRay x264-GRP", Seeders: 40, Size: 2 * tsGB, Link: "http://jackett/dl/1337x/9"},
		{Title: "Night of the Living Dead 1968 720p BluRay x264 [1337x]", Seeders: 20, Size: gbSize(1.2), Link: "http://jackett/dl/1337x/8"}, // a copy of the checked 720p
		{Title: "Night of the Living Dead 1968 480p XviD", Seeders: 2, Size: tsGB},
	}
	better := tsLateBetter(append(early, late...), q, checked, checked[0], false)
	if len(better) != 1 || !strings.Contains(better[0].raw.Title, "1080p") {
		t.Fatalf("late better: %q, want only the new 1080p", titles(better))
	}
}

func gbSize(g float64) int64 { return int64(g * float64(tsGB)) }
