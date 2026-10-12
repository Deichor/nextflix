package subtitles

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"math/rand"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

// Evaluation of the automatic timing on real data: a snapshot of a server's data dir (syncref,
// synclive and the provider subtitles) named by NFX_TESTDATA. Skipped without it.
//
// Truth comes from the file's own subtitle track (the "subs" reference windows, exact cue
// times of the file): per window, the subtitle's local offset is found independently of the
// aligner (best offset by line starts, refined to the median of the matched starts). A pair
// whose windows agree within 0.5 s has a constant offset, so every cue of the file has a true
// time; otherwise only cues inside (or between agreeing) windows count.

type snapFile struct {
	name string
	ref  syncRef
	live []refWindow // finished live windows (synclive), sorted
}

type snapSub struct {
	name  string
	cues  []liveCue
	spans []span
}

func snapshotDir(t *testing.T) string {
	dir := os.Getenv("NFX_TESTDATA")
	if dir == "" {
		t.Skip("NFX_TESTDATA not set")
	}
	return filepath.Join(dir, "subtitles")
}

func loadSnapshot(t *testing.T) ([]*snapFile, []*snapSub) {
	dir := snapshotDir(t)
	byName := map[string]*snapFile{}
	get := func(n string) *snapFile {
		if byName[n] == nil {
			byName[n] = &snapFile{name: n}
		}
		return byName[n]
	}
	refs, _ := filepath.Glob(filepath.Join(dir, "syncref", "*.json"))
	for _, p := range refs {
		b, _ := os.ReadFile(p)
		var r syncRef
		if json.Unmarshal(b, &r) != nil {
			continue
		}
		get(strings.TrimSuffix(filepath.Base(p), ".json")).ref = r
	}
	lives, _ := filepath.Glob(filepath.Join(dir, "synclive", "*.json"))
	for _, p := range lives {
		b, _ := os.ReadFile(p)
		var s liveSaved
		if json.Unmarshal(b, &s) != nil {
			continue
		}
		f := get(strings.TrimSuffix(filepath.Base(p), ".json"))
		for w, d := range s.Windows {
			f.live = append(f.live, refWindow{Start: float64(w) * syncWindowLen, Kind: "live", Speech: unpackBits(d.Bits, d.Frames)})
		}
		sort.Slice(f.live, func(i, j int) bool { return f.live[i].Start < f.live[j].Start })
	}
	var files []*snapFile
	for _, f := range byName {
		if f.ref.Probe.Duration > 0 {
			files = append(files, f)
		}
	}
	sort.Slice(files, func(i, j int) bool { return files[i].name < files[j].name })

	var subs []*snapSub
	seen := map[string]bool{}
	for _, kind := range []string{SourceTorrent, SourceEmbedded, SourceOpenSubtitles, SourceSubDL, SourceSubSource,
		SourceTurkceAltyazi, SourceSubf2m, SourceYIFY} {
		ps, _ := filepath.Glob(filepath.Join(dir, kind, "*.vtt"))
		for _, p := range ps {
			b, _ := os.ReadFile(p)
			cues := vttCues(b)
			if len(cues) < 50 {
				continue
			}
			sig := fmt.Sprint(len(cues), cues[0].start, cues[len(cues)-1].start, cues[len(cues)/2].text)
			if seen[sig] {
				continue // the same subtitle listed twice
			}
			seen[sig] = true
			s := &snapSub{name: kind + "/" + filepath.Base(p)[:8], cues: cues}
			for _, c := range cues {
				s.spans = append(s.spans, span{c.start, c.end})
			}
			subs = append(subs, s)
		}
	}
	return files, subs
}

func (f *snapFile) label() string {
	return fmt.Sprintf("%s (%.0f s, %d read windows %s, %d live)", f.name[:8], f.ref.Probe.Duration, len(f.ref.Windows),
		f.ref.kind(), len(f.live))
}

// truthWin is the subtitle's true offset in one window of the file's own track.
type truthWin struct {
	start, end, offset, jitter float64
	matched                    int
}

type truth struct {
	scale    float64
	wins     []truthWin // sorted
	constant bool
	c        float64
	duration float64
}

func startSignal(cues []span, k float64) []float64 {
	last := 0.0
	for _, c := range cues {
		last = math.Max(last, c.start)
	}
	n := int((last*k+1)*syncRate) + 2
	starts := make([]float64, n)
	for _, c := range cues {
		if i := int(math.Round(c.start * k * syncRate)); i >= 0 && i < n {
			starts[i] = 1
		}
	}
	return starts
}

// findTruth finds the subtitle's true offsets from the file's own track windows; false when it
// is not this file's episode (fewer than 3 windows, or a third of them, agree). A window counts
// when the subtitle's cue starts, or its starts and ends, clearly match there.
func findTruth(cues []span, ref syncRef) (truth, bool) {
	tr, n := truthOf(cues, ref)
	if n < max(3, subsWindows(ref)/3) {
		return truth{}, false
	}
	return tr, true
}

// agreeingWindows is how many own-track windows agree on the subtitle (2: maybe this episode).
func agreeingWindows(cues []span, ref syncRef) int {
	_, n := truthOf(cues, ref)
	return n
}

func subsWindows(ref syncRef) int {
	n := 0
	for _, w := range prepare(ref.Windows) {
		if w.Kind == "subs" {
			n++
		}
	}
	return n
}

func truthOf(cues []span, ref syncRef) (truth, int) {
	var ws []prepared
	for _, w := range prepare(ref.Windows) {
		if w.Kind == "subs" {
			ws = append(ws, w)
		}
	}
	if len(ws) < 3 {
		return truth{}, 0
	}
	sort.Slice(ws, func(i, j int) bool { return ws[i].Start < ws[j].Start })
	d := int(syncMaxOffset * syncRate)
	var best truth
	bestN := 0
	for _, k := range syncScales {
		scaled := make([]span, len(cues))
		for i, c := range cues {
			scaled[i] = span{c.start * k, c.end * k}
		}
		starts := startSignal(scaled, 1)
		type loc struct {
			w prepared
			q int
		}
		var conf []loc
		for _, w := range ws {
			c := boundaryCurve(w.refWindow, scaled, d)
			q := argmax(c, 0, len(c)-1)
			if c[q] >= 0.4*w.lines && prominence(c, q) >= math.Max(4, 0.15*w.lines) {
				conf = append(conf, loc{w, q})
				continue
			}
			c = windowCurve(w, starts, d)
			q = argmax(c, 0, len(c)-1)
			if c[q] >= 0.3*w.lines && prominence(c, q) >= math.Max(3, 0.12*w.lines) {
				conf = append(conf, loc{w, q})
			}
		}
		if len(conf) == 0 {
			continue
		}
		qs := make([]float64, len(conf))
		for i, l := range conf {
			qs[i] = float64(l.q)
		}
		med := median(qs)
		tr := truth{scale: k, duration: ref.Probe.Duration}
		for _, l := range conf {
			if math.Abs(float64(l.q)-med) > 30*syncRate {
				continue
			}
			// refine: median of (reference start - subtitle start) of the matched lines
			off := float64(l.q-d) / syncRate
			var ds []float64
			for _, c := range cues {
				u := c.start * k
				t := u + off
				if t < l.w.Start || t >= l.w.end() {
					continue
				}
				i0 := int(math.Round((t - l.w.Start) * syncRate))
				bestJ, bestD := -1, 61
				for j := max(i0-60, 0); j <= i0+60 && j < len(l.w.Speech); j++ {
					if l.w.Speech[j] == 2 && abs(j-i0) < bestD {
						bestJ, bestD = j, abs(j-i0)
					}
				}
				if bestJ >= 0 {
					ds = append(ds, l.w.Start+float64(bestJ)/syncRate-u)
				}
			}
			if len(ds) < 4 {
				continue
			}
			m := median(ds)
			dev := make([]float64, len(ds))
			for i, x := range ds {
				dev[i] = math.Abs(x - m)
			}
			tr.wins = append(tr.wins, truthWin{l.w.Start, l.w.end(), m, median(dev), len(ds)})
		}
		if len(tr.wins) > bestN || len(tr.wins) == bestN && k == 1 {
			best, bestN = tr, len(tr.wins)
		}
	}
	lo, hi := math.Inf(1), math.Inf(-1)
	var offs []float64
	for _, w := range best.wins {
		lo, hi = math.Min(lo, w.offset), math.Max(hi, w.offset)
		offs = append(offs, w.offset)
	}
	if bestN >= 3 && hi-lo <= 0.5 {
		best.constant, best.c = true, median(offs)
	}
	return best, bestN
}

// at is the true file time of a subtitle time u, false where it is not known.
func (tr truth) at(u float64) (float64, bool) {
	x := u * tr.scale
	if tr.constant {
		t := x + tr.c
		return t, t >= 0 && t <= tr.duration
	}
	for i, w := range tr.wins {
		if t := x + w.offset; t >= w.start && t < w.end {
			return t, true
		}
		if i+1 < len(tr.wins) {
			n := tr.wins[i+1]
			if math.Abs(n.offset-w.offset) <= 0.3 {
				o := (w.offset + n.offset) / 2
				if t := x + o; t >= w.end && t < n.start {
					return t, true
				}
			}
		}
	}
	return 0, false
}

func (tr truth) String() string {
	if tr.constant {
		return fmt.Sprintf("scale %.4f constant %+.2fs (%d windows)", tr.scale, tr.c, len(tr.wins))
	}
	var parts []string
	for _, w := range tr.wins {
		parts = append(parts, fmt.Sprintf("%+.2f@%.0f", w.offset, w.start))
	}
	return fmt.Sprintf("scale %.4f offsets %s", tr.scale, strings.Join(parts, " "))
}

// cueTruth is the true file time of every cue (NaN: unknown).
func (tr truth) cueTruth(cues []span) []float64 {
	out := make([]float64, len(cues))
	for i, c := range cues {
		out[i] = math.NaN()
		if t, ok := tr.at(c.start); ok {
			out[i] = t
		}
	}
	return out
}

// timingErrors are the errors (seconds) of the cues with a known true time, as the fit maps
// them; in limits which cues count (by true time).
func timingErrors(f syncFit, cues []span, truth []float64, in func(t float64) bool) []float64 {
	var out []float64
	for i, c := range cues {
		t := truth[i]
		if math.IsNaN(t) || in != nil && !in(t) {
			continue
		}
		out = append(out, math.Abs(f.mapTime(c.start)-t))
	}
	return out
}

var identityFit = syncFit{Scale: 1, Segments: []syncSegment{{}}}

type errStats struct {
	n                         int
	w03, w05, w1, median, p95 float64
}

func statsOf(errs []float64) errStats {
	if len(errs) == 0 {
		return errStats{}
	}
	s := append([]float64(nil), errs...)
	sort.Float64s(s)
	share := func(tol float64) float64 {
		n := 0
		for _, e := range s {
			if e <= tol {
				n++
			}
		}
		return float64(n) / float64(len(s))
	}
	return errStats{n: len(s), w03: share(0.3), w05: share(0.5), w1: share(1), median: s[len(s)/2], p95: s[int(0.95*float64(len(s)-1))]}
}

func (s errStats) String() string {
	if s.n == 0 {
		return "no cues"
	}
	return fmt.Sprintf("n=%4d %3.0f%% %3.0f%% %3.0f%% med %5.2f p95 %5.2f", s.n, 100*s.w03, 100*s.w05, 100*s.w1, s.median, s.p95)
}

func median(v []float64) float64 {
	s := append([]float64(nil), v...)
	sort.Float64s(s)
	if len(s) == 0 {
		return 0
	}
	return s[len(s)/2]
}

// TestSnapshotTruth lists, per file of the snapshot, the subtitles of its episode and their
// true offsets.
func TestSnapshotTruth(t *testing.T) {
	files, subs := loadSnapshot(t)
	t.Logf("%d files, %d subtitles", len(files), len(subs))
	for _, f := range files {
		t.Logf("file %s", f.label())
		for _, s := range subs {
			if tr, ok := findTruth(s.spans, f.ref); ok {
				t.Logf("   %-28s %s", s.name, tr)
			}
		}
	}
}

// TestSnapshotReplayDebug prints the live replay of one pair (NFX_PAIR=file8,sub8), truth or not.
func TestSnapshotReplayDebug(t *testing.T) {
	pair := strings.Split(os.Getenv("NFX_PAIR"), ",")
	if len(pair) != 2 {
		t.Skip("NFX_PAIR not set")
	}
	files, subs := loadSnapshot(t)
	f, s := findFile(files, pair[0]), findSub(subs, pair[1])
	if f == nil || s == nil {
		t.Fatal("not in the snapshot")
	}
	for _, withRead := range []bool{true, false} {
		if withRead && len(f.ref.Windows) == 0 {
			continue
		}
		t.Logf("%s %s read=%v (%d cues)", f.label(), s.name, withRead, len(s.spans))
		for _, st := range replayLive(t, f, s.spans, withRead, nil) {
			state := "none"
			if st.meta != nil {
				state = fmt.Sprintf("%s score %.2f base %.2f peak %.2f", st.meta.State, st.meta.Fit.Score, st.meta.Fit.Base,
					st.meta.Fit.Peak)
			}
			t.Logf("  min %2d heard %4.0f %-50s %s", st.minute, st.heard, segText(st.served), state)
		}
	}
}

// TestSnapshotMatrix prints how many own-track windows of each file agree on each subtitle.
func TestSnapshotMatrix(t *testing.T) {
	files, subs := loadSnapshot(t)
	for _, f := range files {
		if subsWindows(f.ref) < 3 {
			continue
		}
		var parts []string
		for _, s := range subs {
			if n := agreeingWindows(s.spans, f.ref); n >= 2 {
				parts = append(parts, fmt.Sprintf("%s:%d", s.name, n))
			}
		}
		t.Logf("%s %s", f.label(), strings.Join(parts, " "))
	}
}

// TestSnapshotPairDebug prints the per-window offset curves of one pair (NFX_PAIR=file8,sub8).
func TestSnapshotPairDebug(t *testing.T) {
	pair := strings.Split(os.Getenv("NFX_PAIR"), ",")
	if len(pair) != 2 {
		t.Skip("NFX_PAIR not set")
	}
	files, subs := loadSnapshot(t)
	for _, f := range files {
		if !strings.HasPrefix(f.name, pair[0]) {
			continue
		}
		for _, s := range subs {
			if !strings.Contains(s.name, pair[1]) {
				continue
			}
			t.Logf("%s %s", f.label(), s.name)
			starts := startSignal(s.spans, 1)
			d := int(syncMaxOffset * syncRate)
			all := mergeWindows(f.ref.Windows, f.live)
			for _, w := range prepare(all) {
				c := windowCurve(w, starts, d)
				q := argmax(c, 0, len(c)-1)
				// top 3 local maxima 1.5 s apart
				type pk struct {
					q int
					v float64
				}
				var pks []pk
				used := make([]bool, len(c))
				for len(pks) < 3 {
					b := -1
					for i := range c {
						if !used[i] && (b < 0 || c[i] > c[b]) {
							b = i
						}
					}
					pks = append(pks, pk{b, c[b]})
					for i := max(b-150, 0); i <= b+150 && i < len(c); i++ {
						used[i] = true
					}
				}
				t.Logf("  %-4s %5.0f lines %3.0f mean %.1f best %+.2fs %.1f | %+.2fs %.1f | %+.2fs %.1f  prom %.1f", w.Kind, w.Start, w.lines, mean(c),
					float64(pks[0].q-d)/syncRate, pks[0].v, float64(pks[1].q-d)/syncRate, pks[1].v, float64(pks[2].q-d)/syncRate, pks[2].v, prominence(c, q))
				c2 := boundaryCurve(w.refWindow, s.spans, d)
				q2 := argmax(c2, 0, len(c2)-1)
				t.Logf("        boundaries: best %+.2fs %.1f mean %.1f prom %.1f", float64(q2-d)/syncRate, c2[q2], mean(c2), prominence(c2, q2))
			}
		}
	}
}

// boundaryCurve scores offsets by cue starts and ends (speech onsets and offsets) within 0.3 s.
func boundaryCurve(w refWindow, cues []span, d int) []float64 {
	const k = 30
	kern := make([]float64, len(w.Speech))
	mark := func(i int) {
		for j := max(i-k, 0); j <= i+k && j < len(kern); j++ {
			kern[j] = math.Max(kern[j], 1-math.Abs(float64(j-i))/k)
		}
	}
	for i := 1; i < len(w.Speech); i++ {
		if w.Speech[i] != 0 && w.Speech[i-1] == 0 || w.Speech[i] == 2 {
			mark(i)
		}
		if w.Speech[i] == 0 && w.Speech[i-1] != 0 {
			mark(i)
		}
	}
	last := 0.0
	for _, c := range cues {
		last = math.Max(last, c.end)
	}
	sig := make([]float64, int(last*syncRate)+2)
	for _, c := range cues {
		for _, x := range []float64{c.start, c.end} {
			if i := int(math.Round(x * syncRate)); i >= 0 && i < len(sig) {
				sig[i] = 1
			}
		}
	}
	return windowCurve(prepared{refWindow: w, kernel: kern}, sig, d)
}

// TestSnapshotSpeechLag measures how far detected speech onsets (live windows) lie from the
// cue starts of the file's own track where both cover the same stretch.
func TestSnapshotSpeechLag(t *testing.T) {
	files, _ := loadSnapshot(t)
	total := make([]float64, 401)
	for _, f := range files {
		hist := make([]float64, 401)
		for _, s := range f.ref.Windows {
			if s.Kind != "subs" {
				continue
			}
			for _, l := range f.live {
				lo, hi := math.Max(s.Start, l.Start), math.Min(s.end(), l.end())
				if hi-lo < 20 {
					continue
				}
				var starts, onsets []float64
				for i := 1; i < len(s.Speech); i++ {
					if s.Speech[i] == 2 {
						starts = append(starts, s.Start+float64(i)/syncRate)
					}
				}
				onsets = onsets[:0]
				for _, x := range onsetsOf(l) {
					onsets = append(onsets, x)
				}
				for _, a := range starts {
					if a < lo+2 || a > hi-2 {
						continue
					}
					for _, b := range onsets {
						if d := int(math.Round((b - a) * syncRate)); d >= -200 && d <= 200 {
							hist[d+200]++
						}
					}
				}
			}
		}
		sum := 0.0
		for i, v := range hist {
			total[i] += v
			sum += v
		}
		if sum > 0 {
			t.Logf("%s: onset lag peak %+.2fs (%.0f pairs within 2 s)", f.label(), lagPeak(hist), sum)
		}
	}
	t.Logf("all files: onset lag peak %+.2fs", lagPeak(total))
	for d := -60; d <= 100; d += 10 {
		s := 0.0
		for i := d; i < d+10; i++ {
			s += total[i+200]
		}
		t.Logf("  %+.2f..%+.2f s: %.0f", float64(d)/syncRate, float64(d+10)/syncRate, s)
	}
}

func onsetsOf(w refWindow) []float64 {
	var out []float64
	for i := 1; i < len(w.Speech); i++ {
		if w.Speech[i] != 0 && w.Speech[i-1] == 0 {
			out = append(out, w.Start+float64(i)/syncRate)
		}
	}
	return out
}

// lagPeak is the lag (seconds) with the most pairs, smoothed over ±50 ms.
func lagPeak(hist []float64) float64 {
	best, bestV := 0, -1.0
	for i := 5; i+5 < len(hist); i++ {
		v := 0.0
		for j := i - 5; j <= i+5; j++ {
			v += hist[j]
		}
		if v > bestV {
			best, bestV = i, v
		}
	}
	return float64(best-200) / syncRate
}

// replayStep is what is served after one re-fit of a live replay.
type replayStep struct {
	minute int
	heard  float64 // end of the last grid window heard
	played []span  // stretches heard so far
	meta   *syncMeta
	served syncFit // what the viewer sees (identity when untimed)
	fits   bool
}

// replayLive plays a file's live windows in the given order (one grid window a minute) through
// the real syncer (decide: alignLive, hysteresis, rate reuse; with withRead also the file's
// read windows) and reports what is served after each step.
// replayEvery re-fits after every n-th window only (the wrong-episode sweep sets it for speed).
var replayEvery = 1

func replayLive(t *testing.T, f *snapFile, sub []span, withRead bool, order []int) []replayStep {
	svc := NewService(nil, Config{CacheDir: t.TempDir(), TorrServerURL: "http://torrserver", LiveTiming: true})
	y := svc.sync
	y.onGrow = func(*liveFile) {}
	hash := f.name
	ref := f.ref
	if !withRead {
		ref = syncRef{Probe: ref.Probe, Skipped: true}
	}
	y.probe = func(context.Context, string) (mediaProbe, error) { return ref.Probe, nil }
	inner := cacheProviderSub(t, svc, "eval|"+f.name[:8], sub)
	key, _ := syncKey(inner, hash, 1)
	base, _ := svc.File(context.Background(), inner)
	lf := y.liveFile(hash, 1)
	lf.setDuration(ref.Probe.Duration)
	if order == nil {
		for i := range f.live {
			order = append(order, i)
		}
	}
	var steps []replayStep
	for i := range order {
		if i%replayEvery != replayEvery-1 && i != len(order)-1 {
			continue
		}
		lf.mu.Lock()
		lf.done = map[int]liveWin{}
		for _, j := range order[:i+1] {
			w := f.live[j]
			lf.done[int(math.Round(w.Start/syncWindowLen))] = liveWin{Bits: packBits(w.Speech), Frames: len(w.Speech)}
		}
		lf.complete = false
		lf.checkComplete()
		lf.mu.Unlock()
		y.decide(context.Background(), inner, hash, 1, base, ref, false)
		m := y.meta(key)
		st := replayStep{minute: i + 1, heard: f.live[order[i]].end(), meta: m, served: identityFit}
		for _, j := range order[:i+1] {
			st.played = append(st.played, span{f.live[j].Start, f.live[j].end()})
		}
		if m != nil && m.fits() {
			st.served, st.fits = m.Fit, true
		}
		steps = append(steps, st)
	}
	return steps
}

// replayReport sums up a live replay against the truth.
type replayReport struct {
	steps, fitted, falseSeg, changes, worse int
	firstOK                                 int // minute from which the played part stays right (0: never)
	watched, final                          errStats
}

func (r replayReport) String() string {
	return fmt.Sprintf("fit %2d/%2d false-seg %2d changes %2d worse %2d first-ok %2d | watched %s | final %s",
		r.fitted, r.steps, r.falseSeg, r.changes, r.worse, r.firstOK, r.watched, r.final)
}

// evalReplay: what the viewer watches at a step is the minute before the last one heard (the
// player buffers about 90 s ahead of the picture).
func evalReplay(steps []replayStep, cues []span, truth []float64, verbose func(string, ...any)) replayReport {
	var r replayReport
	var watched []float64
	prevShare := -1.0
	var prev *syncMeta
	for _, st := range steps {
		r.steps++
		if st.fits {
			r.fitted++
		}
		lo, hi := st.heard-150, st.heard-90
		watched = append(watched, timingErrors(st.served, cues, truth, func(t float64) bool { return t >= lo && t < hi })...)
		inPlayed := func(t float64) bool {
			for _, p := range st.played {
				if t >= p.start && t < p.end {
					return true
				}
			}
			return false
		}
		played := statsOf(timingErrors(st.served, cues, truth, inPlayed))
		all := statsOf(timingErrors(st.served, cues, truth, nil))
		if played.n > 0 && played.w05 >= 0.9 {
			if r.firstOK == 0 {
				r.firstOK = st.minute
			}
		} else if played.n > 0 {
			r.firstOK = 0
		}
		if falseSegment(st.served, cues, truth, inPlayed) {
			r.falseSeg++
		}
		if st.meta != nil && prev != nil && fitChanged(prev, *st.meta) {
			r.changes++
		}
		if prevShare >= 0 && all.w05 < prevShare-0.05 {
			r.worse++
		}
		prevShare = all.w05
		prev = st.meta
		if verbose != nil {
			state := "none"
			if st.meta != nil {
				state = st.meta.State
			}
			verbose("  min %2d heard %4.0f %-9s %-50s played %s | all %s", st.minute, st.heard, state, segText(st.served), played, all)
		}
	}
	r.watched = statsOf(watched)
	if len(steps) > 0 {
		r.final = statsOf(timingErrors(steps[len(steps)-1].served, cues, truth, nil))
	}
	return r
}

func segText(f syncFit) string {
	var parts []string
	for i, s := range f.Segments {
		p := fmt.Sprintf("%+.2f", s.Offset)
		if i > 0 {
			p += fmt.Sprintf("@%.0f", s.From)
		}
		parts = append(parts, p)
	}
	if f.Scale != 1 && f.Scale != 0 {
		return fmt.Sprintf("x%.4f %s", f.Scale, strings.Join(parts, " "))
	}
	return strings.Join(parts, " ")
}

// falseSegment reports whether some segment of the fit puts its cues (with known truth, played
// in a heard stretch) more than 1 s off by the median.
func falseSegment(f syncFit, cues []span, truth []float64, heard func(float64) bool) bool {
	if len(f.Segments) < 2 {
		return false
	}
	for i := range f.Segments {
		var errs []float64
		for j, c := range cues {
			u := c.start * f.Scale
			if i > 0 && u < f.Segments[i].From || i+1 < len(f.Segments) && u >= f.Segments[i+1].From ||
				math.IsNaN(truth[j]) || !heard(truth[j]) {
				continue
			}
			errs = append(errs, math.Abs(f.mapTime(c.start)-truth[j]))
		}
		if len(errs) >= 3 && median(errs) > 1 {
			return true
		}
	}
	return false
}

func findFile(files []*snapFile, prefix string) *snapFile {
	for _, f := range files {
		if strings.HasPrefix(f.name, prefix) {
			return f
		}
	}
	return nil
}

func findSub(subs []*snapSub, part string) *snapSub {
	for _, s := range subs {
		if strings.Contains(s.name, part) {
			return s
		}
	}
	return nil
}

// replayCases are the real pairs replayed: file, subtitle and, when the own-track windows do
// not settle it, the known constant truth.
var replayCases = []struct {
	file, sub string
	constant  float64
	known     bool
	note      string
}{
	// Avatar: Seven Havens S01E02 WEB-DL with turkcealtyazi's WEB-DL subtitle (served, the logged
	// ±11 s segments); its own windows agree on 0.00 and the English subdl one is the file's own
	// track
	{"494024e0", "turkcealtyazi/053cb0e3", 0, true, "Avatar E02 / WEB-DL tr"},
	{"494024e0", "subdl/091b57bb", 0, true, "Avatar E02 / own en"},
	{"c162f124", "turkcealtyazi/be95042a", 0, false, "Mentalist S02E11 / HDTV pack"},
	{"bf8fac49", "subf2m/3fcc7d23", 0, false, "Mentalist S02E10 KONTRAST / subf2m"},
	{"5aea8bc2", "subf2m/f410b17f", 0, false, "Gilmore S01E07 MONOLITH / subf2m"},
	{"e95a8a0e", "turkcealtyazi/7e8acdee", 0, false, "Gilmore S01E07 720p / pack"},
	{"1f3b6cde", "turkcealtyazi/203844b3", 0, false, "1f3b6cde / ta"},
}

type evalRow struct {
	name string
	r    replayReport
}

// seekOrder plays from the middle to the end, then from the start (a resume and a seek back).
func seekOrder(n int) []int {
	var out []int
	for i := n / 2; i < n; i++ {
		out = append(out, i)
	}
	for i := 0; i < n/2; i++ {
		out = append(out, i)
	}
	return out
}

// TestSnapshotReplay replays the played audio of the real files against their subtitles (with
// the read windows, as served, and from the audio only) and reports the timing errors, then
// the distortions with known truth and other episodes' subtitles.
func TestSnapshotReplay(t *testing.T) {
	files, subs := loadSnapshot(t)
	verbose := os.Getenv("NFX_EVAL") == "verbose"
	var vf func(string, ...any)
	if verbose {
		vf = t.Logf
	}
	var rows []evalRow
	add := func(name string, r replayReport) {
		rows = append(rows, evalRow{name, r})
		t.Logf("%-58s %s", name, r)
	}
	only := os.Getenv("NFX_CASE")
	want := func(name string) bool { return only == "" || strings.Contains(name, only) }
	for _, c := range replayCases {
		f, s := findFile(files, c.file), findSub(subs, c.sub)
		if f == nil || s == nil || len(f.live) == 0 {
			t.Logf("%s: not in the snapshot", c.note)
			continue
		}
		tr, ok := findTruth(s.spans, f.ref)
		if c.known {
			tr, ok = truth{scale: 1, constant: true, c: c.constant, duration: f.ref.Probe.Duration}, true
		}
		if !ok {
			t.Logf("%s: no truth", c.note)
			continue
		}
		t.Logf("%s: truth %s", c.note, tr)
		ct := tr.cueTruth(s.spans)
		for _, withRead := range []bool{true, false} {
			if withRead && len(f.ref.Windows) == 0 {
				continue
			}
			name := fmt.Sprintf("%s read=%v", c.note, withRead)
			if !want(name) {
				continue
			}
			add(name, evalReplay(replayLive(t, f, s.spans, withRead, nil), s.spans, ct, vf))
			if len(f.live) >= 8 {
				add(name+" seek", evalReplay(replayLive(t, f, s.spans, withRead, seekOrder(len(f.live))), s.spans, ct, vf))
			}
		}
	}
	// Distortions with known truth on Avatar E02 (real speech, real own-track windows)
	f := findFile(files, "494024e0")
	for _, base := range []string{"subdl/091b57bb", "turkcealtyazi/053cb0e3"} {
		s := findSub(subs, base)
		if f == nil || s == nil {
			continue
		}
		ct := truth{scale: 1, constant: true, duration: f.ref.Probe.Duration}.cueTruth(s.spans)
		for _, d := range distortions() {
			sub, tru := d.apply(s.spans, ct)
			for _, withRead := range []bool{true, false} {
				name := fmt.Sprintf("E02 %-5s %-14s read=%v", base[:5], d.name, withRead)
				if !want(name) {
					continue
				}
				add(name, evalReplay(replayLive(t, f, sub, withRead, nil), sub, tru, vf))
			}
		}
	}
	t.Logf("\nSUMMARY (watched: the minute before the last heard one, per step; final: the whole file after the last step)\n%-58s %s",
		"case", "fit  false-seg changes worse first-ok | watched n ≤0.3 ≤0.5 ≤1 med p95 | final")
	for _, r := range rows {
		t.Logf("%-58s %s", r.name, r.r)
	}
}

// TestSnapshotWriteFixture writes the Avatar S01E02 case (own-track windows, played speech, the
// Turkish subtitle's cue times; no text) as a compact fixture into NFX_FIXTURE_OUT.
func TestSnapshotWriteFixture(t *testing.T) {
	out := os.Getenv("NFX_FIXTURE_OUT")
	if out == "" {
		t.Skip("NFX_FIXTURE_OUT not set")
	}
	files, subs := loadSnapshot(t)
	f, s := findFile(files, "494024e0"), findSub(subs, "turkcealtyazi/053cb0e3")
	if f == nil || s == nil {
		t.Fatal("Avatar S01E02 not in the snapshot")
	}
	fx := syncFixture{Duration: f.ref.Probe.Duration}
	for _, w := range f.ref.Windows {
		fx.Subs = append(fx.Subs, fixtureWindowOf(w))
	}
	for _, w := range f.live {
		fx.Live = append(fx.Live, fixtureLive{Start: w.Start, Bits: packBits(w.Speech), N: len(w.Speech)})
	}
	for _, c := range s.spans {
		fx.Sub = append(fx.Sub, [2]float64{math.Round(c.start*1000) / 1000, math.Round(c.end*1000) / 1000})
	}
	b, err := json.Marshal(fx)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(out, "avatar_s01e02.json"), b, 0o644); err != nil {
		t.Fatal(err)
	}
	t.Logf("%d bytes", len(b))
}

// distortion makes a subtitle from one with known cue truth.
type distortion struct {
	name  string
	apply func(cues []span, truth []float64) ([]span, []float64)
}

func mapCues(fn func(u float64) float64) func([]span, []float64) ([]span, []float64) {
	return func(cues []span, truth []float64) ([]span, []float64) {
		out := make([]span, len(cues))
		for i, c := range cues {
			out[i] = span{fn(c.start), fn(c.start) + (c.end - c.start)}
		}
		return out, truth
	}
}

func distortions() []distortion {
	jitter := func(amount, bias float64, seed int64) func([]span, []float64) ([]span, []float64) {
		return func(cues []span, truth []float64) ([]span, []float64) {
			rng := rand.New(rand.NewSource(seed))
			out := make([]span, len(cues))
			for i, c := range cues {
				d := bias + (rng.Float64()*2-1)*amount
				out[i] = span{c.start + d, c.end + d}
			}
			return out, truth
		}
	}
	return []distortion{
		{"none", mapCues(func(u float64) float64 { return u })},
		{"+2.5s", mapCues(func(u float64) float64 { return u + 2.5 })},
		{"-7.37s", mapCues(func(u float64) float64 { return u - 7.37 })},
		{"+30s", mapCues(func(u float64) float64 { return u + 30 })},
		{"-60s", mapCues(func(u float64) float64 { return u - 60 })},
		{"pal", mapCues(func(u float64) float64 { return u * 23.976 / 25 })},
		{"24fps+1s", mapCues(func(u float64) float64 { return u*23.976/24 + 1 })},
		{"title-cut", mapCues(func(u float64) float64 { // the subtitle's release has 2.2 s more title sequence
			if u < 120 {
				return u + 0.5
			}
			return u + 2.7
		})},
		{"ad-breaks", mapCues(func(u float64) float64 { // two ad breaks cut differently
			switch {
			case u < 330:
				return u - 0.4
			case u < 700:
				return u + 1.6
			}
			return u + 4.1
		})},
		{"jitter", jitter(0.3, 0, 1)},
		{"early-fan", jitter(0.25, -0.5, 2)},
		{"drop30", func(cues []span, truth []float64) ([]span, []float64) {
			rng := rand.New(rand.NewSource(3))
			var out []span
			var tr []float64
			for i, c := range cues {
				if rng.Float64() >= 0.3 {
					out, tr = append(out, span{c.start + 1.2, c.end + 1.2}), append(tr, truth[i])
				}
			}
			return out, tr
		}},
		{"merge", func(cues []span, truth []float64) ([]span, []float64) {
			rng := rand.New(rand.NewSource(4))
			var out []span
			var tr []float64
			for i := 0; i < len(cues); i++ {
				c, ti := cues[i], truth[i] // a merged cue keeps its first line's truth
				if i+1 < len(cues) && rng.Float64() < 0.35 && cues[i+1].start-c.end < 1 {
					c.end = cues[i+1].end
					i++
				}
				out, tr = append(out, span{c.start - 0.8, c.end - 0.8}), append(tr, ti)
			}
			return out, tr
		}},
		{"extra", func(cues []span, truth []float64) ([]span, []float64) {
			rng := rand.New(rand.NewSource(5))
			out := append([]span(nil), cues...)
			tr := append([]float64(nil), truth...)
			last := cues[len(cues)-1].end
			for range len(cues) / 8 {
				s := rng.Float64() * last
				out, tr = append(out, span{s, s + 1.5}), append(tr, math.NaN())
			}
			return out, tr
		}},
	}
}

// TestSnapshotEpisodes tells the snapshot's episodes apart by the files' own tracks (files
// sharing a subtitle that 3 of their windows agree on are one episode; a subtitle belongs to the
// episode of the file where most, at least 2, windows agree), then times every subtitle of about
// the file's length to every file with its read windows: other episodes' subtitles must not fit
// (also replayed live), this episode's ones should, at the true offsets.
func TestSnapshotEpisodes(t *testing.T) {
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
	group := make([]int, len(fs))
	for i := range group {
		group[i] = i
	}
	find := func(i int) int {
		for group[i] != i {
			i = group[i]
		}
		return i
	}
	for j := range subs {
		first := -1
		for i := range fs {
			if agree[i][j] >= 3 {
				if first < 0 {
					first = i
				} else {
					group[find(i)] = find(first)
				}
			}
		}
	}
	subGroup := make([]int, len(subs))
	for j := range subs {
		subGroup[j] = -1
		best := 1
		for i := range fs {
			if agree[i][j] > best {
				best, subGroup[j] = agree[i][j], find(i)
			}
		}
	}
	for i, f := range fs {
		var names []string
		for j, s := range subs {
			if subGroup[j] == find(i) {
				names = append(names, s.name)
			}
		}
		t.Logf("episode %d: %s: %s", find(i), f.name[:8], strings.Join(names, " "))
	}

	rightTried, rightAcc, wrongTried, wrongAcc := 0, 0, 0, 0
	liveTried, livePairs, liveSteps, liveAcc := 0, 0, 0, 0
	var rightErrs []float64
	for i, f := range fs {
		dur := f.ref.Probe.Duration
		live := 0
		for j, s := range subs {
			last := s.spans[len(s.spans)-1].end
			if subGroup[j] < 0 || last < 0.75*dur || last > 1.1*dur {
				continue
			}
			same := subGroup[j] == find(i)
			fit, ok := alignWith(clampSpans(s.spans, dur), prepare(f.ref.Windows), syncScales, syncSplitPenalty, -1)
			accepted := ok && fit.good() && lengthFits(s.spans, fit, dur)
			if same {
				rightTried++
				served := identityFit
				if accepted {
					rightAcc++
					served = fit
				} else {
					t.Logf("read: %s rejects its episode's %s: %s", f.name[:8], s.name, describeFit(fit))
				}
				if tr, ok := findTruth(s.spans, f.ref); ok {
					rightErrs = append(rightErrs, timingErrors(served, s.spans, tr.cueTruth(s.spans), nil)...)
				}
				continue
			}
			wrongTried++
			if accepted {
				wrongAcc++
				t.Logf("read: %s accepts %s: %s", f.name[:8], s.name, describeFit(fit))
			}
			if len(f.live) < 3 || live >= 6 {
				continue
			}
			live++
			liveTried++
			acc := 0
			replayEvery = 2
			for _, st := range replayLive(t, f, s.spans, true, nil) {
				liveSteps++
				if st.fits {
					acc++
				}
			}
			replayEvery = 1
			liveAcc += acc
			if acc > 0 {
				livePairs++
				t.Logf("live: %s accepts %s in %d steps", f.name[:8], s.name, acc)
			}
		}
	}
	t.Logf("SUMMARY episodes: read: this episode's subtitles fit %d/%d (truth-checked cues %s); other episodes' fit %d/%d; live replays of other episodes: %d/%d pairs, %d/%d re-fits accepted",
		rightAcc, rightTried, statsOf(rightErrs), wrongAcc, wrongTried, livePairs, liveTried, liveAcc, liveSteps)
}

// syncFixture is a real timing case without text: the file's read windows, the played speech
// and a subtitle's cue times.
type syncFixture struct {
	Duration float64         `json:"duration"`
	Subs     []fixtureWindow `json:"subs"`
	Live     []fixtureLive   `json:"live"`
	Sub      [][2]float64    `json:"sub"`
	Alt      [][2]float64    `json:"alt,omitempty"` // another subtitle of the language
}

// fixtureWindow is a read window as runs of frames [from, to) where a cue shows; the third
// value is 1 when from is a cue start.
type fixtureWindow struct {
	Start  float64  `json:"start"`
	Kind   string   `json:"kind"`
	Frames int      `json:"n"`
	Runs   [][3]int `json:"runs"`
}

type fixtureLive struct {
	Start float64 `json:"start"`
	Bits  []byte  `json:"bits"`
	N     int     `json:"n"`
}

func fixtureWindowOf(w refWindow) fixtureWindow {
	fw := fixtureWindow{Start: w.Start, Kind: w.Kind, Frames: len(w.Speech)}
	for i := 0; i < len(w.Speech); {
		if w.Speech[i] == 0 {
			i++
			continue
		}
		j := i + 1
		for j < len(w.Speech) && w.Speech[j] == 1 {
			j++
		}
		marked := 0
		if w.Speech[i] == 2 {
			marked = 1
		}
		fw.Runs = append(fw.Runs, [3]int{i, j, marked})
		i = j
	}
	return fw
}

func (fw fixtureWindow) window() refWindow {
	w := refWindow{Start: fw.Start, Kind: fw.Kind, Speech: make([]byte, fw.Frames)}
	for _, r := range fw.Runs {
		for i := r[0]; i < r[1] && i < fw.Frames; i++ {
			w.Speech[i] = 1
		}
		if r[2] == 1 && r[0] < fw.Frames {
			w.Speech[r[0]] = 2
		}
	}
	return w
}

func loadFixture(t *testing.T, name string) (*snapFile, []span) {
	f, sub, _ := loadFixtureAlt(t, name)
	return f, sub
}

func loadFixtureAlt(t *testing.T, name string) (*snapFile, []span, []span) {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("testdata", "sync", name))
	if err != nil {
		t.Fatal(err)
	}
	var fx syncFixture
	if err := json.Unmarshal(b, &fx); err != nil {
		t.Fatal(err)
	}
	f := &snapFile{name: syncHash, ref: syncRef{Probe: mediaProbe{Duration: fx.Duration, FPS: 23.976, Audio: 1, TextSub: 2}}}
	for _, w := range fx.Subs {
		f.ref.Windows = append(f.ref.Windows, w.window())
	}
	for _, l := range fx.Live {
		f.live = append(f.live, refWindow{Start: l.Start, Kind: "live", Speech: unpackBits(l.Bits, l.N)})
	}
	spans := func(cs [][2]float64) []span {
		var out []span
		for _, c := range cs {
			out = append(out, span{c[0], c[1]})
		}
		return out
	}
	return f, spans(fx.Sub), spans(fx.Alt)
}
