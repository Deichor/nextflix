package subtitles

import (
	"context"
	"fmt"
	"hash/fnv"
	"log"
	"math"
	"sort"
	"time"
)

// Diagnostics of the timing in the server log (main.go keeps those lines in daily files next to
// the players' logs): lines about a served subtitle carry the id the players log for it, a viewing
// logs how well what it is served matches the speech heard there (residual), and untimed answers
// say what they held back and how long the viewer had them.

const (
	diagResidualStep   = 60.0  // seconds of the viewer's progress between residual lines of a viewing
	diagResidualAround = 90.0  // a residual takes the cues this far before and after the viewer
	diagResidualBack   = 300.0 // and this far back when that has too few on speech
	diagMinCues        = 8     // cue starts with a speech onset near them a residual needs
	diagOnsetRadius    = 1.0   // seconds a speech onset is looked for around a cue start (as nearShare)
	diagWithin         = 0.3   // a cue start counts as on its speech within this
	diagSeenBefore     = 60.0  // moved cues that started this long before the viewer count as seen
	diagViewings       = 500   // viewings kept
)

type playerIDKey struct{}

// withPlayerID marks a file request of a player that shows the subtitle as id (the handler).
func withPlayerID(ctx context.Context, id string) context.Context {
	return context.WithValue(ctx, playerIDKey{}, id)
}

// subLogID is how the players name a subtitle id in their log: 8 hex chars of its FNV-1a hash
// (32 bit; subsLog.ts idHash, over UTF-16 code units, the same as bytes for these ASCII ids).
func subLogID(id string) string {
	h := fnv.New32a()
	h.Write([]byte(id))
	return fmt.Sprintf("%08x", h.Sum32())
}

// syncLogID is subLogID of the id a subtitle served for a file is listed with.
func syncLogID(inner, hash string, index int) string {
	return subLogID(wrapSyncID(inner, hash, index))
}

// viewDiag is what the diagnostics keep of one viewing of a served subtitle.
type viewDiag struct {
	session  time.Time // the viewing (servedSub.first); a new one starts afresh
	aliased  bool      // the player's own id was logged (an AI translation of it)
	rawSince time.Time // the first untimed answer of the viewing (zero: none)
	rawAt    float64   // the player's position then
	held     bool      // the fit an untimed answer held back was logged
	timed    bool      // the first timed answer after untimed ones was logged
	resAt    float64   // the viewer's position at the last residual (-1: none)
	resBusy  bool
}

func timedAnswer(a FileAnswer) bool {
	switch a.Sync {
	case syncStateSynced, syncStateLive, syncStateReplaced, syncStateUnchanged:
		return true
	}
	return false
}

// observe logs the diagnostics of an answer to a player at t (seconds of the file, 0 unknown):
// ctx names the player's id (withPlayerID), else it is no player's request.
func (y *syncer) observe(ctx context.Context, inner, hash string, index int, t float64, a FileAnswer) {
	pid, ok := ctx.Value(playerIDKey{}).(string)
	key, err := syncKey(inner, hash, index)
	if !ok || err != nil {
		return
	}
	id := syncLogID(inner, hash, index)
	other := subLogID(pid) != id
	if source, _, _, err := decodeID(pid); other && (err != nil || source != SourceAI) {
		return // another subtitle's request (a hole reference of the one shown)
	}
	now := time.Now()
	y.mu.Lock()
	s := y.served[key]
	if s == nil {
		y.mu.Unlock()
		return
	}
	d := y.viewDiag(key, s.first)
	alias := other && !d.aliased
	d.aliased = d.aliased || alias
	untimed := a.Sync == syncStatePending
	if untimed && d.rawSince.IsZero() && !d.timed {
		d.rawSince, d.rawAt = now, t
	}
	checkHeld := untimed && !d.held
	var rawFor time.Duration
	rawAt := d.rawAt
	firstTimed := timedAnswer(a) && !d.rawSince.IsZero() && !d.timed
	if firstTimed {
		d.timed, rawFor = true, now.Sub(d.rawSince)
	}
	residual := y.svc.cfg.LiveTiming && (t > 0 || !a.Partial) && !d.resBusy &&
		(d.resAt < 0 || math.Abs(t-d.resAt) >= diagResidualStep)
	d.resBusy = d.resBusy || residual
	y.mu.Unlock()

	if alias {
		log.Printf("subtitles: %s on %s:%d is shown as %s (AI translation) id=%s", shortSubID(inner), hash[:8], index, subLogID(pid), id)
	}
	if checkHeld {
		if held, kind := y.heldFit(key, a.VTT); held != nil {
			mv := movesNear(vttCues(a.VTT), held, t, t+liveNearAhead)
			y.mu.Lock()
			first := !d.held
			d.held = true
			y.mu.Unlock()
			if first {
				log.Printf("subtitles: pending of %s on %s:%d: its %s held back would move %d of %d cues within %.0f s of the viewer at %.0fs: max %+.2fs, median %+.2fs id=%s",
					shortSubID(inner), hash[:8], index, kind, mv.moved, mv.cues, liveNearAhead, t, mv.max, mv.median, id)
			}
		}
	}
	if firstTimed {
		moved := ""
		if base, err := y.svc.File(ctx, inner); err == nil {
			mv := movesNear(vttCues(base), vttCues(a.VTT), t, t+liveNearAhead)
			moved = fmt.Sprintf(": moved %d of %d cues within %.0f s of the viewer: max %+.2fs, median %+.2fs", mv.moved, mv.cues,
				liveNearAhead, mv.max, mv.median)
			if mv.other > 0 {
				moved += fmt.Sprintf(", %d lines of another subtitle", mv.other)
			}
		}
		log.Printf("subtitles: first timed answer of %s on %s:%d at %.0fs after %.0f s of the raw subtitle (from %.0fs)%s (%s) id=%s",
			shortSubID(inner), hash[:8], index, t, rawFor.Seconds(), rawAt, moved, a.Sync, id)
	}
	if residual {
		go func() {
			line, ok := y.residualLine(key, inner, hash, index, t, a, id)
			y.mu.Lock()
			d.resBusy = false
			if ok {
				d.resAt = t
			}
			y.mu.Unlock()
			if ok {
				log.Print(line)
			}
		}()
	}
}

// viewDiag is the diagnostics state of key's viewing that started at session (y.mu held).
func (y *syncer) viewDiag(key string, session time.Time) *viewDiag {
	if y.diags == nil {
		y.diags = map[string]*viewDiag{}
	}
	d := y.diags[key]
	if d != nil && d.session.Equal(session) {
		return d
	}
	if len(y.diags) >= diagViewings {
		for k := range y.diags {
			if y.served[k] == nil {
				delete(y.diags, k)
			}
		}
		if len(y.diags) >= diagViewings {
			y.diags = map[string]*viewDiag{}
		}
	}
	d = &viewDiag{session: session, resAt: -1}
	y.diags[key] = d
	return d
}

// heldFit is what an untimed answer (raw, the subtitle as it is) holds back: a pre-timed fit
// whose played audio has not backed it yet, or a fit that missed the bars (its kind names it, with
// its scores); nil when there is none.
func (y *syncer) heldFit(key string, raw []byte) ([]liveCue, string) {
	m := y.meta(key)
	switch {
	case m == nil:
		return nil, ""
	case m.Pre && (m.fits() || m.State == syncStateReplaced):
		if r, ok := y.result(key, raw); ok {
			return vttCues(r.VTT), "pre-timed fit"
		}
	case !m.fits() && m.State != syncStateReplaced && len(m.Fit.Segments) > 0 && m.Fit.Scale > 0:
		return applyFit(vttCues(raw), m.Fit), fmt.Sprintf("%s fit (score %.2f over %.2f, peak %.2f)", m.State,
			m.Fit.Score, m.Fit.Base, m.Fit.Peak)
	}
	return nil, ""
}

// residualLine compares the cues of answer a near the viewer at t (to the end of the file for a
// final answer) with the speech heard there; with too few cue starts on speech (music, action: a
// film's quiet stretches) it looks back up to diagResidualBack; false when that has too few too.
func (y *syncer) residualLine(key, inner, hash string, index int, t float64, a FileAnswer, id string) (string, bool) {
	f := y.liveFile(hash, index)
	cues := vttCues(a.VTT)
	to := t + diagResidualAround
	if !a.Partial {
		if d := f.knownDuration(); d > to {
			to = d
		}
	}
	var r residual
	var from float64
	for _, back := range []float64{diagResidualAround, diagResidualBack} {
		from = math.Max(t-back, 0)
		if r = residualOf(cues, f.speechIn(from-diagOnsetRadius, to+diagOnsetRadius), from, to); r.n >= diagMinCues {
			break
		}
	}
	if r.n < diagMinCues {
		return "", false
	}
	return fmt.Sprintf("subtitles: residual of %s on %s:%d at %.0fs (%.0f-%.0fs): median %+.2fs, %.0f %% within %.1fs (chance %.0f %%), n=%d (%s) id=%s",
		shortSubID(inner), hash[:8], index, t, from, to, r.median, 100*r.within, diagWithin, 100*r.chance, r.n,
		residualLabel(a, y.meta(key), t), id), true
}

// residualLabel names what a residual measured: the answer's state, the fit's offset at the
// viewer and its reference.
func residualLabel(a FileAnswer, m *syncMeta, t float64) string {
	if a.Sync == syncStatePending {
		return "pending: the subtitle as it is"
	}
	s := a.Sync
	if m != nil {
		if m.Live {
			s += " provisional"
		}
		if m.fits() && len(m.Fit.Segments) > 0 {
			segs := m.Fit.Segments
			u := t - offsetAt(segs, t) // segments start on the subtitle's clock
			s += fmt.Sprintf(", offset %+.2fs", offsetAt(segs, u))
		}
		if m.Ref != "" {
			s += ", " + m.Ref + " reference"
		}
	}
	if !a.Partial {
		s += ", to the end"
	}
	return s
}

// residual is how cue starts lie against the speech onsets (speech after a pause) heard around
// them, the own-track check's measure (nearShare) on served cues.
type residual struct {
	n      int     // cue starts with an onset within diagOnsetRadius
	median float64 // cue start minus its nearest onset (+: the cues come late)
	within float64 // share of them within diagWithin
	// chance: the same share with the cues moved 2-4 s either way (what loose or wrong lines
	// reach: dense speech or music has onsets everywhere)
	chance float64
}

// cueAt is a cue start in frames of the speech window it lies in.
type cueAt struct {
	p  float64
	on []int // the window's onsets
}

func residualOf(cues []liveCue, ws []refWindow, from, to float64) residual {
	r := int(diagOnsetRadius * syncRate)
	var at []cueAt
	for _, w := range ws {
		on := onsetFrames(w.Speech)
		for _, c := range cues {
			if c.start < from || c.start >= to {
				continue
			}
			p := (c.start - w.Start) * syncRate
			if p < float64(r) || p+float64(r) >= float64(len(w.Speech)) {
				continue // the speech around it was not all heard
			}
			at = append(at, cueAt{p, on})
		}
	}
	diffs := func(shift float64) []float64 {
		var ds []float64
		for _, c := range at {
			if d, ok := nearOnset(c.on, c.p-shift*syncRate, r); ok {
				ds = append(ds, d/syncRate)
			}
		}
		return ds
	}
	share := func(ds []float64) float64 {
		if len(ds) == 0 {
			return 0
		}
		n := 0
		for _, d := range ds {
			if math.Abs(d) <= diagWithin+1e-9 {
				n++
			}
		}
		return float64(n) / float64(len(ds))
	}
	ds := diffs(0)
	out := residual{n: len(ds)}
	if len(ds) == 0 {
		return out
	}
	out.median, out.within = median(ds), share(ds)
	var chance []float64
	for _, s := range []float64{-4, -3.5, -3, -2.5, -2, 2, 2.5, 3, 3.5, 4} {
		chance = append(chance, share(diffs(s)))
	}
	out.chance = mean(chance)
	return out
}

// nearOnset is p minus the onset nearest to it (frames) when one lies within r frames.
func nearOnset(on []int, p float64, r int) (float64, bool) {
	k := sort.SearchInts(on, int(math.Ceil(p)))
	best, ok := 0.0, false
	for _, j := range []int{k - 1, k} {
		if j >= 0 && j < len(on) {
			if d := p - float64(on[j]); math.Abs(d) <= float64(r) && (!ok || math.Abs(d) < math.Abs(best)) {
				best, ok = d, true
			}
		}
	}
	return best, ok
}

// speechIn is the speech heard of [from, to): finished grid windows, and unfinished ones by their
// heard stretch (as windowsOf counts them), contiguous ones joined.
func (f *liveFile) speechIn(from, to float64) []refWindow {
	type chunk struct {
		start float64
		e     []uint8
	}
	var done []refWindow
	var open []chunk
	to = math.Min(to, syncMaxUnknown)
	f.mu.Lock()
	for w := max(int(math.Floor(from/syncWindowLen)), 0); float64(w)*syncWindowLen < to; w++ {
		if d, ok := f.done[w]; ok {
			done = append(done, refWindow{Start: float64(w) * syncWindowLen, Kind: "live", Speech: unpackBits(d.Bits, d.Frames)})
			continue
		}
		c, ok := f.chunks[w]
		if !ok {
			continue
		}
		first, last := -1, -1
		for i, v := range c {
			if v != 0 {
				if first < 0 {
					first = i
				}
				last = i
			}
		}
		if first < 0 || f.covered[w] < liveMinRun || float64(f.covered[w]) < liveDoneShare*float64(last-first+1) {
			continue
		}
		open = append(open, chunk{float64(w)*syncWindowLen + float64(first)/syncRate, append([]uint8(nil), c[first:last+1]...)})
	}
	f.mu.Unlock()
	for _, c := range open {
		done = append(done, refWindow{Start: c.start, Kind: "live", Speech: classifyEnergies(c.e)})
	}
	return joinWindows(done)
}

// joinWindows sorts windows that do not overlap and joins the contiguous ones.
func joinWindows(ws []refWindow) []refWindow {
	ws = append([]refWindow(nil), ws...)
	sort.Slice(ws, func(i, j int) bool { return ws[i].Start < ws[j].Start })
	var out []refWindow
	for _, w := range ws {
		if n := len(out); n > 0 && math.Abs(out[n-1].end()-w.Start) < 0.5/syncRate {
			out[n-1].Speech = append(out[n-1].Speech, w.Speech...)
			continue
		}
		w.Speech = append([]byte(nil), w.Speech...) // grown in place when the next one joins
		out = append(out, w)
	}
	return out
}
