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
