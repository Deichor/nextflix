package subtitles

import (
	"math"
	"math/cmplx"
	"sort"
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
// piecewise constant offsets, each change costing syncSplitPenalty matched lines, and the
// change is placed at the subtitle pause that fits best.

const (
	syncRate         = 100  // signal frames per second
	syncMaxOffset    = 90.0 // seconds searched either way
	syncKernel       = 60   // frames: a line start counts within this of a reference one (1 at 0, 0 at the edge)
	syncNearOffset   = 15   // seconds around the global offset that window offsets may take
	syncSplitPenalty = 4.0  // matched lines an offset change between windows has to gain
	syncMergeGap     = 0.35 // window offsets closer than this count as one (extra windows)
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
	Score    float64       `json:"score"` // the result: matched lines per reference line start
	Base     float64       `json:"base"`  // the same averaged over all offsets (chance)
	Peak     float64       `json:"peak"`  // how far the result beats the best offset away from it (same unit)
	// per reference window (sorted by start): the offset it ended up with
	windowOffsets []float64
	windows       []refWindow
}

// Thresholds of a fitting subtitle, from The Mentalist S02E07 against its Netflix track: three
// subtitles of other releases reached gain 0.18-0.24 and peak 0.10-0.16 once timed, the
// subtitle of S02E06 gain 0.13 and peak 0.05.
const (
	syncMinGain = 0.15
	syncMinPeak = 0.08
)

func (f syncFit) good() bool { return f.Score-f.Base >= syncMinGain && f.Peak >= syncMinPeak }

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
	var ws []prepared
	for _, w := range windows {
		if k, n := w.lineStarts(); n >= 4 {
			ws = append(ws, prepared{w, k, float64(n)})
		}
	}
	if len(ws) == 0 || len(cues) < 5 {
		return syncFit{}, false
	}
	sort.Slice(ws, func(i, j int) bool { return ws[i].Start < ws[j].Start })
	sorted := append([]span(nil), cues...)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i].start < sorted[j].start })
	var best syncFit
	found := false
	for _, k := range syncScales {
		f := alignScale(sorted, ws, k)
		// Another rate has to fit clearly better than the same one
		bonus := 0.0
		if k != 1 {
			bonus = 0.05
		}
		if !found || f.Score-bonus > best.Score {
			best, found = f, true
		}
	}
	return best, found
}

func alignScale(cues []span, ws []prepared, k float64) syncFit {
	n := int((cues[len(cues)-1].start*k+1)*syncRate) + 2
	starts := make([]float64, n)
	for _, c := range cues {
		if i := int(math.Round(c.start * k * syncRate)); i >= 0 && i < n {
			starts[i] = 1
		}
	}
	d := int(syncMaxOffset * syncRate)
	curves := make([][]float64, len(ws))
	total := make([]float64, 2*d+1)
	sumLines := 0.0
	for m, w := range ws {
		curves[m] = windowCurve(w, starts, d)
		sumLines += w.lines
		for q, v := range curves[m] {
			total[q] += v
		}
	}
	qg := argmax(total, 0, len(total)-1)
	offset := func(q int) float64 { return float64(q-d) / syncRate }

	fit := syncFit{Scale: k}
	for _, w := range ws {
		fit.windows = append(fit.windows, w.refWindow)
	}
	fit.Base = mean(total) / sumLines

	// Offsets per window (alass-like): piecewise constant within syncNearOffset of the global one,
	// each change costing syncSplitPenalty matched lines (Viterbi over the windows in time order)
	lo, hi := max(qg-syncNearOffset*syncRate, 0), min(qg+syncNearOffset*syncRate, 2*d)
	states := hi - lo + 1
	v := make([]float64, states)
	back := make([][]int32, len(ws))
	for m, c := range curves {
		back[m] = make([]int32, states)
		bestPrev, bestS := math.Inf(-1), 0
		for s := range v {
			if v[s] > bestPrev {
				bestPrev, bestS = v[s], s
			}
		}
		for s := range v {
			stay := v[s]
			back[m][s] = int32(s)
			if m > 0 && bestPrev-syncSplitPenalty > stay {
				stay = bestPrev - syncSplitPenalty
				back[m][s] = int32(bestS)
			}
			if m == 0 {
				stay = 0
			}
			v[s] = stay + c[lo+s]
		}
	}
	path := make([]int, len(ws))
	path[len(ws)-1] = argmax(v, 0, states-1)
	for m := len(ws) - 1; m > 0; m-- {
		path[m-1] = int(back[m][path[m]])
	}

	fit.Segments = []syncSegment{{Offset: offset(lo + path[0])}}
	fit.windowOffsets = make([]float64, len(ws))
	for m := range ws {
		fit.windowOffsets[m] = offset(lo + path[m])
		if m == 0 || path[m] == path[m-1] {
			continue
		}
		prev, cur := fit.windowOffsets[m-1], fit.windowOffsets[m]
		from := splitPoint(cues, k, ws[m-1], ws[m], prev, cur)
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
	return fit
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

// splitPoint picks where the offset changes from d1 to d2 between windows a and b: between two
// lines of the subtitle (cues sorted), where the split fits both windows best, at the longest
// pause among equals (an ad break or a title sequence is quiet).
func splitPoint(cues []span, k float64, a, b prepared, d1, d2 float64) float64 {
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
	pair := []prepared{a, b}
	scores := make([]float64, len(cands))
	top := math.Inf(-1)
	for i, c := range cands {
		scores[i] = piecewiseScore(pair, cues, k, []syncSegment{{Offset: d1}, {From: c.at, Offset: d2}})
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
