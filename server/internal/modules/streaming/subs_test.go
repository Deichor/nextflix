package streaming

import (
	"encoding/json"
	"fmt"
	"os"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestSubNameScore(t *testing.T) {
	for _, c := range []struct {
		name       string
		lang, orig string
		want       float64
	}{
		{"Below S01E01 1080p NF WEB-DL DDP5 1 Atmos H 264-FLUX", "tr", "en", 0},
		{"Below S01E01 1080p NF WEB-DL MULTi-SUB DDP5 1 H 264-GRP", "tr", "en", tsSubMultiPart},
		{"Below.S01E01.MULTISUB.1080p.WEB.h264-GRP", "tr", "en", tsSubMultiPart},
		{"Below S01E01 1080p WEB-DL x265 [Multi Subs]", "tr", "en", tsSubMultiPart},
		{"Below S01E01 1080p WEB-DL TR Sub", "tr", "en", tsSubNamePart},
		{"Below.S01E01.1080p.WEB-DL.TRSub.x264", "tr", "en", tsSubNamePart},
		{"Below S01E01 1080p WEB-DL Türkçe Altyazı", "tr", "en", tsSubNamePart},
		{"Below S01E01 1080p WEB-DL TUR ENG", "tr", "en", tsSubNamePart},
		{"Below S01E01 1080p WEB-DL Turkish", "tr", "en", tsSubNamePart},
		{"Below S01E01 1080p WEB-DL [Subs: ENG, TUR, ARA]", "tr", "en", tsSubNamePart},
		{"Below S01E01 1080p WEB-DL [Subs: ENG, ARA] MULTi-SUB", "tr", "en", tsSubMultiPart},
		{"Below S01E01 1080p WEB-DL MULTi", "tr", "en", 0}, // audio, not subtitles
		// The profile's language is the original: subtitles do not matter
		{"Below S01E01 1080p WEB-DL TR Sub", "tr", "tr", 0},
		{"Below S01E01 1080p WEB-DL MULTi-SUB", "en", "en", 0},
		// English needs a sub word next to the code; and a known foreign original
		{"Squid Game S01E01 1080p WEB-DL ENG", "en", "ko", 0},
		{"Squid Game S01E01 1080p WEB-DL ENG Subs", "en", "ko", tsSubNamePart},
		{"Squid Game S01E01 1080p WEB-DL ENG Subs", "en", "", 0},
		{"Squid Game S01E01 1080p WEB-DL MULTi-SUB", "", "ko", 0},
	} {
		rest := tsNormalize(c.name)[1:]
		if c.name[:5] == "Squid" {
			rest = tsNormalize(c.name)[2:]
		}
		if got := tsSubNameScore(rest, c.lang, c.orig); got != c.want {
			t.Errorf("%q (%s, orig %q): %v, want %v", c.name, c.lang, c.orig, got, c.want)
		}
	}
}

func loadProbe(t *testing.T, name string) *tsProbe {
	t.Helper()
	b, err := os.ReadFile("testdata/" + name)
	if err != nil {
		t.Fatal(err)
	}
	var p tsProbe
	if err := json.Unmarshal(b, &p); err != nil {
		t.Fatal(err)
	}
	return &p
}

func TestProbedSubLangs(t *testing.T) {
	// Text tracks only (PGS is an image), forced ones left out, LANGUAGE in upper case and a
	// language named only in the title count
	got := tsProbedSubLangs(loadProbe(t, "ffprobe_multisub.json"))
	if want := []string{"ar", "en", "es", "tr"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("subs %v, want %v", got, want)
	}
	forcedOnly := probeOf(tsProbeStream{CodecType: "subtitle", CodecName: "subrip", Tags: map[string]string{"language": "tur"},
		Disposition: map[string]int{"forced": 1}})
	if got := tsProbedSubLangs(forcedOnly); len(got) != 0 {
		t.Fatalf("forced-only track: %v", got)
	}
	if got := tsProbedSubLangs(nil); got != nil {
		t.Fatalf("nil probe: %v", got)
	}
}

func TestSubFileLang(t *testing.T) {
	for in, want := range map[string]string{
		"Below.S01E01.1080p.WEB.tr.srt":             "tr",
		"Subs/Below.S01E01/24_Turkish.srt":          "tr",
		"Subs/Below.S01E01/3_English.SDH.srt":       "en",
		"Below.S01E01.tur.srt":                      "tr",
		"Below.S01E01.tur.forced.srt":               "",
		"Subs/Turkish/1.srt":                        "tr",
		"Below.S01E01.1080p.WEB.srt":                "",
		"Subs/Below.S01E01/Türkçe.ass":              "tr",
		"Below.S01E01.1080p.WEB-DL.ENG.ARA.TUR.srt": "tr",
	} {
		if got := tsSubFileLang(in); got != want {
			t.Errorf("tsSubFileLang(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestTorrentSubLangs(t *testing.T) {
	single := []tsFileStat{
		{ID: 1, Path: "Below.S01E01.1080p/Below.S01E01.1080p.mkv", Length: 2 << 30},
		{ID: 2, Path: "Below.S01E01.1080p/Subs/2_English.srt", Length: 60 << 10},
		{ID: 3, Path: "Below.S01E01.1080p/Subs/24_Turkish.srt", Length: 60 << 10},
		{ID: 4, Path: "Below.S01E01.1080p/Subs/25_Turkish.forced.srt", Length: 2 << 10},
	}
	if got := tsTorrentSubLangs(single, single[0]); !reflect.DeepEqual(got, []string{"en", "tr"}) {
		t.Fatalf("single video: %v", got)
	}
	pack := []tsFileStat{
		{ID: 1, Path: "Below.S01/Below.S01E01.mkv", Length: 2 << 30},
		{ID: 2, Path: "Below.S01/Below.S01E02.mkv", Length: 2 << 30},
		{ID: 3, Path: "Below.S01/Subs/Below.S01E01/24_Turkish.srt", Length: 60 << 10},
		{ID: 4, Path: "Below.S01/Subs/Below.S01E02/2_English.srt", Length: 60 << 10},
	}
	if got := tsTorrentSubLangs(pack, pack[1]); !reflect.DeepEqual(got, []string{"en"}) {
		t.Fatalf("pack, episode 2: %v", got)
	}
	if got := tsTorrentSubLangs(pack, pack[0]); !reflect.DeepEqual(got, []string{"tr"}) {
		t.Fatalf("pack, episode 1: %v", got)
	}
}

var belowQuery = tsResolveQuery{mediaType: "tv", title: "Below", season: 1, episode: 1, lang: "tr", origLang: "en"}

// The subtitle hint orders releases within a resolution, never across one
func TestSubHintRanking(t *testing.T) {
	raw := fixture(
		200, "Below S01E01 1080p NF WEB-DL DDP5 1 Atmos H 264-FLUX", 2.0,
		180, "Below S01E01 1080p NF WEB-DL MULTi-SUB DDP5 1 Atmos H 264-GRP", 2.0,
		150, "Below S01E01 720p NF WEB-DL TR Sub DDP5 1 H 264-GRP", 1.0,
	)
	c := tsRankAll(raw, belowQuery)
	if len(c) != 3 || !strings.Contains(c[0].raw.Title, "MULTi-SUB") || !strings.Contains(c[2].raw.Title, "720p") {
		t.Fatalf("order %q", titles(c))
	}
	if c[0].parts.Subs != tsSubMultiPart || c[1].parts.Subs != 0 || c[2].parts.Subs != tsSubNamePart {
		t.Fatalf("subs parts %v %v %v", c[0].parts.Subs, c[1].parts.Subs, c[2].parts.Subs)
	}
	// An English profile ranks them by format alone
	en := belowQuery
	en.lang = "en"
	if c := tsRankAll(raw, en); c[0].parts.Subs != 0 || !strings.Contains(c[0].raw.Title, "FLUX") {
		t.Fatalf("english profile: %q", titles(c))
	}
}

// The probe decides: a check whose file carries the subtitle gains, a MULTi-SUB name without
// it loses its hint; the gain reorders good checks of one resolution but never beats health
func TestProbedSubsRanking(t *testing.T) {
	s := NewService(nil, Config{})
	raw := fixture(
		200, "Below S01E01 1080p NF WEB-DL DDP5 1 Atmos H 264-FLUX", 2.0,
		180, "Below S01E01 1080p NF WEB-DL MULTi-SUB DDP5 1 Atmos H 264-GRP", 2.0,
	)
	c := tsRankAll(raw, belowQuery)
	plain, multi := c[1], c[0]
	video := tsFileStat{ID: 1, Path: "Below.S01E01.mkv", Length: 2 << 30}
	check := func(cand tsCandidate, index int, p *tsProbe, keeps bool) *tsResolveResult {
		r := &tsResolveResult{Hash: cand.hash, Index: index, Quality: "1080p", rank: cand.score, durationChecked: true,
			DurationVerified: true, Health: &tsHealth{Rate: 3e6, Need: 1e6, KeepsUp: keeps}}
		if p != nil {
			s.probeCache.Store(fmt.Sprintf("%s:%d", r.Hash, r.Index), p)
		}
		s.applyProbedSubs(r, cand, belowQuery, []tsFileStat{video}, video)
		return r
	}
	noSubs := probeOf(tsProbeStream{CodecType: "video", CodecName: "h264", Width: 1920, Height: 1080})
	// The plain release carries the Turkish track, the MULTi-SUB one does not
	rPlain := check(plain, 1, loadProbe(t, "ffprobe_multisub.json"), true)
	rMulti := check(multi, 1, noSubs, true)
	if rPlain.rankAdj != tsSubProbedPart || rMulti.rankAdj != -tsSubMultiPart {
		t.Fatalf("adjustments %v %v", rPlain.rankAdj, rMulti.rankAdj)
	}
	if !reflect.DeepEqual(rPlain.Format.Subs, []string{"ar", "en", "es", "tr"}) || len(rMulti.Format.Subs) != 0 {
		t.Fatalf("format subs %v / %v", rPlain.Format.Subs, rMulti.Format.Subs)
	}
	results := []*tsResolveResult{rMulti, rPlain} // ranked order before the probe
	if best, _ := tsChoose(results, []bool{true, true}); best != 1 {
		t.Fatalf("the release with the subtitle should take the pick, got %d", best)
	}
	// ... but not when it does not keep up
	rPlain.Health.KeepsUp = false
	if best, _ := tsChoose(results, []bool{true, true}); best != 0 {
		t.Fatalf("a starving release must not win by its subtitle, got %d", best)
	}
	// Without a probe the name's hint stays; a subtitle file next to the video counts
	r := check(multi, 2, nil, true)
	if r.rankAdj != 0 {
		t.Fatalf("unprobed: %v", r.rankAdj)
	}
	srt := tsFileStat{ID: 4, Path: "Subs/24_Turkish.srt", Length: 50 << 10}
	r = &tsResolveResult{Hash: plain.hash, Index: 3, rank: plain.score}
	s.applyProbedSubs(r, plain, belowQuery, []tsFileStat{video, srt}, video)
	if r.rankAdj != tsSubProbedPart || !reflect.DeepEqual(r.Format.Subs, []string{"tr"}) {
		t.Fatalf("subtitle file: %v %v", r.rankAdj, r.Format.Subs)
	}
}

func subAltEntry(hash, quality string, subs []string, keeps bool, at time.Time) tsSubAltEntry {
	return tsSubAltEntry{
		res: tsResolveResult{Hash: hash, Quality: quality, TorrentTitle: "rel " + hash[:4], Format: &tsTags{Subs: subs},
			Health: &tsHealth{Rate: 3e6, Need: 1e6, KeepsUp: keeps}, durationChecked: true, DurationVerified: true},
		caps: "", at: at,
	}
}

func hashN(n int) string { return fmt.Sprintf("%040x", n) }

func TestSubAltPick(t *testing.T) {
	now := time.Now()
	cur := hashN(1)
	q := belowQuery
	known := map[string]tsSubAltEntry{
		cur:      subAltEntry(cur, "1080p", []string{"tr"}, true, now),                      // the playing one
		hashN(2): subAltEntry(hashN(2), "1080p", []string{"en"}, true, now),                 // no Turkish
		hashN(3): subAltEntry(hashN(3), "1080p", []string{"tr"}, false, now),                // starves
		hashN(4): subAltEntry(hashN(4), "1080p", []string{"tr"}, true, now.Add(-time.Hour)), // stale
		hashN(5): subAltEntry(hashN(5), "480p", []string{"tr"}, true, now),                  // too low
	}
	if r := tsSubAltPick(known, "", q, cur, 1080, "tr", now); r != nil {
		t.Fatalf("nothing fits, got %q", r.TorrentTitle)
	}
	known[hashN(6)] = subAltEntry(hashN(6), "720p", []string{"en", "tr"}, true, now)
	if r := tsSubAltPick(known, "", q, cur, 1080, "tr", now); r == nil || r.Hash != hashN(6) {
		t.Fatalf("a 720p one step down should do, got %v", r)
	}
	known[hashN(7)] = subAltEntry(hashN(7), "1080p", []string{"tr"}, true, now)
	if r := tsSubAltPick(known, "", q, cur, 1080, "tr", now); r == nil || r.Hash != hashN(7) {
		t.Fatalf("the playing resolution first, got %v", r)
	}
	// Another client's checks, avoided torrents, unfit files and a cap do not count
	if r := tsSubAltPick(known, "|hevc", q, cur, 1080, "tr", now); r != nil {
		t.Fatalf("other caps: %v", r.Hash)
	}
	avoid := q
	avoid.avoid = []string{hashN(7)}
	if r := tsSubAltPick(known, "", avoid, cur, 1080, "tr", now); r == nil || r.Hash != hashN(6) {
		t.Fatalf("avoided: %v", r)
	}
	e := known[hashN(7)]
	e.res.unfit = "wrong audio"
	known[hashN(7)] = e
	capped := q
	capped.maxQ = 720
	if r := tsSubAltPick(known, "", capped, cur, 1080, "tr", now); r == nil || r.Hash != hashN(6) {
		t.Fatalf("unfit / capped: %v", r)
	}
}

func TestSubAltScanList(t *testing.T) {
	raw := fixture(
		200, "Below S01E01 1080p NF WEB-DL DDP5 1 Atmos H 264-FLUX", 2.0,
		190, "Below S01E01 1080p NF WEB-DL DDP5 1 H 264-NTb", 2.0,
		150, "Below S01E01 1080p WEB-DL MULTi-SUB x264-GRP", 2.0,
		140, "Below S01E01 2160p NF WEB-DL DV HDR DDP5 1 H 265-GRP", 9.0,
		130, "Below S01E01 480p WEB-DL TR Sub x264-GRP", 0.4,
		120, "Below S01E01 720p NF WEB-DL DDP5 1 H 264-GRP", 1.0,
	)
	q := belowQuery
	q.lang = "en" // ranked for another profile: the scan reads the hints for its own language
	ranked := tsRankAll(raw, q)
	known := map[string]tsSubAltEntry{}
	byTitle := map[string]tsCandidate{}
	for _, c := range ranked {
		byTitle[c.raw.Title] = c
	}
	cur := byTitle["Below S01E01 1080p NF WEB-DL DDP5 1 Atmos H 264-FLUX"].hash
	ntb := byTitle["Below S01E01 1080p NF WEB-DL DDP5 1 H 264-NTb"].hash
	known[ntb] = tsSubAltEntry{} // checked already
	q.lang = "tr"
	list := tsSubAltScanList(ranked, known, q, cur, 1080, "tr")
	got := titles(list)
	want := []string{"Below S01E01 1080p WEB-DL MULTi-SUB x264-GRP", "Below S01E01 720p NF WEB-DL DDP5 1 H 264-GRP"}
	if !reflect.DeepEqual(got, want) { // no 4K without hevc, no 480p, not current or checked
		t.Fatalf("scan list %q, want %q", got, want)
	}
}

func TestSubAltScanRateLimit(t *testing.T) {
	saved := tsSubAltScanEvery
	defer func() { tsSubAltScanEvery = saved }()
	tsSubAltScanEvery = time.Hour
	s := NewService(nil, Config{})
	var scans atomic.Int32
	release := make(chan struct{})
	s.subAltScanFn = func(a *tsSubAlt, q tsResolveQuery, current string, curQ int, lang string) {
		scans.Add(1)
		<-release
	}
	cur := hashN(1)
	if r, scanning := s.subaltFor(belowQuery, cur, 1080, "tr"); r != nil || !scanning {
		t.Fatalf("first ask: %v scanning=%v", r, scanning)
	}
	// Another title waits for the one scan slot
	other := belowQuery
	other.episode = 2
	if _, scanning := s.subaltFor(other, cur, 1080, "tr"); scanning {
		t.Fatal("a second title must not scan while one runs")
	}
	if _, scanning := s.subaltFor(belowQuery, cur, 1080, "tr"); !scanning {
		t.Fatal("the running scan should be reported")
	}
	close(release)
	waitFor(t, func() bool {
		a := s.subAltOf(tsResolveBaseKey(belowQuery), false)
		a.mu.Lock()
		defer a.mu.Unlock()
		return !a.scanning
	})
	if _, scanning := s.subaltFor(belowQuery, cur, 1080, "tr"); scanning || scans.Load() != 1 {
		t.Fatalf("scanned again within the limit: %d", scans.Load())
	}
	// The other title gets its turn once the slot is free; a found release answers at once
	if _, scanning := s.subaltFor(other, cur, 1080, "tr"); !scanning {
		t.Fatal("the other title should scan now")
	}
	waitFor(t, func() bool {
		a := s.subAltOf(tsResolveBaseKey(other), false)
		a.mu.Lock()
		defer a.mu.Unlock()
		return scans.Load() == 2 && !a.scanning
	})
	q := belowQuery
	s.noteChecked(q, &tsResolveResult{Hash: hashN(9), Quality: "1080p", Format: &tsTags{Subs: []string{"tr"}},
		Health: &tsHealth{Rate: 3e6, Need: 1e6, KeepsUp: true}, durationChecked: true, DurationVerified: true})
	if r, _ := s.subaltFor(belowQuery, cur, 1080, "tr"); r == nil || r.Hash != hashN(9) {
		t.Fatalf("recorded check not offered: %v", r)
	}
	tsSubAltScanEvery = 0
	if _, scanning := s.subaltFor(belowQuery, cur, 1080, "en"); !scanning {
		t.Fatal("past the limit a new scan starts")
	}
}

func waitFor(t *testing.T, ok func() bool) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for !ok() {
		if time.Now().After(deadline) {
			t.Fatal("timed out")
		}
		time.Sleep(5 * time.Millisecond)
	}
}
