package subtitles

import (
	"math"
	"testing"
)

// The Mentalist S03E02 (real data, testdata/sync/mentalist_s03e02.json: the file's 8 read audio
// windows, the 39 played minutes, the Turkish subf2m subtitle's cue times). The subtitle runs at
// the file's rate; by the file's own SDH track its lines are +1.03 s late in the teaser, then
// +3.01, +5.08, +7.13 and +9.10 s after the title sequence and three ad breaks.
var mentalistS03E02Truth = []struct{ from, to, offset float64 }{
	{34, 106, 1.03}, {129, 811, 3.01}, {824, 1271, 5.08}, {1284, 2023, 7.13}, {2048, 2551, 9.10},
}

// withoutExtra drops the two windows the first timing read later (between neighbours whose
// offsets differed): the reference of the first minutes.
func withoutExtra(ws []refWindow) []refWindow {
	var out []refWindow
	for _, w := range ws {
		if w.Start != 756.5 && w.Start != 879.25 {
			out = append(out, w)
		}
	}
	return out
}

// With the 6 windows read first and 4 played ones, the rate 1 search stayed around a chance peak
// of the summed offsets (-61.7 s, the true region +1.1 s scoring nearly as high), so 24/23.976
// won by 0.16 (0.37 against 0.21) and the scale lock kept it for every later re-fit, the final one
// too: the ad breaks became a sawtooth (lines up to 0.45 s early) and a stretch was 2 s late.
// Every re-fit, played minute by minute, must stay at the file's rate and end within 0.15 s of
// the truth.
func TestMentalistS03E02StaysAtTheFilesRate(t *testing.T) {
	f, sub := loadFixture(t, "mentalist_s03e02.json")
	f.ref.Windows = withoutExtra(f.ref.Windows)
	steps := replayLive(t, f, sub, true, nil)
	fitted := 0
	for _, st := range steps {
		if !st.fits {
			continue
		}
		fitted++
		if math.Abs(st.served.Scale-1) > 1e-9 {
			t.Fatalf("minute %d: %s", st.minute, describeFit(st.served))
		}
	}
	last := steps[len(steps)-1].served
	for _, p := range mentalistS03E02Truth {
		u := (p.from + p.to) / 2
		if e := last.mapTime(u) - (u + p.offset); fitted < len(steps)-4 || math.Abs(e) > 0.15 {
			t.Fatalf("%d of %d steps fitted; at %.0f s %+.2f s off: %s", fitted, len(steps), u, e, describeFit(last))
		}
	}
}

// A re-fit that kept the served fit (keepStable, keepAccepted) did not note that it tried every
// frame rate, so the next re-fits tried them all again: replayed minute by minute, 23 of S03E02's
// 39 re-fits searched every rate (7 when noted, review), each ~7x the CPU of one rate. Counted
// from the stored results as fitOne decides it; the final fit stays the same.
func TestMentalistS03E02RateSearchWhileKept(t *testing.T) {
	f, sub := loadFixture(t, "mentalist_s03e02.json")
	steps := replayLive(t, f, sub, true, nil)
	full := 0
	var prev *syncMeta
	for i, st := range steps {
		n := len(mergeWindows(f.ref.Windows, f.live[:i+1]))
		final := st.meta != nil && !st.meta.Live
		if !(prev != nil && prev.Live && prev.fits() && prev.Fit.Scale > 0 && !final && 2*n < 3*prev.RateWindows) {
			full++
		}
		prev = st.meta
	}
	last := steps[len(steps)-1].served
	t.Logf("%d of %d re-fits tried every rate; last %s", full, len(steps), describeFit(last))
	if full > 10 {
		t.Fatalf("%d of %d re-fits tried every rate", full, len(steps))
	}
	for _, p := range mentalistS03E02Truth {
		if u := (p.from + p.to) / 2; math.Abs(last.mapTime(u)-(u+p.offset)) > 0.15 {
			t.Fatalf("at %.0f s: %s", u, describeFit(last))
		}
	}
}

// At 24/23.976 (as served) the last change moved 1861 -> 1929 -> 2038 s as the minutes after it
// came in: the two minutes before the break (1920-2040 s) hardly prefer either offset (by 0.2-0.7
// matched lines), so the change went to whichever side they tipped, into the longest subtitle
// pause there (1929 s, 13.7 s; cues of 1936-2030 s served 1.5-1.8 s late for a minute), while the
// break is the 22.6 s pause at 2038 s. The windows heard at 02:24:03: the change lies in the break.
func TestMentalistS03E02ChangeAtTheBreak(t *testing.T) {
	f, sub := loadFixture(t, "mentalist_s03e02.json")
	var live []refWindow
	for _, w := range f.live {
		switch {
		case w.Start < 2160:
			live = append(live, w)
		case w.Start == 2160: // heard for 43 s
			w.Speech = w.Speech[:4300]
			live = append(live, w)
		}
	}
	fit, ok := alignLive(clampSpans(sub, f.ref.Probe.Duration), mergeWindows(f.ref.Windows, live), []float64{24 / 23.976})
	if at := fit.Segments[len(fit.Segments)-1].From; !ok || at < 2023 || at > 2048 {
		t.Fatalf("%s", describeFit(fit))
	}
}

// The same subtitle as if it ran at 24/23.976 (and 23.976/24) on the file: its cue times divided by
// the rate. At the file's rate the fit followed the drift with +0.4 s steps between the ad breaks
// (a sawtooth, lines up to 0.35 s off) and won over the right rate by its 0.05 bonus: the right rate
// netted 0.356 against 0.331 (0.340 at 23.976/24). The rate's fit has the steps of the breaks only.
func TestMentalistS03E02AtAnotherRate(t *testing.T) {
	f, sub := loadFixture(t, "mentalist_s03e02.json")
	for _, k := range []float64{24 / 23.976, 23.976 / 24} {
		scaled := make([]span, len(sub))
		for i, c := range sub {
			scaled[i] = span{c.start / k, c.end / k}
		}
		steps := replayLive(t, f, scaled, true, nil)
		last := steps[len(steps)-1].served
		if math.Abs(last.Scale-k) > 1e-9 {
			t.Fatalf("rate %.4f: %s", k, describeFit(last))
		}
		for _, p := range mentalistS03E02Truth {
			for u := p.from; u <= p.to; u += 10 {
				if e := last.mapTime(u/k) - (u + p.offset); math.Abs(e) > 0.15 {
					t.Fatalf("rate %.4f: at %.0f s %+.2f s off: %s", k, u, e, describeFit(last))
				}
			}
		}
	}
}

func TestDriftSteps(t *testing.T) {
	seg := func(offs ...float64) syncFit {
		f := syncFit{Scale: 1}
		for i, o := range offs {
			f.Segments = append(f.Segments, syncSegment{From: float64(i) * 300, Offset: o})
		}
		return f
	}
	k := 24 / 23.976
	// following a drift towards k: small steps up between the breaks
	if f := seg(1.17, 3.21, 3.61, 4.01, 6.25, 8.57, 8.97); !driftsTo(f, k) || driftsTo(f, 1/k) {
		t.Fatal("small steps up are a drift towards a faster rate only")
	}
	// the breaks alone, or a step back among the small ones
	if driftsTo(seg(1.10, 3.11, 5.19, 7.18, 9.12), k) || driftsTo(seg(3.0, 3.4, 3.8, 3.4), k) {
		t.Fatal("no drift")
	}
	g := seg(1.04, 2.83, 2.43, 4.11)
	g.Scale = k
	if !driftsBack(g) {
		t.Fatal("a fit at 24/23.976 stepping down follows the file's rate")
	}
}
