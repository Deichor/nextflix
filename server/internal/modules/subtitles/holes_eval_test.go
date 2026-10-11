package subtitles

import (
	"encoding/json"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

// Hole detection on real data: every pair of subtitles of one file of the snapshot (NFX_TESTDATA),
// both put on the file's timeline by their true offsets (or the aligner where those change),
// the second one as the reference. Prints every hole found and every near miss for review.

type placedSub struct {
	name string
	lang string
	cues []liveCue // on the file's timeline
}

var trLetters = "ğışçöüĞİŞÇÖÜ"

func guessLang(cues []liveCue) string {
	tr, all := 0, 0
	for _, c := range cues {
		for _, r := range c.text {
			all++
			if strings.ContainsRune(trLetters, r) {
				tr++
			}
		}
	}
	if all > 0 && float64(tr)/float64(all) > 0.01 {
		return "tr"
	}
	return "en"
}

// placeSubs puts every subtitle of the file's episode on its timeline.
func placeSubs(f *snapFile, subs []*snapSub) []placedSub {
	var out []placedSub
	for _, s := range subs {
		tr, ok := findTruth(s.spans, f.ref)
		if !ok {
			continue
		}
		var at func(u float64) float64
		if tr.constant {
			at = func(u float64) float64 { return u*tr.scale + tr.c }
		} else {
			fit, ok := align(clampSpans(s.spans, f.ref.Probe.Duration), f.ref.Windows)
			if !ok || !fit.good() {
				continue
			}
			at = fit.mapTime
		}
		p := placedSub{name: s.name, lang: guessLang(s.cues)}
		for _, c := range s.cues {
			d := c.end - c.start
			st := at(c.start)
			p.cues = append(p.cues, liveCue{start: st, end: st + d, text: c.text})
		}
		out = append(out, p)
	}
	return out
}

func short(s string, n int) string {
	s = strings.ReplaceAll(s, "\n", " / ")
	if r := []rune(s); len(r) > n {
		return string(r[:n]) + "…"
	}
	return s
}

// textAround is the subtitle's cue text ending before t and starting after it.
func textAround(cues []liveCue, t float64) (string, string) {
	var before, after string
	for _, c := range cues {
		if c.end <= t+0.01 {
			before = c.text
		}
		if c.start >= t-0.01 && after == "" {
			after = c.text
		}
	}
	return before, after
}

type holeStats struct{ pairs, holes, lines, near int }

// reportPair logs the holes of a against the reference b (one timeline) and the near misses:
// pauses of 10 s or more with 2 or more reference lines that were not flagged.
func reportPair(t *testing.T, tag string, a, b placedSub, ev holeEvidence, duration float64, st *holeStats) {
	st.pairs++
	ref := aiCues(renderVTT(b.cues, false))
	holes := findHoles(a.cues, ref, ev, duration)
	for _, h := range holes {
		st.holes++
		st.lines += h.Lines
		before, after := textAround(a.cues, h.From)
		t.Logf("HOLE %s %s(%s) <- %s(%s): pause %.0f-%.0f (%.0f s), %d ref lines %.0f-%.0f | before %q | after %q",
			tag, a.name, a.lang, b.name, b.lang, h.From, h.To, h.To-h.From, h.Lines, h.First, h.Last, short(before, 40), short(after, 40))
		for _, c := range holeLines(ref, []subHole{h})[:min(3, h.Lines)] {
			t.Logf("     ref %.1f %q", c.start, short(c.text, 60))
		}
	}
	end := duration
	for _, c := range ref {
		end = math.Max(end, c.end+1)
	}
	for _, g := range subGaps(a.cues, end) {
		if g[1]-g[0] < 10 {
			continue
		}
		n := 0
		var texts []string
		for _, c := range ref {
			if m := (c.start + c.end) / 2; holeDialogue(c.text) && m >= g[0]+holeEdge && m <= g[1]-holeEdge {
				n++
				texts = append(texts, short(c.text, 30))
			}
		}
		found := false
		for _, h := range holes {
			found = found || h.From == g[0]
		}
		if n >= 2 && !found {
			st.near++
			before, after := textAround(a.cues, g[0])
			t.Logf("near %s %s <- %s: pause %.0f-%.0f (%.0f s), %d ref lines %q | before %q | after %q", tag, a.name, b.name,
				g[0], g[1], g[1]-g[0], n, texts, short(before, 40), short(after, 40))
		}
	}
}

func placedOf(s *snapSub) placedSub {
	return placedSub{name: s.name, lang: guessLang(s.cues), cues: s.cues}
}

func TestSnapshotHoles(t *testing.T) {
	files, subs := loadSnapshot(t)
	var st holeStats
	// Subtitles fitting a file's own-track windows, on its timeline, with its windows as evidence
	for _, f := range files {
		placed := placeSubs(f, subs)
		if len(placed) < 2 {
			continue
		}
		ev := evidenceOf(mergeWindows(f.ref.Windows, f.live))
		for i, a := range placed {
			for j, b := range placed {
				if i != j {
					reportPair(t, "file "+f.name[:8], a, b, ev, f.ref.Probe.Duration, &st)
				}
			}
		}
	}
	t.Logf("files: %d pairs, %d holes (%d ref lines), %d near misses", st.pairs, st.holes, st.lines, st.near)
	// Every two subtitles of one episode: the first timed onto the second by the aligner
	var all holeStats
	last := func(s *snapSub) float64 { return s.cues[len(s.cues)-1].end }
	for i, a := range subs {
		for j, b := range subs {
			if j <= i || math.Abs(last(a)-last(b)) > 0.12*last(b) {
				continue
			}
			fit, ok := align(clampSpans(a.spans, 0), fullCueWindows(b.spans, syncWindowLen))
			if !ok || !fit.good() || !lengthFits(a.spans, fit, last(b)) {
				continue
			}
			pa := placedOf(a)
			pa.cues = applyFit(a.cues, fit)
			pb := placedOf(b)
			t.Logf("PAIR %s(%s,%d) ~ %s(%s,%d): scale %.4f, %d offsets, score %.2f", a.name, pa.lang, len(a.cues), b.name, pb.lang,
				len(b.cues), fit.Scale, len(fit.Segments), fit.Score)
			reportPair(t, "pair", pa, pb, holeEvidence{}, 0, &all)
			reportPair(t, "pair", pb, pa, holeEvidence{}, 0, &all)
		}
	}
	t.Logf("subtitle pairs: %d directed pairs, %d holes (%d ref lines), %d near misses", all.pairs, all.holes, all.lines, all.near)
}

// TestHolesAvatarRealData: the Turkish "WEB-DL Fenix" subtitle of Avatar: Seven Havens S01E02
// (a machine translation that skipped whole scenes) against the English GRACE subtitle it was
// made from (same timing), both from the snapshot's cache.
func TestHolesAvatarRealData(t *testing.T) {
	root := os.Getenv("NFX_TESTDATA")
	if root == "" {
		t.Skip("NFX_TESTDATA not set")
	}
	read := func(p string) []liveCue {
		b, err := os.ReadFile(filepath.Join(root, "subtitles", p))
		if err != nil {
			t.Skipf("%s not in the snapshot", p)
		}
		return vttCues(b)
	}
	tr := read("turkcealtyazi/053cb0e3d29b4d1f6c718718f3e0812be2805851.vtt")
	en := read("opensubtitles/35bdcbcb6784f15bd95a02bded166ed9502b4398.vtt")
	ref := aiCues(renderVTT(en, false))
	// The file's windows: its own track (read early) and the played audio
	var ev holeEvidence
	duration := 1365.0
	if b, err := os.ReadFile(filepath.Join(root, "subtitles", "syncref", avatarFileKey+".json")); err == nil {
		var r syncRef
		if json.Unmarshal(b, &r) == nil {
			var live []refWindow
			if b, err := os.ReadFile(filepath.Join(root, "subtitles", "synclive", avatarFileKey+".json")); err == nil {
				var s liveSaved
				if json.Unmarshal(b, &s) == nil {
					for w, d := range s.Windows {
						live = append(live, refWindow{Start: float64(w) * syncWindowLen, Kind: "live", Speech: unpackBits(d.Bits, d.Frames)})
					}
				}
			}
			ev, duration = evidenceOf(mergeWindows(r.Windows, live)), r.Probe.Duration
			t.Logf("evidence: %d own-track windows, %d speech windows, %.0f s", len(ev.own), len(ev.speech), duration)
		}
	}
	holes := findHoles(tr, ref, ev, duration)
	var got []string
	for _, h := range holes {
		got = append(got, fmt.Sprintf("%.0f-%.0f:%d", h.First, h.Last, h.Lines))
	}
	t.Logf("holes %v", got)
	// How weak speech is as evidence: the share detected in the holes and in pauses of both
	stretches := [][2]float64{{175, 216}, {614, 652}, {1071, 1110}, {1139, 1171}}
	for _, h := range holes {
		stretches = append(stretches, [2]float64{h.First, h.Last})
	}
	for _, h := range stretches {
		covered, frames, on := 0.0, 0, 0
		for _, w := range ev.speech {
			lo, hi := math.Max(h[0], w.Start), math.Min(h[1], w.end())
			if hi <= lo {
				continue
			}
			covered += hi - lo
			for i := int((lo - w.Start) * syncRate); i < int((hi-w.Start)*syncRate) && i < len(w.Speech); i++ {
				frames++
				if w.Speech[i] != 0 {
					on++
				}
			}
		}
		t.Logf("%.0f-%.0f: %.0f s heard, speech in %.0f%% of it", h[0], h[1], covered, 100*float64(on)/math.Max(1, float64(frames)))
	}
	if len(holes) != 2 || holes[0].First > 553 || holes[0].Last < 613 || holes[1].First > 1266 || holes[1].Last < 1311 {
		t.Fatalf("holes %v, want about 552-614 and 1265-1312", got)
	}
	fill := holeLines(ref, holes)
	if len(fill) < 35 {
		t.Fatalf("%d lines to fill", len(fill))
	}
	merged := append(append([]liveCue(nil), tr...), fill...)
	sort.Slice(merged, func(i, j int) bool { return merged[i].start < merged[j].start })
	for i := 1; i < len(merged); i++ {
		if merged[i].start < merged[i-1].end-0.001 && (isFill(fill, merged[i]) || isFill(fill, merged[i-1])) &&
			!(isFill(fill, merged[i]) && isFill(fill, merged[i-1])) {
			t.Fatalf("filled line overlaps a subtitle line: %v / %v", merged[i-1], merged[i])
		}
	}
	// The pauses where both are silent (175-216, 1071-1110, 1139-1171 s) are no holes
	for _, h := range holes {
		for _, quiet := range [][2]float64{{175, 216}, {1071, 1110}, {1139, 1171}} {
			if h.First < quiet[1] && h.Last > quiet[0] {
				t.Fatalf("silent stretch %v flagged: %+v", quiet, h)
			}
		}
	}
	t.Logf("%d lines would be filled (%d Turkish cues, %d English)", len(fill), len(tr), len(en))
}

// avatarFileKey is the cache key of the S01E02 file (27bcf6ed:1).
const avatarFileKey = "494024e0a04f7b48929f450b1aee615449e07b93"

func isFill(fill []liveCue, c liveCue) bool {
	for _, f := range fill {
		if f == c {
			return true
		}
	}
	return false
}
