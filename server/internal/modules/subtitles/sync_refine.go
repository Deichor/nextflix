package subtitles

import (
	"math"
	"sort"
)

// Line refinement after the piecewise fit (sync_align.go). Fan subtitles start single lines up
// to a second early or late, and whole stretches of a minute or so seconds off (another cut's
// subtitle: The Mentalist S02E17's HDTV pack is +2 s around 2:00, -3 s around 5:30), which the
// fit's segments do not follow. Where the reference has windows of the file's own track (read,
// or from the play streams' cue tap), the cues are put onto its line starts: first each cue gets
// the local offset of its neighbours (the shift that puts most of their starts onto own line
// starts), then it moves onto the own line start it clearly shows. Cues without that evidence,
// and everything outside the own-track windows, stay as the fit serves them: speech onsets and
// offsets carried to the stretches between windows made those lines worse or no better (see
// docs/ARCHITECTURE.md).

var (
	refineSearch   = 1.0  // seconds a local offset may differ from the fit's
	refineWide     = 4.0  // the same when no offset that near fits
	refineHit      = 0.3  // a cue start counts as on an own line start within this
	refineNear     = 0.4  // the local offset is the median of the cues this near
	refineMinShare = 0.6  // share of the neighbours on own starts (after the offset) to use it
	refineGateTol  = 0.4  // seconds a cue start may be off an own start for that share
	refineMinCues  = 6.0  // neighbours a local offset needs
	refineSide     = 12.0 // neighbours taken on each side of a cue (with it)
	refineSpan     = 60.0 // seconds a neighbour may be away
	refineAgree    = 0.2  // seconds the offsets of both sides may differ to count as one
	refineRadius   = 0.7  // seconds an own line start is looked for around a cue
	refineMargin   = 0.4  // the nearest own start must be this much nearer than the next one
	refineMinMove  = 0.15 // smaller moves are not made
	refineChain    = 0.1  // cues closer than this follow each other (chained)
	refineMinLen   = 0.7  // a shortened cue keeps at least this (or its length when shorter)
)

func refineParams() map[string]*float64 {
	return map[string]*float64{"search": &refineSearch, "wide": &refineWide, "hit": &refineHit, "near": &refineNear,
		"share": &refineMinShare, "cues": &refineMinCues, "radius": &refineRadius, "margin": &refineMargin,
		"minmove": &refineMinMove, "chain": &refineChain, "minlen": &refineMinLen, "gatetol": &refineGateTol,
		"side": &refineSide, "span": &refineSpan, "agree": &refineAgree}
}

// nearestTwo returns the event nearest to t (index, -1 without events) and the distances of it
// and of the next nearest.
func nearestTwo(ev []float64, t float64) (int, float64, float64) {
	j := sort.SearchFloat64s(ev, t)
	best, d1, d2 := -1, math.Inf(1), math.Inf(1)
	for k := max(j-2, 0); k < min(j+2, len(ev)); k++ {
		d := math.Abs(ev[k] - t)
		if d < d1 {
			best, d1, d2 = k, d, d1
		} else if d < d2 {
			d2 = d
		}
	}
	return best, d1, d2
}

// localOffset is the shift that puts most of starts onto own line starts (clearly more than any
// shift half a second away), refined to the median of the near ones; ok is false when too few
// starts lie on own starts after it.
func localOffset(starts, own []float64, search float64) (float64, bool) {
	if float64(len(starts)) < refineMinCues || float64(len(own)) < refineMinCues {
		return 0, false
	}
	steps := int(math.Round(search * syncRate))
	h := int(math.Round(refineHit * syncRate))
	count := make([]int, 2*steps+1)
	for _, s := range starts {
		last := -steps - 1 // a cue counts once per shift
		j := sort.SearchFloat64s(own, s-search-refineHit)
		for ; j < len(own) && own[j] <= s+search+refineHit; j++ {
			c := int(math.Round((own[j] - s) * syncRate))
			for q := max(c-h, -steps, last+1); q <= min(c+h, steps); q++ {
				count[q+steps]++
				last = q
			}
		}
	}
	best := 0
	for q := -steps; q <= steps; q++ {
		if n, b := count[q+steps], count[best+steps]; n > b || n == b && abs(q) < abs(best) {
			best = q
		}
	}
	away := int(0.5 * syncRate)
	for q := -steps; q <= steps; q++ {
		if abs(q-best) > away && count[q+steps] > count[best+steps]-2 {
			return 0, false // another shift fits nearly as well
		}
	}
	var ds []float64
	for _, s := range starts {
		if k, d1, _ := nearestTwo(own, s+float64(best)/syncRate); k >= 0 && d1 <= refineNear {
			ds = append(ds, own[k]-s)
		}
	}
	if len(ds) == 0 {
		return 0, false
	}
	d := median(ds)
	on := 0
	for _, s := range starts {
		if _, d1, _ := nearestTwo(own, s+d); d1 <= refineGateTol {
			on++
		}
	}
	return d, float64(on) >= refineMinShare*float64(len(starts))
}

// refineWindows are the own-track windows the refinement uses: read and live ones, also where
// they overlap (the fit takes one window per stretch).
func refineWindows(read, live []refWindow) []refWindow {
	var out []refWindow
	for _, w := range append(append([]refWindow(nil), read...), live...) {
		if w.Kind == "subs" {
			out = append(out, w)
		}
	}
	return out
}

// refineMoved reports whether the refinement moved a cue by refineMinMove (a fit that changes
// nothing is then served refined).
func refineMoved(cues, timed []liveCue) bool {
	if len(cues) != len(timed) {
		return true
	}
	for i := range cues {
		if math.Abs(timed[i].start-cues[i].start) >= refineMinMove-1e-9 {
			return true
		}
	}
	return false
}

// refineFit times the cues with the fit and refines them.
func refineFit(cues []liveCue, f syncFit, windows []refWindow) []liveCue {
	timed := make([]liveCue, 0, len(cues))
	clock := make([]float64, 0, len(cues))
	for _, c := range cues {
		if t := applyFit([]liveCue{c}, f); len(t) == 1 {
			timed = append(timed, t[0])
			clock = append(clock, c.start*f.Scale)
		}
	}
	return refineCues(timed, clock, windows)
}

// refineCues moves the served cues (on the file's timeline) onto the own-track windows of the
// reference. clock is each cue's start on the subtitle's own clock (scaled, before the fit's
// offsets; nil: as served): a cue's neighbours keep their distances to it on that clock, so
// lines the fit put on the wrong side of an offset change move with their stretch.
func refineCues(cues []liveCue, clock []float64, windows []refWindow) []liveCue {
	n := len(cues)
	out := append([]liveCue(nil), cues...)
	var subs []refWindow
	for _, w := range windows {
		if w.Kind == "subs" {
			subs = append(subs, w)
		}
	}
	if n == 0 || len(subs) == 0 {
		return out
	}
	order := make([]int, n)
	for i := range order {
		order[i] = i
	}
	sort.SliceStable(order, func(a, b int) bool { return cues[order[a]].start < cues[order[b]].start })
	starts, clk := make([]float64, n), make([]float64, n) // by order
	for k, i := range order {
		starts[k], clk[k] = cues[i].start, cues[i].start
		if clock != nil {
			clk[k] = clock[i]
		}
	}
	sort.Slice(subs, func(i, j int) bool { return subs[i].Start < subs[j].Start })
	own := ownStartsOf(subs)
	cover := coverOf(subs)
	// the cues whose surroundings the own track shows, and their covered stretch
	var idx []int
	stretch := make([]int, n)
	for k := range n {
		if stretch[k] = cover.at(starts[k]-refineRadius, starts[k]+refineRadius); stretch[k] >= 0 {
			idx = append(idx, k)
		}
	}
	side := int(refineSide)
	offsetOf := func(set []int, k int) (float64, bool) {
		var ss []float64
		for _, m := range set {
			if d := clk[m] - clk[k]; stretch[m] == stretch[k] && math.Abs(d) <= refineSpan {
				ss = append(ss, starts[k]+d)
			}
		}
		if d, ok := localOffset(ss, own, refineSearch); ok {
			return d, true
		}
		return localOffset(ss, own, refineWide)
	}
	offLine := func(t float64) float64 {
		_, d1, _ := nearestTwo(own, t)
		return d1
	}
	shift := make([]float64, n)
	for p, k := range idx {
		dl, okl := offsetOf(idx[max(p-side+1, 0):p+1], k)
		dr, okr := offsetOf(idx[p:min(p+side, len(idx))], k)
		var d float64
		switch {
		case okl && okr && math.Abs(dl-dr) <= refineAgree:
			d = (dl + dr) / 2
		case okl && okr && math.Min(math.Abs(dl), math.Abs(dr)) > refineRadius:
			continue // two large offsets that disagree: neither is clear
		case okl || okr:
			// the offset changes here (or one side only): the side that puts the cue itself
			// nearer to an own line start
			el, er := math.Inf(1), math.Inf(1)
			if okl {
				el = offLine(starts[k] + dl)
			}
			if okr {
				er = offLine(starts[k] + dr)
			}
			if e := math.Min(el, er); e > refineRadius || e >= offLine(starts[k]) {
				continue
			}
			if d = dl; er < el {
				d = dr
			}
		default:
			continue
		}
		shift[k] = d
		// Single lines: the own start nearest to the cue (after the local offset), clearly
		// nearer than the next one, and no other cue nearer to it
		t := starts[k] + d
		j, d1, d2 := nearestTwo(own, t)
		// (the own track has to show that no other own start is as near)
		if j < 0 || d1 > refineRadius || d2-d1 < refineMargin || !cover.has(t-d1-refineMargin, t+d1+refineMargin) {
			continue
		}
		mutual := true
		for _, m := range []int{k - 1, k + 1} {
			if m >= 0 && m < n && math.Abs(t+clk[m]-clk[k]-own[j]) < d1 {
				mutual = false
			}
		}
		if mutual {
			shift[k] = own[j] - starts[k]
		}
	}
	for k := range shift {
		if math.Abs(shift[k]) < refineMinMove {
			shift[k] = 0
		}
	}
	sorted := make([]liveCue, n)
	for k, i := range order {
		sorted[k] = cues[i]
	}
	for range n {
		keepOrder(starts, shift)
		ends, bad := cueEnds(sorted, shift)
		if bad < 0 {
			for k, i := range order {
				out[i].start, out[i].end = math.Max(sorted[k].start+shift[k], 0), ends[k]
			}
			return out
		}
		// the next cue's move squeezes cue bad: the larger move of the two is undone
		if math.Abs(shift[bad]) > math.Abs(shift[bad+1]) {
			shift[bad] = 0
		} else {
			shift[bad+1] = 0
		}
	}
	return out
}

// cueEnds places the ends of the moved cues (sorted by start): a cue chained to the next one
// ends where that one now starts (less the old gap), else it keeps its length but does not
// reach into the next one when it did not before. bad is a cue left shorter than refineMinLen
// (or its length when shorter), -1 when none is.
func cueEnds(cues []liveCue, shift []float64) ([]float64, int) {
	ends := make([]float64, len(cues))
	for k, c := range cues {
		s, e := c.start+shift[k], c.end+shift[k]
		if k+1 < len(cues) {
			next := cues[k+1]
			ns := next.start + shift[k+1]
			gap := next.start - c.end
			switch {
			case gap >= 0 && gap < refineChain:
				e = ns - gap
			case gap >= 0 && e > ns-0.04:
				e = ns - 0.04
			}
		}
		if shift[k] != 0 || k+1 < len(cues) && shift[k+1] != 0 {
			if e-s < math.Min(refineMinLen, c.end-c.start)-1e-9 {
				return nil, k
			}
		}
		ends[k] = e
	}
	return ends, -1
}

// keepOrder undoes moves that would swap cues: the larger move of such a pair, until none is.
func keepOrder(starts, shift []float64) {
	for range len(starts) {
		changed := false
		for k := 0; k+1 < len(starts); k++ {
			if starts[k+1]+shift[k+1] >= starts[k]+shift[k]+math.Min(0.04, starts[k+1]-starts[k])-1e-9 {
				continue
			}
			if math.Abs(shift[k]) >= math.Abs(shift[k+1]) {
				shift[k] = 0
			} else {
				shift[k+1] = 0
			}
			changed = true
		}
		if !changed {
			return
		}
	}
}

// coverage is the stretches of the file the own-track windows cover (sorted, merged).
type coverage []span

func coverOf(subs []refWindow) coverage {
	var c coverage
	for _, w := range subs {
		if k := len(c) - 1; k >= 0 && w.Start <= c[k].end+1e-6 {
			c[k].end = math.Max(c[k].end, w.end())
			continue
		}
		c = append(c, span{w.Start, w.end()})
	}
	return c
}

// at is the covered stretch [a, b] lies in, -1 none.
func (c coverage) at(a, b float64) int {
	i := sort.Search(len(c), func(i int) bool { return c[i].end >= b })
	if i < len(c) && c[i].start <= a {
		return i
	}
	return -1
}

func (c coverage) has(a, b float64) bool { return c.at(a, b) >= 0 }

func ownStartsOf(ws []refWindow) []float64 {
	var out []float64
	for _, w := range ws {
		if w.Kind != "subs" {
			continue
		}
		for i, v := range w.Speech {
			if v == 2 {
				out = append(out, w.Start+float64(i)/syncRate)
			}
		}
	}
	sort.Float64s(out)
	// overlapping windows mark the same line starts
	k := 0
	for i, s := range out {
		if i == 0 || s-out[k-1] > 1.5/syncRate {
			out[k] = s
			k++
		}
	}
	return out[:k]
}

// median is the middle value (the upper one of an even count), 0 without values.
func median(v []float64) float64 {
	s := append([]float64(nil), v...)
	sort.Float64s(s)
	if len(s) == 0 {
		return 0
	}
	return s[len(s)/2]
}
