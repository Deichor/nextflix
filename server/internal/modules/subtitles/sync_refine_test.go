package subtitles

import (
	"math"
	"math/rand"
	"testing"
)

var refineTestWindows = []float64{100, 300}

func spansToCues(ss []span) []liveCue {
	out := make([]liveCue, len(ss))
	for i, s := range ss {
		out[i] = liveCue{start: s.start, end: s.end}
	}
	return out
}

func inRefineWindow(t float64) bool {
	for _, s := range refineTestWindows {
		if t >= s+1 && t < s+syncWindowLen-1 {
			return true
		}
	}
	return false
}

// Loose lines inside own-track windows land on (or within refineMinMove of) the own lines;
// everything else stays.
func TestRefineSnapsLooseLines(t *testing.T) {
	truth := dialogue(1, 600)
	own := ownWindows(truth, refineTestWindows)
	rng := rand.New(rand.NewSource(7))
	loose := make([]span, len(truth))
	for i, c := range truth {
		j := 0.0
		if i%2 == 0 {
			j = (rng.Float64()*2 - 1) * 0.45
		}
		loose[i] = span{c.start + j, c.end + j}
	}
	got := refineCues(spansToCues(loose), nil, own)
	in, onLine := 0, 0
	for i, c := range got {
		if !inRefineWindow(truth[i].start) {
			if c.start != loose[i].start {
				t.Fatalf("cue %d at %.2f outside the windows moved to %.2f", i, loose[i].start, c.start)
			}
			continue
		}
		in++
		if math.Abs(c.start-truth[i].start) < refineMinMove {
			onLine++
		}
	}
	if in == 0 || float64(onLine) < 0.9*float64(in) {
		t.Fatalf("%d of %d cues in the windows on their lines", onLine, in)
	}
	for i := 1; i < len(got); i++ {
		if got[i].start < got[i-1].start {
			t.Fatalf("cue %d starts before cue %d", i, i-1)
		}
		if loose[i].start >= loose[i-1].end && got[i].start < got[i-1].end-1e-9 {
			t.Fatalf("cue %d now overlaps cue %d", i, i-1)
		}
	}
}

// A subtitle that is right, and another episode's subtitle, stay as they are.
func TestRefineKeepsRightAndOther(t *testing.T) {
	truth := dialogue(1, 600)
	own := ownWindows(truth, refineTestWindows)
	for name, ss := range map[string][]span{"right": truth, "other": dialogue(2, 600)} {
		cues := spansToCues(ss)
		for i, c := range refineCues(cues, nil, own) {
			if c != cues[i] {
				t.Fatalf("%s: cue %d %.2f-%.2f moved to %.2f-%.2f", name, i, cues[i].start, cues[i].end, c.start, c.end)
			}
		}
	}
}

// A chained cue keeps its chain: when the next one moves, it ends where that one now starts.
func TestRefineChainedCues(t *testing.T) {
	var truth []span
	for t := 101.0; t < 159; t += 2.5 {
		truth = append(truth, span{t, t + 2.3})
	}
	own := ownWindows(truth, []float64{100})
	loose := make([]span, len(truth))
	for i, c := range truth {
		loose[i] = span{c.start, c.start + 2.5} // chained back to back
	}
	k := len(loose) / 2
	loose[k].start += 0.4
	loose[k-1].end += 0.4
	got := refineCues(spansToCues(loose), nil, own)
	if math.Abs(got[k].start-truth[k].start) > 0.02 {
		t.Fatalf("cue %d at %.2f, want %.2f", k, got[k].start, truth[k].start)
	}
	if math.Abs(got[k-1].end-got[k].start) > 1e-9 {
		t.Fatalf("cue %d ends %.2f, the next starts %.2f", k-1, got[k-1].end, got[k].start)
	}
}
