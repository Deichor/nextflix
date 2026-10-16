package subtitles

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"log"
	"math"
	"os"
	"sort"
	"strings"
	"testing"
	"time"
)

// The first timing of a viewing (real data, NFX_TESTDATA; the pairs the server served, from
// NFX_PAIRS: lines "<file key> <source>/<first 8 of the raw file> ..."). Each pair is played
// from the file's start and re-fitted through decide every step seconds of heard audio, without
// the read windows ("none": while the first read runs, files above the byte budget) and with
// them ("all": read before playback, as the next episode's pre-timing does). Reported: the heard
// second of the first accepted fit, of the first right one (the cues of the first 5 minutes
// within 0.5 s, 90 %), and when the viewer sees it on the TV (split stream, see firstWall).

type firstRun struct {
	accepted, right float64 // heard seconds (-1: not within the replay)
	fit             syncFit // the first accepted fit
	state           string
	errs            errStats  // its cues of the first 5 minutes against the truth
	byHeard         []bool    // right at each step (step i: heard h0 + i*step)
	served          []syncFit // served at each step (identity: untimed)
	h0, step        float64
	took            []time.Duration // each decide
}

// servedAt is the fit served once h seconds were heard.
func (r firstRun) servedAt(h float64) syncFit {
	i := int(math.Floor((h - r.h0) / r.step))
	if i < 0 || len(r.served) == 0 {
		return identityFit
	}
	return r.served[min(i, len(r.served)-1)]
}

const firstUpto = 600.0

// replayFirst re-fits from the start every step seconds of audio (unfinished grid windows count
// from 20 s heard), the read windows by read, and scores each served fit against truth.
func replayFirst(t *testing.T, f *snapFile, sub []span, read string, step float64, truth []float64) firstRun {
	svc := NewService(nil, Config{CacheDir: t.TempDir(), TorrServerURL: "http://torrserver", LiveTiming: true})
	y := svc.sync
	y.onGrow = func(*liveFile) {}
	ref := f.ref
	if read == "none" {
		ref = syncRef{Probe: ref.Probe, Skipped: true}
	}
	y.probe = func(context.Context, string) (mediaProbe, error) { return ref.Probe, nil }
	inner := cacheProviderSub(t, svc, "first|"+f.name[:8], sub)
	key, _ := syncKey(inner, f.name, 1)
	base, _ := svc.File(context.Background(), inner)
	lf := y.liveFile(f.name, 1)
	lf.setDuration(ref.Probe.Duration)
	live := map[int]refWindow{}
	for _, w := range f.live {
		live[int(math.Round(w.Start/syncWindowLen))] = w
	}
	h0 := step
	if read == "all" {
		h0 = 0 // pre-timed: the read windows alone, before playback
	}
	run := firstRun{accepted: -1, right: -1, h0: h0, step: step}
	for h := h0; h <= firstUpto; h += step {
		lf.mu.Lock()
		lf.done = map[int]liveWin{}
		for g := 0; float64(g+1)*syncWindowLen <= h; g++ {
			if w, ok := live[g]; ok {
				lf.done[g] = liveWin{Bits: packBits(w.Speech), Frames: len(w.Speech)}
			}
		}
		g := int(h / syncWindowLen)
		if rest := h - float64(g)*syncWindowLen; rest*syncRate >= liveMinRun {
			if w, ok := live[g]; ok {
				n := min(int(rest*syncRate), len(w.Speech))
				lf.done[g] = liveWin{Bits: packBits(w.Speech[:n]), Frames: n}
			}
		}
		lf.complete = false
		lf.mu.Unlock()
		start := time.Now()
		y.decide(context.Background(), inner, f.name, 1, base, ref, false)
		run.took = append(run.took, time.Since(start))
		served, state := identityFit, "none"
		if m := y.meta(key); m != nil {
			state = m.State
			if m.fits() {
				served = m.Fit
			}
		}
		errs := statsOf(timingErrors(served, sub, truth, func(x float64) bool { return x < 300 }))
		ok := errs.n > 0 && errs.w05 >= 0.9
		run.byHeard = append(run.byHeard, ok)
		run.served = append(run.served, served)
		if run.accepted < 0 && (state == syncStateSynced || state == syncStateUnchanged) {
			run.accepted, run.fit, run.state, run.errs = h, served, state, errs
		}
		if run.right < 0 && ok {
			run.right = h
		}
	}
	return run
}

// firstTruth is the true file time of every cue: the fit from every window (read and live; the
// own-track windows alone often miss the first minutes, whose offset differs on HDTV subtitles of
// web releases).
func firstTruth(t *testing.T, f *snapFile, sub []span) ([]float64, string) {
	svc := NewService(nil, Config{CacheDir: t.TempDir(), TorrServerURL: "http://torrserver", LiveTiming: true})
	y := svc.sync
	y.onGrow = func(*liveFile) {}
	y.probe = func(context.Context, string) (mediaProbe, error) { return f.ref.Probe, nil }
	inner := cacheProviderSub(t, svc, "truth|"+f.name[:8], sub)
	key, _ := syncKey(inner, f.name, 1)
	base, _ := svc.File(context.Background(), inner)
	lf := y.liveFile(f.name, 1)
	lf.setDuration(f.ref.Probe.Duration)
	lf.mu.Lock()
	for _, w := range f.live {
		lf.done[int(math.Round(w.Start/syncWindowLen))] = liveWin{Bits: packBits(w.Speech), Frames: len(w.Speech)}
	}
	lf.mu.Unlock()
	y.decide(context.Background(), inner, f.name, 1, base, f.ref, false)
	m := y.meta(key)
	if m == nil || !m.fits() {
		return nil, ""
	}
	out := make([]float64, len(sub))
	for i, c := range sub {
		out[i] = m.Fit.mapTime(c.start)
	}
	return out, "fit " + segText(m.Fit)
}

// tapHeard is how much audio the speech tap has heard t seconds after a split stream opened on
// TorBox: about 15x real time until it is ~390 s ahead (server log 2026-10-16: "past its start
// after 11s: 153 s ahead", "after 3s: 41 s ahead"; the TV holds ~6 min of audio), then 1x.
func tapHeard(t float64) float64 {
	if t <= 26 {
		return 15 * t
	}
	return 390 + (t - 26)
}

// firstWall is when the TV shows the first right timing (seconds after the open): re-fits at
// refits(k), polls at polls(k), the timing right for heard >= h (byHeard at step).
func firstWall(byHeard []bool, step, h0 float64, refits, polls func(k int) float64) float64 {
	rightAt := func(h float64) bool {
		i := int((h - h0) / step)
		if i < 0 {
			return false
		}
		if i >= len(byHeard) {
			i = len(byHeard) - 1
		}
		return byHeard[i]
	}
	for k := 0; k < 400; k++ {
		at := refits(k)
		if !rightAt(tapHeard(at)) {
			continue
		}
		for j := 0; j < 400; j++ {
			if p := polls(j); p >= at {
				return p
			}
		}
	}
	return -1
}

// firstView models the first minutes of a viewing opened at 0 on the TV (split stream on
// TorBox, picture 2 s behind the open, the subtitle first asked 5 s after it): the server
// re-fits from firstRefit on, every pendingGap seconds while nothing fits (15 s once something
// does), with tapHeard of audio and the read windows from readAt on (0: pre-timed, read before
// the open); the TV asks again pendingPoll seconds after an untimed answer (each waiting 2.5 s
// for the running timing), 15 s after a timed one.
type firstView struct {
	firstRefit, pendingGap, pendingPoll, readAt float64
	// confirm: a pre-timed fit is served once a re-fit with live windows backs it (else at once)
	confirm bool
}

type stamped struct {
	at  float64
	fit syncFit
}

// untimed: the raw cues (identityFit; a real fit has a score)
func untimed(f syncFit) bool { return f.Score == 0 && f.Base == 0 }

// watched returns the error of every cue of the first 5 minutes (true time 6-300 s) as the TV
// shows it, and when (seconds after the open) the TV first shows a timing that puts 90 % of them
// within 0.5 s (-1: not within 400 s).
func (v firstView) watched(none, all firstRun, sub []span, truth []float64) ([]float64, float64) {
	var server []stamped
	if v.readAt == 0 && !v.confirm {
		server = append(server, stamped{0, all.servedAt(0)})
	}
	for t := v.firstRefit; t < 400; {
		r := none
		if t >= v.readAt {
			r = all
		}
		f := r.servedAt(tapHeard(t))
		server = append(server, stamped{t, f})
		if untimed(f) {
			t += v.pendingGap
		} else {
			t += 15
		}
	}
	storedAt := func(t float64) syncFit {
		f := identityFit
		for _, s := range server {
			if s.at <= t {
				f = s.fit
			}
		}
		return f
	}
	var client []stamped
	t := 5.0
	for t < 400 {
		f := storedAt(t)
		if untimed(f) && v.readAt > 0 {
			t += 2.5 // the request waits for the running timing
			f = storedAt(t)
		}
		client = append(client, stamped{t, f})
		if untimed(f) {
			t += v.pendingPoll
		} else {
			t += 15
		}
	}
	var errs []float64
	for i, c := range sub {
		T := truth[i]
		if math.IsNaN(T) || T < 6 || T >= 300 {
			continue
		}
		w := T + 2
		f := syncFit{}
		for _, s := range client {
			if s.at <= w {
				f = s.fit
			}
		}
		if len(f.Segments) == 0 {
			continue // no answer yet
		}
		errs = append(errs, math.Abs(f.mapTime(c.start)-T))
	}
	rightAt := -1.0
	for _, s := range client {
		if st := statsOf(timingErrors(s.fit, sub, truth, func(x float64) bool { return x >= 6 && x < 300 })); st.n > 0 && st.w05 >= 0.9 {
			rightAt = s.at
			break
		}
	}
	return errs, rightAt
}

func TestSnapshotFirstFit(t *testing.T) {
	files, subs := loadSnapshot(t)
	path := os.Getenv("NFX_PAIRS")
	if path == "" {
		t.Skip("NFX_PAIRS not set")
	}
	log.SetOutput(io.Discard) // thousands of re-fits
	t.Cleanup(func() { log.SetOutput(os.Stderr) })
	fh, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer fh.Close()
	type pair struct{ file, sub, note string }
	var pairs []pair
	sc := bufio.NewScanner(fh)
	for sc.Scan() {
		p := strings.Fields(sc.Text())
		if len(p) >= 2 {
			pairs = append(pairs, pair{p[0], p[1], strings.Join(p[2:], " ")})
		}
	}
	type agg struct {
		runs, accepted, right, wrongFirst int
		at, rightAt                       []float64
		wallOld, wallNew                  []float64
		errs                              []float64
		took                              []float64 // ms per decide
	}
	groups := map[string]*agg{}
	seen := map[string]bool{}
	for _, p := range pairs {
		f, s := findFile(files, p.file), findSub(subs, p.sub)
		if f == nil || s == nil || f.name != p.file || seen[p.file+p.sub] {
			continue
		}
		if c := os.Getenv("NFX_CASE"); c != "" && !strings.Contains(p.file+" "+p.sub, c) {
			continue
		}
		seen[p.file+p.sub] = true
		played := map[int]bool{}
		for _, w := range f.live {
			played[int(math.Round(w.Start/syncWindowLen))] = true
		}
		if !played[0] || !played[1] || !played[2] || !played[3] {
			t.Logf("%s %s: not played from the start", f.name[:8], p.sub)
			continue
		}
		truth, how := firstTruth(t, f, s.spans)
		if truth == nil {
			t.Logf("%s %s: no truth (does not fit the whole file)", f.name[:8], p.sub)
			continue
		}
		t.Logf("%s %-24s %s | truth %s", f.label(), p.sub, p.note, how)
		runs := map[string]firstRun{}
		for _, read := range []string{"none", "all"} {
			if read == "all" && len(f.ref.Windows) == 0 {
				continue
			}
			for _, step := range []float64{30, 10} {
				r := replayFirst(t, f, s.spans, read, step, truth)
				name := fmt.Sprintf("read=%s step=%.0f", read, step)
				runs[name] = r
				if os.Getenv("NFX_EVAL") == "verbose" {
					last := ""
					for i, fit := range r.served {
						if txt := segText(fit); txt != last || i == 0 {
							t.Logf("      %s heard %4.0f right=%-5v %s", name, r.h0+float64(i)*step, r.byHeard[i], txt)
							last = txt
						}
					}
				}
				g := groups[name]
				if g == nil {
					g = &agg{}
					groups[name] = g
				}
				g.runs++
				for _, d := range r.took {
					g.took = append(g.took, float64(d.Microseconds())/1000)
				}
				h0 := step
				if read == "all" {
					h0 = 0
				}
				// Old cadence: re-fits 3 s after the first 30 s of audio, then every 15 s; the
				// subtitle is first served at 5 s (its list), so the first re-fit that sees it is
				// at 20 s; polls 15 s apart, each waiting 2.5 s while the timing job runs.
				old := firstWall(r.byHeard, step, h0, func(k int) float64 { return 20 + 15*float64(k) },
					func(k int) float64 { return 7.5 + 17.5*float64(k) })
				// New: a re-fit when the subtitle is first served (3 s debounce), then every 5 s
				// while it is pending; polls every 5 s while pending.
				nw := firstWall(r.byHeard, step, h0, func(k int) float64 { return 8 + 5*float64(k) },
					func(k int) float64 { return 7.5 + 7.5*float64(k) })
				if read == "all" && len(r.byHeard) > 0 && r.byHeard[0] {
					old, nw = 5, 5 // timed before the open: the first answer has it
				}
				t.Logf("   %-18s first fit at %4.0f s heard (%-9s %s; first 5 min %s) | right from %4.0f s | TV sees it at %3.0f s (old cadence %3.0f s)",
					name, r.accepted, r.state, segText(r.fit), r.errs, r.right, nw, old)
				if r.accepted >= 0 {
					g.accepted++
					g.at = append(g.at, r.accepted)
					g.errs = append(g.errs, r.errs.median)
					if r.errs.w05 < 0.9 {
						g.wrongFirst++
					}
				}
				if r.right >= 0 {
					g.right++
					g.rightAt = append(g.rightAt, r.right)
				}
				if old >= 0 {
					g.wallOld = append(g.wallOld, old)
				}
				if nw >= 0 {
					g.wallNew = append(g.wallNew, nw)
				}
			}
		}
		// What the TV shows in the first 5 minutes: today (re-fits 15 s apart, the first one
		// that sees the subtitle at 20 s, polls 15 s apart, the read windows in 45 s after the
		// open: 20 s delay and the reads), re-fits and polls every 5 s while untimed, and that
		// with the next episode pre-timed
		allOf := func(step string) (firstRun, float64) {
			if r, ok := runs["read=all step="+step]; ok {
				return r, 45
			}
			return runs["read=none step="+step], 1e9 // above the byte budget: no read
		}
		all30, readAt := allOf("30")
		all10, _ := allOf("10")
		views := []struct {
			name      string
			v         firstView
			none, all firstRun
		}{
			{"today", firstView{20, 15, 15, readAt, false}, runs["read=none step=30"], all30},
			{"faster", firstView{6, 5, 5, readAt, false}, runs["read=none step=10"], all10},
			{"pre-timed", firstView{6, 5, 5, 0, false}, runs["read=none step=10"], all10},
			{"pre-timed+confirm", firstView{6, 5, 5, 0, true}, runs["read=none step=10"], all10},
		}
		var line []string
		for _, vw := range views {
			v := vw.v
			if strings.HasPrefix(vw.name, "pre-timed") && readAt > 1e8 {
				v = firstView{6, 5, 5, readAt, false} // nothing to read before
			}
			errs, rightAt := v.watched(vw.none, vw.all, s.spans, truth)
			g := groups["watched "+vw.name]
			if g == nil {
				g = &agg{}
				groups["watched "+vw.name] = g
			}
			g.runs++
			g.errs = append(g.errs, errs...)
			if rightAt >= 0 {
				g.right++
				g.rightAt = append(g.rightAt, rightAt)
			}
			st := statsOf(errs)
			line = append(line, fmt.Sprintf("%s %3.0f%% ≤0.5 s med %.2f right at %3.0f s", vw.name, 100*st.w05, st.median, rightAt))
		}
		t.Logf("   watched (first 5 min as shown): %s", strings.Join(line, " | "))
	}

	// Other episodes' subtitles in the first minutes (known from the episode sweep): accepted
	// re-fits are false
	other := map[string][]string{
		"679e10d8": {"turkcealtyazi/0fb09db5", "turkcealtyazi/be95042a", "embedded/764c0c56", "turkcealtyazi/7e8acdee",
			"subf2m/f410b17f", "turkcealtyazi/203844b3", "subf2m/1f247d22", "turkcealtyazi/4bee7ec7", "turkcealtyazi/5fcbb111",
			"subf2m/f7defb5d", "turkcealtyazi/7a324f0e"},
		"c162f124": {"turkcealtyazi/0fb09db5", "turkcealtyazi/a6a6a3f2", "turkcealtyazi/b21eacc1", "turkcealtyazi/bfd83592",
			"subf2m/3fcc7d23", "embedded/764c0c56", "turkcealtyazi/7e8acdee", "subf2m/f410b17f", "turkcealtyazi/203844b3"},
		"2e80f2f6": {"turkcealtyazi/0fb09db5", "turkcealtyazi/be95042a", "subf2m/3fcc7d23", "turkcealtyazi/a6a6a3f2",
			"embedded/764c0c56", "turkcealtyazi/7a324f0e", "turkcealtyazi/4bee7ec7"},
	}
	for _, step := range []float64{30, 10} {
		if os.Getenv("NFX_CASE") != "" {
			break
		}
		pairsN, acceptedN, refits, acceptedRefits := 0, 0, 0, 0
		for fp, names := range other {
			f := findFile(files, fp)
			if f == nil {
				continue
			}
			for _, name := range names {
				s := findSub(subs, name)
				if s == nil {
					continue
				}
				pairsN++
				nan := make([]float64, len(s.spans))
				for i := range nan {
					nan[i] = math.NaN()
				}
				r := replayFirst(t, f, s.spans, "none", step, nan)
				refits += len(r.byHeard)
				if r.accepted >= 0 {
					acceptedN++
					t.Logf("other episode: %s accepts %s at %.0f s (step %.0f)", fp, name, r.accepted, step)
				}
				_ = acceptedRefits
			}
		}
		t.Logf("other episodes, step %.0f s: %d/%d pairs accepted (%d re-fits each up to %.0f s)", step, acceptedN, pairsN, refits, firstUpto)
	}

	names := make([]string, 0, len(groups))
	for n := range groups {
		names = append(names, n)
	}
	sort.Strings(names)
	t.Logf("SUMMARY first timing (played from 0; heard seconds; TV: split stream on TorBox, seconds after the open)")
	for _, n := range names {
		g := groups[n]
		if strings.HasPrefix(n, "watched") {
			t.Logf("%-26s pairs %2d | cues of the first 5 minutes as the TV shows them: %s | right timing shown %d, median at %3.0f s, p90 %3.0f s",
				n, g.runs, statsOf(g.errs), g.right, quant(g.rightAt, 0.5), quant(g.rightAt, 0.9))
			continue
		}
		t.Logf("%-18s decide: %d calls, median %.1f ms, p95 %.1f ms, max %.1f ms", n, len(g.took), quant(g.took, 0.5),
			quant(g.took, 0.95), quant(g.took, 1))
		t.Logf("%-18s pairs %2d | accepted %2d, median at %4.0f s heard, first fit wrong (first 5 min <90%% within 0.5 s) %d | right %2d, median from %4.0f s | TV sees the right timing: median %3.0f s, p90 %3.0f s (old cadence %3.0f / %3.0f s)",
			n, g.runs, g.accepted, quant(g.at, 0.5), g.wrongFirst, g.right, quant(g.rightAt, 0.5), quant(g.wallNew, 0.5),
			quant(g.wallNew, 0.9), quant(g.wallOld, 0.5), quant(g.wallOld, 0.9))
	}
}

func quant(v []float64, q float64) float64 {
	if len(v) == 0 {
		return -1
	}
	s := append([]float64(nil), v...)
	sort.Float64s(s)
	return s[int(q*float64(len(s)-1))]
}
