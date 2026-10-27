package subtitles

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"math"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
)

// The first minutes of real viewings that slipped (snapshot of 2026-10-27, NFX_TESTDATA; the
// server and TV logs of 2026-10-22..26 give each case's numbers). Each is replayed as it ran: the
// file's read windows as they were when the episode opened (pre-timed: the first pass read before
// it, the extra windows that pass read served by a stub reader, 10 s each; else the first pass
// lands readAt seconds after the open), the played audio and the own track's cue tap heard as the
// log says (heard: seconds of audio by seconds after the open), the own-track check as it went,
// re-fits through decide every 5 s while the subtitle is untimed in the first 5 minutes (15 s
// else, and only on new audio), and the TV's answers (at the open, then every 5 s while pending,
// 15 s else). Scored: the cues of the first 5 minutes as the TV shows them (2 s behind the open)
// against the file's final fit (every window read and played: the best truth there is for these
// files), and when it first shows a timing that puts 90 % of them within 0.5 s.

type startCase struct {
	name   string
	file   string // syncref name (the sha1 of the file key)
	sub    string // source/raw name
	first  int    // windows of the reference's first pass (the rest: extra reads, by start)
	pre    bool   // the first pass was read (and fitted) before the open
	readAt float64
	heard  [][2]float64 // (seconds after the open, seconds of audio heard)
	truth  string       // the final fit, as logged
}

var startCases = []startCase{
	// 53c757ba:1, opensubtitles/tr/3099466 (session m8us48bp, 2026-10-22 02:23): pre-timed right
	// (-1.01 s), replaced 5 s in by the set-aside own track's -3.03 s until 02:23:33
	{"S03E15", "2076e82d", "opensubtitles/6ac209b2", 6, true, 0,
		[][2]float64{{5.7, 39}, {10.8, 49}, {31, 180}, {47, 240}, {400, 490}},
		"-1.00 -3.35@237 -4.98@1076 -6.15@1523 -8.42@2131"},
	// db733675:1, turkcealtyazi/tr S03E16 (session dap8wyik, 09:18): pending, then -0.87 s from
	// 10 s, -2.39 s from 56 s. The final fit (-2.44 s from 0) misses the teaser: its played speech,
	// the cue tap and the read window at 20 s put the lines up to the title card (raw 88 s) 1.4 s
	// later (the change in the pause before the dialogue at raw 121 s)
	{"S03E16", "0d6a62b8", "turkcealtyazi/114a3bac", 6, true, 0,
		[][2]float64{{7.3, 28}, {13.3, 60}, {32.3, 158}, {47.3, 229}, {400, 490}},
		"-0.98 -2.44@114 -3.97@837 -5.14@1417 -6.57@2047"},
	// acf8062b:23, turkcealtyazi/tr S03E22 (session r2febpqx, 2026-10-25 17:08): the read ran
	// while it played (6 windows 80 s after the open, 5 extra by 132 s); the unchecked own track
	// (another cut) timed it -1.64 s
	{"S03E22", "7ea562c1", "turkcealtyazi/9e158ce5", 6, false, 80,
		[][2]float64{{33, 142}, {36, 168}, {68, 240}, {140, 240}, {400, 500}},
		"-0.82 -2.57@164 -3.90@973 -4.52@1637 -5.10@2140"},
	// 121ba5e7:1, turkcealtyazi/tr S04E01 (session hwo5xtgf, 2026-10-26 18:09): pre-timed from the
	// set-aside track (-2.99 s; the teaser -1.22 s), pending for 16 s
	{"S04E01", "6c125c4a", "turkcealtyazi/192bf001", 6, true, 0,
		[][2]float64{{7.4, 46}, {12.4, 120}, {17.4, 207}, {22.4, 207}, {31, 238}, {400, 490}},
		"-1.12 -2.99@207 -4.73@594 -6.12@1280 -8.54@1836"},
	// 6c044aaa:1, turkcealtyazi/tr S03E18 (2026-10-22 18:22, A2): pre-timed from the speech (its own
	// track set aside) with the steps at 149 and 558 s; the set-aside track's fit at 10 s had them
	// at 275 and 902 s as the final fit does, the speech fit took over again at 15 s
	{"S03E18", "ebdba264", "turkcealtyazi/c49a3237", 6, true, 0,
		[][2]float64{{8, 39}, {13, 60}, {16, 111}, {31, 210}, {400, 580}},
		"-0.73 -2.30@275 -3.55@923 -4.67@1583 -5.77@1961"},
	// aaa77dcb:2, Westworld S02E01 4K muxed, turkcealtyazi/tr (session x19adwmk, 2026-10-24 10:00):
	// pre-timed from its start windows (-0.96 s), pending for 12 s
	{"WW S02E01", "4d687085", "turkcealtyazi/28e5cd5a", 3, true, 0,
		[][2]float64{{22, 27}, {400, 405}},
		"-0.96"},
}

// parseFit reads offsets as logged ("-1.00 -3.35@237 ...").
func parseFit(s string) syncFit {
	f := syncFit{Scale: 1}
	for _, p := range strings.Fields(s) {
		off, from, _ := strings.Cut(p, "@")
		o, _ := strconv.ParseFloat(off, 64)
		a, _ := strconv.ParseFloat(from, 64)
		f.Segments = append(f.Segments, syncSegment{From: a, Offset: o})
	}
	return f
}

// heardAt interpolates the heard audio (1x after the last point).
func heardAt(pts [][2]float64, t float64) float64 {
	px, py := 0.0, 0.0
	for _, p := range pts {
		if t <= p[0] {
			return py + (t-px)/(p[0]-px)*(p[1]-py)
		}
		px, py = p[0], p[1]
	}
	return py + t - px
}

// loadSub reads a subtitle of the snapshot by its source/raw name prefix.
func loadSub(t *testing.T, name string) *snapSub {
	ps, _ := filepath.Glob(filepath.Join(snapshotDir(t), name+"*.vtt"))
	if len(ps) == 0 {
		return nil
	}
	b, _ := os.ReadFile(ps[0])
	s := &snapSub{name: name, cues: vttCues(b)}
	for _, c := range s.cues {
		s.spans = append(s.spans, span{c.start, c.end})
	}
	return s
}

// snapOwn is the cue tap's own-track windows of a file (synclive "own").
func snapOwn(t *testing.T, name string) map[int]liveWin {
	b, err := os.ReadFile(filepath.Join(snapshotDir(t), "synclive", name+".json"))
	if err != nil {
		return nil
	}
	var s liveSaved
	if json.Unmarshal(b, &s) != nil {
		return nil
	}
	return s.Own
}

type timelineEntry struct {
	at      float64
	shown   syncFit // identityFit: untimed
	pending bool
	what    string
}

type startRun struct {
	timeline   []timelineEntry
	errs       []float64
	rightAt    float64 // seconds after the open the TV first shows a right timing (-1: not by 330 s)
	wrong      int     // cues of the first 5 minutes shown more than 0.5 s off
	untimed    int     // of them, shown untimed (untimedFar: more than 2 s off)
	untimedFar int
	rawShown   int // cues of the first 5 minutes shown untimed
	// moves: changes of a timing shown (to another or untimed) in the first 5 minutes; pre: the
	// pre-timed fit (none: did not fit); shownTimed: a timing was shown, lastShown the last one
	moves      int
	pre        syncFit
	shownTimed bool
	lastShown  syncFit
}

const startUpto = 330.0

// startSpec is how a viewing starts: the reference's first pass (first windows; pre: read and
// fitted before the open, else landing readAt seconds after it, never when negative), the audio
// heard by seconds after the open, and every cue's true time (NaN: unknown).
type startSpec struct {
	first  int
	pre    bool
	readAt float64
	heard  func(t float64) float64
	truth  []float64
}

func (c startCase) spec(sub []span) startSpec {
	tf := parseFit(c.truth)
	truth := make([]float64, len(sub))
	for i, s := range sub {
		truth[i] = tf.mapTime(s.start)
	}
	return startSpec{c.first, c.pre, c.readAt, func(t float64) float64 { return heardAt(c.heard, t) }, truth}
}

// replayStart replays one case through the real syncer.
func replayStart(t *testing.T, c startSpec, f *snapFile, own map[int]liveWin, sub []span, verbose bool) startRun {
	svc := NewService(nil, Config{CacheDir: t.TempDir(), TorrServerURL: "http://torrserver", LiveTiming: true})
	y := svc.sync
	y.onGrow = func(*liveFile) {}
	full := f.ref
	first := full
	first.Windows = append([]refWindow(nil), full.Windows[:min(c.first, len(full.Windows))]...)
	first.Speech = nil
	for _, s := range full.Speech {
		if hasWindowAt(first.Windows, s.Start) {
			first.Speech = append(first.Speech, s)
		}
	}
	y.probe = func(context.Context, string) (mediaProbe, error) { return full.Probe, nil }
	hash := f.name
	inner := cacheProviderSub(t, svc, "start|"+f.name[:8], sub)
	key, _ := syncKey(inner, hash, 1)
	base, _ := svc.File(context.Background(), inner)
	fk := syncFileKey(hash, 1)

	now := -300.0 // the virtual clock: seconds after the open
	var run startRun
	note := func(what string) {
		m := y.meta(key)
		e := timelineEntry{at: now, shown: identityFit, pending: untimedMeta(m), what: what}
		if !e.pending && m.fits() {
			e.shown = m.Fit
		}
		if n := len(run.timeline); n > 0 && run.timeline[n-1].pending == e.pending && segText(run.timeline[n-1].shown) == segText(e.shown) {
			return
		}
		run.timeline = append(run.timeline, e)
		if verbose && m != nil {
			lf := y.liveFile(hash, 1)
			lf.mu.Lock()
			chk := lf.check
			lf.mu.Unlock()
			t.Logf("   %6.1f s %-8s %-9s pre=%-5v ref=%-15s win=%2d live=%d (%3.0f s) %s | check %q nofit=%v tight=%v pairs=%d",
				now, what, m.State, m.Pre, m.Ref, m.Windows, m.LiveWindows, m.LiveSeconds, segText(m.Fit), chk.State, chk.NoFit, chk.Tight, chk.Pairs)
		}
	}
	// extra window reads come from the reference as it was read later, 10 s a window (its own
	// track, then its speech)
	y.read = func(_ context.Context, _ string, p mediaProbe, at, _ float64) (refWindow, error) {
		note("reading")
		now += 5
		ws := full.Windows
		if p.OwnSub < 0 {
			ws = append(slices.Clone(full.Speech), full.Windows...)
		}
		for _, w := range ws {
			if math.Abs(w.Start-at) < 1 && (p.OwnSub >= 0 || w.Kind != "subs") {
				return w, nil
			}
		}
		return refWindow{}, errors.New("not read in the snapshot")
	}
	st, release := y.refState(fk)
	st.mu.Lock()
	r0 := first
	st.ref = &r0
	st.mu.Unlock()
	release()
	stored := func() syncRef {
		st, release := y.refState(fk)
		defer release()
		st.mu.Lock()
		defer st.mu.Unlock()
		return *st.ref
	}
	ctx := context.Background()
	lf := y.liveFile(hash, 1)
	lf.setDuration(full.Probe.Duration)
	if c.pre {
		y.decideRead(ctx, inner, hash, 1, base, first)
		note("pre")
		if m := y.meta(key); m != nil && m.fits() {
			run.pre = m.Fit
		}
	}

	// the open: the file plays, the subtitle is asked for
	now = 0
	lf.mu.Lock()
	lf.taps, lf.ownTaps = 1, 1
	lf.mu.Unlock()
	if firstReq, _ := y.noteServed(key, inner, hash, 1, 0); firstReq && untimedMeta(y.meta(key)) {
		y.markPending(hash, 1)
	}
	note("open")
	live := map[int]refWindow{}
	for _, w := range f.live {
		live[int(math.Round(w.Start/syncWindowLen))] = w
	}
	setHeard := func(h float64) {
		lf.mu.Lock()
		defer lf.mu.Unlock()
		lf.done, lf.own, lf.ownRead, lf.ownDone = map[int]liveWin{}, map[int][]uint8{}, map[int]int{}, map[int]liveWin{}
		for g := 0; float64(g)*syncWindowLen < h; g++ {
			rest := int(math.Min(h-float64(g)*syncWindowLen, syncWindowLen) * syncRate)
			if w, ok := live[g]; ok {
				if n := min(rest, len(w.Speech)); n == len(w.Speech) || n >= liveMinRun {
					lf.done[g] = liveWin{Bits: packBits(w.Speech[:n]), Frames: n}
				}
			}
			o, ok := own[g]
			if !ok {
				continue
			}
			if rest >= o.Frames {
				lf.ownDone[g] = o
				continue
			}
			bits := unpackBits(o.Bits, o.Frames)
			fr := make([]uint8, liveWindowFrames)
			for i := 0; i < rest; i++ {
				fr[i] = ownRead
				if bits[i] != 0 {
					fr[i] |= ownShows
				}
			}
			for _, i := range o.Starts {
				if i < rest {
					fr[i] |= ownStarts
				}
			}
			lf.own[g], lf.ownRead[g] = fr, rest
		}
		lf.complete = false
	}
	read := c.pre || full.Skipped // above the byte budget nothing is read (but a pre-timing's start)
	nextRefit, lastHeard := 3.0, -1.0
	for tick := 0.0; tick <= startUpto; tick += 0.5 {
		if tick < now {
			continue // a decide reading windows runs (it holds the subtitle's decide lock)
		}
		now = tick
		h := c.heard(tick)
		setHeard(h)
		if !read && c.readAt >= 0 && tick >= c.readAt {
			read = true
			y.decideRead(ctx, inner, hash, 1, base, stored())
			note("read")
			continue
		}
		pending := untimedMeta(y.meta(key)) && tick < livePendingFor.Seconds()
		step := liveRefitGrowth
		if pending {
			step = livePendingGrowth
		}
		if tick < nextRefit || h-lastHeard < step && lastHeard >= 0 {
			continue
		}
		ref := syncRef{Probe: full.Probe, Skipped: true, Busy: !full.Skipped} // the first pass is being read
		if read {
			ref = stored()
		}
		y.decide(ctx, inner, hash, 1, base, ref, false)
		note("refit")
		lastHeard = h
		gap := y.refitGap.Seconds()
		if untimedMeta(y.meta(key)) && tick < livePendingFor.Seconds() {
			gap = y.pendingGap.Seconds()
		}
		nextRefit = tick + gap
	}

	// the TV's answers
	at := func(p float64) timelineEntry {
		e := timelineEntry{shown: identityFit, pending: true}
		for _, x := range run.timeline {
			if x.at <= p {
				e = x
			}
		}
		return e
	}
	var polls []timelineEntry
	for p := 0.5; p < startUpto; {
		e := at(p)
		e.at = p
		if n := len(polls); n > 0 && p < 300 && !untimed(polls[n-1].shown) && segText(polls[n-1].shown) != segText(e.shown) {
			run.moves++
		}
		if !untimed(e.shown) {
			run.shownTimed, run.lastShown = true, e.shown
		}
		polls = append(polls, e)
		if e.pending && p < 180 {
			p += 5
		} else {
			p += 15
		}
	}
	run.rightAt = -1
	for _, p := range polls {
		var errs []float64
		for i, s := range sub {
			if T := c.truth[i]; T >= 0 && T < 300 {
				errs = append(errs, math.Abs(p.shown.mapTime(s.start)-T))
			}
		}
		if st := statsOf(errs); st.n > 0 && st.w05 >= 0.9 && run.rightAt < 0 {
			run.rightAt = p.at
		}
	}
	for i, s := range sub {
		T := c.truth[i]
		if math.IsNaN(T) || T < 0 || T >= 300 {
			continue
		}
		var p *timelineEntry
		for i := range polls {
			if polls[i].at <= T+2 {
				p = &polls[i]
			}
		}
		if p == nil {
			continue
		}
		e := math.Abs(p.shown.mapTime(s.start) - T)
		run.errs = append(run.errs, e)
		if untimed(p.shown) {
			run.rawShown++
		}
		if e > 0.5 {
			run.wrong++
			if untimed(p.shown) {
				run.untimed++
				if e > 2 {
					run.untimedFar++
				}
			}
		}
	}
	return run
}

// midCase is a viewing replayed a grid window at a time (both taps), the read reference as its
// first windows were: 09b1b75d:1 S03E17 (session bln0pe5d, 2026-10-22): its Turkish subtitle,
// timed from the windows of its own track set aside (no fit to the speech), moved as a whole by
// -0.33 s at 600 s heard and +0.37 s at 740 s.
type midCase struct {
	name, file, sub string
	read            int   // read windows
	order           []int // grid windows in the order heard
	truth           string
}

var midCases = []midCase{
	{"S03E17", "6f1d72be", "turkcealtyazi/5368ef84", 10, gridOrder(0, 42),
		"-0.44 -2.05@214 -2.64@354 -3.80@989 -3.21@1122 -4.09@1188 -5.22@1519 -6.73@2101"},
}

func gridOrder(a, b int) []int {
	var out []int
	for g := a; g <= b; g++ {
		out = append(out, g)
	}
	return out
}

// TestSnapshotMidCases replays midCases and counts the moves of the whole track (every cue by
// the same amount) and how far each served fit lies from the truth.
func TestSnapshotMidCases(t *testing.T) {
	files, subs := loadSnapshot(t)
	log.SetOutput(io.Discard)
	t.Cleanup(func() { log.SetOutput(os.Stderr) })
	for _, c := range midCases {
		f, s := findFile(files, c.file), findSub(subs, c.sub)
		if s == nil {
			s = loadSub(t, c.sub)
		}
		if f == nil || s == nil {
			t.Logf("%s: not in the snapshot", c.name)
			continue
		}
		r := replayMid(t, f, snapOwn(t, f.name), s.spans, c.read, c.order, parseFit(c.truth), true)
		t.Logf("%s: %d served changes, %d of them whole-track moves of 0.2 s or more; near the viewer median |error| %.2f s, p90 %.2f s",
			c.name, r.moves, r.whole, quant(r.near, 0.5), quant(r.near, 0.9))
	}
}

type midRun struct {
	moves, whole int       // served changes, of them moves of the whole track by 0.2 s or more
	near         []float64 // |median error| of the 5 minutes ahead of the viewer, per re-fit
}

// replayMid plays the grid windows in order (both taps; read: the reference's first windows),
// re-fitting after each.
func replayMid(t *testing.T, f *snapFile, own map[int]liveWin, sub []span, read int, order []int, truth syncFit, verbose bool) midRun {
	svc := NewService(nil, Config{CacheDir: t.TempDir(), TorrServerURL: "http://torrserver", LiveTiming: true})
	y := svc.sync
	y.onGrow = func(*liveFile) {}
	ref := f.ref
	ref.Windows = ref.Windows[:min(read, len(ref.Windows))]
	y.probe = func(context.Context, string) (mediaProbe, error) { return ref.Probe, nil }
	inner := cacheProviderSub(t, svc, "mid|"+f.name[:8], sub)
	key, _ := syncKey(inner, f.name, 1)
	base, _ := svc.File(context.Background(), inner)
	lf := y.liveFile(f.name, 1)
	lf.setDuration(ref.Probe.Duration)
	live := map[int]refWindow{}
	for _, w := range f.live {
		live[int(math.Round(w.Start/syncWindowLen))] = w
	}
	var r midRun
	var last syncFit
	for i, g := range order {
		lf.mu.Lock()
		if w, ok := live[g]; ok {
			lf.done[g] = liveWin{Bits: packBits(w.Speech), Frames: len(w.Speech)}
		}
		if o, ok := own[g]; ok {
			lf.ownDone[g] = o
		}
		lf.complete = false
		lf.mu.Unlock()
		y.decide(context.Background(), inner, f.name, 1, base, ref, false)
		m := y.meta(key)
		lf.mu.Lock()
		chk := lf.check
		lf.mu.Unlock()
		if m == nil || !m.fits() {
			continue
		}
		// the viewer about 90 s behind the audio: the cues of the next 5 minutes there
		at := float64(g)*syncWindowLen - 30
		var errs []float64
		for _, sp := range sub {
			if T := truth.mapTime(sp.start); T >= at && T < at+300 {
				errs = append(errs, m.Fit.mapTime(sp.start)-T)
			}
		}
		med := 0.0
		if len(errs) > 0 {
			slices.Sort(errs)
			med = errs[len(errs)/2]
			r.near = append(r.near, math.Abs(med))
		}
		if len(last.Segments) > 0 && segText(last) != segText(m.Fit) {
			r.moves++
			if d, ok := wholeShift(last, m.Fit); ok && math.Abs(d) >= 0.2 {
				r.whole++
				t.Logf("   window %2d (%d heard): the whole track moved %+.2f s", g, i+1, d)
			}
		}
		if verbose {
			t.Logf("   window %2d: %-9s ref=%-10s %s | check %q nofit=%v lag %+.2f pairs %d | near the viewer %+.2f s",
				g, m.State, m.Ref, segText(m.Fit), chk.State, chk.NoFit, chk.Lag, chk.Pairs, med)
		}
		last = m.Fit
	}
	return r
}

// wholeShift reports whether b is a moved as a whole (same splits, every offset by d within 0.05 s).
func wholeShift(a, b syncFit) (float64, bool) {
	if len(a.Segments) != len(b.Segments) || a.Scale != b.Scale {
		return 0, false
	}
	d := b.Segments[0].Offset - a.Segments[0].Offset
	for i := range a.Segments {
		if math.Abs(b.Segments[i].Offset-a.Segments[i].Offset-d) > 0.05 || i > 0 && math.Abs(b.Segments[i].From-a.Segments[i].From) > 1 {
			return 0, false
		}
	}
	return d, true
}

// startCurves prints, per window of the first 6 minutes (read, played speech, cue tap), the
// subtitle's best offsets there (its lines moved by the truth first: offsets are off the truth).
func startCurves(t *testing.T, f *snapFile, own map[int]liveWin, sub []span, truth syncFit) {
	moved := make([]span, len(sub))
	for i, s := range sub {
		moved[i] = span{truth.mapTime(s.start), truth.mapTime(s.end)}
	}
	starts := startSignal(moved, 1)
	d := int(syncMaxOffset * syncRate)
	ws := append([]refWindow(nil), f.ref.Windows...)
	ws = append(ws, f.live...)
	for g, o := range own {
		s := unpackBits(o.Bits, o.Frames)
		for _, i := range o.Starts {
			s[i] = 2
		}
		ws = append(ws, refWindow{Start: float64(g) * syncWindowLen, Kind: "tap", Speech: s})
	}
	slices.SortFunc(ws, func(a, b refWindow) int { return int(a.Start - b.Start) })
	for _, w := range prepare(ws) {
		if w.Start > 400 {
			continue
		}
		c := windowCurve(w, starts, d)
		lo, hi := d-300, d+300 // Â±3 s around the truth
		var top []string
		used := map[int]bool{}
		for len(top) < 3 {
			b := -1
			for q := lo; q <= hi; q++ {
				if !used[q] && (b < 0 || c[q] > c[b]) {
					b = q
				}
			}
			top = append(top, fmt.Sprintf("%+.2fs %.1f", float64(b-d)/syncRate, c[b]))
			for q := b - 40; q <= b+40; q++ {
				used[q] = true
			}
		}
		t.Logf("   %-5s %5.0f lines %3.0f at truth %.1f, best %s", w.Kind, w.Start, w.lines, c[d], strings.Join(top, " | "))
	}
}

// TestSnapshotStartCases replays the start cases (startCases) and sums them up.
func TestSnapshotStartCases(t *testing.T) {
	files, subs := loadSnapshot(t)
	verbose := os.Getenv("NFX_EVAL") == "verbose"
	if !verbose {
		log.SetOutput(io.Discard)
		t.Cleanup(func() { log.SetOutput(os.Stderr) })
	}
	var all []float64
	var rights []float64
	wrong, untimedN, n := 0, 0, 0
	for _, c := range startCases {
		if n := os.Getenv("NFX_CASE"); n != "" && n != c.name {
			continue
		}
		f, s := findFile(files, c.file), findSub(subs, c.sub)
		if s == nil {
			s = loadSub(t, c.sub) // listed under another name with the same cues
		}
		if f == nil || s == nil {
			t.Logf("%s: not in the snapshot (file %v, subtitle %v)", c.name, f != nil, s != nil)
			continue
		}
		if verbose {
			startCurves(t, f, snapOwn(t, f.name), s.spans, parseFit(c.truth))
		}
		if verbose {
			var ws []string
			for _, w := range f.ref.Windows {
				ws = append(ws, fmt.Sprintf("%s@%.0f", w.Kind, w.Start))
			}
			t.Logf("%s %s: reference %s (first %d), %d live windows, %d own", c.name, f.label(), strings.Join(ws, " "), c.first,
				len(f.live), len(snapOwn(t, f.name)))
		}
		r := replayStart(t, c.spec(s.spans), f, snapOwn(t, f.name), s.spans, verbose)
		st := statsOf(r.errs)
		var tl []string
		for _, e := range r.timeline {
			txt := segText(e.shown)
			if e.pending {
				txt = "untimed"
			}
			tl = append(tl, fmt.Sprintf("%.0fs %s", e.at, txt))
		}
		t.Logf("%-10s right from %4.0f s | first 5 min as shown: %s, %d off by >0.5 s (%d untimed) | %s", c.name, r.rightAt, st,
			r.wrong, r.untimed, strings.Join(tl, " -> "))
		all = append(all, r.errs...)
		wrong += r.wrong
		untimedN += r.untimed
		n++
		if r.rightAt >= 0 {
			rights = append(rights, r.rightAt)
		} else {
			rights = append(rights, startUpto)
		}
	}
	t.Logf("SUMMARY start cases %d: first 5 min as shown %s, %d cues off by >0.5 s (%d untimed); right timing shown median %.0f s, max %.0f s",
		n, statsOf(all), wrong, untimedN, quant(rights, 0.5), quant(rights, 1))
}

// TestSnapshotStartAll replays the start of every viewing the snapshot allows, as the start cases
// above: each file played from its start (grid windows 0-3 heard) with up to NFX_PER_FILE (3)
// subtitles that fit the whole file (firstTruth; the best fitting first), opened pre-timed and
// while its first read runs (it lands 45 s after the open; above the byte budget nothing is read
// but a pre-timing's start windows); the TV's split stream hears 8x real time up to 200 s ahead, a
// muxed one (files above the budget) 2x up to 28 s ahead. Up to NFX_OTHERS (1) subtitles of about
// the file's length that fit none of it are opened too: a timing shown for them is false.
// NFX_KINDS names movies and series (lines
// "<file> <kind> <title>"). With NFX_EVAL=backing the pre-timed fits are also scored by the
// played audio as it comes (playBacking).
func TestSnapshotStartAll(t *testing.T) {
	files, subs := loadSnapshot(t)
	if os.Getenv("NFX_EVAL") != "verbose" {
		log.SetOutput(io.Discard)
	}
	t.Cleanup(func() { log.SetOutput(os.Stderr) })
	kinds := map[string]string{}
	if p := os.Getenv("NFX_KINDS"); p != "" {
		b, _ := os.ReadFile(p)
		for _, line := range strings.Split(string(b), "\n") {
			if f := strings.Fields(line); len(f) >= 2 {
				kinds[f[0]] = strings.Join(f[1:], " ")
			}
		}
	}
	others := 1
	if v, err := strconv.Atoi(os.Getenv("NFX_OTHERS")); err == nil {
		others = v
	}
	type agg struct {
		runs, wrong, timedWrong, untimedWrong, untimedFar, rawShown, moves int
		errs, rights                                                       []float64
	}
	groups := map[string]*agg{}
	add := func(name string, r startRun) {
		g := groups[name]
		if g == nil {
			g = &agg{}
			groups[name] = g
		}
		g.runs++
		g.errs = append(g.errs, r.errs...)
		g.wrong += r.wrong
		g.untimedWrong += r.untimed
		g.untimedFar += r.untimedFar
		g.rawShown += r.rawShown
		g.timedWrong += r.wrong - r.untimed
		g.moves += r.moves
		rt := r.rightAt
		if rt < 0 {
			rt = startUpto
		}
		g.rights = append(g.rights, rt)
	}
	var backing []backRow
	falseShown, falseRuns := 0, 0
	perFile := 3
	if v, err := strconv.Atoi(os.Getenv("NFX_PER_FILE")); err == nil {
		perFile = v
	}
	// files in parallel (each replay has its own syncer); results under mu
	var mu sync.Mutex
	jobs := make(chan *snapFile)
	var wg sync.WaitGroup
	for range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for f := range jobs {
				startFile(t, f, subs, kinds, others, perFile, &mu, add, &backing, &falseShown, &falseRuns)
			}
		}()
	}
	for _, f := range files {
		played := map[int]bool{}
		for _, w := range f.live {
			played[int(math.Round(w.Start/syncWindowLen))] = true
		}
		if !played[0] || !played[1] || !played[2] || !played[3] {
			continue
		}
		if c := os.Getenv("NFX_CASE"); c != "" && !slices.ContainsFunc(strings.Split(c, ","), func(p string) bool { return strings.HasPrefix(f.name, p) }) {
			continue
		}
		jobs <- f
	}
	close(jobs)
	wg.Wait()
	names := make([]string, 0, len(groups))
	for n := range groups {
		names = append(names, n)
	}
	slices.Sort(names)
	t.Logf("SUMMARY start of every viewing (first 5 minutes as the TV shows them; right: 90 %% within 0.5 s)")
	for _, n := range names {
		g := groups[n]
		t.Logf("%-20s runs %3d | %s | off by >0.5 s: %4d cues (%d timed wrong, %d untimed, %d of them >2 s; %d shown untimed) | right shown median %3.0f s, p90 %3.0f s | moves %d",
			n, g.runs, statsOf(g.errs), g.wrong, g.timedWrong, g.untimedWrong, g.untimedFar, g.rawShown, quant(g.rights, 0.5), quant(g.rights, 0.9), g.moves)
	}
	t.Logf("other subtitles: %d of %d replays showed a timing", falseShown, falseRuns)
	if len(backing) > 0 {
		summarizeBacking(t, backing)
	}
}

// startFile replays the start of one file of TestSnapshotStartAll.
func startFile(t *testing.T, f *snapFile, subs []*snapSub, kinds map[string]string, others, perFile int, mu *sync.Mutex,
	add func(string, startRun), backing *[]backRow, falseShown, falseRuns *int) {
	kind := "unknown"
	if k, ok := kinds[f.name]; ok {
		kind, _, _ = strings.Cut(k, " ")
	}
	own := snapOwn(t, f.name)
	first := firstPass(f.ref)
	heard := func(t float64) float64 { return math.Min(8*t, t+200) }
	if f.ref.Skipped {
		heard = func(t float64) float64 { return math.Min(2*t, t+28) }
	}
	type cand struct {
		s    *snapSub
		gain float64
	}
	var fitting []cand
	var unfit []*snapSub
	for _, s := range subs {
		if g := quickGain(f, s.spans); g >= 0.1 {
			fitting = append(fitting, cand{s, g})
		} else if len(unfit) < others && aboutLength(s.spans, f.ref.Probe.Duration) {
			unfit = append(unfit, s)
		}
	}
	// the best fitting ones first, at most perFile that fit the whole file
	slices.SortFunc(fitting, func(a, b cand) int { return int(math.Copysign(1, b.gain-a.gain)) })
	done := 0
	for _, c := range fitting {
		s := c.s
		if v := os.Getenv("NFX_SUB"); v != "" && !strings.Contains(s.name, v) {
			continue
		}
		if done >= perFile {
			break
		}
		truth, _ := firstTruth(t, f, s.spans)
		if truth == nil {
			continue
		}
		done++
		type named struct {
			name string
			sp   startSpec
		}
		var specs []named
		if first > 0 {
			specs = append(specs, named{"pre-timed", startSpec{first, true, 0, heard, truth}})
		}
		open := startSpec{first, false, 45, heard, truth}
		if f.ref.Skipped {
			open = startSpec{0, false, -1, heard, truth}
		}
		specs = append(specs, named{"opened", open})
		var line []string
		for _, sc := range specs {
			r := replayStart(t, sc.sp, f, own, s.spans, os.Getenv("NFX_EVAL") == "verbose")
			var rows []backRow
			if sc.sp.pre && os.Getenv("NFX_EVAL") == "backing" && len(r.pre.Segments) > 0 {
				rows = backRows(f, s.spans, r.pre, truth, kind)
			}
			mu.Lock()
			add(kind+" "+sc.name, r)
			add("all "+sc.name, r)
			*backing = append(*backing, rows...)
			mu.Unlock()
			st := statsOf(r.errs)
			line = append(line, fmt.Sprintf("%s %3.0f%% within 0.5 s, %d off (%d untimed), right at %3.0f s, %d moves [%s]", sc.name,
				100*st.w05, r.wrong, r.untimed, r.rightAt, r.moves, shownText(r)))
		}
		t.Logf("%s %s %s | %s", f.label(), s.name, kinds[f.name], strings.Join(line, " | "))
	}
	for _, s := range unfit {
		nan := make([]float64, len(s.spans))
		for i := range nan {
			nan[i] = math.NaN()
		}
		sp := startSpec{first, false, 45, heard, nan}
		if f.ref.Skipped {
			sp = startSpec{0, false, -1, heard, nan}
		}
		r := replayStart(t, sp, f, own, s.spans, false)
		mu.Lock()
		*falseRuns++
		if r.shownTimed {
			*falseShown++
		}
		mu.Unlock()
		if r.shownTimed {
			t.Logf("%s: another subtitle %s shown timed: %s", f.name[:8], s.name, segText(r.lastShown))
		}
	}
}

// shownText is what a replay showed when (seconds after the open).
func shownText(r startRun) string {
	var parts []string
	for _, e := range r.timeline {
		txt := segText(e.shown)
		if e.pending {
			txt = "untimed"
		}
		parts = append(parts, fmt.Sprintf("%.0fs %s", e.at, txt))
	}
	return strings.Join(parts, " -> ")
}

// startFixtures are the cases kept as fixtures (testdata/sync): the read windows (with the speech
// read with them), the played speech and the cue tap of the first grid windows, the cue times.
var startFixtures = []struct {
	name, file, sub string
	read, grid      int
}{
	{"mentalist_s03e15_start.json", "2076e82d", "opensubtitles/6ac209b2", 8, 4},
	{"mentalist_s03e22_start.json", "7ea562c1", "turkcealtyazi/9e158ce5", 11, 5},
	{"mentalist_s03e17_lag.json", "6f1d72be", "turkcealtyazi/5368ef84", 10, 13},
}

// TestSnapshotWriteStartFixtures writes startFixtures into NFX_FIXTURE_OUT.
func TestSnapshotWriteStartFixtures(t *testing.T) {
	out := os.Getenv("NFX_FIXTURE_OUT")
	if out == "" {
		t.Skip("NFX_FIXTURE_OUT not set")
	}
	files, subs := loadSnapshot(t)
	for _, c := range startFixtures {
		f, s := findFile(files, c.file), findSub(subs, c.sub)
		if s == nil {
			s = loadSub(t, c.sub)
		}
		if f == nil || s == nil {
			t.Fatalf("%s not in the snapshot", c.name)
		}
		fx := syncFixture{Duration: f.ref.Probe.Duration}
		ws := f.ref.Windows[:min(c.read, len(f.ref.Windows))]
		for _, w := range ws {
			fx.Subs = append(fx.Subs, fixtureWindowOf(w))
		}
		for _, w := range f.ref.Speech {
			if hasWindowAt(ws, w.Start) {
				fx.Speech = append(fx.Speech, fixtureWindowOf(w))
			}
		}
		for _, w := range f.live {
			if w.Start < float64(c.grid)*syncWindowLen {
				fx.Live = append(fx.Live, fixtureLive{Start: w.Start, Bits: packBits(w.Speech), N: len(w.Speech)})
			}
		}
		own := snapOwn(t, f.name)
		for g := range c.grid {
			if o, ok := own[g]; ok {
				w := refWindow{Start: float64(g) * syncWindowLen, Kind: "subs", Speech: unpackBits(o.Bits, o.Frames)}
				for _, i := range o.Starts {
					w.Speech[i] = 2
				}
				fx.Own = append(fx.Own, fixtureWindowOf(w))
			}
		}
		for _, sp := range s.spans {
			fx.Sub = append(fx.Sub, [2]float64{math.Round(sp.start*1000) / 1000, math.Round(sp.end*1000) / 1000})
		}
		b, err := json.Marshal(fx)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(out, c.name), b, 0o644); err != nil {
			t.Fatal(err)
		}
		t.Logf("%s: %d bytes", c.name, len(b))
	}
}

// loadStartFixture is loadFixture with the speech read with the own-track windows.
func loadStartFixture(t *testing.T, name string) (*snapFile, []span) {
	t.Helper()
	f, sub := loadFixture(t, name)
	b, err := os.ReadFile(filepath.Join("testdata", "sync", name))
	if err != nil {
		t.Fatal(err)
	}
	var fx syncFixture
	if err := json.Unmarshal(b, &fx); err != nil {
		t.Fatal(err)
	}
	for _, w := range fx.Speech {
		f.ref.Speech = append(f.ref.Speech, w.window())
	}
	f.ref.Paired = true
	return f, sub
}

// firstPass is how many of the reference's windows its first pass read (windowStarts, in read
// order; the rest are extra reads); above the byte budget the start windows a pre-timing reads.
func firstPass(r syncRef) int {
	if r.Skipped {
		return len(r.Windows)
	}
	n := 0
	for _, a := range windowStarts(r.Probe.Duration, syncWindowLen, r.windowCap()) {
		if n < len(r.Windows) && math.Abs(r.Windows[n].Start-a) < 1 {
			n++
		}
	}
	if n == 0 {
		return len(r.Windows)
	}
	return n
}

// quickGain is a loose first test of a subtitle on the file (its read windows and the speech read
// with them, else the first 10 played minutes): its gain over chance at the common rates.
func quickGain(f *snapFile, sub []span) float64 {
	var ws []refWindow
	if !f.ref.Skipped && len(f.ref.Windows) >= 4 {
		ws = append(append(ws, f.ref.Windows...), f.ref.Speech...)
	} else {
		for _, w := range f.live {
			if w.Start < 600 {
				ws = append(ws, w)
			}
		}
	}
	fit, ok := alignWith(clampSpans(sub, f.ref.Probe.Duration), prepare(ws), []float64{1, 25 / 23.976, 23.976 / 25}, syncSplitPenalty, -1)
	if !ok {
		return 0
	}
	return fit.Score - fit.Base
}

// aboutLength reports a subtitle whose last line lies within the last fifth of the file.
func aboutLength(sub []span, duration float64) bool {
	if len(sub) < 100 || duration <= 0 {
		return false
	}
	last := sub[len(sub)-1].end
	return last > 0.8*duration && last < duration+60
}

// backRow is the played audio's backing of a pre-timed fit at h seconds heard (playBacking) and
// whether the fit is right there (90 % of at least 3 cues heard within 0.5 s).
type backRow struct {
	kind, file     string
	h              float64
	at, peak, gain float64
	right, scored  bool
}

var backHeard = []float64{20, 30, 45, 60, 90, 120}

func backRows(f *snapFile, sub []span, pre syncFit, truth []float64, kind string) []backRow {
	var out []backRow
	cues := clampSpans(sub, f.ref.Probe.Duration)
	for _, h := range backHeard {
		var ws []refWindow
		for _, w := range f.live {
			if w.Start >= h {
				continue
			}
			n := min(len(w.Speech), int((h-w.Start)*syncRate))
			if n == len(w.Speech) || n >= liveMinRun {
				ws = append(ws, refWindow{Start: w.Start, Kind: "live", Speech: w.Speech[:n]})
			}
		}
		at, peak, gain := playBacking(pre, cues, ws)
		var errs []float64
		for i, s := range sub {
			if T := truth[i]; !math.IsNaN(T) && T >= 0 && T < h {
				errs = append(errs, math.Abs(pre.mapTime(s.start)-T))
			}
		}
		st := statsOf(errs)
		out = append(out, backRow{kind, f.name[:8], h, at, peak, gain, st.n >= 3 && st.w05 >= 0.9, st.n >= 3})
	}
	return out
}

func summarizeBacking(t *testing.T, rows []backRow) {
	for _, peak := range []float64{1, 2, 3, 4} {
		for _, gain := range []float64{0.05, 0.10, syncMinGain} {
			var parts []string
			for _, h := range backHeard {
				rb, wb, rn, wn := 0, 0, 0, 0
				for _, r := range rows {
					if r.h != h || !r.scored {
						continue
					}
					backed := r.at > 0 && r.peak >= peak && r.gain >= gain
					switch {
					case backed && r.right:
						rb++
					case backed:
						wb++
					case r.right:
						rn++
					default:
						wn++
					}
				}
				parts = append(parts, fmt.Sprintf("%3.0fs %2d/%2d right, %d/%d wrong", h, rb, rb+rn, wb, wb+wn))
			}
			t.Logf("backing peak %.0f gain %.2f (backed of): %s", peak, gain, strings.Join(parts, " | "))
		}
	}
	for _, r := range rows {
		if r.scored && !r.right && r.at > 0 && r.peak >= 1 {
			t.Logf("   wrong pre-timed fit with peak >= 1: %s %s at %3.0f s heard: peak %.1f gain %.2f", r.file, r.kind, r.h, r.peak, r.gain)
		}
	}
}
