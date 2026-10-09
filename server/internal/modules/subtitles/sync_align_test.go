package subtitles

import (
	"fmt"
	"math"
	"math/rand"
	"testing"
)

func TestXcorrMatchesDirectSum(t *testing.T) {
	rng := rand.New(rand.NewSource(1))
	r := make([]float64, 37)
	x := make([]float64, 120)
	for i := range r {
		r[i] = rng.Float64()
	}
	for i := range x {
		x[i] = rng.Float64()*2 - 1
	}
	got := xcorr(r, x)
	for p := range got {
		want := 0.0
		for n := range r {
			want += r[n] * x[n+p]
		}
		if math.Abs(got[p]-want) > 1e-9 {
			t.Fatalf("p=%d: %v want %v", p, got[p], want)
		}
	}
}

// dialogue makes random subtitle lines over length seconds like a TV drama: lines of 1-4 s,
// mostly short pauses, some longer ones and now and then a scene without words.
func dialogue(seed int64, length float64) []span {
	rng := rand.New(rand.NewSource(seed))
	var out []span
	for t := 5.0; t < length; {
		d := 1 + rng.Float64()*3
		out = append(out, span{t, t + d})
		var gap float64
		switch p := rng.Float64(); {
		case p < 0.65:
			gap = 0.2 + rng.Float64()*1.3
		case p < 0.95:
			gap = 2 + rng.Float64()*6
		default:
			gap = 10 + rng.Float64()*30
		}
		t += d + gap
	}
	return out
}

// truthWindows cuts the file's true cues into reference windows. noise is a speech detector's
// mistakes: that share of line starts is lost and as many false ones appear, and every start
// is off by up to noise seconds; drop leaves out every n-th line (another language).
func truthWindows(cues []span, starts []float64, noise float64, drop int, seed int64) []refWindow {
	rng := rand.New(rand.NewSource(seed))
	var kept []span
	for i, c := range cues {
		if drop > 0 && i%drop == 0 {
			continue
		}
		kept = append(kept, c)
	}
	var out []refWindow
	for _, s := range starts {
		w := refWindow{Start: s, Kind: "subs", Speech: make([]byte, int(syncWindowLen*syncRate))}
		for _, c := range kept {
			if c.start < s || c.start >= s+syncWindowLen {
				continue
			}
			if rng.Float64() < noise {
				c.start = s + rng.Float64()*syncWindowLen // lost, a false one instead
			}
			jitter := (rng.Float64()*2 - 1) * noise
			markCue(w.Speech, c.start-s+jitter, c.end-s)
		}
		out = append(out, w)
	}
	return out
}

func shiftSpans(cues []span, d float64) []span {
	out := make([]span, len(cues))
	for i, c := range cues {
		out[i] = span{c.start + d, c.end + d}
	}
	return out
}

// subtitleFor makes the subtitle a file's cues were shown from: file = scale*sub + offset(sub).
func subtitleFor(file []span, scale float64, offset func(u float64) float64) []span {
	out := make([]span, len(file))
	for i, c := range file {
		// invert per cue: the cue's scaled start decides its segment
		u := c.start
		for k := 0; k < 4; k++ {
			u = c.start - offset(u)
		}
		d := offset(u)
		out[i] = span{(c.start - d) / scale, (c.end - d) / scale}
	}
	return out
}

// mappedWithin reports the share of subtitle lines that land within tol of the file's lines.
func mappedWithin(f syncFit, sub, file []span, tol float64) float64 {
	ok := 0
	for i := range sub {
		if math.Abs(f.mapTime(sub[i].start)-file[i].start) <= tol {
			ok++
		}
	}
	return float64(ok) / float64(len(sub))
}

const episode = 2560.0

func TestAlignConstantOffset(t *testing.T) {
	file := dialogue(1, episode)
	sub := subtitleFor(file, 1, func(float64) float64 { return 7.37 })
	ws := truthWindows(file, windowStarts(episode, syncWindowLen, syncMaxWindows), 0, 0, 1)
	f, ok := align(sub, ws)
	if !ok || f.Scale != 1 || len(f.Segments) != 1 || math.Abs(f.Segments[0].Offset-7.37) > 0.02 || !f.good() {
		t.Fatalf("fit %s", describeFit(f))
	}
}

func TestAlignFramerate(t *testing.T) {
	file := dialogue(2, episode)
	k := 25 / 23.976 // a PAL subtitle on a film-rate file
	sub := subtitleFor(file, k, func(float64) float64 { return -3.2 })
	ws := truthWindows(file, windowStarts(episode, syncWindowLen, syncMaxWindows), 0.05, 7, 2)
	f, ok := align(sub, ws)
	if !ok || math.Abs(f.Scale-k) > 1e-9 || !f.good() || mappedWithin(f, sub, file, 0.05) < 0.98 {
		t.Fatalf("fit scale %.5f segments %+v score %.2f base %.2f peak %.1f", f.Scale, f.Segments, f.Score, f.Base, f.Peak)
	}
}

// An ad break cut differently: the subtitle is right at first and 4 s late after the break
// (what The Mentalist S02E07's HDTV subtitle did on the Netflix file).
func TestAlignSplit(t *testing.T) {
	split := 1400.0
	var file []span // the ad break: 15 s without words
	for _, c := range dialogue(3, episode) {
		if c.end < split-5 || c.start > split+10 {
			file = append(file, c)
		}
	}
	off := func(u float64) float64 {
		if u < split {
			return 0.2
		}
		return -4
	}
	sub := subtitleFor(file, 1, off)
	ws := truthWindows(file, windowStarts(episode, syncWindowLen, syncMaxWindows), 0.1, 5, 3)
	f, ok := align(sub, ws)
	if !ok || len(f.Segments) != 2 {
		t.Fatalf("fit %s", describeFit(f))
	}
	// More windows where the offset changes place the split by evidence (two rounds like fitOne)
	var extra []float64
	for round := 0; round < 2; round++ {
		more := f.extraWindows(syncWindowLen, syncMaxWindows-len(ws))
		extra = append(extra, more...)
		ws = append(ws, truthWindows(file, more, 0.1, 5, int64(4+round))...)
		f, ok = align(sub, ws)
	}
	if !ok || !f.good() || len(f.Segments) != 2 || math.Abs(f.Segments[0].Offset-0.2) > 0.03 ||
		math.Abs(f.Segments[1].Offset+4) > 0.03 {
		t.Fatalf("fit %s (extra %v)", describeFit(f), extra)
	}
	if share := mappedWithin(f, sub, file, 0.05); share < 0.98 {
		t.Fatalf("only %.2f of the lines land on time; split at %.1f", share, f.Segments[1].From)
	}
}

func TestAlignSpeechNoise(t *testing.T) {
	file := dialogue(5, episode)
	sub := subtitleFor(file, 1, func(float64) float64 { return -12.5 })
	// a weak speech detector: a quarter of the frames wrong
	ws := truthWindows(file, windowStarts(episode, syncWindowLen, syncMaxWindows), 0.25, 0, 5)
	f, ok := align(sub, ws)
	if !ok || !f.good() || f.Scale != 1 || mappedWithin(f, sub, file, 0.1) < 0.95 {
		t.Fatalf("fit %s", describeFit(f))
	}
}

func TestAlignRejectsAnotherEpisode(t *testing.T) {
	file := dialogue(6, episode)
	other := dialogue(7, episode)
	ws := truthWindows(file, windowStarts(episode, syncWindowLen, syncMaxWindows), 0.1, 0, 6)
	f, ok := align(other, ws)
	if ok && f.good() {
		t.Fatalf("another episode fits: score %.2f base %.2f peak %.1f", f.Score, f.Base, f.Peak)
	}
}

func TestApplyFitAndLength(t *testing.T) {
	f := syncFit{Scale: 1, Segments: []syncSegment{{Offset: -2}, {From: 100, Offset: 1}}}
	got := applyFit([]liveCue{{start: 1, end: 1.5, text: "a"}, {start: 50, end: 52, text: "b"}, {start: 120, end: 121, text: "c"}}, f)
	if len(got) != 2 || got[0].start != 48 || got[1].start != 121 || got[1].end != 122 {
		t.Fatalf("got %+v", got)
	}
	spans := []span{{10, 12}, {1500, 1502}}
	if !lengthFits(spans, syncFit{Scale: 1, Segments: []syncSegment{{}}}, 2500) {
		t.Fatal("fitting length rejected")
	}
	if lengthFits(spans, syncFit{Scale: 1, Segments: []syncSegment{{}}}, 1000) {
		t.Fatal("a subtitle longer than the video must not fit")
	}
}

func TestWindowStarts(t *testing.T) {
	s := windowStarts(episode, syncWindowLen, syncMaxWindows)
	if len(s) != 6 || s[0] != 20 || s[len(s)-1] != episode-syncWindowLen-60 {
		t.Fatalf("starts %v", s)
	}
	if s := windowStarts(7200, syncWindowLen, syncMaxWindows); len(s) != 10 {
		t.Fatalf("film starts %v", s)
	}
	if s := windowStarts(200, syncWindowLen, syncMaxWindows); len(s) < 1 || s[len(s)-1]+syncWindowLen > 200 {
		t.Fatalf("short starts %v", s)
	}
}

func describeFit(f syncFit) string {
	return fmt.Sprintf("scale %.4f segments %+v score %.2f base %.2f peak %.1f window offsets %v", f.Scale, f.Segments,
		f.Score, f.Base, f.Peak, f.windowOffsets)
}
