package subtitles

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"maps"
	"math"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strconv"
	"strings"
	"testing"
)

// The served subtitle of real viewings that were replaced by another one (real data,
// NFX_TESTDATA; skipped without it): the file's read reference and its live windows (speech and
// the own-track cue tap, synclive) are replayed one grid window a minute through decide with the
// subtitles listed for it, the viewer 90 s behind the audio heard (the TV's split stream). Each
// step reports what is served and how its cues moved against the step before: cues the viewer
// saw already, cues heard but not shown yet, cues ahead of the audio, and cues that went back
// to where they were two or more steps before (a flip: noise). NFX_FB_DEBUG=1 prints every moved
// cue, NFX_FB_CHECK=1 the own-track check at each step.

type fbSub struct{ source, lang, ref string }

type fbCase struct {
	name  string
	hash  string
	index int
	subs  []fbSub // as listed for the file, the served one first
}

var fbCases = []fbCase{
	{"S03E23 NF (pack double episode)", "111e346eb941a4f378c2cba98800adf991dda08d", 1, []fbSub{
		{SourceTurkceAltyazi, "tr", "333899|/sub/333899/the-mentalist.html|3|23|1"},
		{SourceSubf2m, "tr", "the-mentalist-third-season|turkish|3578445|3|23|1"},
		{SourceTurkceAltyazi, "tr", "368804|/sub/368804/the-mentalist.html|3|23|1"},
	}},
	{"S03E20 NF", "9af641c0b868bf616aaaea177521059a36a5736f", 1, []fbSub{
		{SourceTurkceAltyazi, "tr", "333899|/sub/333899/the-mentalist.html|3|20|1"},
		{SourceSubf2m, "tr", "the-mentalist-third-season|turkish|3578445|3|20|1"},
	}},
}

const fbViewerLag = 90.0 // seconds the viewer is behind the audio heard

func copySnapFile(t *testing.T, from, to, kind, key, ext string) bool {
	name := hashKey(key) + ext
	b, err := os.ReadFile(filepath.Join(from, kind, name))
	if err != nil {
		return false
	}
	if err := os.MkdirAll(filepath.Join(to, kind), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(to, kind, name), b, 0o644); err != nil {
		t.Fatal(err)
	}
	return true
}

type fbStep struct {
	minute       int
	state, by    string
	offsets      string
	shown, ahead int // moved cues the viewer saw already, heard but not shown yet
	beyond       int // moved cues past the audio heard (read windows)
	flips        int // of the moved: back where they were two or more steps before
	refined      int // of the moved: the fit did not move them (the line refinement did)
	switched     bool
	maxMove      float64
}

// replayFallback replays one case and returns its steps.
func replayFallback(t *testing.T, c fbCase) []fbStep {
	dir := snapshotDir(t)
	tmp := t.TempDir()
	fk := syncFileKey(c.hash, c.index)
	if !copySnapFile(t, dir, tmp, "syncref", fk, ".json") || !copySnapFile(t, dir, tmp, "synclive", fk, ".json") {
		t.Skipf("%s: no reference or live windows in the snapshot", c.name)
	}
	for _, s := range c.subs {
		if !copySnapFile(t, dir, tmp, s.source, s.source+"|"+s.ref, ".vtt") {
			t.Skipf("%s: subtitle %s/%s not in the snapshot", c.name, s.source, s.ref)
		}
	}
	b, _ := os.ReadFile(filepath.Join(tmp, "syncref", hashKey(fk)+".json"))
	var ref syncRef
	if err := json.Unmarshal(b, &ref); err != nil {
		t.Fatal(err)
	}
	svc := replayService(tmp, ref)
	y := svc.sync
	var list []Sub
	for _, s := range c.subs {
		id := encodeID(s.source, s.lang, s.ref)
		list = append(list, Sub{ID: wrapSyncID(id, c.hash, c.index), Source: s.source, Language: s.lang})
	}
	y.mu.Lock()
	y.lists[fk] = list
	y.mu.Unlock()
	inner := encodeID(c.subs[0].source, c.subs[0].lang, c.subs[0].ref)
	return replaySteps(t, svc, c.hash, c.index, inner, ref)
}

// replayService is a service on the cache dir that reads nothing and re-fits only when asked.
func replayService(dir string, ref syncRef) *Service {
	svc := NewService(nil, Config{CacheDir: dir, TorrServerURL: "http://torrserver", LiveTiming: true})
	y := svc.sync
	y.onGrow = func(*liveFile) {}
	y.refitGap = 0
	y.probe = func(context.Context, string) (mediaProbe, error) { return ref.Probe, nil }
	y.read = func(context.Context, string, mediaProbe, float64, float64) (refWindow, error) {
		return refWindow{}, errors.New("no reads in a replay")
	}
	return svc
}

// replaySteps plays the live windows the file's liveFile holds (as loaded from synclive) one grid
// window at a time, in order, through decide of inner, the viewer fbViewerLag behind them.
func replaySteps(t *testing.T, svc *Service, hash string, index int, inner string, ref syncRef) []fbStep {
	y := svc.sync
	key, _ := syncKey(inner, hash, index)
	base, err := svc.File(context.Background(), inner)
	if err != nil {
		t.Fatal(err)
	}
	lf := y.liveFile(hash, index)
	lf.setDuration(ref.Probe.Duration)
	lf.mu.Lock()
	allDone, allOwn := lf.done, lf.ownDone
	lf.mu.Unlock()
	last := 0
	for w := range allDone {
		last = max(last, w)
	}
	for w := range allOwn {
		last = max(last, w)
	}
	var steps []fbStep
	var prev, prevFit []liveCue
	prevBy := ""
	history := map[int][]float64{} // cue index -> starts served, same subtitle
	for g := 0; g <= last; g++ {
		heard := float64(g+1) * syncWindowLen
		lf.mu.Lock()
		lf.done, lf.ownDone = map[int]liveWin{}, map[int]liveWin{}
		for w, d := range allDone {
			if w <= g {
				lf.done[w] = d
			}
		}
		for w, d := range allOwn {
			if w <= g {
				lf.ownDone[w] = d
			}
		}
		lf.complete = false
		lf.checkComplete()
		lf.mu.Unlock()
		viewer := math.Max(heard-fbViewerLag, 1)
		y.noteServed(key, inner, hash, index, viewer)
		y.decide(context.Background(), inner, hash, index, base, ref, false)
		m := y.meta(key)
		st := fbStep{minute: g + 1, state: "none"}
		if os.Getenv("NFX_FB_CHECK") != "" {
			c := lf.ownChecked()
			t.Logf("   min %d check %s tight=%v pairs=%d offsets %v local %d", g+1, c.State, c.Tight, c.Pairs, c.Fit.Segments, len(c.local))
		}
		served := y.servedCues(key, base, m)
		servedBase, fit := base, identityFit
		if m != nil {
			st.state = m.State
			if m.Live {
				st.state += " (provisional)"
			}
			fit = m.Fit
			if m.State == syncStateReplaced {
				st.by = shortSubID(m.ReplacedBy)
				altKey, _ := syncKey(m.ReplacedBy, hash, index)
				servedBase, _ = svc.File(context.Background(), m.ReplacedBy)
				if am := y.meta(altKey); am != nil {
					fit = am.Fit
				}
			}
			if untimedMeta(m) {
				fit = identityFit
			}
			st.offsets = segText(fit)
		}
		fitOnly := applyFit(vttCues(servedBase), fit)
		if prev != nil && (st.by != prevBy || len(served) != len(prev)) {
			st.switched = true
			history = map[int][]float64{}
		} else if prev != nil {
			for i, cue := range served {
				d := cue.start - prev[i].start
				if math.Abs(d) < 0.1 {
					continue
				}
				switch {
				case prev[i].start < viewer:
					st.shown++
				case prev[i].start < heard:
					st.ahead++
				default:
					st.beyond++
				}
				if math.Abs(d) > math.Abs(st.maxMove) {
					st.maxMove = d
				}
				h := history[i]
				flip := false
				for k := 0; k+1 < len(h); k++ {
					if math.Abs(h[k]-cue.start) < 0.05 {
						st.flips++
						flip = true
						break
					}
				}
				if os.Getenv("NFX_FB_DEBUG") != "" {
					lf.mu.Lock()
					cover := coverOf(subsWindowsSorted(ref.Windows, lf.ownWindows()))
					lf.mu.Unlock()
					edge := math.Inf(1)
					for _, sp := range cover {
						if prev[i].start >= sp.start-1 && prev[i].start <= sp.end+1 {
							edge = math.Min(prev[i].start-sp.start, sp.end-prev[i].start)
						}
					}
					off := 0.0
					if len(fit.Segments) > 0 {
						off = offsetAt(fit.Segments, cue.start)
					}
					t.Logf("   min %d (offset %+.3f) cue %d %.2f -> %.2f (%+.2f) flip=%v edge %.0fs history %v", g+1, off, i,
						prev[i].start, cue.start, d, flip, edge, h)
				}
				if len(fitOnly) == len(served) && i < len(prevFit) && math.Abs(fitOnly[i].start-prevFit[i].start) < 0.05 {
					st.refined++
				}
			}
		}
		for i, cue := range served {
			if h := history[i]; len(h) == 0 || math.Abs(h[len(h)-1]-cue.start) >= 0.05 {
				history[i] = append(h, cue.start)
			}
		}
		prev, prevBy, prevFit = served, st.by, fitOnly
		steps = append(steps, st)
	}
	return steps
}

func subsWindowsSorted(read, live []refWindow) []refWindow {
	ws := subsWindowsOf(read, live)
	sort.Slice(ws, func(i, j int) bool { return ws[i].Start < ws[j].Start })
	return ws
}

func (s fbStep) String() string {
	by := ""
	if s.by != "" {
		by = " by " + s.by
	}
	sw := ""
	if s.switched {
		sw = " SWITCHED"
	}
	return fmt.Sprintf("min %2d %s%s [%s]%s moved: shown %d, ahead %d, beyond %d (flips %d, refined %d, max %+.2fs)",
		s.minute, s.state, by, s.offsets, sw, s.shown, s.ahead, s.beyond, s.flips, s.refined, s.maxMove)
}

// TestSnapshotWriteRefineFixture writes the S03E23 case (its read windows and their speech, the
// live speech and cue tap windows, the subf2m Turkish subtitle's cue times; no text) into
// NFX_FIXTURE_OUT as mentalist_s03e23.json.
func TestSnapshotWriteRefineFixture(t *testing.T) {
	out := os.Getenv("NFX_FIXTURE_OUT")
	if out == "" {
		t.Skip("NFX_FIXTURE_OUT not set")
	}
	c := fbCases[0]
	dir := snapshotDir(t)
	fk := syncFileKey(c.hash, c.index)
	var ref syncRef
	var live liveSaved
	for kind, v := range map[string]any{"syncref": &ref, "synclive": &live} {
		b, err := os.ReadFile(filepath.Join(dir, kind, hashKey(fk)+".json"))
		if err != nil {
			t.Fatal(err)
		}
		if err := json.Unmarshal(b, v); err != nil {
			t.Fatal(err)
		}
	}
	s := c.subs[1]
	b, err := os.ReadFile(filepath.Join(dir, s.source, hashKey(s.source+"|"+s.ref)+".vtt"))
	if err != nil {
		t.Fatal(err)
	}
	fx := syncFixture{Duration: ref.Probe.Duration}
	for _, w := range ref.Windows {
		fx.Subs = append(fx.Subs, fixtureWindowOf(w))
	}
	for _, w := range ref.Speech {
		fx.Speech = append(fx.Speech, fixtureWindowOf(w))
	}
	for _, g := range sortedKeys(live.Windows) {
		d := live.Windows[g]
		fx.Live = append(fx.Live, fixtureLive{Start: float64(g) * syncWindowLen, Bits: d.Bits, N: d.Frames})
	}
	for _, g := range sortedKeys(live.Own) {
		d := live.Own[g]
		w := refWindow{Start: float64(g) * syncWindowLen, Kind: "subs", Speech: unpackBits(d.Bits, d.Frames)}
		for _, i := range d.Starts {
			if i >= 0 && i < len(w.Speech) {
				w.Speech[i] = 2
			}
		}
		fx.Own = append(fx.Own, fixtureWindowOf(w))
	}
	for _, cue := range vttCues(b) {
		fx.Sub = append(fx.Sub, [2]float64{math.Round(cue.start*1000) / 1000, math.Round(cue.end*1000) / 1000})
	}
	jb, err := json.Marshal(fx)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(out, "mentalist_s03e23.json"), jb, 0o644); err != nil {
		t.Fatal(err)
	}
	t.Logf("%d bytes, %d read windows, %d live, %d own, %d cues", len(jb), len(fx.Subs), len(fx.Live), len(fx.Own), len(fx.Sub))
}

func sortedKeys(m map[int]liveWin) []int { return slices.Sorted(maps.Keys(m)) }

// TestSnapshotRefitStability replays every file of the snapshot with live windows (synclive: the
// played speech, the cue tap's own-track windows and the own-track check, as kept) against the
// subtitles of its episode (3 own-track windows agree on their cues, read or tapped; a file
// without own-track windows: the subtitles its windows fit) through replaySteps, served by
// themselves, and sums the cue moves between steps. Flips (a cue back where it was two or more
// steps before) are noise; moves of cues the viewer saw already are visible only after a seek
// back. Each pair is replayed with keepOffsets off and on. NFX_PAIRS (lines "<file key>
// <source>/<first 8 of the raw file> ...": the pairs the server served) takes those pairs of
// NFX_TESTDATA instead; NFX_STAB_MORE names more snapshot data dirs (comma-separated, paired as
// above); NFX_STAB_SUBS caps the subtitles per file (default 1); NFX_STAB_ONLY ("<file8>
// <subtitle>") replays that pair alone, step by step.
func TestSnapshotRefitStability(t *testing.T) {
	type tally struct{ pairs, steps, shown, ahead, beyond, flips, flipped int }
	var tallies [2]tally // keepOffsets off, on
	saved := keepOffsets
	t.Cleanup(func() { keepOffsets = saved })
	perFile := 1
	if v, err := strconv.Atoi(os.Getenv("NFX_STAB_SUBS")); err == nil && v > 0 {
		perFile = v
	}
	dirs := []string{os.Getenv("NFX_TESTDATA")}
	for _, d := range strings.Split(os.Getenv("NFX_STAB_MORE"), ",") {
		if d != "" {
			dirs = append(dirs, d)
		}
	}
	for di, data := range dirs {
		t.Setenv("NFX_TESTDATA", data)
		dir := snapshotDir(t)
		files, subs := loadSnapshot(t)
		var served map[string]map[string]bool // file -> subtitles served for it
		if p := os.Getenv("NFX_PAIRS"); p != "" && di == 0 {
			b, err := os.ReadFile(p)
			if err != nil {
				t.Fatal(err)
			}
			served = map[string]map[string]bool{}
			for _, l := range strings.Split(string(b), "\n") {
				if fs := strings.Fields(l); len(fs) >= 2 {
					if served[fs[0]] == nil {
						served[fs[0]] = map[string]bool{}
					}
					served[fs[0]][fs[1]] = true
				}
			}
		}
		for _, f := range files {
			raw, err := os.ReadFile(filepath.Join(dir, "synclive", f.name+".json"))
			if err != nil {
				continue
			}
			var saved liveSaved
			if json.Unmarshal(raw, &saved) != nil || len(saved.Windows)+len(saved.Own) < 6 {
				continue
			}
			dur := f.ref.Probe.Duration
			withOwn := f.ref
			withOwn.Windows = append([]refWindow(nil), f.ref.Windows...)
			for g, d := range saved.Own {
				w := refWindow{Start: float64(g) * syncWindowLen, Kind: "subs", Speech: unpackBits(d.Bits, d.Frames)}
				for _, i := range d.Starts {
					if i >= 0 && i < len(w.Speech) {
						w.Speech[i] = 2
					}
				}
				withOwn.Windows = append(withOwn.Windows, w)
			}
			sort.Slice(withOwn.Windows, func(i, j int) bool { return withOwn.Windows[i].Start < withOwn.Windows[j].Start })
			var live []refWindow
			for g, d := range saved.Windows {
				live = append(live, refWindow{Start: float64(g) * syncWindowLen, Kind: "live", Speech: unpackBits(d.Bits, d.Frames)})
			}
			n := 0
			only := os.Getenv("NFX_STAB_ONLY") // "<file8> <subtitle>": that pair, step by step
			for _, s := range subs {
				last := s.spans[len(s.spans)-1].end
				switch {
				case only != "" && only != f.name[:8]+" "+s.name:
					continue
				case served != nil:
					if !served[f.name][s.name] {
						continue
					}
				case n == perFile || last < 0.75*dur || last > 1.1*dur:
					continue
				case subsWindows(withOwn) >= 3:
					if agreeingWindows(s.spans, withOwn) < 3 {
						continue
					}
				default:
					if fit, ok := alignLive(clampSpans(s.spans, dur), mergeWindows(f.ref.Windows, live), syncScales); !ok ||
						!fit.good() || !lengthFits(s.spans, fit, dur) {
						continue
					}
				}
				n++
				var line [2]string
				for k, keep := range []bool{false, true} {
					keepOffsets = keep
					tmp := t.TempDir()
					if err := os.MkdirAll(filepath.Join(tmp, "synclive"), 0o755); err != nil {
						t.Fatal(err)
					}
					if err := os.WriteFile(filepath.Join(tmp, "synclive", hashKey(syncFileKey(syncHash, 1))+".json"), raw, 0o644); err != nil {
						t.Fatal(err)
					}
					svc := replayService(tmp, f.ref)
					inner := cacheProviderSub(t, svc, "stab|"+f.name[:8]+"|"+s.name, s.spans)
					st := replaySteps(t, svc, syncHash, 1, inner, f.ref)
					tl := &tallies[k]
					var pf, ps int
					for _, x := range st {
						if only != "" {
							t.Logf("keep=%v %s", keep, x)
						}
						tl.steps++
						tl.shown += x.shown
						tl.ahead += x.ahead
						tl.beyond += x.beyond
						pf += x.flips
						ps += x.shown
					}
					tl.pairs++
					tl.flips += pf
					if pf > 0 {
						tl.flipped++
					}
					end := st[len(st)-1]
					line[k] = fmt.Sprintf("%s [%s]: shown %d, flips %d", end.state, end.offsets, ps, pf)
				}
				t.Logf("%s %-24s %2d windows | off: %s | on: %s", f.label(), s.name, len(saved.Windows), line[0], line[1])
			}
		}
	}
	for k, name := range []string{"kept offsets off", "kept offsets on"} {
		tl := tallies[k]
		t.Logf("SUMMARY stability, %s: %d pairs, %d steps; moved cues: shown %d, ahead %d, beyond %d; flips %d (%d pairs)",
			name, tl.pairs, tl.steps, tl.shown, tl.ahead, tl.beyond, tl.flips, tl.flipped)
	}
}

// TestSnapshotFallbackReplay prints the replay of each case and its sums.
func TestSnapshotFallbackReplay(t *testing.T) {
	flags := log.Flags()
	log.SetFlags(0) // the server's log lines pass the runner's filter of dated lines
	t.Cleanup(func() { log.SetFlags(flags) })
	for _, c := range fbCases {
		t.Run(strings.ReplaceAll(c.name, " ", "_"), func(t *testing.T) {
			steps := replayFallback(t, c)
			var shown, ahead, beyond, flips, refined, switches int
			for _, s := range steps {
				t.Log(s)
				shown += s.shown
				ahead += s.ahead
				beyond += s.beyond
				flips += s.flips
				refined += s.refined
				if s.switched {
					switches++
				}
			}
			t.Logf("%s: %d steps, %d switches; moved cues: shown %d, ahead %d, beyond %d; flips %d, refined %d",
				c.name, len(steps), switches, shown, ahead, beyond, flips, refined)
		})
	}
}
