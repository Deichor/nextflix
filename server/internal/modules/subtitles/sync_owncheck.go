package subtitles

import (
	"math"
	"sort"
)

// Own-track check: a subtitle track inside the file is not timed to it just because it is
// there. The Mentalist S03E01 (d3g WEBRip): its PGS track was made for another cut (+1.8 s in
// the teaser, +4 to +10 s after the act breaks); S02E16 "Code Red": its ASS track the same.
// And a track on the file's timeline may be loose: S02E22 (NF WEB-DL) carries roll-up
// captions whose lines start ±0.85 s (p10 / p90) from their speech, so moving lines onto them
// makes those lines worse. So own-track windows only time subtitles once the track's lines
// were timed to the speech of the same stretches (the speech read with each window, the played
// audio heard with the cue tap) by the aligner, at the file's rate: offsets within the speech
// lag (the lines lead the speech by 0 to 0.4 s) make it timed to the file, an offset elsewhere
// another cut's; no fit although the speech had the lines for one leaves it not known, but its
// windows then do not time subtitles alone (checkOwnTrack). A timed track is tight when, at one
// offset, most of its line starts have a speech onset (speech after a pause) right there; only a
// tight track refines single lines.

const (
	ownLagLo       = -0.5 // offsets (speech after the lines) a track timed to the file may have
	ownLagHi       = 0.6
	ownCheckMin    = 2    // stretches the check needs
	ownNearTol     = 0.2  // seconds: a line start "on" a speech onset (tightness)
	ownTightShare  = 0.55 // share of line starts (with an onset within 1 s) on one: tight
	ownLooseShare  = 0.45 // a tight track stays tight down to this share (settleOwn)
	ownChanceMin   = 8    // stretches after which a track that does not fit
	ownChanceShare = 0.3  // and has no more line starts on an onset than this at any lag is another cut's
)

const (
	ownTimed = "timed" // timed to the file: its windows time subtitles
	ownOther = "other" // not timed to the file (another cut): speech only
)

// ownPair is a stretch with both the file's own subtitle track and its speech (frames of the
// same span).
type ownPair struct {
	start  float64
	own    []byte // 0, 1 a cue shows, 2 a cue starts
	speech []byte // 0, 1 speech
}

// ownCheck is what the comparison found ("" State: not known yet).
type ownCheck struct {
	State   string
	Tight   bool
	Fit     syncFit // the own track timed to the speech
	Near    float64 // share of line starts with a speech onset within ownNearTol (of those within 1 s)
	Pairs   int     // stretches compared
	Matched int     // line starts with an onset within 1 s
	// NoFit: not known, although the speech had the lines for a fit: the track's lines found none
	// there, so they do not time subtitles alone (checkOwnTrack)
	NoFit bool
}

// ownSaved is a decided check as kept with the file's live windows (a restart goes on from it).
type ownSaved struct {
	State string `json:"state"`
	Tight bool   `json:"tight,omitempty"`
	Pairs int    `json:"pairs"`
}

// checkOwn times the own track's lines to the speech of the pairs.
func checkOwn(pairs []ownPair) ownCheck {
	pairs = distinctPairs(pairs)
	c := ownCheck{Pairs: len(pairs)}
	if len(pairs) < ownCheckMin {
		return c
	}
	var cues []span
	ws := make([]refWindow, 0, len(pairs))
	for _, p := range pairs {
		cues = append(cues, pairCues(p)...)
		ws = append(ws, refWindow{Start: p.start, Kind: "live", Speech: p.speech})
	}
	sort.Slice(cues, func(i, j int) bool { return cues[i].start < cues[j].start })
	fit, ok := alignLive(cues, ws, syncScales[:1])
	c.Fit = fit
	if ok && fit.good() {
		// every offset within the lag (loose lines may let it step inside that), none elsewhere
		c.State = ownTimed
		for _, s := range fit.Segments {
			if s.Offset < ownLagLo || s.Offset > ownLagHi {
				c.State = ownOther
			}
		}
		c.Near, c.Matched = nearShare(pairs, fit.Segments[0].Offset)
		c.Tight = c.State == ownTimed && len(fit.Segments) == 1 && c.Near >= ownTightShare
		return c
	}
	// No fit: over enough stretches, line starts no nearer to speech onsets than by chance (a
	// fifth: within 0.2 s of 1 s) at every lag mean another cut, loose within its stretches
	// (S03E01's PGS track: 26-28 %; loose captions on the file's timeline: 36-44 %). Before that
	// a track that found no fit with enough speech lines is not known, but not trusted either
	// (The Mentalist S03E03 KONTRAST: 6 stretches, its lines 0 to 4 s off by stretch, 35 % at best).
	c.NoFit = ok
	if ok || len(pairs) >= ownChanceMin {
		for lag := ownLagLo; lag <= ownLagHi+1e-9; lag += 0.1 {
			if near, n := nearShare(pairs, lag); near > c.Near {
				c.Near, c.Matched = near, n
			}
		}
	}
	if len(pairs) >= ownChanceMin && c.Near < ownChanceShare {
		c.State, c.NoFit = ownOther, false
	}
	return c
}

// distinctPairs keeps one pair per stretch (the first of those overlapping by half).
func distinctPairs(pairs []ownPair) []ownPair {
	var out []ownPair
	var kept []refWindow
	for _, p := range pairs {
		w := refWindow{Start: p.start, Speech: p.own}
		if len(p.own) == 0 || len(p.speech) != len(p.own) || overlapsHalf(w, kept) {
			continue
		}
		out = append(out, p)
		kept = append(kept, w)
	}
	return out
}

// pairCues are the own track's cues that start in the pair (seconds of the file).
func pairCues(p ownPair) []span {
	var out []span
	for i, v := range p.own {
		if v != 2 {
			continue
		}
		j := i + 1
		for j < len(p.own) && p.own[j] == 1 {
			j++
		}
		out = append(out, span{p.start + float64(i)/syncRate, p.start + float64(j)/syncRate})
	}
	return out
}

// onsetFrames are the frames where speech starts after a pause.
func onsetFrames(speech []byte) []int {
	var out []int
	for i := 1; i < len(speech); i++ {
		if speech[i] != 0 && speech[i-1] == 0 {
			out = append(out, i)
		}
	}
	return out
}

// nearShare is the share of own line starts (moved by lag) whose nearest speech onset within
// 1 s lies within ownNearTol, and how many had one within 1 s.
func nearShare(pairs []ownPair, lag float64) (float64, int) {
	near, n := 0, 0
	for _, p := range pairs {
		onsets := onsetFrames(p.speech)
		for i, v := range p.own {
			if v != 2 {
				continue
			}
			want := float64(i) + lag*syncRate
			k := sort.SearchInts(onsets, int(math.Ceil(want)))
			d := math.Inf(1)
			for _, j := range []int{k - 1, k} {
				if j >= 0 && j < len(onsets) {
					d = math.Min(d, math.Abs(float64(onsets[j])-want))
				}
			}
			if d > syncRate {
				continue
			}
			n++
			if d <= ownNearTol*syncRate {
				near++
			}
		}
	}
	if n == 0 {
		return 0, 0
	}
	return float64(near) / float64(n), n
}
