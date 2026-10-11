package subtitles

import (
	"math"
	"sort"
	"strings"
)

// Holes: stretches where a subtitle served for a file has no lines while a reference of the same
// file (another subtitle on the file's timeline) has dialogue, e.g. a machine translation that
// skipped whole scenes. Found on the file's timeline after timing; normal pauses (music, action,
// credits) have no reference lines either, so they are not holes. holes_fill.go serves them.

const (
	holeMinGap   = 15.0 // seconds without a cue of the subtitle
	holeMinLines = 4    // reference lines in such a pause that make it a hole
	holeEdge     = 1.5  // a reference line counts when its middle is this far inside the pause
	// the file's own track (read windows) contradicts a hole when it shows fewer than this share
	// of the reference's line starts there
	holeOwnShare = 1.0 / 3
	// detected speech contradicts a hole when less than this share of its frames is speech
	holeQuietShare = 0.03
)

// subHole is a hole of a subtitle on the file's timeline.
type subHole struct {
	From  float64 `json:"from"` // the subtitle's pause
	To    float64 `json:"to"`
	Lines int     `json:"lines"` // reference lines in it
	// First and Last bound the reference lines (what is missing)
	First float64 `json:"first"`
	Last  float64 `json:"last"`
}

// holeEvidence is what else is known of the file: windows of its own subtitle track (cue
// starts) and of detected speech (read audio and played audio).
type holeEvidence struct {
	own    []refWindow
	speech []refWindow
}

func evidenceOf(windows []refWindow) holeEvidence {
	var ev holeEvidence
	for _, w := range windows {
		if w.Kind == "subs" {
			ev.own = append(ev.own, w)
		} else {
			ev.speech = append(ev.speech, w)
		}
	}
	return ev
}

// holeDialogue reports whether a reference line (HI parts already removed) is spoken dialogue:
// not a song (many subtitles leave lyrics out) and not only symbols.
func holeDialogue(text string) bool {
	p := strings.TrimSpace(cueTag.ReplaceAllString(text, ""))
	p = strings.TrimSpace(strings.TrimLeft(p, "-"))
	for _, m := range []string{"♪", "♫", "#", "*"} {
		if strings.HasPrefix(p, m) {
			return false
		}
	}
	return hasLetter(p)
}

// subGaps are the pauses of a subtitle: before its first cue, between its cues and after its
// last one (up to end).
func subGaps(sub []liveCue, end float64) [][2]float64 {
	if len(sub) == 0 {
		return nil
	}
	cs := append([]liveCue(nil), sub...)
	sort.Slice(cs, func(i, j int) bool { return cs[i].start < cs[j].start })
	var gaps [][2]float64
	at := 0.0
	for _, c := range cs {
		if c.start > at {
			gaps = append(gaps, [2]float64{at, c.start})
		}
		at = math.Max(at, c.end)
	}
	if end > at {
		gaps = append(gaps, [2]float64{at, end})
	}
	return gaps
}

// findHoles compares a subtitle with a reference of the same file (both on the file's timeline,
// the reference without HI parts). With no reference lines (ref nil) the own-track windows
// alone count, which finds holes that cannot be filled.
func findHoles(sub, ref []liveCue, ev holeEvidence, duration float64) []subHole {
	if len(sub) == 0 {
		return nil
	}
	var lines []liveCue
	for _, c := range ref {
		if holeDialogue(c.text) {
			lines = append(lines, c)
		}
	}
	end := duration
	for _, c := range lines {
		end = math.Max(end, c.end+1)
	}
	for _, w := range ev.own {
		end = math.Max(end, w.end())
	}
	ownTrusted := ownTrackComplete(ev.own, lines)
	var out []subHole
	for _, g := range subGaps(sub, end) {
		if g[1]-g[0] < holeMinGap {
			continue
		}
		a, b := g[0]+holeEdge, g[1]-holeEdge
		h := subHole{From: g[0], To: g[1], First: math.Inf(1), Last: math.Inf(-1)}
		var mids []float64
		for _, c := range lines {
			if m := (c.start + c.end) / 2; m >= a && m <= b {
				h.Lines++
				h.First, h.Last = math.Min(h.First, c.start), math.Max(h.Last, c.end)
				mids = append(mids, m)
			}
		}
		if ref == nil {
			// Only the file's own track: its line starts in the pause
			for _, w := range ev.own {
				for i, v := range w.Speech {
					if t := w.Start + float64(i)/syncRate; v == 2 && t >= a && t <= b {
						h.Lines++
						h.First, h.Last = math.Min(h.First, t), math.Max(h.Last, t+1)
					}
				}
			}
		}
		if h.Lines < holeMinLines {
			continue
		}
		if ref != nil && (ownTrusted && ownContradicts(ev.own, a, b, mids) || quietContradicts(ev.speech, a, b)) {
			continue
		}
		out = append(out, h)
	}
	return out
}

// ownTrackComplete reports whether the own-track windows hold about as many line starts as the
// reference has lines there: a forced-only track (only signs and foreign parts) cannot speak
// for the dialogue.
func ownTrackComplete(own []refWindow, lines []liveCue) bool {
	if len(own) == 0 {
		return false
	}
	starts, expected := 0, 0
	for _, w := range own {
		_, n := w.lineStarts()
		starts += n
		for _, c := range lines {
			if m := (c.start + c.end) / 2; m >= w.Start && m < w.end() {
				expected++
			}
		}
	}
	return expected > 0 && float64(starts) >= 0.5*float64(expected)
}

// ownContradicts: the file's own track covers most of the pause and has (almost) no lines where
// the reference has them, so the reference's lines are not this file's.
func ownContradicts(own []refWindow, a, b float64, mids []float64) bool {
	covered, starts, expected := 0.0, 0, 0
	for _, w := range own {
		lo, hi := math.Max(a, w.Start), math.Min(b, w.end())
		if hi <= lo {
			continue
		}
		covered += hi - lo
		for i := int((lo - w.Start) * syncRate); i < int((hi-w.Start)*syncRate) && i < len(w.Speech); i++ {
			if w.Speech[i] == 2 {
				starts++
			}
		}
		for _, m := range mids {
			if m >= lo && m < hi {
				expected++
			}
		}
	}
	return covered >= 0.5*(b-a) && expected >= 3 && float64(starts) < holeOwnShare*float64(expected)
}

// quietContradicts: the played or read audio covers most of the pause and is (almost) silent.
func quietContradicts(speech []refWindow, a, b float64) bool {
	covered, frames, on := 0.0, 0, 0
	for _, w := range speech {
		lo, hi := math.Max(a, w.Start), math.Min(b, w.end())
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
	return covered >= 0.5*(b-a) && frames > 0 && float64(on) < holeQuietShare*float64(frames)
}

// holeLines are the reference lines that fill the holes, cut to the subtitle's pause so they
// never overlap its own cues (lines left shorter than holeMinCue are dropped).
func holeLines(ref []liveCue, holes []subHole) []liveCue {
	var out []liveCue
	for _, h := range holes {
		for _, c := range ref {
			if !holeDialogue(c.text) {
				continue
			}
			if m := (c.start + c.end) / 2; m < h.From || m > h.To {
				continue
			}
			s, e := math.Max(c.start, h.From+holeMargin), math.Min(c.end, h.To-holeMargin)
			if e-s < holeMinCue {
				continue
			}
			out = append(out, liveCue{start: s, end: e, text: c.text})
		}
	}
	return out
}

const (
	holeMargin = 0.05 // seconds kept free next to the subtitle's own cues
	holeMinCue = 0.5
)

// holeTotal is the reference lines missing in all holes.
func holeTotal(hs []subHole) int {
	n := 0
	for _, h := range hs {
		n += h.Lines
	}
	return n
}
