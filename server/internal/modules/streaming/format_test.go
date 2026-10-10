package streaming

import (
	"context"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestParseTags(t *testing.T) {
	for _, tc := range []struct {
		title string
		want  tsTags
	}{
		{"A.Knight.of.the.Seven.Kingdoms.S01E01.2160p.HMAX.WEB-DL.DDP5.1.Atmos.DV.HDR.H.265-FLUX",
			tsTags{Audio: "eac3", Channels: 6, Atmos: true, DV: true, HDR10: true, Source: "webdl"}},
		{"A Knight of the Seven Kingdoms S01E01 The Hedge Knight 2160p HMAX WEB-DL DDP5 1 H 265-NTb",
			tsTags{Audio: "eac3", Channels: 6, Source: "webdl"}},
		{"A Knight of the Seven Kingdoms S01E01 2160p HMAX WEB DL DDP5 1 At…",
			tsTags{Audio: "eac3", Channels: 6, Source: "webdl", Truncated: true}},
		{"A Knight of the Seven Kingdoms S01E01 The Hedge Knight 2160p HMAX WEB-DL DD",
			tsTags{Audio: "ac3", Source: "webdl", Truncated: true}},
		{"A Knight of the Seven Kingdoms S01E01 2160p HMAX WEB-DL DV H 265 DDP5 1 Atm",
			tsTags{Audio: "eac3", Channels: 6, DV: true, Source: "webdl", Truncated: true}},
		{"Game of Thrones S01E01 Winter Is Coming 2160p MAX WEB-DL TrueHD 7 1 Atmos DV HDR H 265-FLUX",
			tsTags{Audio: "truehd", Channels: 8, Atmos: true, DV: true, HDR10: true, Source: "webdl"}},
		{"Toy.Story.5.2026.2160p.BluRay.REMUX.DV.P7.HDR.MULTi.TrueHD.Atmos.H265-BTM",
			tsTags{Audio: "truehd", Atmos: true, DV: true, HDR10: true, Source: "remux"}},
		{"Toy Story 5 (2026) [2160p h265 iT WEB-DL DV HDR10+ eng DDP 5.1 Atmos multisub] -BTM",
			tsTags{Audio: "eac3", Channels: 6, Atmos: true, DV: true, HDR10: true, HDR10Plus: true, Source: "webdl"}},
		{"Toy Story 5 (2026) [1080p] [WEBRip] [5.1]", tsTags{Channels: 6, Source: "webrip"}},
		{"Heat.1995.2160p.UHD.BluRay.x265.HDR.DTS-HD.MA.5.1-GRP", tsTags{Audio: "dtshd", Channels: 6, HDR10: true, Source: "bluray"}},
		{"Heat 1995 1080p BluRay DTS x264-STS", tsTags{Audio: "dts", Source: "bluray"}},
		{"Dune 2021 2160p UHD BluRay DTS-X 7.1 HDR x265-GRP", tsTags{Audio: "dtsx", Channels: 8, Atmos: true, HDR10: true, Source: "bluray"}},
		{"Dune.2021.1080p.WEB-DL.E-AC3.5.1.H.264-GRP", tsTags{Audio: "eac3", Channels: 6, Source: "webdl"}},
		{"Dune 2021 1080p WEBRip x264 AAC2.0-GRP", tsTags{Audio: "aac", Channels: 2, Source: "webrip"}},
		{"Dune 2021 1080p WEBRip x265 6CH-GRP", tsTags{Channels: 6, Source: "webrip"}},
		{"Dune 2021 1080p BluRay DD+7.1 x264-GRP", tsTags{Audio: "eac3", Channels: 8, Source: "bluray"}},
		{"Dune 2021 1080p BluRay AC3 5.1 x264-GRP", tsTags{Audio: "ac3", Channels: 6, Source: "bluray"}},
		{"Dune 2021 720p HDTV x264-GRP", tsTags{Source: "hdtv"}},
		{"Tears of Steel (2012) DMRip [4k version] [Hi10P, Hi444PP]", tsTags{Source: "hdrip", Undecodable: true}},
		{"Anime S01E01 1080p BluRay 10bit x264 FLAC-GRP", tsTags{Audio: "flac", Source: "bluray", Undecodable: true}},
	} {
		got := tsParseTags(tc.title)
		got.codecs, got.audioCut = nil, false
		if !reflect.DeepEqual(got, tc.want) {
			t.Errorf("%q:\n got %+v\nwant %+v", tc.title, got, tc.want)
		}
	}
}

func TestLooksTruncated(t *testing.T) {
	for title, want := range map[string]bool{
		"A Knight of the Seven Kingdoms S01E01 2160p HMAX WEB DL DDP5 1 At   ":             true,
		"A Knight of the Seven Kingdoms S01E01 The Hedge Knight 2160p HMAX WEB-DL DD":      true,
		"A Knight of the Seven Kingdoms S01E01 2160p HMAX WEB-DL H 265 DDP5 1 Atmos-":      true,
		"A Knight of the Seven Kingdoms S01E01 The Hedge Knight 720p HMAX WEB DL DDP5 1 …": true,
		"A Knight of the Seven Kingdoms S01E01 The Hedge Knight 2160p HMAX   ":             true,
		"Show S01E01 1080p WEB-DL":                                                     true,
		"Dune 2021 2160p WEB-DL DV H265 Atmos D...":                                    true,
		"A Knight of the Seven Kingdoms S01E01 2160p HMAX WEB-DL DDP5 1 H 265-NTb":     false,
		"Game.of.Thrones.S01E01.2160p.UHD.BluRay.x265-SCOTLUHD[eztv.io].mkv[eztvx.to]": false,
		"Toy Story 5 (2026) [1080p] [WEBRip] [5.1]":                                    false,
		"Heat 1995 1080p BluRay DTS x264-STS":                                          false,
		"Night of the Living Dead 1968 480p DVDRip":                                    false,
	} {
		if got := tsLooksTruncated(title); got != want {
			t.Errorf("%q: %v, want %v", title, got, want)
		}
	}
}

// kr builds a Jackett result with an optional info hash.
func kr(title string, seeders int, size int64, hash string) JackettResult {
	return JackettResult{Title: title, Seeders: seeders, Size: size, InfoHash: hash}
}

// Live Jackett results for "A Knight of the Seven Kingdoms" S01E01 (2026-10-08): the same
// torrents listed by several indexers, some of which cut the names off.
var knightS01E01 = []JackettResult{
	kr("A.Knight.of.the.Seven.Kingdoms.S01E01.The.Hedge.Knight.2160p.HMAX.WEB-DL.DDP5.1.Atmos.DV.HDR.H.265-FLUX.mkv", 148, 6120328396, "7f025bf7f5d776c05835fca092af71fd17f555ff"),
	kr("A Knight of the Seven Kingdoms S01E01 2160p HMAX WEB DL DDP5 1 At ", 188, 6120328396, ""),
	kr("A Knight of the Seven Kingdoms S01E01 The Hedge Knight 2160p HMAX WEB-DL DD", 142, 3103113871, "54F05A5E9C55E90BD2EEB24FADD5726BC51D6295"),
	kr("www.UIndex.org - A Knight of the Seven Kingdoms S01E01 The Hedge Knight 2160p HMAX WEB-DL DDP5 1 H 265-NTb", 304, 3103113871, "54f05a5e9c55e90bd2eeb24fadd5726bc51d6295"),
	kr("A Knight of the Seven Kingdoms S01E01 The Hedge Knight 2160p HMAX WEB-DL DD", 1867, 3113851289, ""),
	kr("www.UIndex.org - A Knight of the Seven Kingdoms S01E01 The Hedge Knight 2160p HMAX WEB-DL DDP5 1 DV H 265-NTb", 60, 6420976107, "9e1446ccf49f37eddfedcce9a7377ae0f136b495"),
	kr("A Knight of the Seven Kingdoms S01E01 The Hedge Knight 2160p HMAX WEB-DL DD", 582, 6442450944, ""),
	kr("A Knight of the Seven Kingdoms S01E01 2160p HMAX WEB-DL DV H 265 DDP5 1 Atm", 123, 6439890517, "D167C6DBFDEDD1EFC8F4A902A41592B0442F5FB9"),
	kr("A Knight of the Seven Kingdoms S01E01 2160p HMAX WEB-DL H 265 DDP5 1 Atmos-", 92, 3126358274, "02D4F7DF5544CF2456C56F8ADD2ECC2A4ABC0942"),
	kr("A Knight of the Seven Kingdoms S01E01 The Hedge Knight REPACK 1080p AMZN WEB-DL DDP5 1 Atmos H 264-FLUX", 133, 2740158668, "22C0A0DC89AF36BF2A718FE560AB30849851DA3C"),
	kr("A Knight of the Seven Kingdoms S01E01 The Hedge Knight REPACK 1080p AMZN WEB DL DDP5 1 Atmos H 264 FLUX EZTV", 2896, 2740158668, "22C0A0DC89AF36BF2A718FE560AB30849851DA3C"),
}

var (
	knightQuery = tsResolveQuery{mediaType: "tv", title: "A Knight of the Seven Kingdoms", season: 1, episode: 1, runtime: 42, hevc: true, hdr: true}
	knightPC    = func() tsResolveQuery {
		q := knightQuery
		q.acodecs = tsParseAudioCodecs("aac,opus,flac,mp3", true)
		return q
	}()
	knightTV = func() tsResolveQuery {
		q := knightQuery
		q.dv, q.atmos = true, true
		q.acodecs = tsParseAudioCodecs("aac,ac3,eac3,opus", true)
		return q
	}()
)

func TestRankKnightPCAndTV(t *testing.T) {
	flux := "A.Knight.of.the.Seven.Kingdoms.S01E01.The.Hedge.Knight.2160p.HMAX.WEB-DL.DDP5.1.Atmos.DV.HDR.H.265-FLUX.mkv"
	for name, q := range map[string]tsResolveQuery{"pc": knightPC, "tv": knightTV} {
		c := tsRankAll(knightS01E01, q)
		if len(c) == 0 || c[0].raw.Title != flux {
			t.Fatalf("%s: want the Atmos DV HDR FLUX first, got %q", name, titles(c))
		}
		// Cut-off copies took the full name of their torrent and merged into it
		for _, cand := range c {
			if strings.HasSuffix(strings.TrimSpace(cand.raw.Title), " DD") || strings.HasSuffix(strings.TrimSpace(cand.raw.Title), " At") {
				t.Fatalf("%s: a cut-off name survived: %q", name, titles(c))
			}
		}
		if c[0].quality != 2160 {
			t.Fatalf("%s: resolution lost", name)
		}
	}
	// PC: the DV-only NTb (profile 5) shows wrong colours in a browser, the TV plays it
	pc, tv := tsRankAll(knightS01E01, knightPC), tsRankAll(knightS01E01, knightTV)
	dvOnly := func(c []tsCandidate) int {
		for i, cand := range c {
			if strings.Contains(cand.raw.Title, "DDP5 1 DV H 265-NTb") {
				return i
			}
		}
		return -1
	}
	if i := dvOnly(pc); i < 0 || pc[i].parts.Penalty > -90 {
		t.Fatalf("pc: DV-only NTb not penalised: %q", titles(pc))
	}
	if i := dvOnly(tv); i < 0 || i > 2 {
		t.Fatalf("tv: DV-only NTb ranked #%d: %q", i+1, titles(tv))
	}
}

// The case that picked NTb: its 1867 seeders do not outweigh a better format with 505.
func TestRankFormatBeatsBiggerSwarmInTier(t *testing.T) {
	raw := []JackettResult{
		kr("A Knight of the Seven Kingdoms S01E01 The Hedge Knight 2160p HMAX WEB-DL DDP5 1 H 265-NTb", 1867, 3_330_000_000, hx('1')),
		kr("A.Knight.of.the.Seven.Kingdoms.S01E01.2160p.HMAX.WEB-DL.DDP5.1.Atmos.DV.HDR.H.265-FLUX", 505, 6_660_000_000, hx('2')),
		kr("A Knight of the Seven Kingdoms S01E01 1080p WEB-DL DDP5 1 Atmos H 264-GRP", 4000, 2_700_000_000, hx('3')),
	}
	for name, q := range map[string]tsResolveQuery{"pc": knightPC, "tv": knightTV} {
		c := tsRankAll(raw, q)
		if !strings.Contains(c[0].raw.Title, "FLUX") || c[2].quality != 1080 {
			t.Fatalf("%s: got %q", name, titles(c))
		}
	}
	// A format-preferred release with almost no seeders does not block a healthy one
	thin := append([]JackettResult(nil), raw...)
	thin[1].Seeders = 3
	if c := tsRankAll(thin, knightTV); !strings.Contains(c[0].raw.Title, "NTb") {
		t.Fatalf("thin FLUX beat a big NTb swarm: %q", titles(c))
	}
	// A 1080p never wins on format alone
	for _, q := range []tsResolveQuery{knightPC, knightTV} {
		if c := tsRankAll(raw[1:], q); c[0].quality != 2160 {
			t.Fatalf("1080p Atmos beat a 4K: %q", titles(c))
		}
	}
}

func TestAudioScoreByCaps(t *testing.T) {
	score := func(title string, q tsResolveQuery) float64 { return tsAudioScore(tsParseTags(title), q) }
	atmos, ddp51, ddp71, stereo := "X 2160p DDP5.1 Atmos-G", "X 2160p DDP5.1-G", "X 2160p DDP7.1-G", "X 2160p AAC2.0-G"
	truehd := "X 2160p TrueHD 7.1 Atmos-G"
	tv := knightTV
	if !(score(atmos, tv) > score(ddp71, tv) && score(ddp71, tv) > score(ddp51, tv) && score(ddp51, tv) > score(stereo, tv)) {
		t.Fatal("tv: want Atmos > 7.1 > 5.1 > 2.0")
	}
	// TrueHD only counts as native when declared
	if score(truehd, tv) >= score(ddp51, tv) {
		t.Fatalf("tv without truehd: TrueHD Atmos %.0f >= DDP 5.1 %.0f", score(truehd, tv), score(ddp51, tv))
	}
	tv.acodecs = tsParseAudioCodecs("aac,eac3,truehd", true)
	if score(truehd, tv) != score(atmos, tv) {
		t.Fatal("tv with truehd: TrueHD Atmos should rank like DDP Atmos")
	}
	// PC: multichannel > stereo, Atmos is just 5.1
	pc := knightPC
	if score(atmos, pc) != score(ddp51, pc) || score(ddp51, pc) <= score(stereo, pc) || score(truehd, pc) != score(ddp51, pc) {
		t.Fatal("pc: want Atmos = 7.1 = 5.1 > 2.0")
	}
	// eac3 without atmos=1 ranks like a PC
	eac3 := knightPC
	eac3.acodecs = tsParseAudioCodecs("aac,eac3", true)
	if score(atmos, eac3) != score(ddp51, eac3) {
		t.Fatal("eac3 without atmos: Atmos must not count")
	}
	// A name cut in its audio tag is unknown, not 5.1
	if got := score("X 2160p HMAX WEB-DL DD", pc); got != 2 {
		t.Fatalf("cut audio: %.0f", got)
	}
}

func TestHDRScoreByCaps(t *testing.T) {
	hdr := func(title string, q tsResolveQuery) float64 {
		s, p := tsHDRScore(tsParseTags(title), q, tsMaybeTruncatedDV(title))
		return s + p
	}
	dvHDR, hdr10, dvOnly, sdr := "X 2160p DV HDR H265-G", "X 2160p HDR10 H265-G", "X 2160p DV H265-G", "X 2160p H265-G"
	tv, pc := knightTV, knightPC
	if !(hdr(dvHDR, tv) > hdr(hdr10, tv) && hdr(dvOnly, tv) == hdr(dvHDR, tv) && hdr(hdr10, tv) > hdr(sdr, tv)) {
		t.Fatal("dv client: want DV > HDR10 > SDR, DV-only fine")
	}
	if !(hdr(dvHDR, pc) == hdr(hdr10, pc) && hdr(hdr10, pc) > hdr(sdr, pc) && hdr(dvOnly, pc) <= -90) {
		t.Fatal("browser: want DV+HDR10 = HDR10 > SDR, DV-only penalised")
	}
	pc.hdr = false
	if hdr(hdr10, pc) >= hdr(sdr, pc) {
		t.Fatal("SDR display: HDR is tone-mapped, SDR should win")
	}
}

func TestRateScore(t *testing.T) {
	small := tsRateScore(3_100_000_000, 2160, 42, "tv", 1)
	big := tsRateScore(6_200_000_000, 2160, 42, "tv", 1)
	huge := tsRateScore(40_000_000_000, 2160, 42, "tv", 1)
	if !(big > small && big > huge) || tsRateScore(50*tsGB, 2160, 42, "tv", 2) != 0 {
		t.Fatalf("rate scores: small %.1f big %.1f huge %.1f", small, big, huge)
	}
}

func TestFullerTitles(t *testing.T) {
	got := tsFullerTitles(knightS01E01)
	want := map[int]string{
		1: knightS01E01[0].Title, // LimeTorrents "... DDP5 1 At": same size, continues in the FLUX name
		2: knightS01E01[3].Title, // same hash: the NTb name
		4: knightS01E01[3].Title, // TGx without a hash, 1867 seeders: same size (within 2 %), continues
		6: knightS01E01[5].Title,
		8: knightS01E01[8].Title, // different hash from the NTb of nearly the same size: kept
	}
	for i, w := range want {
		if got[i] != w {
			t.Errorf("#%d %q -> %q, want %q", i, knightS01E01[i].Title, got[i], w)
		}
	}
	if !tsContinues(strings.Fields(tsStem(knightS01E01[0].Title)), strings.Fields(tsStem(knightS01E01[1].Title))) {
		t.Fatal("the cut name does not continue in the full one")
	}
	if tsContinues(strings.Fields(tsStem(knightS01E01[3].Title)), strings.Fields(tsStem(knightS01E01[8].Title))) {
		t.Fatal("another release matched")
	}
}

func probeOf(streams ...tsProbeStream) *tsProbe { return &tsProbe{Streams: streams} }

func dvVideo(profile, compat int, trc string) tsProbeStream {
	v := tsProbeStream{CodecType: "video", CodecName: "hevc", PixFmt: "yuv420p10le", Width: 3840, Height: 2160, ColorTransfer: trc}
	if profile > 0 {
		v.SideData = append(v.SideData, struct {
			Type      string `json:"side_data_type"`
			DVProfile int    `json:"dv_profile"`
			BLCompat  int    `json:"dv_bl_signal_compatibility_id"`
		}{Type: "DOVI configuration record", DVProfile: profile, BLCompat: compat})
	}
	return v
}

func TestProbedTags(t *testing.T) {
	eac3 := tsProbeStream{CodecType: "audio", CodecName: "eac3", Channels: 6, Tags: map[string]string{"title": "English DDP 5.1 Atmos"}}
	stereo := tsProbeStream{CodecType: "audio", CodecName: "aac", Channels: 2}
	p5, _ := tsProbedTags(probeOf(dvVideo(5, 0, ""), stereo, eac3))
	if !p5.DV || !p5.DVOnly || p5.HDR10 || p5.Audio != "eac3" || p5.Channels != 6 || !p5.Atmos {
		t.Fatalf("profile 5: %+v", p5)
	}
	p8, _ := tsProbedTags(probeOf(dvVideo(8, 1, "smpte2084"), tsProbeStream{CodecType: "audio", CodecName: "truehd", Channels: 8, Profile: "Dolby TrueHD + Dolby Atmos"}))
	if !p8.DV || p8.DVOnly || !p8.HDR10 || p8.Audio != "truehd" || p8.Channels != 8 || !p8.Atmos {
		t.Fatalf("profile 8.1: %+v", p8)
	}
	hi10, _ := tsProbedTags(probeOf(tsProbeStream{CodecType: "video", CodecName: "h264", PixFmt: "yuv444p10le"}))
	if !hi10.Undecodable {
		t.Fatal("Hi444PP decodable")
	}

	// A cut-off name gets the probed audio; a full one keeps its own
	cut := tsParseTags("A Knight of the Seven Kingdoms S01E01 The Hedge Knight 2160p HMAX WEB-DL DD")
	if m := tsMergeProbed(cut, p8); m.Audio != "truehd" || m.Channels != 8 || !m.Atmos || !m.HDR10 || !m.Probed {
		t.Fatalf("merged cut name: %+v", m)
	}
	full := tsParseTags("X 2160p WEB-DL DDP5.1 DV HDR H265-GRP")
	if m := tsMergeProbed(full, p5); m.Audio != "eac3" || !m.DVOnly || m.HDR10 {
		t.Fatalf("merged full name with a profile 5 file: %+v", m)
	}
}

// HDR follows the file both ways: a name that claims HDR on an SDR file loses it.
func TestMergeProbedHDRFollowsFile(t *testing.T) {
	name := tsParseTags("X 2160p WEB-DL DDP5.1 DV HDR10+ H265-GRP")
	if !name.DV || !name.HDR10 || !name.HDR10Plus {
		t.Fatalf("name tags %+v", name)
	}
	sdr, _ := tsProbedTags(probeOf(dvVideo(0, 0, "")))
	if m := tsMergeProbed(name, sdr); m.DV || m.HDR10 || m.HDR10Plus || m.DVOnly {
		t.Fatalf("merged with an SDR file: %+v", m)
	}
	hdr10, _ := tsProbedTags(probeOf(dvVideo(0, 0, "smpte2084")))
	if m := tsMergeProbed(name, hdr10); m.DV || !m.HDR10 || !m.HDR10Plus {
		t.Fatalf("merged with an HDR10 file: %+v", m)
	}
	// An audio-only probe says nothing about the video
	audio, _ := tsProbedTags(probeOf(tsProbeStream{CodecType: "audio", CodecName: "eac3", Channels: 6}))
	if m := tsMergeProbed(name, audio); !m.DV || !m.HDR10 || !m.HDR10Plus {
		t.Fatalf("merged with an audio-only probe: %+v", m)
	}
}

// The probe corrects a cut-off name: the rank follows and a DV profile 5 file is unfit for
// a browser, fine for a DV TV.
func TestApplyProbedFormat(t *testing.T) {
	s := NewService(nil, Config{})
	raw := kr("A Knight of the Seven Kingdoms S01E01 The Hedge Knight 2160p HMAX WEB-DL DD", 50, 3*tsGB, hx('d'))
	c, ok := tsScore(raw, knightTV, tsNormalize(knightTV.title))
	if !ok {
		t.Fatal("not scored")
	}
	s.probeCache.Store(hx('d')+":0", probeOf(dvVideo(5, 0, ""),
		tsProbeStream{CodecType: "audio", CodecName: "eac3", Channels: 6, Tags: map[string]string{"title": "Atmos"}}))
	for _, tc := range []struct {
		name  string
		q     tsResolveQuery
		unfit bool
		up    bool
	}{{"tv", knightTV, false, true}, {"pc", knightPC, true, false}} {
		c, _ := tsScore(raw, tc.q, tsNormalize(tc.q.title))
		r := &tsResolveResult{Hash: hx('d'), Index: 0, rank: c.score}
		s.applyProbedFormat(r, c, tc.q)
		if (r.unfit != "") != tc.unfit || (r.rank > c.score) != tc.up || r.Format == nil || !r.Format.Probed || !r.HDR {
			t.Fatalf("%s: unfit %q rank %.1f -> %.1f format %+v", tc.name, r.unfit, c.score, r.rank, r.Format)
		}
	}
	_ = c
}

func TestChooseProbeReorderAndUnfit(t *testing.T) {
	a := measured("a", "2160p", 6, 2)
	b := measured("b", "2160p", 6, 2)
	a.rank, b.rank = 180, 175
	b.rankAdj, b.rank = 9, 184 // the probe found Atmos in b's cut-off name
	if i, _ := tsChoose([]*tsResolveResult{a, b}, []bool{true, true}); i != 1 {
		t.Fatalf("got %d, want the probe-corrected b", i)
	}
	// A running b is not waited for
	if i, final := tsChoose([]*tsResolveResult{a, b}, []bool{true, false}); i != 0 || !final {
		t.Fatalf("got %d final=%v", i, final)
	}
	// Not for a different resolution or an avoided torrent
	b.Quality = "1080p"
	if i, _ := tsChoose([]*tsResolveResult{a, b}, []bool{true, true}); i != 0 {
		t.Fatal("a probed format beat a resolution")
	}
	b.Quality, b.avoided = "2160p", true
	if i, _ := tsChoose([]*tsResolveResult{a, b}, []bool{true, true}); i != 0 {
		t.Fatal("an avoided torrent came back")
	}

	// Unfit (DV profile 5 on a browser): any other good one wins, even a lower resolution,
	// and it is not final while others run
	p5 := measured("p5", "2160p", 6, 2)
	p5.unfit = "dv5"
	hd := measured("hd", "1080p", 3, 1)
	if i, _ := tsChoose([]*tsResolveResult{p5, hd}, []bool{true, true}); i != 1 {
		t.Fatal("unfit file picked over a good one")
	}
	if i, final := tsChoose([]*tsResolveResult{p5, nil}, []bool{true, false}); i != 0 || final {
		t.Fatalf("alone: got %d final=%v, want it but not final", i, final)
	}
	if i, final := tsChoose([]*tsResolveResult{p5, nil}, []bool{true, true}); i != 0 || !final {
		t.Fatalf("all done: got %d final=%v", i, final)
	}
}

// snap is a running check's snapshot whose duration is not checked yet, with a health
// verdict (sampled < 0) or sampled seconds of a live health.
func snap(name, quality string, rate, need float64, sampled time.Duration) *tsResolveResult {
	r := measured(name, quality, rate, need)
	r.partial, r.durationChecked = true, true
	if sampled >= 0 {
		r.Health.partial, r.Health.KeepsUp, r.Health.sampled = true, false, sampled
	}
	return r
}

// Sintel: a dead 4K with a verdict and a verified duration was picked over a 1080p
// delivering 0.20 of its need whose duration was still being probed.
func TestCapSintelPrefersPlayable1080p(t *testing.T) {
	dead4K := measured("4k", "2160p", 0.01, 5.12)
	dead4K.DurationVerified, dead4K.durationChecked = true, true
	hd := snap("hd", "1080p", 0.2*0.9, 0.9, -1)
	if i := tsChoosePlayable([]*tsResolveResult{dead4K, hd}); i != 1 {
		t.Fatalf("got %d, want the 1080p", i)
	}

	ms := time.Millisecond
	shortCap(t, 60*ms)
	live := newLive(2)
	p := tsPickBestKeep(context.Background(), nil, 2, func(int) time.Duration { return time.Second }, nil,
		func(ctx context.Context, i int) (*tsResolveResult, error) {
			if i == 0 {
				return dead4K, nil
			}
			live.set(1, hd)
			<-ctx.Done()
			return nil, ctx.Err()
		}, live)
	if p.best != 1 || !p.live || !p.kept[1] {
		t.Fatalf("got #%d live=%v, want the 1080p snapshot", p.best, p.live)
	}
	p.stop(1)
}

// Tears of Steel: a 4096x1714 Hi444PP 4K needing 15.66 MB/s at 0.00 was picked. It is
// unfit (no browser decodes it) and below 0.3 against a 1080p delivering more.
func TestCapTearsOfSteelAvoidsUndecodable4K(t *testing.T) {
	hi444 := measured("hi444", "2160p", 0, 15.66)
	hi444.unfit = "hi444pp"
	hd := measured("hd", "1080p", 0.06, 1.2)
	sd := snap("sd", "720p", 0, 0.6, 500*time.Millisecond) // too few samples to count
	if i := tsChoosePlayable([]*tsResolveResult{hi444, hd, sd}); i != 1 {
		t.Fatalf("got %d, want the 1080p", i)
	}
	// Even a fit 4K at 0.25 loses to a 1080p with a higher ratio
	uhd := measured("uhd", "2160p", 0.25*6, 6)
	hd27 := measured("hd", "1080p", 0.27*1.5, 1.5)
	if i := tsChoosePlayable([]*tsResolveResult{uhd, hd27}); i != 1 {
		t.Fatal("a starving 4K beat a 1080p with a higher ratio")
	}
	// Charade: 4K at 0.44 against a 1080p at 0.6: close ratios, the lower need wins
	uhd44 := measured("uhd", "2160p", 0.44*6, 6)
	hd60 := measured("hd", "1080p", 0.6*1.5, 1.5)
	if i := tsChoosePlayable([]*tsResolveResult{uhd44, hd60}); i != 1 {
		t.Fatal("the 4K at 0.44 beat the 1080p at 0.6")
	}
	// The name already ranks it low
	raw := fixture(
		50, "Tears of Steel (2012) DMRip [4k version] [Hi10P, Hi444PP]", 11.3,
		50, "Tears of Steel 2012 1080p BluRay x264", 1.5,
	)
	q := tsResolveQuery{mediaType: "movie", title: "Tears of Steel", year: 2012, hevc: true}
	if c := tsRankAll(raw, q); len(c) != 2 || c[0].quality != 1080 {
		t.Fatalf("got %q", titles(c))
	}
}

// Nothing measured at the cap (metadata only, a second of samples): resolve waits up to
// tsResolveCapExtra for a real measurement instead of answering blind.
func TestCapWaitsForAMeasurement(t *testing.T) {
	ms := time.Millisecond
	shortCap(t, 40*ms)
	tsResolveCapExtra = 400 * ms
	live := newLive(2)
	start := time.Now()
	p := tsPickBestKeep(context.Background(), nil, 2, func(int) time.Duration { return time.Second }, nil,
		func(ctx context.Context, i int) (*tsResolveResult, error) {
			if i == 0 {
				live.set(0, snap("a", "2160p", 0.9*5, 5, time.Second))
			} else {
				time.Sleep(120 * ms)
				live.set(1, snap("b", "1080p", 0.5, 1, 3*time.Second))
			}
			<-ctx.Done()
			return nil, ctx.Err()
		}, live)
	took := time.Since(start)
	if p.best != 1 || took < 100*ms || took > 350*ms {
		t.Fatalf("got #%d after %s, want the measured #1 once it arrives", p.best, took)
	}
	p.stop(1)

	// Still nothing measured when the extra time ends: the most playable snapshot answers
	live = newLive(1)
	start = time.Now()
	p = tsPickBestKeep(context.Background(), nil, 1, func(int) time.Duration { return time.Second }, nil,
		func(ctx context.Context, i int) (*tsResolveResult, error) {
			live.set(0, snap("a", "1080p", 0.3, 1, time.Second))
			<-ctx.Done()
			return nil, ctx.Err()
		}, live)
	if took := time.Since(start); p.best != 0 || took < 400*ms || took > 700*ms {
		t.Fatalf("got #%d after %s, want #0 at cap + extra", p.best, took)
	}
	p.stop(0)
}

// A starving 4K pick: a 1080p that keeps up becomes the cached source and a spare, and
// sidegrade offers it although it is below the playing resolution.
func TestStarvingPickGetsLowerSidegrade(t *testing.T) {
	s, _ := newWarmService(t)
	q := tsResolveQuery{mediaType: "movie", title: "Sintel", year: 2010, hevc: true}
	key := tsResolveKey(q)
	q.warm = s.holdWarm(key)
	pickRes := result(hx('a'), "2160p", 0.05)
	alt := result(hx('b'), "1080p", 3)
	var stopped atomic.Bool
	later := make(chan tsOutcome, 1)
	p := &tsPick{best: 0, res: pickRes, live: true, results: []*tsResolveResult{nil, nil}, kept: []bool{true, true},
		errs: make([]error, 2), later: later, cancels: []context.CancelFunc{func() { stopped.Store(true) }, func() {}}}
	s.storeSource(key, *pickRes)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	s.afterPick(key, pickRes, &tsUpgradeSeed{q: q, cands: []tsCandidate{{quality: 2160}, {quality: 1080}}, pick: p, ctx: ctx, cancel: cancel})
	later <- tsOutcome{i: 1, res: alt}
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		if c, _ := s.cachedSource(key); c != nil && c.Hash == alt.Hash {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	if c, _ := s.cachedSource(key); c == nil || c.Hash != alt.Hash {
		t.Fatalf("cached source %v, want the 1080p that keeps up", c)
	}
	if got := s.sidegradeFor(key, tsResolveQuery{}, hx('a'), 2160, tsSideOpts{}); got == nil || got.Hash != alt.Hash {
		t.Fatalf("sidegrade = %v", got)
	}
	// A healthy source never gets a lower one
	if tsLowerSpare(alt, measured("x", "2160p", 9, 1)) {
		t.Fatal("lower spare for a healthy pick")
	}
}
