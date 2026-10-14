package subtitles

import (
	"fmt"
	"math"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"testing"
)

// Evaluation of the line refinement (sync_refine.go) on real data (NFX_TESTDATA, skipped
// otherwise): the signed timing error of single lines against the file's own track, as the
// piecewise fit serves them and as refined. Positive errors are late, negative early. Lines are
// scored in three groups:
//   - held: an own-track window is held out of the reference in turn (fit and refinement run
//     without it; its stretch has only the played audio then, as most of a file has), so these
//     are the lines the refinement knows nothing exact about;
//   - extra: own-track lines read only for the evaluation (NFX_GT, see .claude/tmp/gt);
//   - inref: lines inside own-track windows of the reference, which the refinement may use.
// Each line also counts by where it lies in its fit segment (first, middle or last third), to
// show drift within segments.

// lineErr is one line's error before and after the refinement and its place in its segment.
type lineErr struct {
	before, after float64
	third         int
}

type signedStats struct {
	n                     int
	medAbs, p10, p50, p90 float64
	early, late           float64 // shares beyond 0.15 s
	w015, w03, w05        float64
	moved, worse, better  float64 // worse / better: moved 0.15 s away from / towards the truth
}

func signedOf(errs, before []float64) signedStats {
	s := signedStats{n: len(errs)}
	if s.n == 0 {
		return s
	}
	sorted := append([]float64(nil), errs...)
	sort.Float64s(sorted)
	q := func(p float64) float64 { return sorted[int(math.Round(p*float64(len(sorted)-1)))] }
	s.p10, s.p50, s.p90 = q(0.1), q(0.5), q(0.9)
	var abs []float64
	n := float64(len(errs))
	for _, e := range errs {
		abs = append(abs, math.Abs(e))
		switch {
		case e < -0.15:
			s.early++
		case e > 0.15:
			s.late++
		}
		if math.Abs(e) <= 0.15+1e-9 {
			s.w015++
		}
		if math.Abs(e) <= 0.3+1e-9 {
			s.w03++
		}
		if math.Abs(e) <= 0.5+1e-9 {
			s.w05++
		}
	}
	s.medAbs = median(abs)
	s.early, s.late, s.w015, s.w03, s.w05 = s.early/n, s.late/n, s.w015/n, s.w03/n, s.w05/n
	if before != nil {
		for i, e := range errs {
			if math.Abs(e-before[i]) > 1e-6 {
				s.moved++
			}
			switch d := math.Abs(e) - math.Abs(before[i]); {
			case d > 0.15:
				s.worse++
			case d < -0.15:
				s.better++
			}
		}
		s.moved, s.worse, s.better = s.moved/n, s.worse/n, s.better/n
	}
	return s
}

func (s signedStats) String() string {
	if s.n == 0 {
		return "n=   0"
	}
	return fmt.Sprintf("n=%4d |med| %.2f p10/50/90 %+.2f/%+.2f/%+.2f early %3.0f%% late %3.0f%% ≤.15 %3.0f%% ≤.3 %3.0f%% ≤.5 %3.0f%%",
		s.n, s.medAbs, s.p10, s.p50, s.p90, 100*s.early, 100*s.late, 100*s.w015, 100*s.w03, 100*s.w05)
}

func (s signedStats) short() string {
	return fmt.Sprintf("|med| %.2f %+.2f/%+.2f/%+.2f e%3.0f%% l%3.0f%% %3.0f%% %3.0f%% %3.0f%% (moved %2.0f%%, better %2.0f%%, worse %2.0f%%)",
		s.medAbs, s.p10, s.p50, s.p90, 100*s.early, 100*s.late, 100*s.w015, 100*s.w03, 100*s.w05, 100*s.moved, 100*s.better, 100*s.worse)
}

// linePair is a cue (index into the subtitle) and the own-track line start it shows.
type linePair struct {
	cue  int
	want float64
}

// linePairs pairs cues with the own-track line starts of [lo, hi). Each cue is predicted at
// pred(i) moved by the stretch's offset (the shift that puts most predicted starts within
// 0.15 s of own starts, refined to the median of the near ones); a pair is the own start
// nearest to the prediction within 1.5 s, at least 0.5 s nearer than the next one, and no
// other cue is nearer to it. The pairs depend on the cue, not on where it is served, so the
// same pairs score the fit and the refinement. Without text a cue may still pair with the
// wrong line where the subtitle splits lines differently, so errors near 1 s are partly that.
func linePairs(n int, pred func(int) float64, own []float64, lo, hi float64) []linePair {
	var in []float64
	for _, o := range own {
		if o >= lo && o < hi {
			in = append(in, o)
		}
	}
	sort.Float64s(in)
	var idx []int
	for i := range n {
		if p := pred(i); p >= lo-2 && p < hi+2 {
			idx = append(idx, i)
		}
	}
	if len(in) < 4 || len(idx) < 4 {
		return nil
	}
	count := func(delta float64) int {
		k := 0
		for _, i := range idx {
			if _, d, _ := nearestTwo(in, pred(i)+delta); d <= 0.15 {
				k++
			}
		}
		return k
	}
	bestD, bestN := 0.0, -1
	for q := -300; q <= 300; q++ {
		delta := float64(q) / 100
		if c := count(delta); c > bestN || c == bestN && math.Abs(delta) < math.Abs(bestD) {
			bestD, bestN = delta, c
		}
	}
	var ds []float64
	for _, i := range idx {
		if k, d, _ := nearestTwo(in, pred(i)+bestD); k >= 0 && d <= 0.5 {
			ds = append(ds, in[k]-pred(i))
		}
	}
	if len(ds) < 4 {
		return nil
	}
	delta := median(ds)
	type cand struct {
		cue int
		d   float64
	}
	by := map[int]cand{}
	for _, i := range idx {
		p := pred(i) + delta
		k, d1, d2 := nearestTwo(in, p)
		if k < 0 || d1 > 1.5 || d2-d1 < 0.5 || in[k] < lo+0.5 || in[k] >= hi-0.5 {
			continue
		}
		if c, ok := by[k]; ok && c.d <= d1 {
			continue
		}
		by[k] = cand{i, d1}
	}
	var out []linePair
	for k, c := range by {
		mutual := true
		for _, j := range idx {
			if j != c.cue && math.Abs(pred(j)+delta-in[k]) < c.d {
				mutual = false
				break
			}
		}
		if mutual {
			out = append(out, linePair{c.cue, in[k]})
		}
	}
	sort.Slice(out, func(a, b int) bool { return out[a].cue < out[b].cue })
	return out
}

// fitAsServed fits like fitOne: with live windows the live rules, else the read rules; scales
// limits the rates tried (nil: all).
func fitAsServed(spans []span, read, live []refWindow, duration float64, scales []float64) (syncFit, []refWindow, bool) {
	if scales == nil {
		scales = syncScales
	}
	timed := clampSpans(spans, duration)
	ws := mergeWindows(read, live)
	var fit syncFit
	var ok bool
	if len(live) > 0 {
		fit, ok = alignLive(timed, ws, scales)
	} else {
		fit, ok = alignWith(timed, prepare(ws), scales, syncSplitPenalty, -1)
	}
	return fit, ws, ok && fit.good() && lengthFits(spans, fit, duration)
}

// servedStarts maps every cue with the fit, then refines; index i is cue i of spans.
func servedStarts(spans []span, fit syncFit, read, live []refWindow) (fitted, refined []float64) {
	cues := make([]liveCue, len(spans))
	for i, c := range spans {
		cues[i] = liveCue{start: c.start, end: c.end, text: strconv.Itoa(i)}
	}
	fitted = make([]float64, len(spans))
	for _, c := range applyFit(cues, fit) {
		i, _ := strconv.Atoi(c.text)
		fitted[i] = c.start
	}
	refined = make([]float64, len(spans))
	for _, c := range refineFit(cues, fit, refineWindows(read, live)) {
		i, _ := strconv.Atoi(c.text)
		refined[i] = c.start
	}
	return fitted, refined
}

// segmentThird is where file time t lies in its fit segment: 0 first third, 1 middle, 2 last.
func segmentThird(f syncFit, t, duration float64) int {
	lo, hi := 0.0, duration
	for i, s := range f.Segments {
		start := s.From + s.Offset
		if i == 0 {
			start = 0
		}
		if t >= start {
			lo = start
			if i+1 < len(f.Segments) {
				n := f.Segments[i+1]
				hi = n.From + n.Offset
			} else {
				hi = duration
			}
		}
	}
	if hi <= lo {
		return 1
	}
	return min(int(3*(t-lo)/(hi-lo)), 2)
}

// refineResult is the errors of one subtitle on one file.
type refineResult struct {
	held, extra, inref []lineErr
	skipped            int // held-out fits that were not accepted
}

func (r *refineResult) add(o refineResult) {
	r.held, r.extra, r.inref = append(r.held, o.held...), append(r.extra, o.extra...), append(r.inref, o.inref...)
	r.skipped += o.skipped
}

func pairErrors(pairs []linePair, fitted, refined []float64, f syncFit, duration float64) []lineErr {
	var out []lineErr
	for _, p := range pairs {
		out = append(out, lineErr{fitted[p.cue] - p.want, refined[p.cue] - p.want, segmentThird(f, p.want, duration)})
	}
	return out
}

// evalRefine scores the refinement of one subtitle on a file; extra are own-track line starts
// outside the reference (with the stretches they cover).
func evalRefine(f *snapFile, spans []span, extra []float64, extraCover []span) (refineResult, bool) {
	dur := f.ref.Probe.Duration
	var r refineResult
	fit, ws, ok := fitAsServed(spans, f.ref.Windows, f.live, dur, nil)
	if !ok {
		return r, false
	}
	pred := func(i int) float64 { return fit.mapTime(spans[i].start) }
	fitted, refined := servedStarts(spans, fit, f.ref.Windows, f.live)
	for _, w := range ws {
		if w.Kind != "subs" {
			continue
		}
		pairs := linePairs(len(spans), pred, ownStartsOf([]refWindow{w}), w.Start, w.end())
		r.inref = append(r.inref, pairErrors(pairs, fitted, refined, fit, dur)...)
	}
	for _, c := range extraCover {
		r.extra = append(r.extra, pairErrors(linePairs(len(spans), pred, extra, c.start, c.end), fitted, refined, fit, dur)...)
	}
	for h, w := range f.ref.Windows {
		if w.Kind != "subs" {
			continue
		}
		pairs := linePairs(len(spans), pred, ownStartsOf([]refWindow{w}), w.Start, w.end())
		if len(pairs) == 0 {
			continue
		}
		read := append(append([]refWindow(nil), f.ref.Windows[:h]...), f.ref.Windows[h+1:]...)
		hf, _, ok := fitAsServed(spans, read, f.live, dur, []float64{fit.Scale})
		if !ok {
			r.skipped++
			continue
		}
		hfitted, hrefined := servedStarts(spans, hf, read, f.live)
		r.held = append(r.held, pairErrors(pairs, hfitted, hrefined, hf, dur)...)
	}
	return r, true
}

// refineCases are the real pairs (snapshot file key prefix, subtitle) of the evaluation; gt
// names own-track VTTs read only for it (NFX_GT, see .claude/tmp/gt): 60 s windows.
var refineCases = []struct {
	file, sub, note string
	gt              []string
}{
	{"57a10159", "turkcealtyazi/4f148112", "Mentalist S02E18 / HDTV pack tr", []string{"e18w690_s3.vtt"}},
	{"07e92e95", "turkcealtyazi/8888c021", "Mentalist S02E17 / HDTV pack tr", []string{"e17w1110_s3.vtt", "e17w1500_s3.vtt"}},
	{"57a10159", "subf2m/b2455f2b", "Mentalist S02E18 / subf2m en", nil},
	{"07e92e95", "subf2m/6959a2fc", "Mentalist S02E17 / subf2m en", nil},
	{"5e0b532a", "opensubtitles/9b6e2df2", "Mentalist S02E13 / os tr", nil},
	{"9576c7f1", "opensubtitles/0464b0d3", "Mentalist S02E14 / os tr", nil},
	{"1f3b6cde", "turkcealtyazi/203844b3", "1f3b6cde / ta", nil},
	{"1f3b6cde", "subf2m/1f247d22", "1f3b6cde / subf2m", nil},
	{"2e80f2f6", "turkcealtyazi/7e8acdee", "Gilmore 2e80f2f6 / pack", nil},
	{"2f0ecb16", "turkcealtyazi/4bee7ec7", "2f0ecb16 / ta", nil},
	{"5aea8bc2", "turkcealtyazi/7e8acdee", "Gilmore MONOLITH / pack", nil},
	{"5aea8bc2", "subf2m/f410b17f", "Gilmore MONOLITH / subf2m", nil},
	{"e95a8a0e", "turkcealtyazi/7e8acdee", "Gilmore 720p / pack", nil},
	{"8a033e7e", "turkcealtyazi/0fb09db5", "8a033e7e / ta", nil},
	{"ab89c83b", "subf2m/f7defb5d", "ab89c83b / subf2m", nil},
	{"bf8fac49", "turkcealtyazi/a6a6a3f2", "Mentalist S02E10 / ta a6a6", nil},
	{"bf8fac49", "turkcealtyazi/bfd83592", "Mentalist S02E10 / ta bfd8", nil},
	{"bf8fac49", "subf2m/3fcc7d23", "Mentalist S02E10 / subf2m", nil},
	{"c162f124", "turkcealtyazi/8179e196", "Mentalist S02E11 / ta 8179", nil},
	{"c162f124", "turkcealtyazi/be95042a", "Mentalist S02E11 / HDTV pack", nil},
	{"111fa066", "subf2m/6f620e5c", "111fa066 / subf2m", nil},
	{"7e7565ef", "opensubtitles/461df872", "7e7565ef / os 461d", nil},
	{"7e7565ef", "opensubtitles/a8e6e13c", "7e7565ef / os a8e6", nil},
	{"7e7565ef", "opensubtitles/7a3c3749", "7e7565ef / os 7a3c", nil},
	{"d7da2284", "subf2m/5b2344fb", "d7da2284 / subf2m 5b23", nil},
	// already right (one offset, the file's release)
	{"111fa066", "opensubtitles/a09fd4ef", "RIGHT 111fa066 / os", nil},
	{"14f732f5", "subf2m/6f620e5c", "RIGHT 14f732f5 / subf2m", nil},
	{"494024e0", "turkcealtyazi/053cb0e3", "RIGHT Avatar E02 / WEB-DL tr", nil},
	{"494024e0", "subdl/091b57bb", "RIGHT Avatar E02 / own en", nil},
	{"494024e0", "opensubtitles/35bdcbcb", "RIGHT Avatar E02 / os", nil},
	{"4963a25e", "subf2m/691e0fa2", "RIGHT 4963a25e / subf2m", nil},
	{"7e7565ef", "subf2m/483c9ed2", "RIGHT 7e7565ef / subf2m", nil},
	{"abbc4bbd", "subdl/ecb9bdef", "RIGHT abbc4bbd / subdl", nil},
	{"d7da2284", "opensubtitles/d51adc50", "RIGHT d7da2284 / os", nil},
	{"d7da2284", "subf2m/e9136df6", "RIGHT d7da2284 / subf2m", nil},
}

func loadGTStarts(t *testing.T, names []string) ([]float64, []span) {
	dir := os.Getenv("NFX_GT")
	if dir == "" {
		return nil, nil
	}
	var starts []float64
	var cover []span
	for _, n := range names {
		b, err := os.ReadFile(filepath.Join(dir, n))
		if err != nil {
			t.Logf("gt %s: %v", n, err)
			continue
		}
		cues := vttCues(b)
		if len(cues) == 0 {
			continue
		}
		lo, hi := math.Inf(1), math.Inf(-1)
		for _, c := range cues {
			starts = append(starts, c.start)
			lo, hi = math.Min(lo, c.start), math.Max(hi, c.end)
		}
		cover = append(cover, span{lo - 1, hi + 1})
	}
	sort.Float64s(starts)
	return starts, cover
}

// TestSnapshotRefine reports per-line errors before and after the refinement (NFX_CASE limits
// the cases by note, NFX_PARAMS sets parameters for a sweep).
func TestSnapshotRefine(t *testing.T) {
	files, subs := loadSnapshot(t)
	setRefineParams(t, os.Getenv("NFX_PARAMS"))
	only := os.Getenv("NFX_CASE")
	var loose, right refineResult
	defer func() {
		logRefine(t, "TOTAL loose", loose)
		logThirds(t, "TOTAL loose", loose)
		logRefine(t, "TOTAL right", right)
		logThirds(t, "TOTAL right", right)
	}()
	for _, c := range refineCases {
		if only != "" && !strings.Contains(c.note, only) {
			continue
		}
		f, s := findFile(files, c.file), findSub(subs, c.sub)
		if f == nil || s == nil {
			t.Logf("%s: not in the snapshot", c.note)
			continue
		}
		extra, cover := loadGTStarts(t, c.gt)
		r, ok := evalRefine(f, s.spans, extra, cover)
		if !ok {
			t.Logf("%s: not accepted", c.note)
			continue
		}
		if strings.HasPrefix(c.note, "RIGHT") {
			right.add(r)
		} else {
			loose.add(r)
		}
		logRefine(t, c.note, r)
		if os.Getenv("NFX_EVAL") == "verbose" {
			logThirds(t, c.note, r)
		}
	}
}

// setRefineParams sets refinement parameters for a sweep: "radius=0.5,margin=0.4".
func setRefineParams(t *testing.T, s string) {
	for _, kv := range strings.Split(s, ",") {
		k, v, ok := strings.Cut(kv, "=")
		if !ok {
			continue
		}
		x, err := strconv.ParseFloat(v, 64)
		p := refineParams()[k]
		if p == nil || err != nil {
			t.Fatalf("param %q", kv)
		}
		*p = x
	}
}

func splitErrs(es []lineErr) (before, after []float64) {
	for _, e := range es {
		before, after = append(before, e.before), append(after, e.after)
	}
	return before, after
}

func logRefine(t *testing.T, name string, r refineResult) {
	row := func(kind string, es []lineErr) {
		if len(es) == 0 {
			return
		}
		b, a := splitErrs(es)
		t.Logf("%-34s %-5s %s", name, kind, signedOf(b, nil))
		t.Logf("%-34s %-5s %4s -> %s", "", "", "", signedOf(a, b).short())
		name = ""
	}
	row("held", r.held)
	row("extra", r.extra)
	row("inref", r.inref)
}

// logThirds splits the held-out and extra lines by their place in the fit segment.
func logThirds(t *testing.T, name string, r refineResult) {
	for th, label := range []string{"first", "middle", "last"} {
		var es []lineErr
		for _, e := range append(append([]lineErr(nil), r.held...), r.extra...) {
			if e.third == th {
				es = append(es, e)
			}
		}
		if len(es) == 0 {
			continue
		}
		b, a := splitErrs(es)
		t.Logf("%-34s %-6s %s -> %s", name, label, signedOf(b, nil), signedOf(a, b).short())
	}
}

// TestSnapshotRefineOthers fits other episodes' subtitles to every file with own-track windows
// (accepted or not, at the file's rate) and refines them: nearly all their cues must stay where
// the fit put them, so the refinement does not make another episode's subtitle look like this
// one. The same for this episode's subtitles shows how many windows show a local offset.
func TestSnapshotRefineOthers(t *testing.T) {
	files, subs := loadSnapshot(t)
	var fs []*snapFile
	for _, f := range files {
		if subsWindows(f.ref) >= 3 {
			fs = append(fs, f)
		}
	}
	agree := make([][]int, len(fs))
	for i, f := range fs {
		agree[i] = make([]int, len(subs))
		for j, s := range subs {
			agree[i][j] = agreeingWindows(s.spans, f.ref)
		}
	}
	type win struct{ starts, own []float64 }
	type tally struct {
		pairs, cues, moved int
		wins               []win
	}
	var other, same tally
	for i, f := range fs {
		dur := f.ref.Probe.Duration
		for j, s := range subs {
			last := s.spans[len(s.spans)-1].end
			if last < 0.75*dur || last > 1.1*dur {
				continue
			}
			mine, elsewhere := agree[i][j] >= 3, false
			for k := range fs {
				elsewhere = elsewhere || k != i && agree[k][j] >= 3
			}
			if !mine && (agree[i][j] > 1 || !elsewhere) {
				continue // unclear whose subtitle it is
			}
			fit, ok := alignWith(clampSpans(s.spans, dur), prepare(f.ref.Windows), syncScales[:1], syncSplitPenalty, -1)
			if !ok {
				continue
			}
			cues := make([]liveCue, len(s.spans))
			for k, c := range s.spans {
				cues[k] = liveCue{start: c.start, end: c.end}
			}
			fitted := applyFit(cues, fit)
			refined := refineFit(cues, fit, f.ref.Windows)
			tl := &other
			if mine {
				tl = &same
			}
			tl.pairs++
			tl.cues += len(fitted)
			for k := range fitted {
				if refined[k].start != fitted[k].start {
					tl.moved++
				}
			}
			starts := make([]float64, len(fitted))
			for k, c := range fitted {
				starts[k] = c.start
			}
			sort.Float64s(starts)
			for _, w := range f.ref.Windows {
				if w.Kind != "subs" {
					continue
				}
				a, b := sort.SearchFloat64s(starts, w.Start+refineRadius), sort.SearchFloat64s(starts, w.end()-refineRadius)
				own := ownStartsOf([]refWindow{w})
				if float64(b-a) < refineMinCues || float64(len(own)) < refineMinCues {
					continue
				}
				tl.wins = append(tl.wins, win{starts[a:b], own})
			}
		}
	}
	pass := func(ws []win) float64 {
		n := 0
		for _, w := range ws {
			if _, ok := localOffset(w.starts, w.own, refineSearch); ok {
				n++
			} else if _, ok := localOffset(w.starts, w.own, refineWide); ok {
				n++
			}
		}
		return float64(n) / float64(max(len(ws), 1))
	}
	po, ps := pass(other.wins), pass(same.wins)
	mo := float64(other.moved) / float64(max(other.cues, 1))
	t.Logf("SUMMARY other episodes: %d pairs, windows used %.1f%% of %d, cues moved %.1f%% of %d", other.pairs, 100*po,
		len(other.wins), 100*mo, other.cues)
	t.Logf("SUMMARY this episode:   %d pairs, windows used %.1f%% of %d, cues moved %.1f%% of %d", same.pairs, 100*ps,
		len(same.wins), 100*float64(same.moved)/float64(max(same.cues, 1)), same.cues)
	// (only accepted fits are refined; these are mostly not, so this bounds the chance matches)
	if mo > 0.02 {
		t.Errorf("other episodes' subtitles are refined: %.1f%% of cues", 100*mo)
	}
}

// fullOwnCases are files whose whole own track was read for the evaluation (NFX_GT: cue times
// only, "mm:ss.mmm --> mm:ss.mmm" lines), as the play streams' cue tap gives it once played.
var fullOwnCases = []struct{ file, times, note string }{
	{"07e92e95", "e17_full_s3.times", "Mentalist S02E17"},
	{"57a10159", "e18_full_s3.times", "Mentalist S02E18"},
}

func loadFullOwn(t *testing.T, name string) []span {
	b, err := os.ReadFile(filepath.Join(os.Getenv("NFX_GT"), name))
	if err != nil {
		t.Logf("%s: %v", name, err)
		return nil
	}
	var out []span
	for _, l := range strings.Split(string(b), "\n") {
		a, z, ok := strings.Cut(l, "-->")
		if !ok {
			continue
		}
		s, ok1 := parseVTTTime(a)
		e, ok2 := parseVTTTime(strings.Fields(z)[0])
		if ok1 && ok2 {
			out = append(out, span{s, e})
		}
	}
	return out
}

// tapWindows are the own-track windows the cue tap gives for a whole viewing (60 s grid, those
// with too few lines left out) followed by the live speech windows of other stretches.
func tapWindows(track []span, live []refWindow, duration float64) []refWindow {
	var own []refWindow
	for _, w := range ownWindows(track, gridStarts(0, duration)) {
		if n := len(ownStartsOf([]refWindow{w})); n >= ownMinStarts {
			if e := duration - w.Start; e < syncWindowLen {
				w.Speech = w.Speech[:int(e*syncRate)]
			}
			own = append(own, w)
		}
	}
	out := append([]refWindow(nil), own...)
	for _, w := range live {
		if !overlapsHalf(w, own) {
			out = append(out, w)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Start < out[j].Start })
	return out
}

// TestSnapshotRefineFull scores every line of the files with a whole own track: as served today
// (read and live windows), refined with the read own-track windows (a), and with the own track
// of the whole episode from the cue tap, fitted then refined (b). Subtitles of the episode count
// (3 own-track windows agree), and other episodes' subtitles must not be refined.
func TestSnapshotRefineFull(t *testing.T) {
	files, subs := loadSnapshot(t)
	setRefineParams(t, os.Getenv("NFX_PARAMS"))
	var sums [4][]float64
	var base []float64
	for _, c := range fullOwnCases {
		f := findFile(files, c.file)
		track := loadFullOwn(t, c.times)
		if f == nil || len(track) == 0 {
			t.Logf("%s: no data", c.note)
			continue
		}
		dur := f.ref.Probe.Duration
		tap := tapWindows(track, f.live, dur)
		full := ownWindows(track, gridStarts(0, dur))
		var trackStarts []float64
		for _, s := range track {
			trackStarts = append(trackStarts, s.start)
		}
		otherMoved, otherCues, otherFits, otherTried := 0, 0, 0, 0
		for _, s := range subs {
			last := s.spans[len(s.spans)-1].end
			if last < 0.75*dur || last > 1.1*dur {
				continue
			}
			listed := false
			for _, rc := range refineCases {
				listed = listed || rc.file == c.file && strings.Contains(s.name, rc.sub)
			}
			if agree := agreeingWindows(s.spans, f.ref); !listed && agree < 3 {
				// another episode's (or another cut's) subtitle: fitted to the tap's reference, it
				// must not fit, or not move
				if agree > 0 {
					continue
				}
				otherTried++
				fit, _, ok := fitAsServed(s.spans, f.ref.Windows, tap, dur, syncScales[:1])
				if !ok {
					continue
				}
				elsewhere := false
				for _, g := range files {
					elsewhere = elsewhere || g != f && subsWindows(g.ref) >= 3 && agreeingWindows(s.spans, g.ref) >= 3
				}
				if !elsewhere {
					continue // unclear whose subtitle it is (this episode at another rate?)
				}
				fitted, refined := servedStarts(s.spans, fit, f.ref.Windows, tap)
				otherFits++
				t.Logf("%s other %s fitted: %s", c.note, s.name, describeFit(fit))
				for i := range fitted {
					otherCues++
					if fitted[i] != refined[i] {
						otherMoved++
					}
				}
				continue
			}
			now, _, ok := fitAsServed(s.spans, f.ref.Windows, f.live, dur, nil)
			if !ok {
				t.Logf("%s %s: not accepted today", c.note, s.name)
				continue
			}
			pred := func(i int) float64 { return now.mapTime(s.spans[i].start) }
			var pairs []linePair
			for _, w := range full {
				pairs = append(pairs, linePairs(len(s.spans), pred, trackStarts, w.Start, w.end())...)
			}
			fittedNow, refinedNow := servedStarts(s.spans, now, f.ref.Windows, f.live)
			fit, _, ok := fitAsServed(s.spans, f.ref.Windows, tap, dur, nil)
			fittedTap, refinedTap := fittedNow, refinedNow
			if ok {
				fittedTap, refinedTap = servedStarts(s.spans, fit, f.ref.Windows, tap)
			} else {
				t.Logf("%s %s: not accepted with the tap", c.note, s.name)
			}
			var errs [4][]float64
			for _, p := range pairs {
				if os.Getenv("NFX_EVAL") == "lines" {
					t.Logf("LINE %s %8.2f %+.2f %+.2f %+.2f", c.note[len(c.note)-3:], p.want, fittedNow[p.cue]-p.want, fittedTap[p.cue]-p.want, refinedTap[p.cue]-p.want)
				}
				for k, v := range [][]float64{fittedNow, refinedNow, fittedTap, refinedTap} {
					errs[k] = append(errs[k], v[p.cue]-p.want)
				}
			}
			t.Logf("%s %-28s today      %s (%d segments)", c.note, s.name, signedOf(errs[0], nil), len(now.Segments))
			t.Logf("%s %-28s (a) refine %s", c.note, "", signedOf(errs[1], errs[0]).short())
			t.Logf("%s %-28s (b) fit    %s (%d segments)", c.note, "", signedOf(errs[2], errs[0]).short(), len(fit.Segments))
			t.Logf("%s %-28s (b) refine %s", c.note, "", signedOf(errs[3], errs[0]).short())
			t.Logf("%s %-28s (b) refine %s (against the fit)", c.note, "", signedOf(errs[3], errs[2]).short())
			for k := range sums {
				sums[k] = append(sums[k], errs[k]...)
			}
			base = append(base, errs[0]...)
		}
		t.Logf("%s other subtitles: %d tried, %d fitted with the tap (another episode's), cues moved by the refinement %d of %d", c.note, otherTried, otherFits, otherMoved, otherCues)
		if otherCues > 0 && float64(otherMoved) > 0.01*float64(otherCues) {
			t.Errorf("%s: other episodes' subtitles refined: %d of %d cues", c.note, otherMoved, otherCues)
		}
	}
	t.Logf("TOTAL today      %s", signedOf(sums[0], nil))
	t.Logf("TOTAL (a) refine %s", signedOf(sums[1], base).short())
	t.Logf("TOTAL (b) fit    %s", signedOf(sums[2], base).short())
	t.Logf("TOTAL (b) refine %s", signedOf(sums[3], base).short())
	t.Logf("TOTAL (b) refine %s (against the fit)", signedOf(sums[3], sums[2]).short())
}
