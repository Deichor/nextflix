package subtitles

import (
	"context"
	"fmt"
	"io"
	"log"
	"math"
	"os"
	"slices"
	"strings"
	"testing"
)

// The start of a viewing of a file above the byte budget (real data, NFX_TESTDATA: the 4K
// Westworld episodes, a muxed stream whose tap hears bigAhead seconds ahead of the picture). Each
// pair is played from its start point (past the intro where the snapshot's played audio starts
// later) and re-fitted every 10 s of heard audio, as while a viewing's subtitle is untimed:
// "before" from the played audio alone, "pre" pre-timed from the speech windows startWindows
// picks (taken from the played audio of the same stretch: the snapshot keeps the finished grid
// windows, so a window starts where that audio does). A fit shows once it fits and is not
// pre-timed, bigPoll after the re-fit (the TV's poll while pending). Truth: the fit over every
// played window. A subtitle of about the file's length that does not fit the whole file is
// another episode's, or this episode's made for another cut (Westworld S01E01's Turkish ones for
// a shorter cut): a fit shown in the replay counts as false unless it puts half of its lines of
// the scored stretch within 0.3 s of a right pair's lines.

const (
	bigAhead = 30.0  // seconds the tap hears ahead of the picture
	bigPoll  = 5.0   // seconds until the TV shows a stored timing
	bigUpto  = 400.0 // seconds of audio replayed after the start
	bigScore = 180.0 // cues within this long after the start are scored
	// bigResume is where a resumed viewing starts (30:00): pre-timed, the start windows lie far
	// from what it plays
	bigResume = 1800.0
)

// bigStarts are the play starts of the viewings in the snapshot (server log 2026-10-18: "live audio
// of cfac5bad:2 from 105s", "… of 1c694e73:2 from 100s").
var bigStarts = map[string]float64{"d950979c": 105, "e697abd9": 100}

func bigStartOf(f *snapFile) float64 {
	if p, ok := bigStarts[f.name[:8]]; ok {
		return p
	}
	if len(f.live) == 0 || f.live[0].Start == 0 {
		return 0
	}
	return f.live[0].Start - 15
}

// startFromLive builds the speech windows at starts from the played audio (grid windows).
func startFromLive(f *snapFile, starts []float64) []refWindow {
	var out []refWindow
	for _, a := range starts {
		frames := make([]byte, int(syncWindowLen*syncRate))
		have := make([]bool, len(frames))
		for _, w := range f.live {
			for i, v := range w.Speech {
				if j := int(math.Round((w.Start-a)*syncRate)) + i; j >= 0 && j < len(frames) {
					frames[j], have[j] = v, true
				}
			}
		}
		first := -1
		for i, h := range have {
			if h {
				first = i
				break
			}
		}
		if first < 0 || len(frames)-first < 30*syncRate {
			continue
		}
		out = append(out, refWindow{Start: a + float64(first)/syncRate, Kind: "audio", Speech: frames[first:]})
	}
	return out
}

type bigStep struct {
	heard float64 // seconds of the file the tap heard up to
	shown syncFit // identityFit: untimed
}

// replayBig replays the start of a viewing from p; pre: pre-timed with the start windows.
func replayBig(t *testing.T, f *snapFile, sub []span, p float64, pre bool) []bigStep {
	svc := NewService(nil, Config{CacheDir: t.TempDir(), TorrServerURL: "http://torrserver", LiveTiming: true})
	y := svc.sync
	y.onGrow = func(*liveFile) {}
	ref := syncRef{Probe: f.ref.Probe, Skipped: true}
	y.probe = func(context.Context, string) (mediaProbe, error) { return ref.Probe, nil }
	inner := cacheProviderSub(t, svc, "big|"+f.name[:8], sub)
	key, _ := syncKey(inner, f.name, 1)
	base, _ := svc.File(context.Background(), inner)
	if pre {
		ref.Windows = startFromLive(f, startWindows(ref.Probe, vttCues(base)))
		ref.Start = true
	}
	lf := y.liveFile(f.name, 1)
	lf.setDuration(ref.Probe.Duration)
	if pre {
		y.decide(context.Background(), inner, f.name, 1, base, ref, true) // before the open: nothing heard
	}
	live := map[int]refWindow{}
	for _, w := range f.live {
		live[int(math.Round(w.Start/syncWindowLen))] = w
	}
	var out []bigStep
	for h := p + 20; h <= p+bigUpto; h += 10 {
		lf.mu.Lock()
		lf.done = map[int]liveWin{}
		lf.taps = 1
		for g, w := range live {
			from := math.Max(p, w.Start)
			n := min(len(w.Speech), int((h-w.Start)*syncRate))
			k := int(math.Round((from - w.Start) * syncRate))
			if n-k < liveMinRun || k > 0 {
				continue // too little heard, or the window was entered mid-way (the snapshot lacks it)
			}
			lf.done[g] = liveWin{Bits: packBits(w.Speech[:n]), Frames: n}
		}
		lf.complete = false
		lf.mu.Unlock()
		y.decide(context.Background(), inner, f.name, 1, base, ref, false)
		st := bigStep{heard: h, shown: identityFit}
		if m := y.meta(key); m != nil && m.fits() && !m.Pre {
			st.shown = m.Fit
		}
		out = append(out, st)
	}
	return out
}

// shownAt is what the TV shows when the picture is at v.
func shownAt(steps []bigStep, v float64) syncFit {
	f := identityFit
	for _, s := range steps {
		if s.heard+bigPoll <= v+bigAhead {
			f = s.shown
		}
	}
	return f
}

type bigReport struct {
	errs      errStats
	untimed   float64 // seconds of dialogue (first cue after the start to the first timed one shown)
	firstShow float64 // picture position of the first timing shown (NaN: none)
}

func evalBig(steps []bigStep, sub []span, truth []float64, p float64) bigReport {
	r := bigReport{firstShow: math.NaN()}
	var errs []float64
	first := math.NaN()
	for i, c := range sub {
		T := truth[i]
		if math.IsNaN(T) || T < p || T >= p+bigScore {
			continue
		}
		if math.IsNaN(first) {
			first = T
		}
		fit := shownAt(steps, T)
		errs = append(errs, math.Abs(fit.mapTime(c.start)-T))
	}
	end := p + bigScore
	for _, s := range steps {
		if !untimed(s.shown) {
			r.firstShow = s.heard + bigPoll - bigAhead
			end = r.firstShow
			break
		}
	}
	if !math.IsNaN(first) {
		r.untimed = math.Max(0, end-first)
	}
	r.errs = statsOf(errs)
	return r
}

func TestSnapshotBigStart(t *testing.T) {
	files, subs := loadSnapshot(t)
	log.SetOutput(io.Discard)
	t.Cleanup(func() { log.SetOutput(os.Stderr) })
	only := os.Getenv("NFX_CASE")
	type agg struct {
		errs          []float64
		untimed       []float64
		pairs, others int
		falseAccepts  int
		otherCut      int // shown, and on this episode's lines (a subtitle of it made for another cut)
	}
	groups := map[string]*agg{"before": {}, "pre": {}, "before resume": {}, "pre resume": {}}
	for _, f := range files {
		if !f.ref.Skipped || len(f.live) < 10 || only != "" && !strings.Contains(f.name, only) {
			continue
		}
		p := bigStartOf(f)
		played := map[int]bool{}
		for _, w := range f.live {
			played[int(math.Round(w.Start/syncWindowLen))] = true
		}
		g0 := int(math.Ceil(p / syncWindowLen))
		if !played[g0] || !played[g0+1] || !played[g0+2] {
			continue
		}
		d := f.ref.Probe.Duration
		type cand struct {
			s     *snapSub
			truth []float64
			how   string
		}
		var cands []cand
		var anchors []float64 // true times of the right pairs' lines in the scored stretch
		for _, s := range subs {
			last := s.spans[len(s.spans)-1].end
			if last < 0.75*d || last > 1.1*d {
				continue
			}
			truth, how := firstTruth(t, f, s.spans)
			cands = append(cands, cand{s, truth, how})
			for _, T := range truth {
				if T >= p && T < p+bigScore {
					anchors = append(anchors, T)
				}
			}
		}
		// thisEpisode: a fit that puts half of the subtitle's lines of the stretch within 0.3 s of
		// a right pair's line is this episode's subtitle made for another cut (no fit over the whole
		// file), not another episode's
		thisEpisode := func(sub []span, fit syncFit) bool {
			n, near := 0, 0
			for _, c := range sub {
				x := fit.mapTime(c.start)
				if x < p || x >= p+bigScore {
					continue
				}
				n++
				if slices.ContainsFunc(anchors, func(a float64) bool { return math.Abs(a-x) <= 0.3 }) {
					near++
				}
			}
			return n > 0 && 2*near >= n
		}
		for _, c := range cands {
			s, truth, how := c.s, c.truth, c.how
			right := truth != nil
			if !right {
				truth = make([]float64, len(s.spans))
				for i := range truth {
					truth[i] = math.NaN()
				}
			}
			var line []string
			for _, mode := range []string{"before", "pre"} {
				steps := replayBig(t, f, s.spans, p, mode == "pre")
				g := groups[mode]
				if !right {
					g.others++
					for _, st := range steps {
						if untimed(st.shown) {
							continue
						}
						what := "other episode"
						if thisEpisode(s.spans, st.shown) {
							what = "this episode, another cut"
							g.otherCut++
						} else {
							g.falseAccepts++
						}
						t.Logf("%s: %s shows %s (%s) at %.0f s heard: %s", what, f.name[:8], s.name, mode, st.heard, segText(st.shown))
						break
					}
					continue
				}
				r := evalBig(steps, s.spans, truth, p)
				g.pairs++
				g.untimed = append(g.untimed, r.untimed)
				for i, c := range s.spans {
					if T := truth[i]; !math.IsNaN(T) && T >= p && T < p+bigScore {
						g.errs = append(g.errs, math.Abs(shownAt(steps, T).mapTime(c.start)-T))
					}
				}
				line = append(line, fmt.Sprintf("%s: first timing shown at %4.0f s, %3.0f s of dialogue untimed, cues %.0f-%.0f s %s", mode,
					r.firstShow, r.untimed, p, p+bigScore, r.errs))
				if os.Getenv("NFX_EVAL") == "verbose" {
					lastTxt := ""
					for _, st := range steps {
						if txt := segText(st.shown); txt != lastTxt {
							t.Logf("      %s heard %4.0f %s", mode, st.heard, txt)
							lastTxt = txt
						}
					}
				}
			}
			// A viewing resumed mid-episode: the start windows lie far from the played audio
			if q := bigResume; right && played[int(q/syncWindowLen)] && played[int(q/syncWindowLen)+1] && played[int(q/syncWindowLen)+2] {
				for _, mode := range []string{"before", "pre"} {
					steps := replayBig(t, f, s.spans, q, mode == "pre")
					r := evalBig(steps, s.spans, truth, q)
					g := groups[mode+" resume"]
					g.pairs++
					g.untimed = append(g.untimed, r.untimed)
					for i, c := range s.spans {
						if T := truth[i]; !math.IsNaN(T) && T >= q && T < q+bigScore {
							g.errs = append(g.errs, math.Abs(shownAt(steps, T).mapTime(c.start)-T))
						}
					}
					line = append(line, fmt.Sprintf("%s resumed at %.0f s: first timing shown at %4.0f s, %3.0f s of dialogue untimed, cues %.0f-%.0f s %s",
						mode, q, r.firstShow, r.untimed, q, q+bigScore, r.errs))
				}
			}
			if right {
				t.Logf("%s from %.0f s %-24s truth %s\n    %s", f.label(), p, s.name, how, strings.Join(line, "\n    "))
			}
		}
	}
	t.Logf("SUMMARY start of files above the byte budget (tap %.0f s ahead, re-fit every 10 s, poll %.0f s; cues of the first %.0f s)",
		bigAhead, bigPoll, bigScore)
	for _, mode := range []string{"before", "pre", "before resume", "pre resume"} {
		g := groups[mode]
		t.Logf("%-13s pairs %2d | cues %s | dialogue untimed: median %3.0f s, max %3.0f s | not fitting the whole file: %d, shown %d on this episode's lines, %d otherwise (false)",
			mode, g.pairs, statsOf(g.errs), quant(g.untimed, 0.5), quant(g.untimed, 1), g.others, g.otherCut, g.falseAccepts)
	}
}
