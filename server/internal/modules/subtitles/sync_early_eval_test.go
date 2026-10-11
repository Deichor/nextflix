package subtitles

import (
	"context"
	"fmt"
	"math"
	"os"
	"strings"
	"testing"
)

// The first minutes of playback (real data, NFX_TESTDATA): the play stream's tap hears about
// earlyAhead seconds ahead of the picture (the player's buffer), the file is re-fitted every 30 s
// of audio (unfinished grid windows count from 20 s), and a cue is shown with the timing served
// when the picture reaches it.

const (
	earlyAhead   = 90.0
	earlyStep    = 30.0
	earlyMinutes = 5
)

type earlyFit struct {
	heard  float64
	served syncFit
	state  string
}

// replayEarly re-fits from the file's start: read is "all" (the read windows), "early" (only
// those in the first minutes) or "none" (a file above the byte budget).
func replayEarly(t *testing.T, f *snapFile, sub []span, read string) []earlyFit {
	svc := NewService(nil, Config{CacheDir: t.TempDir(), TorrServerURL: "http://torrserver", LiveTiming: true})
	y := svc.sync
	y.onGrow = func(*liveFile) {}
	ref := f.ref
	switch read {
	case "none":
		ref = syncRef{Probe: ref.Probe, Skipped: true}
	case "early":
		ref = syncRef{Probe: ref.Probe, Skipped: true}
		for _, w := range f.ref.Windows {
			if w.Start < 150 { // the first read window (20-80 s)
				ref.Windows = append(ref.Windows, w)
			}
		}
	case "own2":
		ref = syncRef{Probe: ref.Probe, Skipped: true}
	}
	var own []refWindow // read at the play start: ready about 30 s into playback
	if read == "own2" {
		own = ownWindows(earlyOwnTrack[f.name[:8]], []float64{0, syncWindowLen})
	}
	y.probe = func(context.Context, string) (mediaProbe, error) { return ref.Probe, nil }
	inner := cacheProviderSub(t, svc, "early|"+f.name[:8], sub)
	key, _ := syncKey(inner, f.name, 1)
	base, _ := svc.File(context.Background(), inner)
	lf := y.liveFile(f.name, 1)
	lf.setDuration(ref.Probe.Duration)
	live := map[int]refWindow{}
	for _, w := range f.live {
		live[int(math.Round(w.Start/syncWindowLen))] = w
	}
	var out []earlyFit
	for h := earlyStep; h <= earlyMinutes*60+earlyAhead; h += earlyStep {
		lf.mu.Lock()
		lf.done = map[int]liveWin{}
		for g := 0; float64(g+1)*syncWindowLen <= h; g++ {
			if w, ok := live[g]; ok {
				lf.done[g] = liveWin{Bits: packBits(w.Speech), Frames: len(w.Speech)}
			}
		}
		g := int(h / syncWindowLen)
		if rest := h - float64(g)*syncWindowLen; rest*syncRate >= liveMinRun {
			if w, ok := live[g]; ok {
				n := min(int(rest*syncRate), len(w.Speech))
				lf.done[g] = liveWin{Bits: packBits(w.Speech[:n]), Frames: n}
			}
		}
		lf.complete = false
		lf.mu.Unlock()
		r := ref
		if h >= 120 && len(own) > 0 {
			r.Windows = own
		}
		y.decide(context.Background(), inner, f.name, 1, base, r, false)
		e := earlyFit{heard: h, served: identityFit, state: "none"}
		if m := y.meta(key); m != nil {
			e.state = m.State
			if m.fits() {
				e.served = m.Fit
			}
		}
		out = append(out, e)
	}
	return out
}

type earlyReport struct {
	watched errStats
	perMin  [earlyMinutes]float64 // share of the cues of each minute within 0.5 s
	firstOK int                   // minute from which every early minute is right (earlyMinutes: never)
	fitAt   float64               // seconds of audio of the first fit (0: none)
}

func (r earlyReport) String() string {
	var mins []string
	for _, v := range r.perMin {
		mins = append(mins, fmt.Sprintf("%3.0f", 100*v))
	}
	return fmt.Sprintf("first-ok min %d, first fit at %3.0f s heard | minutes 0-5 %s | per minute ≤0.5s %s %%", r.firstOK,
		r.fitAt, r.watched, strings.Join(mins, " "))
}

// evalEarly scores the cues of the first minutes with the timing served when the picture reaches them.
func evalEarly(fits []earlyFit, cues []span, truth []float64) earlyReport {
	var r earlyReport
	var all []float64
	var n, ok [earlyMinutes]int
	for _, e := range fits {
		if e.state == syncStateSynced || e.state == syncStateUnchanged {
			r.fitAt = e.heard
			break
		}
	}
	for i, c := range cues {
		tt := truth[i]
		if math.IsNaN(tt) || tt < 0 || tt >= earlyMinutes*60 {
			continue
		}
		served := identityFit
		for _, e := range fits {
			if e.heard <= tt+earlyAhead {
				served = e.served
			}
		}
		err := math.Abs(served.mapTime(c.start) - tt)
		all = append(all, err)
		m := int(tt / 60)
		n[m]++
		if err <= 0.5 {
			ok[m]++
		}
	}
	r.watched = statsOf(all)
	r.firstOK = earlyMinutes
	for m := earlyMinutes - 1; m >= 0; m-- {
		r.perMin[m] = 1
		if n[m] > 0 {
			r.perMin[m] = float64(ok[m]) / float64(n[m])
		}
		if r.perMin[m] < 0.9 {
			break
		}
		r.firstOK = m
	}
	for m := range r.perMin {
		if n[m] > 0 {
			r.perMin[m] = float64(ok[m]) / float64(n[m])
		}
	}
	return r
}

// TestSnapshotEarly reports the first minutes: real pairs, the Avatar distortions, and other
// episodes' subtitles (any timed result in the first minutes counts as a false accept).
func TestSnapshotEarly(t *testing.T) {
	files, subs := loadSnapshot(t)
	verbose := os.Getenv("NFX_EVAL") == "verbose"
	type agg struct {
		errs     []float64
		firstOK  []int
		runs     int
		never    int
		fitSumAt float64
		fitted   int
	}
	groups := map[string]*agg{}
	add := func(group, name string, fits []earlyFit, cues []span, truth []float64) {
		r := evalEarly(fits, cues, truth)
		t.Logf("%-44s %s", name, r)
		if verbose {
			for _, e := range fits {
				t.Logf("    heard %3.0f %-9s %s", e.heard, e.state, segText(e.served))
			}
		}
		g := groups[group]
		if g == nil {
			g = &agg{}
			groups[group] = g
		}
		g.runs++
		g.firstOK = append(g.firstOK, r.firstOK)
		if r.firstOK == earlyMinutes {
			g.never++
		}
		if r.fitAt > 0 {
			g.fitted++
			g.fitSumAt += r.fitAt
		}
		for i, c := range cues {
			if tt := truth[i]; !math.IsNaN(tt) && tt >= 0 && tt < earlyMinutes*60 {
				served := identityFit
				for _, e := range fits {
					if e.heard <= tt+earlyAhead {
						served = e.served
					}
				}
				g.errs = append(g.errs, math.Abs(served.mapTime(c.start)-tt))
			}
		}
	}

	// Avatar S01E02: the real subtitles and the distortions (truth: one offset, 0.00)
	if f := findFile(files, "494024e0"); f != nil {
		if own := findSub(subs, "subdl/091b57bb"); own != nil {
			earlyOwnTrack["494024e0"] = own.spans // its own English track
		}
		for _, base := range []string{"subdl/091b57bb", "turkcealtyazi/053cb0e3"} {
			s := findSub(subs, base)
			if s == nil {
				continue
			}
			ct := truth{scale: 1, constant: true, duration: f.ref.Probe.Duration}.cueTruth(s.spans)
			for _, d := range append(distortions(), teaserDistortion) {
				sub, tru := d.apply(s.spans, ct)
				for _, read := range []string{"all", "early", "own2", "none"} {
					add("avatar read="+read, fmt.Sprintf("E02 %s %-10s read=%s", base[:5], d.name, read), replayEarly(t, f, sub, read), sub, tru)
				}
			}
		}
	}
	// The Mentalist S02E10 AMZN (above the byte budget, timed from the audio only): truth from
	// its converged fit on 12 minutes (+1.24 s, +3.26 s after the title sequence at ~218 s)
	if f, s := findFile(files, "679e10d8"), findSub(subs, "subf2m/3fcc7d23"); f != nil && s != nil {
		tr := make([]float64, len(s.spans))
		for i, c := range s.spans {
			switch {
			case c.start+1.24 < 210:
				tr[i] = c.start + 1.24
			case c.start+3.26 > 226:
				tr[i] = c.start + 3.26
			default:
				tr[i] = math.NaN()
			}
		}
		add("mentalist", "Mentalist S02E10 AMZN subf2m read=none", replayEarly(t, f, s.spans, "none"), s.spans, tr)
	}

	// Other episodes in the first minutes (known from the episode sweep)
	other := map[string][]string{
		"494024e0": nil, // filled below: subtitles of about its length that are not its own
		"679e10d8": {"turkcealtyazi/0fb09db5", "turkcealtyazi/be95042a", "embedded/764c0c56", "turkcealtyazi/7e8acdee",
			"subf2m/f410b17f", "turkcealtyazi/203844b3", "subf2m/1f247d22", "turkcealtyazi/4bee7ec7", "turkcealtyazi/5fcbb111",
			"subf2m/f7defb5d", "turkcealtyazi/7a324f0e"},
		"c162f124": {"turkcealtyazi/0fb09db5", "turkcealtyazi/a6a6a3f2", "turkcealtyazi/b21eacc1", "turkcealtyazi/bfd83592",
			"subf2m/3fcc7d23", "embedded/764c0c56", "turkcealtyazi/7e8acdee", "subf2m/f410b17f", "turkcealtyazi/203844b3"},
		"2e80f2f6": {"turkcealtyazi/0fb09db5", "turkcealtyazi/be95042a", "subf2m/3fcc7d23", "turkcealtyazi/a6a6a3f2",
			"embedded/764c0c56", "turkcealtyazi/7a324f0e", "turkcealtyazi/4bee7ec7"},
	}
	if f := findFile(files, "494024e0"); f != nil {
		for _, s := range subs {
			last := s.spans[len(s.spans)-1].end
			if last > 0.75*f.ref.Probe.Duration && last < 1.1*f.ref.Probe.Duration && !strings.Contains(s.name, "091b57bb") &&
				!strings.Contains(s.name, "053cb0e3") {
				other["494024e0"] = append(other["494024e0"], s.name)
			}
		}
	}
	pairs, accepted, refits, acceptedRefits := 0, 0, 0, 0
	for fp, names := range other {
		f := findFile(files, fp)
		if f == nil {
			continue
		}
		for _, name := range names {
			s := findSub(subs, name)
			if s == nil {
				continue
			}
			reads := []string{"early", "none"}
			if earlyOwnTrack[fp] != nil {
				reads = append(reads, "own2")
			}
			for _, read := range reads {
				pairs++
				acc := 0
				for _, e := range replayEarly(t, f, s.spans, read) {
					refits++
					if e.state == syncStateSynced || e.state == syncStateUnchanged {
						acc++
					}
				}
				acceptedRefits += acc
				if acc > 0 {
					accepted++
					t.Logf("other episode: %s accepts %s (read=%s) in %d re-fits", fp, name, read, acc)
				}
			}
		}
	}

	t.Logf("SUMMARY early minutes (picture %0.f s behind the audio, re-fit every %.0f s)", earlyAhead, earlyStep)
	for _, name := range []string{"avatar read=all", "avatar read=early", "avatar read=own2", "avatar read=none", "mentalist"} {
		g := groups[name]
		if g == nil {
			continue
		}
		sum := 0
		for _, m := range g.firstOK {
			sum += m
		}
		at := 0.0
		if g.fitted > 0 {
			at = g.fitSumAt / float64(g.fitted)
		}
		t.Logf("%-18s runs %2d | mean first-ok minute %.2f (never in 5 min: %d) | first fit after %.0f s of audio (%d fitted) | minutes 0-5 %s",
			name, g.runs, float64(sum)/float64(g.runs), g.never, at, g.fitted, statsOf(g.errs))
	}
	t.Logf("other episodes in the first minutes: %d/%d pairs accepted, %d/%d re-fits", accepted, pairs, acceptedRefits, refits)
}

// TestSnapshotEarlyBars prints, for the first 60-150 s of played audio, how one offset at the
// file's rate fits this episode's subtitles and other episodes' (gain over chance, peak over
// the runner-up, lines), to set the bars of an early fit.
func TestSnapshotEarlyBars(t *testing.T) {
	files, subs := loadSnapshot(t)
	type pair struct {
		file, sub string
		right     bool
		shift     float64
	}
	var ps []pair
	for _, s := range []string{"subdl/091b57bb", "turkcealtyazi/053cb0e3"} {
		for _, d := range []float64{0, 2.5, -7.37, 30, -60} {
			ps = append(ps, pair{"494024e0", s, true, d})
		}
	}
	ps = append(ps, pair{"679e10d8", "subf2m/3fcc7d23", true, 0}, pair{"c162f124", "turkcealtyazi/be95042a", true, 0})
	for _, s := range []string{"turkcealtyazi/0fb09db5", "turkcealtyazi/be95042a", "embedded/764c0c56", "turkcealtyazi/7e8acdee",
		"subf2m/f410b17f", "turkcealtyazi/203844b3", "subf2m/1f247d22", "turkcealtyazi/4bee7ec7", "turkcealtyazi/5fcbb111",
		"subf2m/f7defb5d", "turkcealtyazi/7a324f0e"} {
		ps = append(ps, pair{"679e10d8", s, false, 0})
	}
	for _, s := range []string{"turkcealtyazi/0fb09db5", "turkcealtyazi/a6a6a3f2", "turkcealtyazi/b21eacc1", "turkcealtyazi/bfd83592",
		"subf2m/3fcc7d23", "embedded/764c0c56", "turkcealtyazi/7e8acdee", "subf2m/f410b17f", "turkcealtyazi/203844b3"} {
		ps = append(ps, pair{"c162f124", s, false, 0})
	}
	for _, s := range []string{"turkcealtyazi/0fb09db5", "turkcealtyazi/be95042a", "subf2m/3fcc7d23", "turkcealtyazi/a6a6a3f2",
		"embedded/764c0c56", "turkcealtyazi/7a324f0e", "turkcealtyazi/4bee7ec7"} {
		ps = append(ps, pair{"2e80f2f6", s, false, 0})
	}
	if f := findFile(files, "494024e0"); f != nil {
		for _, s := range subs {
			last := s.spans[len(s.spans)-1].end
			if last > 0.75*f.ref.Probe.Duration && last < 1.1*f.ref.Probe.Duration && !strings.Contains(s.name, "091b57bb") &&
				!strings.Contains(s.name, "053cb0e3") {
				ps = append(ps, pair{"494024e0", s.name, false, 0})
			}
		}
	}
	for _, h := range []float64{60, 90, 120, 150, 180} {
		for _, p := range ps {
			f, s := findFile(files, p.file), findSub(subs, p.sub)
			if f == nil || s == nil {
				continue
			}
			var ws []refWindow
			for _, w := range f.live {
				if w.Start >= h {
					continue
				}
				n := min(len(w.Speech), int((h-w.Start)*syncRate))
				ws = append(ws, refWindow{Start: w.Start, Kind: "live", Speech: w.Speech[:n]})
			}
			pw := prepare(ws)
			cues := shiftSpans(s.spans, p.shift)
			fit, ok := alignWith(clampSpans(cues, f.ref.Probe.Duration), pw, syncScales[:1], math.Inf(1), 0)
			if !ok {
				continue
			}
			t.Logf("BAR h=%3.0f right=%-5v lines %3.0f gain %.3f peak %.3f offset %+7.2f %s %s", h, p.right, refLines(pw),
				fit.Score-fit.Base, fit.Peak, fit.Segments[0].Offset+p.shift, p.file, p.sub)
			// a prior (sibling episodes): the true offset known within 1 s for this episode's
			// subtitle, an arbitrary small one for another's
			prior := 0.7
			if p.right {
				prior = priorTruth[p.file] - p.shift + 0.8
			}
			total := make([]float64, 2*int(syncMaxOffset*syncRate)+1)
			lines := 0.0
			starts := startSignal(clampSpans(cues, f.ref.Probe.Duration), 1)
			for _, w := range pw {
				lines += w.lines
				for q, v := range windowCurve(w, starts, int(syncMaxOffset*syncRate)) {
					total[q] += v
				}
			}
			d := int(syncMaxOffset * syncRate)
			qp := d + int(math.Round(prior*syncRate))
			local, far := math.Inf(-1), math.Inf(-1)
			for q, v := range total {
				switch {
				case abs(q-qp) <= 150:
					local = math.Max(local, v)
				case abs(q-qp) > 300:
					far = math.Max(far, v)
				}
			}
			t.Logf("PRIOR h=%3.0f right=%-5v lines %3.0f margin %.3f %s %s", h, p.right, lines, (local-far)/lines, p.file, p.sub)
		}
	}
}

// priorTruth is the true teaser offset of the right pairs of TestSnapshotEarlyBars (live audio).
var priorTruth = map[string]float64{"494024e0": 0.28, "679e10d8": 1.24, "c162f124": -0.2}

// earlyOwnTrack is the file's own subtitle track where the snapshot has it (filled by the test).
var earlyOwnTrack = map[string][]span{}

func ownWindows(track []span, starts []float64) []refWindow {
	if len(track) == 0 {
		return nil
	}
	var out []refWindow
	for _, s := range starts {
		w := refWindow{Start: s, Kind: "subs", Speech: make([]byte, int(syncWindowLen*syncRate))}
		for _, c := range track {
			markCue(w.Speech, c.start-s, c.end-s)
		}
		out = append(out, w)
	}
	return out
}

// teaserDistortion: a teaser 1.2 s early, then 1.5 s late after a title sequence cut differently
// (the shape of The Mentalist's HDTV subtitles on the web releases).
var teaserDistortion = distortion{"teaser", mapCues(func(u float64) float64 {
	if u < 150 {
		return u - 1.2
	}
	return u + 1.5
})}
