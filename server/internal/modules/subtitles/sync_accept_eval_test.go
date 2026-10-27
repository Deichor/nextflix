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
	"strconv"
	"strings"
	"sync"
	"testing"
)

// When the first live fits are accepted (real data, NFX_TESTDATA): a pair is played from the
// file's start, the speech and the cue tap's own-track windows heard together, and re-fitted
// through decide every step seconds of heard audio (unfinished grid windows from 20 s heard).

// loadOwnTap reads the cue tap's own-track windows of a snapshot file (synclive "own") into f.tap,
// and keeps the file's saved own-track check (savedChecks).
func loadOwnTap(t *testing.T, f *snapFile) {
	b, err := os.ReadFile(filepath.Join(snapshotDir(t), "synclive", f.name+".json"))
	if err != nil {
		return
	}
	var s liveSaved
	if json.Unmarshal(b, &s) != nil {
		return
	}
	if s.Check != nil {
		savedChecks[f.name] = s.Check
	}
	if len(s.Own) == 0 {
		return
	}
	f.tap = map[int]liveWin{}
	for g, w := range s.Own {
		f.tap[g] = w
	}
}

// savedChecks are the snapshot files' own-track checks as the server kept them (the truth fits
// use them, as the server's last re-fits did).
var savedChecks = map[string]*ownSaved{}

type heardStep struct {
	h      float64
	meta   *syncMeta
	served syncFit
	fits   bool
	fresh  *syncMeta // the step's own fit, before what decide keeps (replayFresh)
}

// replayFresh makes replayHeard fit each step once more outside decide (traces); replayHook is
// called after each step's decide.
var (
	replayFresh = false
	replayHook  func(y *syncer, lf *liveFile, ref syncRef, st heardStep)
)

// truncWin is a finished window cut to its first n frames (heard so far).
func truncWin(w liveWin, n int) liveWin {
	bits := unpackBits(w.Bits, w.Frames)
	n = min(n, len(bits))
	out := liveWin{Bits: packBits(bits[:n]), Frames: n}
	for _, s := range w.Starts {
		if s < n {
			out.Starts = append(out.Starts, s)
		}
	}
	return out
}

// replayHeard re-fits through decide at h0, h0+step, ... heard seconds up to upto, the file
// played from 0; the read windows join from readFrom heard seconds on (<0 never), before that
// the reference is a stand-in (busy: being read, else skipped). It returns what each step serves.
func replayHeard(t *testing.T, f *snapFile, sub []span, step, upto, readFrom float64, busy bool) []heardStep {
	return replayHeardFrom(t, f, sub, 0, step, upto, readFrom, busy)
}

// replayHeardFrom is replayHeard with the file played from grid window from (seconds).
func replayHeardFrom(t *testing.T, f *snapFile, sub []span, from, step, upto, readFrom float64, busy bool) []heardStep {
	svc := NewService(nil, Config{CacheDir: t.TempDir(), TorrServerURL: "http://torrserver", LiveTiming: true})
	y := svc.sync
	y.onGrow = func(*liveFile) {}
	y.probe = func(context.Context, string) (mediaProbe, error) { return f.ref.Probe, nil }
	inner := cacheProviderSub(t, svc, "accept|"+f.name[:8], sub)
	key, _ := syncKey(inner, f.name, 1)
	base, _ := svc.File(context.Background(), inner)
	lf := y.liveFile(f.name, 1)
	lf.setDuration(f.ref.Probe.Duration)
	speech := map[int]liveWin{}
	for _, w := range f.live {
		speech[int(math.Round(w.Start/syncWindowLen))] = liveWin{Bits: packBits(w.Speech), Frames: len(w.Speech)}
	}
	var out []heardStep
	g0 := int(math.Round(from / syncWindowLen))
	for h := step; h <= upto+1e-9; h += step {
		lf.mu.Lock()
		lf.done, lf.ownDone = map[int]liveWin{}, map[int]liveWin{}
		lf.own, lf.ownRead = map[int][]uint8{}, map[int]int{}
		for g := g0; float64(g-g0+1)*syncWindowLen <= h; g++ {
			if w, ok := speech[g]; ok {
				lf.done[g] = w
			}
			if w, ok := f.tap[g]; ok {
				lf.ownDone[g] = w
			}
		}
		// the window being heard: speech from 20 s (as finished, like replayFirst), the own track
		// unfinished (no own-track check pair yet)
		g := g0 + int(h/syncWindowLen)
		if n := int((h - float64(g-g0)*syncWindowLen) * syncRate); n > 0 {
			if w, ok := speech[g]; ok && n >= liveMinRun {
				lf.done[g] = truncWin(w, n)
			}
			if w, ok := f.tap[g]; ok {
				bits := unpackBits(w.Bits, w.Frames)
				c := make([]uint8, liveWindowFrames)
				for i := 0; i < n && i < len(bits); i++ {
					c[i] = ownRead
					if bits[i] != 0 {
						c[i] |= ownShows
					}
				}
				for _, s := range w.Starts {
					if s < n {
						c[s] |= ownStarts
					}
				}
				lf.own[g], lf.ownRead[g] = c, min(n, len(bits))
			}
		}
		lf.complete = false
		lf.mu.Unlock()
		ref := f.ref
		if readFrom < 0 || h < readFrom {
			ref = syncRef{Probe: f.ref.Probe, Skipped: true, Busy: busy}
		}
		st := heardStep{h: h, served: identityFit}
		if replayFresh {
			live, _ := lf.windows()
			r := y.fitRef(inner, f.name, 1, ref, live, nil)
			if !tooLittleRef(r.ref, r.live) {
				m, _, _ := y.fitAside(context.Background(), inner, f.name, 1, r, base, false, y.meta(key), false)
				st.fresh = &m
			}
		}
		y.decide(context.Background(), inner, f.name, 1, base, ref, false)
		if m := y.meta(key); m != nil {
			c := *m
			st.meta = &c
			if m.fits() {
				st.served, st.fits = m.Fit, true
			}
		}
		if replayHook != nil {
			replayHook(y, lf, ref, st)
		}
		out = append(out, st)
	}
	return out
}

type offsetPeak struct{ off, v float64 }

// offsetPeaks are the n best single offsets (local maxima 1.5 s apart) of the windows at scale k,
// in matched lines per reference line.
func offsetPeaks(cues []span, windows []refWindow, k float64, n int) []offsetPeak {
	ws := prepare(windows)
	if len(ws) == 0 || len(cues) == 0 {
		return nil
	}
	sorted := append([]span(nil), cues...)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i].start < sorted[j].start })
	starts := startSignal(sorted, k)
	d := int(syncMaxOffset * syncRate)
	total := make([]float64, 2*d+1)
	for _, w := range ws {
		for q, v := range windowCurve(w, starts, d) {
			total[q] += v
		}
	}
	lines := refLines(ws)
	var out []offsetPeak
	used := make([]bool, len(total))
	for len(out) < n {
		best := -1
		for q, v := range total {
			if !used[q] && (best < 0 || v > total[best]) {
				best = q
			}
		}
		if best < 0 {
			break
		}
		out = append(out, offsetPeak{float64(best-d) / syncRate, total[best] / lines})
		for q := max(best-150, 0); q <= min(best+150, len(total)-1); q++ {
			used[q] = true
		}
	}
	return out
}

func peaksText(ps []offsetPeak) string {
	var parts []string
	for _, p := range ps {
		parts = append(parts, fmt.Sprintf("%+.2f:%.2f", p.off, p.v))
	}
	return strings.Join(parts, " ")
}

// TestSnapshotAcceptTrace prints the re-fits of one pair (NFX_PAIR=file8,sub8) played from 0:
// each fit, the bars it missed and the best single offsets of its windows.
func TestSnapshotAcceptTrace(t *testing.T) {
	pair := strings.Split(os.Getenv("NFX_PAIR"), ",")
	if len(pair) != 2 {
		t.Skip("NFX_PAIR not set")
	}
	files, subs := loadSnapshot(t)
	f, s := findFile(files, pair[0]), findSub(subs, pair[1])
	if f == nil || s == nil {
		t.Fatal("not in the snapshot")
	}
	loadOwnTap(t, f)
	if n, err := strconv.Atoi(os.Getenv("NFX_READN")); err == nil && n < len(f.ref.Windows) {
		f.ref.Windows = f.ref.Windows[:n] // the first pass's windows only
	}
	t.Logf("%s %s (%d cues), %d own windows, read windows at %v", f.label(), s.name, len(s.spans), len(f.tap), windowStartsOf(f.ref.Windows))
	upto := 420.0
	if v, err := strconv.ParseFloat(os.Getenv("NFX_UPTO"), 64); err == nil {
		upto = v
	}
	reads := []float64{-1, 0}
	if v, err := strconv.ParseFloat(os.Getenv("NFX_READFROM"), 64); err == nil {
		reads = []float64{v}
	}
	for _, read := range reads {
		if read >= 0 && len(f.ref.Windows) == 0 {
			continue
		}
		t.Logf("read from %.0f s heard:", read)
		replayFresh = true
		t.Cleanup(func() { replayFresh, replayHook = false, nil })
		if os.Getenv("NFX_WHATIF") != "" {
			sp := clampSpans(s.spans, f.ref.Probe.Duration)
			replayHook = func(y *syncer, lf *liveFile, ref syncRef, st heardStep) {
				speech, _ := lf.windowsOf(false)
				lf.mu.Lock()
				own := lf.ownWindows()
				lf.mu.Unlock()
				if len(speech) == 0 {
					return
				}
				sf, _ := alignLive(sp, speech, syncScales[:1])
				both, _ := alignLive(sp, append(append([]refWindow(nil), own...), speech...), syncScales[:1])
				t.Logf("      speech %-38s score %.3f base %.3f peak %.3f lines %.0f | own+speech %-30s score %.3f base %.3f peak %.3f lag %.2f",
					segText(sf), sf.Score, sf.Base, sf.Peak, refLines(prepare(speech)), segText(both), both.Score, both.Base, both.Peak, both.Lag)
			}
		}
		for _, st := range replayHeard(t, f, s.spans, 10, upto, read, true) {
			if st.fresh == nil {
				t.Logf("  heard %4.0f none", st.h)
				continue
			}
			m := st.fresh
			if st.meta != nil && st.meta.fits() && fitChanged(st.meta, *m) {
				t.Logf("  heard %4.0f served %s", st.h, segText(st.served))
			}
			t.Logf("  heard %4.0f %-9s %-40s score %.3f base %.3f peak %.3f (%s) | %d windows (%d live) %s lines %.0f | peaks %s",
				st.h, m.State, segText(m.Fit), m.Fit.Score, m.Fit.Base, m.Fit.Peak, m.Fit.missedBars(), m.Windows, m.LiveWindows,
				m.Ref, refLines(prepare(m.Fit.windows)), peaksText(offsetPeaks(s.spans, m.Fit.windows, m.Fit.Scale, 4)))
			if at := os.Getenv("NFX_WINAT"); at != "" && strings.Contains(","+at+",", fmt.Sprintf(",%.0f,", st.h)) {
				var offs []float64
				for _, x := range strings.Split(os.Getenv("NFX_OFFS"), ",") {
					if v, err := strconv.ParseFloat(x, 64); err == nil {
						offs = append(offs, v)
					}
				}
				for _, line := range windowTable(s.spans, m.Fit, offs) {
					t.Logf("      %s", line)
				}
			}
			if os.Getenv("NFX_WHATIF") != "" && len(m.Fit.windows) > 0 {
				ws := prepare(m.Fit.windows)
				sp := clampSpans(s.spans, f.ref.Probe.Duration)
				one, _ := alignWith(sp, ws, syncScales[:1], math.Inf(1), 0)
				chg, _ := alignWith(sp, ws, syncScales[:1], syncSplitPenalty, 1)
				free, _ := alignWith(sp, ws, syncScales[:1], syncSplitPenalty, -1)
				for _, x := range []struct {
					n string
					f syncFit
				}{{"one", one}, {"1chg", chg}, {"free", free}} {
					t.Logf("      %-5s %-40s score %.3f base %.3f peak %.3f", x.n, segText(x.f), x.f.Score, x.f.Base, x.f.Peak)
				}
			}
		}
	}
}

// windowTable lists the fit's windows: kind, lines, the offset each ended up with, its best
// offset within ±15 s of it and what it scores (matched lines) at the given offsets.
func windowTable(cues []span, f syncFit, offs []float64) []string {
	ws := prepare(f.windows)
	sort.Slice(ws, func(i, j int) bool { return ws[i].Start < ws[j].Start })
	sorted := append([]span(nil), cues...)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i].start < sorted[j].start })
	starts := startSignal(sorted, f.Scale)
	d := int(syncMaxOffset * syncRate)
	var out []string
	for m, w := range ws {
		if w.Kind == "subs" {
			w.Start += f.Lag
		}
		c := windowCurve(w, starts, d)
		at := func(o float64) float64 { return c[max(0, min(len(c)-1, int(math.Round(o*syncRate))+d))] }
		own := 0.0
		if m < len(f.windowOffsets) {
			own = f.windowOffsets[m]
		}
		q := argmax(c, int(math.Round(own*syncRate))+d-15*syncRate, int(math.Round(own*syncRate))+d+15*syncRate)
		line := fmt.Sprintf("%6.0f %-4s lines %3.0f fit %+6.2f (%5.1f) best %+6.2f (%5.1f) base %4.1f |", w.Start, w.Kind, w.lines, own, at(own),
			float64(q-d)/syncRate, c[q], mean(c))
		for _, o := range offs {
			line += fmt.Sprintf(" %+.2f:%4.1f", o, at(o))
		}
		out = append(out, line)
	}
	return out
}

func windowStartsOf(ws []refWindow) []float64 {
	var out []float64
	for _, w := range ws {
		out = append(out, math.Round(w.Start))
	}
	return out
}

// TestSnapshotWriteAcceptFixtures writes the cases of sync_accept_test.go (read own-track
// windows with their speech, the played speech and the cue tap's own windows of the first
// minutes, a subtitle's cue times; no text) into the directory NFX_FIXTURE_OUT.
func TestSnapshotWriteAcceptFixtures(t *testing.T) {
	out := os.Getenv("NFX_FIXTURE_OUT")
	if out == "" {
		t.Skip("NFX_FIXTURE_OUT not set")
	}
	files, subs := loadSnapshot(t)
	for _, c := range []struct {
		name, file, sub string
		reads           int     // read windows, the first pass's
		upto            float64 // played stretch kept
	}{
		{"mentalist_s03e20.json", "16b47b61", "turkcealtyazi/f186b471", 6, 1080},
		{"avatar_s01e07.json", "3f5c5bf8", "turkcealtyazi/561163c0", 4, 300},
	} {
		f, s := findFile(files, c.file), findSub(subs, c.sub)
		if f == nil || s == nil {
			t.Fatalf("%s not in the snapshot", c.name)
		}
		loadOwnTap(t, f)
		fx := syncFixture{Duration: f.ref.Probe.Duration}
		for _, w := range f.ref.Windows[:min(c.reads, len(f.ref.Windows))] {
			fx.Subs = append(fx.Subs, fixtureWindowOf(w))
			for _, sp := range f.ref.Speech {
				if sp.Start == w.Start {
					fx.Speech = append(fx.Speech, fixtureWindowOf(sp))
				}
			}
		}
		for _, w := range f.live {
			if w.end() <= c.upto {
				fx.Live = append(fx.Live, fixtureLive{Start: w.Start, Bits: packBits(w.Speech), N: len(w.Speech)})
			}
		}
		for g, o := range f.tap {
			if float64(g+1)*syncWindowLen > c.upto {
				continue
			}
			w := refWindow{Start: float64(g) * syncWindowLen, Kind: "subs", Speech: unpackBits(o.Bits, o.Frames)}
			for _, i := range o.Starts {
				if i < len(w.Speech) {
					w.Speech[i] = 2
				}
			}
			fx.Own = append(fx.Own, fixtureWindowOf(w))
		}
		sort.Slice(fx.Own, func(i, j int) bool { return fx.Own[i].Start < fx.Own[j].Start })
		for _, sp := range s.spans {
			fx.Sub = append(fx.Sub, [2]float64{math.Round(sp.start*1000) / 1000, math.Round(sp.end*1000) / 1000})
		}
		b, err := json.Marshal(fx)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(out, c.name), b, 0o644); err != nil {
			t.Fatal(err)
		}
		t.Logf("%s: %d read, %d speech, %d live, %d own windows, %d cues, %d bytes", c.name, len(fx.Subs), len(fx.Speech),
			len(fx.Live), len(fx.Own), len(fx.Sub), len(b))
	}
}

// longestRun is the longest stretch of grid windows played without a gap (seconds of the file).
func longestRun(f *snapFile) (from, end float64) {
	got := map[int]bool{}
	for _, w := range f.live {
		got[int(math.Round(w.Start/syncWindowLen))] = true
	}
	best, bestN := 0, 0
	for g := range got {
		if got[g-1] {
			continue
		}
		n := 0
		for got[g+n] {
			n++
		}
		if n > bestN || n == bestN && g < best {
			best, bestN = g, n
		}
	}
	return float64(best) * syncWindowLen, float64(best+bestN) * syncWindowLen
}

// readKinds reads NFX_KINDS ("<file key> <movie|tv> <title>").
func readKinds() map[string]string {
	out := map[string]string{}
	b, err := os.ReadFile(os.Getenv("NFX_KINDS"))
	if err != nil {
		return out
	}
	for _, l := range strings.Split(string(b), "\n") {
		if p := strings.Fields(l); len(p) >= 2 {
			out[p[0]] = p[1]
		}
	}
	return out
}

// TestSnapshotHeldSweep judges the acceptance variants on every file of the snapshot played for 4
// minutes or more without a gap: of the subtitles of about its length, up to 3 that fit the whole
// file (screened on its read windows, else on its played ones) and up to 4 that do not are
// replayed through decide as that stretch was heard, with and without the read windows. Reported
// by movies and series (NFX_KINDS; files not listed count by length: 70 minutes or more a movie).
// Positives: re-fits serving a segment more than 1 s off in the heard part, served changes, the
// cues of the 200 s before the heard head within 0.5 s. Others: every accepted re-fit is false.
func TestSnapshotHeldSweep(t *testing.T) {
	files, subs := loadSnapshot(t)
	kinds := readKinds()
	variants := []acceptVariant{acceptVariants[0], acceptVariants[1]}
	if os.Getenv("NFX_SPLIT") != "" {
		variants = append(variants, acceptVariants[2])
	}
	workers := 8
	if n, err := strconv.Atoi(os.Getenv("NFX_WORKERS")); err == nil && n > 0 {
		workers = n
	}
	type agg struct {
		files, pairs, steps, fitted, falseSeg, changes int
		ahead                                          []float64
	}
	var mu sync.Mutex
	aggs := map[string]*agg{}
	get := func(n string) *agg { // mu held
		if aggs[n] == nil {
			aggs[n] = &agg{}
		}
		return aggs[n]
	}
	type job struct {
		f         *snapFile
		kind      string
		from, end float64
		pos, neg  []*snapSub
		truths    map[*snapSub][]float64
	}
	only := os.Getenv("NFX_CASE")
	var jobs []*job
	for _, f := range files {
		from, end := longestRun(f)
		if end-from < 240 || only != "" && !strings.HasPrefix(f.name, only) {
			continue
		}
		loadOwnTap(t, f) // (before the workers: savedChecks)
		kind := kinds[f.name]
		switch {
		case kind == "tv":
			kind = "series"
		case kind == "" && f.ref.Probe.Duration >= 70*60:
			kind = "movie?"
		case kind == "":
			kind = "series?"
		}
		jobs = append(jobs, &job{f: f, kind: kind, from: from, end: end, truths: map[*snapSub][]float64{}})
	}
	run := func(fn func(j *job)) {
		ch := make(chan *job)
		var wg sync.WaitGroup
		for range workers {
			wg.Add(1)
			go func() {
				defer wg.Done()
				for j := range ch {
					fn(j)
				}
			}()
		}
		for _, j := range jobs {
			ch <- j
		}
		close(ch)
		wg.Wait()
	}
	// Positives and others of each file
	run(func(j *job) {
		f := j.f
		d := f.ref.Probe.Duration
		screen := f.ref.Windows
		if len(prepare(screen)) < 4 {
			screen = nil
			for i := 0; i < len(f.live); i += max(1, len(f.live)/10) {
				screen = append(screen, f.live[i])
			}
		}
		var cands []*snapSub
		for _, s := range subs {
			if last := s.spans[len(s.spans)-1].end; math.Abs(last-d) <= 0.15*d {
				cands = append(cands, s)
			}
		}
		sort.SliceStable(cands, func(a, b int) bool {
			return math.Abs(cands[a].spans[len(cands[a].spans)-1].end-d) < math.Abs(cands[b].spans[len(cands[b].spans)-1].end-d)
		})
		for _, s := range cands {
			sf, ok := alignWith(clampSpans(s.spans, d), prepare(screen), syncScales, syncSplitPenalty, -1)
			if ok && sf.Score-sf.Base >= 0.08 {
				if len(j.pos) >= 3 {
					continue
				}
				if tf, ok := fullFit(t, f, s.spans); ok {
					tr := make([]float64, len(s.spans))
					for i, c := range s.spans {
						tr[i] = tf.mapTime(c.start)
					}
					j.truths[s] = tr
					j.pos = append(j.pos, s)
				}
			} else if len(j.neg) < 4 {
				j.neg = append(j.neg, s)
			}
		}
		t.Logf("%s %-7s %s played %.0f-%.0f s: %d fit, %d others", f.label(), j.kind, kinds[f.name], j.from, j.end, len(j.pos), len(j.neg))
		mu.Lock()
		get(j.kind).files++
		mu.Unlock()
	})
	for _, v := range variants {
		undo := v.set()
		run(func(j *job) {
			f, from, end := j.f, j.from, j.end
			for _, s := range j.pos {
				for _, read := range []float64{0, -1} {
					if read >= 0 && len(f.ref.Windows) == 0 {
						continue
					}
					upto := math.Min(end-from, 1200)
					if read < 0 {
						upto = math.Min(end-from, 600)
					}
					steps := replayHeardFrom(t, f, s.spans, from, 30, upto, read, false)
					var prev *syncMeta
					fitted, fs, ch := 0, 0, 0
					var ahead []float64
					for _, st := range steps {
						if st.fits {
							fitted++
						}
						h := st.h
						if falseSegment(st.served, s.spans, j.truths[s], func(x float64) bool { return x >= from && x < from+h }) {
							fs++
						}
						if st.meta != nil && prev != nil && fitChanged(prev, *st.meta) {
							ch++
						}
						prev = st.meta
						ahead = append(ahead, timingErrors(st.served, s.spans, j.truths[s], func(x float64) bool {
							return x >= from+h-200 && x < from+h
						})...)
					}
					if fs > 0 {
						t.Logf("   %s %-8s read=%-5v %s: %d of %d re-fits serve a false segment", f.name[:8], v.name, read >= 0, s.name, fs, len(steps))
					}
					mu.Lock()
					g := get(fmt.Sprintf("%s %s read=%v", j.kind, v.name, read >= 0))
					g.pairs++
					g.steps += len(steps)
					g.fitted += fitted
					g.falseSeg += fs
					g.changes += ch
					g.ahead = append(g.ahead, ahead...)
					mu.Unlock()
				}
			}
			for _, s := range j.neg {
				steps := replayHeardFrom(t, f, s.spans, from, 30, math.Min(end-from, 420), -1, false)
				acc := 0
				for _, st := range steps {
					if st.fits {
						acc++
					}
				}
				if acc > 0 {
					t.Logf("   %s %-8s other subtitle %s accepted in %d re-fits", f.name[:8], v.name, s.name, acc)
				}
				mu.Lock()
				g := get(fmt.Sprintf("%s %s others", j.kind, v.name))
				g.pairs++
				g.steps += len(steps)
				g.fitted += acc
				if acc > 0 {
					g.falseSeg++
				}
				mu.Unlock()
			}
		})
		undo()
	}
	names := make([]string, 0, len(aggs))
	for n := range aggs {
		names = append(names, n)
	}
	sort.Strings(names)
	t.Logf("SUMMARY")
	for _, n := range names {
		g := aggs[n]
		switch {
		case g.files > 0:
			t.Logf("%-28s %d files", n, g.files)
		case strings.HasSuffix(n, "others"):
			t.Logf("%-28s %3d pairs, %d accepted in %d of %d re-fits", n, g.pairs, g.falseSeg, g.fitted, g.steps)
		default:
			t.Logf("%-28s %3d pairs, re-fits %4d fitted %4d false-seg %3d changes %3d | ahead %s", n, g.pairs, g.steps, g.fitted,
				g.falseSeg, g.changes, statsOf(g.ahead))
		}
	}
}

// TestGeneratedSplitBar (NFX_GEN set) measures the peak bar of the one offset change below
// syncMinWindows live windows on generated episodes: 4-7 contiguous windows of speech over an
// offset change (1-6 s), how often the change is found, and how often another episode's subtitle
// fits the same windows.
func TestGeneratedSplitBar(t *testing.T) {
	if os.Getenv("NFX_GEN") == "" {
		t.Skip("NFX_GEN not set")
	}
	saved := liveSplitPeak
	defer func() { liveSplitPeak = saved }()
	const trials = 400
	for _, bar := range []float64{0.12, 0.15, 0.20} {
		liveSplitPeak = bar
		found, wrong, tried := 0, 0, 0
		for i := int64(0); i < trials; i++ {
			rng := rand.New(rand.NewSource(7000 + i))
			n := 4 + int(i%4)
			from := math.Round(rng.Float64()*(episode-float64(n)*syncWindowLen-120)/syncWindowLen) * syncWindowLen
			at := from + 30 + rng.Float64()*(float64(n)*syncWindowLen-60)
			file := breakFile(5000+i, episode, at)
			o1 := math.Round((rng.Float64()*40-20)*100) / 100
			d := 1 + rng.Float64()*5
			if rng.Intn(2) == 0 {
				d = -d
			}
			sub := subtitleFor(file, 1, func(u float64) float64 {
				if u < at+5 {
					return o1
				}
				return o1 + d
			})
			ws := speechWindows(file, gridStarts(from, from+float64(n)*syncWindowLen), 0.1, 0.15, 0.2, i)
			if f, ok := alignLive(sub, ws, syncScales); ok {
				tried++
				if f.good() && len(f.Segments) == 2 && math.Abs(f.Segments[0].Offset-o1-0.1) < 0.25 &&
					math.Abs(f.Segments[1].Offset-o1-d-0.1) < 0.25 {
					found++
				}
			}
			if f, ok := alignLive(dialogue(9000+i, episode), ws, syncScales); ok && f.good() {
				wrong++
			}
		}
		t.Logf("one-change peak bar %.2f: change found %d/%d, another episode fits %d/%d", bar, found, tried, wrong, trials)
	}
}

// acceptVariant sets the acceptance rules a sweep compares and returns the undo.
type acceptVariant struct {
	name      string
	held      float64 // syncHeldGain (1: off)
	splitPeak float64 // liveSplitPeak
}

func (v acceptVariant) set() func() {
	held, split := syncHeldGain, liveSplitPeak
	syncHeldGain, liveSplitPeak = v.held, v.splitPeak
	return func() { syncHeldGain, liveSplitPeak = held, split }
}

var acceptVariants = []acceptVariant{
	{"before", 1, 2.5 * syncMinPeak},
	{"held", 0.12, 2.5 * syncMinPeak},
	{"split15", 1, 0.15},
}

// fullFit is the pair's fit from every window of the file (read, played speech and own track):
// the truth of the replays.
func fullFit(t *testing.T, f *snapFile, sub []span) (syncFit, bool) {
	svc := NewService(nil, Config{CacheDir: t.TempDir(), TorrServerURL: "http://torrserver", LiveTiming: true})
	y := svc.sync
	y.onGrow = func(*liveFile) {}
	y.probe = func(context.Context, string) (mediaProbe, error) { return f.ref.Probe, nil }
	inner := cacheProviderSub(t, svc, "full|"+f.name[:8], sub)
	key, _ := syncKey(inner, f.name, 1)
	base, _ := svc.File(context.Background(), inner)
	lf := y.liveFile(f.name, 1)
	lf.setDuration(f.ref.Probe.Duration)
	lf.mu.Lock()
	for _, w := range f.live {
		lf.done[int(math.Round(w.Start/syncWindowLen))] = liveWin{Bits: packBits(w.Speech), Frames: len(w.Speech)}
	}
	for g, w := range f.tap {
		lf.ownDone[g] = w
	}
	if s := savedChecks[f.name]; s != nil {
		if c, _, ok := s.check(); ok {
			lf.check, lf.checkN = c, 1<<30 // as the server kept it
		}
	}
	lf.mu.Unlock()
	y.decide(context.Background(), inner, f.name, 1, base, f.ref, false)
	if m := y.meta(key); m != nil && m.fits() {
		return m.Fit, true
	}
	return syncFit{}, false
}

// playedFrom0 is the end of the stretch played without a gap from the file's start (0: none).
func playedFrom0(f *snapFile) float64 {
	got := map[int]bool{}
	for _, w := range f.live {
		got[int(math.Round(w.Start/syncWindowLen))] = true
	}
	g := 0
	for got[g] {
		g++
	}
	return float64(g) * syncWindowLen
}

type pairLine struct{ file, sub string }

func readPairLines(t *testing.T) []pairLine {
	path := os.Getenv("NFX_PAIRS")
	if path == "" {
		t.Skip("NFX_PAIRS not set")
	}
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var out []pairLine
	for _, l := range strings.Split(string(b), "\n") {
		if p := strings.Fields(l); len(p) >= 2 {
			out = append(out, pairLine{p[0], p[1]})
		}
	}
	return out
}

// stepLatency is, per offset change of the truth (file time), how many seconds of audio past it
// were heard before the served fit put 80 % of the cues of the minute after it within 0.3 s
// (-1: not within the replay).
func stepLatency(steps []heardStep, sub []span, truth []float64, tf syncFit, upto float64) []float64 {
	var out []float64
	for i := 1; i < len(tf.Segments); i++ {
		s := tf.Segments[i]
		if math.Abs(s.Offset-tf.Segments[i-1].Offset) < 0.4 {
			continue
		}
		b := s.From + s.Offset
		if b+60 > upto {
			continue
		}
		in := func(x float64) bool { return x >= b+2 && x < b+60 }
		lat := -1.0
		for _, st := range steps {
			if st.h < b {
				continue
			}
			if e := statsOf(timingErrors(st.served, sub, truth, in)); e.n >= 3 && e.w03 >= 0.8 {
				lat = st.h - b
				break
			}
		}
		out = append(out, lat)
	}
	return out
}

// TestSnapshotAcceptSweep replays the served pairs (NFX_PAIRS) played from 0 under each rule
// variant: with the read windows from the start (to 1200 s heard) and without them (the first
// 600 s), against the fit from all windows. Reported: re-fits serving a segment more than 1 s
// off in the heard part, the cues of the 200 s before the heard head within 0.5 s (what the
// viewer sees next), the first accepted fit and the audio past each true offset change before it
// was served. Then other subtitles of the files' length that fit no file (other episodes) are
// replayed without the read windows (first 420 s): every accepted re-fit is false.
func TestSnapshotAcceptSweep(t *testing.T) {
	files, subs := loadSnapshot(t)
	pairs := readPairLines(t)
	only := os.Getenv("NFX_CASE")
	type agg struct {
		steps, fitted, falseSeg int
		ahead                   []float64
		firstAt, lat            []float64
		missed                  int
	}
	aggs := map[string]*agg{}
	get := func(n string) *agg {
		if aggs[n] == nil {
			aggs[n] = &agg{}
		}
		return aggs[n]
	}
	positive := map[string]bool{}
	seen := map[string]bool{}
	for _, p := range pairs {
		f, s := findFile(files, p.file), findSub(subs, p.sub)
		if f == nil || s == nil || seen[p.file+p.sub] || only != "" && !strings.Contains(p.file+" "+p.sub, only) {
			continue
		}
		seen[p.file+p.sub] = true
		loadOwnTap(t, f)
		end := playedFrom0(f)
		if end < 240 {
			t.Logf("%s %s: not played from the start", f.name[:8], s.name)
			continue
		}
		tf, ok := fullFit(t, f, s.spans)
		if !ok {
			t.Logf("%s %s: no truth (does not fit the whole file)", f.name[:8], s.name)
			continue
		}
		positive[f.name+"|"+s.name] = true
		truth := make([]float64, len(s.spans))
		for i, c := range s.spans {
			truth[i] = tf.mapTime(c.start)
		}
		t.Logf("%s %-24s played 0-%.0f s | truth %s", f.label(), s.name, end, segText(tf))
		for _, v := range acceptVariants {
			undo := v.set()
			for _, read := range []float64{0, -1} {
				upto := math.Min(end, 1200)
				if read < 0 {
					upto = math.Min(end, 600)
				} else if len(f.ref.Windows) == 0 {
					continue
				}
				step := 20.0
				if read >= 0 {
					step = 10 // as often as the near-viewer re-fits
				}
				steps := replayHeard(t, f, s.spans, step, upto, read, false)
				name := fmt.Sprintf("%s read=%v", v.name, read >= 0)
				g := get(name)
				fitted, falseSeg, firstAt := 0, 0, -1.0
				var ahead []float64
				for _, st := range steps {
					g.steps++
					if st.fits {
						fitted++
						if firstAt < 0 {
							firstAt = st.h
						}
					}
					h := st.h
					if falseSegment(st.served, s.spans, truth, func(x float64) bool { return x < h }) {
						falseSeg++
					}
					ahead = append(ahead, timingErrors(st.served, s.spans, truth, func(x float64) bool { return x >= h-200 && x < h })...)
				}
				lat := stepLatency(steps, s.spans, truth, tf, upto)
				g.fitted += fitted
				g.falseSeg += falseSeg
				g.ahead = append(g.ahead, ahead...)
				if firstAt >= 0 {
					g.firstAt = append(g.firstAt, firstAt)
				}
				for _, l := range lat {
					if l < 0 {
						g.missed++
					} else {
						g.lat = append(g.lat, l)
					}
				}
				t.Logf("   %-20s fitted %3d/%3d false-seg %2d first %4.0f | ahead %s | step latency %v", name, fitted, len(steps),
					falseSeg, firstAt, statsOf(ahead), lat)
			}
			undo()
		}
	}
	// Other episodes: subtitles of about the file's length that do not fit it
	if os.Getenv("NFX_NEG") != "0" {
		done := map[string]bool{}
		for _, p := range pairs {
			f := findFile(files, p.file)
			if f == nil || done[f.name] || only != "" && !strings.Contains(p.file+" "+p.sub, only) || playedFrom0(f) < 240 {
				continue
			}
			done[f.name] = true
			d := f.ref.Probe.Duration
			n := 0
			for _, s := range subs {
				last := s.spans[len(s.spans)-1].end
				if positive[f.name+"|"+s.name] || math.Abs(last-d) > 0.12*d || n >= 25 {
					continue
				}
				if _, ok := fullFit(t, f, s.spans); ok {
					continue // this episode's after all
				}
				n++
				for _, v := range []acceptVariant{acceptVariants[0], acceptVariants[2]} {
					undo := v.set()
					steps := replayHeard(t, f, s.spans, 20, math.Min(playedFrom0(f), 420), -1, false)
					g := get("other episodes " + v.name)
					acc := 0
					for _, st := range steps {
						g.steps++
						if st.fits {
							acc++
						}
					}
					g.fitted += acc
					if acc > 0 {
						g.falseSeg++
						t.Logf("other episode: %s accepts %s (%s) in %d re-fits", f.name[:8], s.name, v.name, acc)
					}
					undo()
				}
			}
			t.Logf("%s: %d other subtitles replayed", f.name[:8], n)
		}
	}
	names := make([]string, 0, len(aggs))
	for n := range aggs {
		names = append(names, n)
	}
	sort.Strings(names)
	t.Logf("SUMMARY")
	for _, n := range names {
		g := aggs[n]
		if strings.HasPrefix(n, "other") {
			t.Logf("%-28s %d of %d re-fits accepted, %d pairs", n, g.fitted, g.steps, g.falseSeg)
			continue
		}
		t.Logf("%-28s re-fits %4d fitted %4d false-seg %3d | ahead %s | first accepted median %4.0f s | step latency median %3.0f p90 %3.0f max %3.0f, missed %d",
			n, g.steps, g.fitted, g.falseSeg, statsOf(g.ahead), quant(g.firstAt, 0.5), quant(g.lat, 0.5), quant(g.lat, 0.9),
			quant(g.lat, 1), g.missed)
	}
}
