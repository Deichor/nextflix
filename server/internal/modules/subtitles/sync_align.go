package subtitles

import (
	"fmt"
	"math"
	"math/cmplx"
	"sort"
	"strings"
)

// Subtitle timing correction, the math (sync.go runs it). Like ffsubsync, the subtitle and a
// reference of the playing file become signals in 10 ms frames that are cross-correlated (FFT)
// over every offset, and a framerate scale is tried from the common ratios. The reference is
// windows of the file's own text subtitle track or of detected speech. The signals are line
// starts (a cue's start; speech after a pause), not ffsubsync's speech on/off: many subtitles
// chain their cues back to back, which leaves an on/off signal with little to align. Fan
// subtitles start lines up to a second early or late, so a line counts as on time within 0.6 s
// (closer is better) and offsets come from many lines. Like alass (simplified), the offset may
// change between windows (ad breaks or a title sequence cut differently): a Viterbi pass picks
// piecewise constant offsets, each change costing syncSplitPenalty matched lines plus
// syncChangeCost per second it moves, every run of one offset has to beat its neighbours on its
// own windows, and the change is placed at the subtitle pause that fits best.

const (
	syncRate         = 100  // signal frames per second
	syncMaxOffset    = 90.0 // seconds searched either way
	syncKernel       = 60   // frames: a line start counts within this of a reference one (1 at 0, 0 at the edge)
	syncNearOffset   = 15   // seconds around the global offset that window offsets may take
	syncSplitPenalty = 4.0  // matched lines an offset change between windows has to gain
	syncMinChange    = 40   // frames an offset change moves at least (smaller ones are detector noise)
	syncMergeGap     = 0.35 // window offsets closer than this count as one (extra windows)
	syncChangeCost   = 0.5  // matched lines per second an offset change moves, on top of the penalty
	syncLagStep      = 10   // frames: steps of the speech lag tried
	syncMaxLag       = 40   // frames the own track's lines may lead the speech (0-0.35 s measured by file)
	syncZoneLines    = 2.0  // matched lines a window next to an offset change has to prefer its side by to place it
)

// syncScales are subtitle time multipliers: same rate, then 25 <-> 23.976 / 24 conversions.
var syncScales = []float64{1, 25 / 23.976, 23.976 / 25, 24 / 23.976, 23.976 / 24, 25.0 / 24, 24.0 / 25}

// refWindow is a stretch of the playing file's reference signal, one value per frame: 0 nothing,
// 1 speech or a cue showing, 2 a cue starts here.
type refWindow struct {
	Start  float64 `json:"start"` // seconds of the file
	Kind   string  `json:"kind"`  // "subs" (its own subtitle track) or "audio" (speech detection)
	Speech []byte  `json:"speech"`
	Read   int64   `json:"-"` // bytes of the file read for it (logs)
}

func (w refWindow) end() float64 { return w.Start + float64(len(w.Speech))/syncRate }

func (w refWindow) speechFrames() int {
	n := 0
	for _, v := range w.Speech {
		if v != 0 {
			n++
		}
	}
	return n
}

// lineStarts returns the window's line starts as triangles of syncKernel frames either side
// (marked cue starts; for speech, where it follows a pause) and how many there are.
func (w refWindow) lineStarts() ([]float64, int) {
	marked := false
	for _, v := range w.Speech {
		marked = marked || v == 2
	}
	k := make([]float64, len(w.Speech))
	n := 0
	for i := 1; i < len(w.Speech); i++ {
		start := w.Speech[i] != 0 && w.Speech[i-1] == 0
		if marked {
			start = w.Speech[i] == 2
		}
		if !start {
			continue
		}
		n++
		for j := max(i-syncKernel, 0); j <= i+syncKernel && j < len(k); j++ {
			k[j] = math.Max(k[j], 1-math.Abs(float64(j-i))/syncKernel)
		}
	}
	return k, n
}

// span is a cue's time in seconds.
type span struct{ start, end float64 }

// syncMaxUnknown: with the file's length unknown, cues past this are not timed.
const syncMaxUnknown = 6 * 3600.0

// clampSpans drops cues starting past the end of the file (+60 s) and cuts their ends there:
// the signals are sized by the last cue, a broken one at 99:59:59 would take hundreds of MB.
func clampSpans(spans []span, duration float64) []span {
	limit := syncMaxUnknown
	if duration > 0 {
		limit = duration + 60
	}
	out := make([]span, 0, len(spans))
	for _, s := range spans {
		if s.start < 0 || s.start > limit {
			continue
		}
		out = append(out, span{s.start, math.Min(s.end, limit)})
	}
	return out
}

// syncSegment: subtitle times (after Scale) from From on are moved by Offset. The first
// segment covers everything before the second one (its From is not used).
type syncSegment struct {
	From   float64 `json:"from"`
	Offset float64 `json:"offset"`
}

type syncFit struct {
	Scale    float64       `json:"scale"`
	Segments []syncSegment `json:"segments"`
	Score    float64       `json:"score"`         // the result: matched lines per reference line start
	Base     float64       `json:"base"`          // the same averaged over all offsets (chance)
	Peak     float64       `json:"peak"`          // how far the result beats the best offset away from it (same unit)
	Lag      float64       `json:"lag,omitempty"` // seconds own-track windows were taken later (own track and speech mixed)
	// per reference window (sorted by start): the offset it ended up with
	windowOffsets []float64
	windows       []refWindow
	minPeak       float64 // a higher peak bar for a small reference (alignLive)
	note          string  // a fit of another shape that was not taken, and why (logs)
}

// Thresholds of a fitting subtitle, from The Mentalist S02E07 against its Netflix track: three
// subtitles of other releases reached gain 0.18-0.24 and peak 0.10-0.16 once timed, the
// subtitle of S02E06 gain 0.13 and peak 0.05.
const (
	syncMinGain = 0.15
	syncMinPeak = 0.08
)

func (f syncFit) good() bool {
	return f.Score-f.Base >= syncMinGain && f.Peak >= math.Max(syncMinPeak, f.minPeak)
}

// missedBars names the bars the fit misses and by how much, and the fit of another shape not
// taken (logs; "" when there is nothing to say).
func (f syncFit) missedBars() string {
	var out []string
	if g := f.Score - f.Base; g < syncMinGain {
		out = append(out, fmt.Sprintf("gain %.3f under %.2f", g, syncMinGain))
	}
	if bar := math.Max(syncMinPeak, f.minPeak); f.Peak < bar {
		out = append(out, fmt.Sprintf("peak %.3f under %.2f", f.Peak, bar))
	}
	if f.note != "" {
		out = append(out, f.note)
	}
	return strings.Join(out, "; ")
}

// offsetsText is the fit's offsets as logged: "-5.88s -9.64s@865s".
func offsetsText(f syncFit) string {
	offs := make([]string, len(f.Segments))
	for i, s := range f.Segments {
		offs[i] = fmt.Sprintf("%+.2fs", s.Offset)
		if i > 0 {
			offs[i] += fmt.Sprintf("@%.0fs", s.From)
		}
	}
	return strings.Join(offs, " ")
}

// linedUp reports a fit that missed only the peak bar while the subtitle as it is already sits
// at its best offset and well above chance: an ambiguous peak there is loose line timing, not
// another cut. Seen on The Mentalist S02E16 (ViSUM AMZN WEB-DL, 3 live windows): the Amazon
// subtitle (within 0.3 s by the file's own track) was replaced by an HDTV one timed -5.3 s from
// those minutes, 2-5 s off before minute 25.
func (f syncFit) linedUp() bool {
	return f.Score-f.Base >= syncMinGain && math.Abs(f.Scale-1) < 1e-6 && len(f.Segments) == 1 &&
		math.Abs(f.Segments[0].Offset) < syncLinedUpOffset
}

const syncLinedUpOffset = 0.5

// changes reports whether applying the fit moves anything noticeably.
func (f syncFit) changes() bool {
	if math.Abs(f.Scale-1) > 1e-6 || len(f.Segments) > 1 {
		return true
	}
	return len(f.Segments) == 1 && math.Abs(f.Segments[0].Offset) >= 0.1
}

// mapTime moves a subtitle time onto the file.
func (f syncFit) mapTime(t float64) float64 {
	u := t * f.Scale
	return u + offsetAt(f.Segments, u)
}

func offsetAt(segs []syncSegment, u float64) float64 {
	off := 0.0
	for i, s := range segs {
		if i == 0 || u >= s.From {
			off = s.Offset
		}
	}
	return off
}

// prepared is a reference window ready for scoring.
type prepared struct {
	refWindow
	kernel []float64
	lines  float64
}

// align finds the scale and offsets that fit cues best to the reference windows.
func align(cues []span, windows []refWindow) (syncFit, bool) {
	return alignWith(cues, prepare(windows), syncScales, syncSplitPenalty, -1)
}

func prepare(windows []refWindow) []prepared {
	var ws []prepared
	for _, w := range windows {
		if k, n := w.lineStarts(); n >= 4 {
			ws = append(ws, prepared{w, k, float64(n)})
		}
	}
	return ws
}

// alignLive is align for a reference with live windows: long contiguous runs, at first only a
// few minutes of the file. On generated episodes, few or contiguous windows let another
// episode's subtitle clear the bars through the other frame rates and offset changes (5-15 %
// with 3-20 windows). So a fit needs liveMinLines reference line starts (one offset: none of
// 279 such cases fitted another episode, 8 of 141 with fewer lines), below syncMinWindows
// windows one offset is fitted at the first of scales (from liveSplitWindows also one change,
// such as a title sequence cut differently, at peak 0.20: none of 264 such cases fitted another
// episode, 3 at 0.12), other rates need windows spread over 25 minutes or a plain drift over
// contiguous ones, and the peak bar is higher. An offset change costs what it costs with read
// windows: a cost growing with the windows dropped a teaser's own offset once the episode was
// heard (seen on real files), while the change cost by distance and mergeUnsupported keep
// detector noise out.
func alignLive(cues []span, windows []refWindow, scales []float64) (syncFit, bool) {
	ws := prepare(windows)
	n := len(ws)
	if refLines(ws) < liveMinLines {
		return syncFit{}, false
	}
	if n < syncMinWindows {
		f, ok := alignWith(cues, ws, scales[:1], math.Inf(1), 0)
		f.minPeak = 1.5 * syncMinPeak
		if n >= liveSplitWindows {
			// One change (a title sequence) at a higher bar, else the single offset
			if g, ok2 := alignWith(cues, ws, scales[:1], syncSplitPenalty, 1); ok2 && len(g.Segments) > 1 {
				g.minPeak = liveSplitPeak
				if g.good() {
					return g, true
				}
				f.note = fmt.Sprintf("one change %s not taken: %s", offsetsText(g), g.missedBars())
			}
		}
		return f, ok
	}
	first, last, covered := math.Inf(1), math.Inf(-1), 0.0
	for _, w := range ws {
		first, last = math.Min(first, w.Start), math.Max(last, w.end())
		covered += w.end() - w.Start
	}
	if last-first < 1500 && covered > (last-first)/2 {
		// Contiguous: only rates whose drift over the stretch is plain (25 <-> 24 fps: 20 s in 8 min)
		keep := []float64{scales[0]}
		for _, k := range scales[1:] {
			if math.Abs(k-1)*(last-first) >= liveMinDrift {
				keep = append(keep, k)
			}
		}
		scales = keep
	}
	f, ok := alignWith(cues, ws, scales, syncSplitPenalty, -1)
	f.minPeak = 1.25 * syncMinPeak
	return f, ok
}

// liveSplitPeak is the peak bar of a live fit's one offset change below syncMinWindows windows.
var liveSplitPeak = 2.5 * syncMinPeak

func refLines(ws []prepared) float64 {
	n := 0.0
	for _, w := range ws {
		n += w.lines
	}
	return n
}

// alignWith fits over the given scales; each offset change costs penalty matched lines (+Inf:
// one offset for all).
func alignWith(cues []span, ws []prepared, scales []float64, penalty float64, maxChanges int) (syncFit, bool) {
	if len(ws) == 0 || len(cues) < 5 {
		return syncFit{}, false
	}
	sort.Slice(ws, func(i, j int) bool { return ws[i].Start < ws[j].Start })
	sorted := append([]span(nil), cues...)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i].start < sorted[j].start })
	var best syncFit
	found := false
	lines := refLines(ws)
	for _, k := range scales {
		f := alignScale(sorted, ws, k, penalty, maxChanges)
		// Another rate has to fit clearly better than the same one, net of its offset changes: a
		// near rate turns the steps of ad breaks into drift and matches a few more lines with more,
		// smaller changes (The Mentalist S03E02: 24/23.976 instead of the file's rate)
		bonus := 0.0
		if k != 1 {
			bonus = 0.05
		}
		if !found || netScore(f, lines)-bonus > netScore(best, lines) {
			best, found = f, true
		}
	}
	return best, found
}

// netScore is a fit's score less what its offset changes cost (matched lines per reference line).
func netScore(f syncFit, lines float64) float64 {
	n := f.Score
	for i := 1; i < len(f.Segments); i++ {
		n -= (syncSplitPenalty + syncChangeCost*math.Abs(f.Segments[i].Offset-f.Segments[i-1].Offset)) / lines
	}
	return n
}

func alignScale(cues []span, ws []prepared, k float64, penalty float64, maxChanges int) syncFit {
	n := int((cues[len(cues)-1].start*k+1)*syncRate) + 2
	starts := make([]float64, n)
	for _, c := range cues {
		if i := int(math.Round(c.start * k * syncRate)); i >= 0 && i < n {
			starts[i] = 1
		}
	}
	d := int(syncMaxOffset * syncRate)
	curves := make([][]float64, len(ws))
	subs, speech := 0, 0
	for m, w := range ws {
		curves[m] = windowCurve(w, starts, d)
		if w.Kind == "subs" {
			subs++
		} else {
			speech++
		}
	}
	// Detected speech starts a little after the lines of the file's own track (0-0.4 s, by file):
	// with windows of both, that lag is fitted too, else the two kinds look like an offset change.
	// The own track's windows move onto the speech: subtitles are timed to what is said.
	lags := []int{0}
	if subs >= 2 && speech >= 2 {
		for l := syncLagStep; l <= syncMaxLag; l += syncLagStep {
			lags = append(lags, l)
		}
	}
	var best syncFit
	bestRaw := math.Inf(-1)
	for _, lag := range lags {
		f, raw := alignLag(cues, ws, k, curves, d, lag, penalty, maxChanges)
		if raw > bestRaw+0.5 {
			best, bestRaw = f, raw
		}
	}
	return best
}

// alignLag fits with the own track's windows taken lag frames later.
func alignLag(cues []span, orig []prepared, k float64, unshifted [][]float64, d, lag int, penalty float64, maxChanges int) (syncFit, float64) {
	ws := orig
	curves := unshifted
	if lag != 0 {
		ws = make([]prepared, len(orig))
		curves = make([][]float64, len(unshifted))
		for m, w := range orig {
			ws[m], curves[m] = w, unshifted[m]
			if w.Kind != "subs" {
				continue
			}
			ws[m].Start += float64(lag) / syncRate
			c := make([]float64, len(unshifted[m]))
			copy(c[lag:], unshifted[m])
			curves[m] = c
		}
	}
	total := make([]float64, 2*d+1)
	sumLines := 0.0
	for m, w := range ws {
		sumLines += w.lines
		for q, v := range curves[m] {
			total[q] += v
		}
	}
	qg := argmax(total, 0, len(total)-1)
	offset := func(q int) float64 { return float64(q-d) / syncRate }

	fit := syncFit{Scale: k, Lag: float64(lag) / syncRate}
	for _, w := range orig {
		fit.windows = append(fit.windows, w.refWindow)
	}
	fit.Base = mean(total) / sumLines

	// Offsets per window (alass-like): piecewise constant within syncNearOffset of the global one,
	// each change costing penalty matched lines plus syncChangeCost per second it moves (Viterbi
	// over the windows in time order)
	lo, hi := max(qg-syncNearOffset*syncRate, 0), min(qg+syncNearOffset*syncRate, 2*d)
	states := hi - lo + 1
	lam := syncChangeCost / syncRate
	// With maxChanges >= 0, layer l holds paths with l changes so far; otherwise one layer
	// changes into itself
	layers := 1
	if maxChanges >= 0 {
		layers = maxChanges + 1
	}
	v := make([][]float64, layers)
	back := make([][][]int32, len(ws)) // layer*states + state of the window before
	// best previous state below / above each state (a change must move at least syncMinChange)
	preV, sufV := make([]float64, states), make([]float64, states)
	preI, sufI := make([]int32, states), make([]int32, states)
	for m, c := range curves {
		back[m] = make([][]int32, layers)
		next := make([][]float64, layers)
		for l := range layers {
			back[m][l] = make([]int32, states)
			next[l] = make([]float64, states)
			src := l
			if maxChanges >= 0 {
				src = l - 1
			}
			change := m > 0 && src >= 0
			if change {
				// best previous state below / above, less lam per frame of distance
				u := v[src]
				preV[0], preI[0] = u[0], 0
				for s := 1; s < states; s++ {
					preV[s], preI[s] = preV[s-1], preI[s-1]
					if x := u[s] + lam*float64(s); x > preV[s] {
						preV[s], preI[s] = x, int32(s)
					}
				}
				sufV[states-1], sufI[states-1] = u[states-1]-lam*float64(states-1), int32(states-1)
				for s := states - 2; s >= 0; s-- {
					sufV[s], sufI[s] = sufV[s+1], sufI[s+1]
					if x := u[s] - lam*float64(s); x > sufV[s] {
						sufV[s], sufI[s] = x, int32(s)
					}
				}
			}
			base := int32(src * states)
			for s := range states {
				stay := 0.0
				if l > 0 {
					stay = math.Inf(-1)
				}
				back[m][l][s] = int32(l*states + s)
				if m > 0 {
					stay = v[l][s]
				}
				if change {
					if j := s - syncMinChange; j >= 0 {
						if x := preV[j] - lam*float64(s) - penalty; x > stay {
							stay, back[m][l][s] = x, base+preI[j]
						}
					}
					if j := s + syncMinChange; j < states {
						if x := sufV[j] + lam*float64(s) - penalty; x > stay {
							stay, back[m][l][s] = x, base+sufI[j]
						}
					}
				}
				next[l][s] = stay + c[lo+s]
			}
		}
		v = next
	}
	path := make([]int, len(ws))
	cur := argmax(v[0], 0, states-1)
	for l := 1; l < layers; l++ {
		if s := argmax(v[l], 0, states-1); v[l][s] > v[cur/states][cur%states] {
			cur = l*states + s
		}
	}
	for m := len(ws) - 1; m >= 0; m-- {
		path[m] = cur % states
		cur = int(back[m][cur/states][cur%states])
	}
	mergeUnsupported(path, curves, lo, penalty)

	fit.Segments = []syncSegment{{Offset: offset(lo + path[0])}}
	fit.windowOffsets = make([]float64, len(ws))
	for m := range ws {
		fit.windowOffsets[m] = offset(lo + path[m])
		if m == 0 || path[m] == path[m-1] {
			continue
		}
		prev, cur := fit.windowOffsets[m-1], fit.windowOffsets[m]
		// Windows next to the change that hardly prefer either offset do not place it: the split
		// is looked for over them too (The Mentalist S03E02: two windows before an ad break leaned
		// either way by under a line, and the change went 110 s early into another pause)
		s1, s2 := path[m-1], path[m]
		a, b := m-1, m
		for a > 0 && path[a-1] == s1 && curves[a][lo+s1]-curves[a][lo+s2] < syncZoneLines {
			a--
		}
		for b+1 < len(path) && path[b+1] == s2 && curves[b][lo+s2]-curves[b][lo+s1] < syncZoneLines {
			b++
		}
		from := splitPoint(cues, k, ws[a:b+1], prev, cur)
		if from <= fit.Segments[len(fit.Segments)-1].From {
			fit.Segments[len(fit.Segments)-1].Offset = cur
			continue
		}
		fit.Segments = append(fit.Segments, syncSegment{From: from, Offset: cur})
	}
	raw := piecewiseScore(ws, cues, k, fit.Segments)
	fit.Score = raw / sumLines
	// Peak: how far the result beats the best single offset away from the chosen ones
	qa, qb := lo+path[0], lo+path[0]
	for _, p := range path {
		qa, qb = min(qa, lo+p), max(qb, lo+p)
	}
	other := math.Inf(-1)
	for q, x := range total {
		if q < qa-150 || q > qb+150 {
			other = math.Max(other, x)
		}
	}
	fit.Peak = (raw - other) / sumLines
	return fit, raw
}

// mergeUnsupported gives every run of windows with one offset the offset of a neighbouring run
// when it does not beat that by minGain matched lines on its own windows: the Viterbi may keep
// such a run as a bridge between two offsets closer than syncMinChange (seen on Avatar: Seven
// Havens S01E02, where the file's own track and the played audio disagreed by 0.23 s and one
// live window went +13 s to get there), or for a gain its neighbours bring.
func mergeUnsupported(path []int, curves [][]float64, lo int, minGain float64) {
	for {
		type run struct{ a, b, s int } // windows [a, b) at state s
		var runs []run
		for m := range path {
			if m == 0 || path[m] != path[m-1] {
				runs = append(runs, run{m, m + 1, path[m]})
			} else {
				runs[len(runs)-1].b = m + 1
			}
		}
		if len(runs) < 2 {
			return
		}
		sum := func(r run, s int) float64 {
			x := 0.0
			for m := r.a; m < r.b; m++ {
				x += curves[m][lo+s]
			}
			return x
		}
		weak, to, weakest := -1, 0, minGain
		for i, r := range runs {
			own := sum(r, r.s)
			gain, best, bestS := math.Inf(1), math.Inf(-1), 0
			for _, j := range []int{i - 1, i + 1} {
				if j < 0 || j >= len(runs) {
					continue
				}
				x := sum(r, runs[j].s)
				gain = math.Min(gain, own-x)
				if x > best {
					best, bestS = x, runs[j].s
				}
			}
			if gain < weakest {
				weak, to, weakest = i, bestS, gain
			}
		}
		if weak < 0 {
			return
		}
		for m := runs[weak].a; m < runs[weak].b; m++ {
			path[m] = to
		}
	}
}

// windowCurve scores every offset for one window: c[q] is offset (q-d)/syncRate seconds.
func windowCurve(w prepared, starts []float64, d int) []float64 {
	n := len(w.kernel)
	base := int(math.Round(w.Start*syncRate)) - d
	x := make([]float64, n+2*d)
	for i := range x {
		if j := base + i; j >= 0 && j < len(starts) {
			x[i] = starts[j]
		}
	}
	c := xcorr(w.kernel, x) // c[p]: subtitle frame start+i-(d-p), i.e. offset d-p
	out := make([]float64, 2*d+1)
	for q := range out {
		out[q] = c[2*d-q]
	}
	return out
}

// splitPoint picks where the offset changes from d1 to d2 over windows ws (sorted; the first at
// d1, the last at d2, any between hardly preferring either): between two lines of the subtitle
// (cues sorted), where the split fits the windows best, at the longest pause among equals (an ad
// break or a title sequence is quiet).
func splitPoint(cues []span, k float64, ws []prepared, d1, d2 float64) float64 {
	a, b := ws[0], ws[len(ws)-1]
	lo, hi := a.Start-d1, b.end()-d2
	type cand struct{ at, gap float64 }
	var cands []cand
	for i := 0; i+1 < len(cues); i++ {
		s, e := cues[i].start*k, cues[i+1].start*k
		mid := (math.Max(cues[i].end*k, s) + e) / 2
		if e > s && mid > lo && mid < hi {
			cands = append(cands, cand{math.Min(mid, e), e - cues[i].end*k})
		}
	}
	if len(cands) == 0 {
		return (a.end() - d1 + b.Start - d2) / 2
	}
	scores := make([]float64, len(cands))
	top := math.Inf(-1)
	for i, c := range cands {
		scores[i] = piecewiseScore(ws, cues, k, []syncSegment{{Offset: d1}, {From: c.at, Offset: d2}})
		top = math.Max(top, scores[i])
	}
	best := -1
	for i, c := range cands {
		if scores[i] >= top-0.5 && (best < 0 || c.gap > cands[best].gap) {
			best = i
		}
	}
	return cands[best].at
}

// rescore is what an earlier fit scores on the current windows (its scale, offsets and lag), in
// matched lines, and the windows' line starts.
func rescore(cues []span, windows []refWindow, f syncFit) (raw, lines float64) {
	ws := prepare(windows)
	for i := range ws {
		if ws[i].Kind == "subs" {
			ws[i].Start += f.Lag
		}
		lines += ws[i].lines
	}
	return piecewiseScore(ws, cues, f.Scale, f.Segments), lines
}

// piecewiseScore sums what the subtitle's line starts, moved by segs, land on in the windows.
func piecewiseScore(ws []prepared, cues []span, k float64, segs []syncSegment) float64 {
	total := 0.0
	for _, c := range cues {
		u := c.start * k
		t := u + offsetAt(segs, u)
		for _, w := range ws {
			if i := int(math.Round((t - w.Start) * syncRate)); i >= 0 && i < len(w.kernel) {
				total += w.kernel[i]
			}
		}
	}
	return total
}

// extraWindows proposes reference windows between neighbours whose offsets differ and are far
// apart, so the change is placed by evidence instead of by guess (at most max).
func (f syncFit) extraWindows(length float64, max int) []float64 {
	var out []float64
	for m := 0; m+1 < len(f.windows) && len(out) < max; m++ {
		a, b := f.windows[m], f.windows[m+1]
		if math.Abs(f.windowOffsets[m]-f.windowOffsets[m+1]) <= syncMergeGap {
			continue
		}
		if gap := b.Start - a.end(); gap > 2.5*length {
			out = append(out, a.end()+(gap-length)/2)
		}
	}
	return out
}

func abs(x int) int {
	if x < 0 {
		return -x
	}
	return x
}

func argmax(v []float64, lo, hi int) int {
	lo, hi = max(lo, 0), min(hi, len(v)-1)
	best := lo
	for i := lo; i <= hi; i++ {
		if v[i] > v[best] {
			best = i
		}
	}
	return best
}

func mean(v []float64) float64 {
	if len(v) == 0 {
		return 0
	}
	s := 0.0
	for _, x := range v {
		s += x
	}
	return s / float64(len(v))
}

// prominence is how far v[q] stands above every value more than 1.5 s away.
func prominence(v []float64, q int) float64 {
	other := math.Inf(-1)
	for i, x := range v {
		if (i < q-150 || i > q+150) && x > other {
			other = x
		}
	}
	if math.IsInf(other, -1) {
		return 0
	}
	return v[q] - other
}

// xcorr returns c[p] = sum over n of r[n]*x[n+p] for p in 0..len(x)-len(r), via FFT.
func xcorr(r, x []float64) []float64 {
	size := 1
	for size < len(r)+len(x) {
		size <<= 1
	}
	a := make([]complex128, size)
	b := make([]complex128, size)
	for i, v := range r {
		a[i] = complex(v, 0)
	}
	for i, v := range x {
		b[i] = complex(v, 0)
	}
	fft(a, false)
	fft(b, false)
	for i := range a {
		a[i] = cmplx.Conj(a[i]) * b[i]
	}
	fft(a, true)
	out := make([]float64, len(x)-len(r)+1)
	for p := range out {
		out[p] = real(a[p])
	}
	return out
}

// fft is an in-place radix-2 FFT (len(a) a power of two); inverse scales by 1/n.
func fft(a []complex128, inverse bool) {
	n := len(a)
	for i, j := 1, 0; i < n; i++ {
		bit := n >> 1
		for ; j&bit != 0; bit >>= 1 {
			j ^= bit
		}
		j ^= bit
		if i < j {
			a[i], a[j] = a[j], a[i]
		}
	}
	for size := 2; size <= n; size <<= 1 {
		ang := 2 * math.Pi / float64(size)
		if !inverse {
			ang = -ang
		}
		w := complex(math.Cos(ang), math.Sin(ang))
		for start := 0; start < n; start += size {
			wk := complex(1, 0)
			for k := 0; k < size/2; k++ {
				u := a[start+k]
				v := a[start+k+size/2] * wk
				a[start+k] = u + v
				a[start+k+size/2] = u - v
				wk *= w
			}
		}
	}
	if inverse {
		for i := range a {
			a[i] /= complex(float64(n), 0)
		}
	}
}
