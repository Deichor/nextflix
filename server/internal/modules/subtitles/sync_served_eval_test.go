package subtitles

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"math"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"testing"
)

// What a viewer is served, on all files of a snapshot (NFX_TESTDATA), split by films (80 minutes
// or longer) and series: the best fitting subtitle of each file with heard audio, and the best
// one with offset steps, are replayed through decide, and the answers are measured against all the speech heard. Run on two commits
// to compare them. NFX_SERVED_SUBS: subtitles per file (default 1). NFX_SERVED_STEP: windows
// between re-fits (0: only the final fit). NFX_SERVED_OUT: a file for one line per pair.
// NFX_SERVED_ONLY: file names starting so. NFX_SERVED_PAIRS: such a file: its pairs instead of
// looking for the best fitting ones (minutes). NFX_SERVED_DUMP: a directory for the final answers;
// NFX_SERVED_CMP: such a directory of an earlier run: the lines moved a second or more since then
// are measured there and here. NFX_SERVED_ALTS: the other subtitles listed for a file are its
// alternatives (replacements counted).
const servedFilmSeconds = 4800

type servedRow struct {
	file, sub, state, offsets string
	dur                       float64
	cues, stacked, inverted   int
	within, chance, median    float64
	n                         int
	good, soft, atChance      int
	shown25, shown50, flips   int
	movedN, movedHits         int // lines moved since NFX_SERVED_CMP: with an onset near, within 0.3 s
	movedWas                  int // the same before
	swaps, backs              int // changes of the subtitle served, back to one served before
}

func servedStacks(cues []liveCue) int {
	n, end := 0, 0.0
	for i, c := range cues {
		if i > 0 && c.start < end-1 {
			n++
		}
		end = math.Max(end, c.end)
	}
	return n
}

// servedInverted counts lines served before a line that comes earlier in the subtitle (lines
// whose text is unique in it).
func servedInverted(raw, cues []liveCue) int {
	count := map[string]int{}
	index := map[string]int{}
	for i, c := range raw {
		count[c.text]++
		index[c.text] = i
	}
	n, top := 0, -1
	for _, c := range cues {
		if count[c.text] != 1 {
			continue
		}
		if i := index[c.text]; i < top {
			n++
		} else {
			top = i
		}
	}
	return n
}

func replayAnswers(t *testing.T, tmp string, raw []byte, f *snapFile, s *snapSub, alts []*snapSub, step int) servedRow {
	dur := f.ref.Probe.Duration
	if err := os.MkdirAll(filepath.Join(tmp, "synclive"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(tmp, "synclive", hashKey(syncFileKey(syncHash, 1))+".json"), raw, 0o644); err != nil {
		t.Fatal(err)
	}
	svc := replayService(tmp, f.ref)
	y := svc.sync
	vtt := renderVTT(s.cues, false)
	ref := "served|" + f.name[:8] + "|" + s.name
	if err := svc.files.write(SourceTurkceAltyazi, SourceTurkceAltyazi+"|"+ref, ".vtt", vtt); err != nil {
		t.Fatal(err)
	}
	inner := encodeID(SourceTurkceAltyazi, "tr", ref)
	list := []Sub{{ID: inner, Source: SourceTurkceAltyazi, Language: "tr"}}
	for _, a := range alts {
		altRef := "served|" + f.name[:8] + "|" + a.name
		if err := svc.files.write(SourceTurkceAltyazi, SourceTurkceAltyazi+"|"+altRef, ".vtt", renderVTT(a.cues, false)); err != nil {
			t.Fatal(err)
		}
		list = append(list, Sub{ID: encodeID(SourceTurkceAltyazi, "tr", altRef), Source: SourceTurkceAltyazi, Language: "tr"})
	}
	y.listed(list, nil, syncHash, 1)
	key, _ := syncKey(inner, syncHash, 1)
	base, err := svc.File(context.Background(), inner)
	if err != nil {
		t.Fatal(err)
	}
	lf := y.liveFile(syncHash, 1)
	lf.setDuration(dur)
	lf.mu.Lock()
	allDone, allOwn := lf.done, lf.ownDone
	lf.mu.Unlock()
	last := 0
	var speech []refWindow
	for w, d := range allDone {
		last = max(last, w)
		speech = append(speech, refWindow{Start: float64(w) * syncWindowLen, Kind: "live", Speech: unpackBits(d.Bits, d.Frames)})
	}
	for w := range allOwn {
		last = max(last, w)
	}
	speech = joined(speech)
	row := servedRow{file: f.name[:8], sub: s.name, dur: dur, state: "none"}
	var prev []liveCue
	history := map[int][]float64{}
	prevBy := inner
	seenBy := map[string]bool{inner: true}
	for g := 0; g <= last; g++ {
		if g != last && (step == 0 || g%step != 0) {
			continue
		}
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
		y.noteServed(key, inner, syncHash, 1, viewer)
		y.decide(context.Background(), inner, syncHash, 1, base, f.ref, false)
		m := y.meta(key)
		served := y.servedCues(key, base, m)
		if by := servedBy(inner, m); by != prevBy {
			row.swaps++
			if seenBy[by] {
				row.backs++
			}
			seenBy[by] = true
			prevBy = by
			prev = nil
			history = map[int][]float64{}
		}
		if step > 0 {
			if os.Getenv("NFX_SERVED_TRACE") != "" {
				r := residualOf(served, speech, math.Max(viewer-diagResidualAround, 0), viewer+diagResidualAround)
				st, off := "none", ""
				if m != nil {
					st, off = m.State, offsetsText(m.Fit)
				}
				t.Logf("%s %s at %.0f: %s [%s] median %+.2f within %.0f%% chance %.0f%% n=%d", f.name[:8], s.name, viewer, st, off, r.median, 100*r.within, 100*r.chance, r.n)
			}
			if r := residualOf(served, speech, math.Max(viewer-diagResidualAround, 0), viewer+diagResidualAround); r.n >= diagMinCues {
				switch {
				case r.within < r.chance+0.10:
					row.atChance++
				case math.Abs(r.median) >= 0.2 || r.within < r.chance+0.20:
					row.soft++
				default:
					row.good++
				}
			}
			if prev != nil && len(prev) == len(served) {
				for i, c := range served {
					d := math.Abs(c.start - prev[i].start)
					if d < 0.1 {
						continue
					}
					if prev[i].start < viewer {
						if d >= 0.25 {
							row.shown25++
						}
						if d >= 0.5 {
							row.shown50++
						}
					}
					for _, h := range history[i][:max(len(history[i])-1, 0)] {
						if math.Abs(h-c.start) < 0.05 {
							row.flips++
							break
						}
					}
				}
			}
			for i, c := range served {
				if h := history[i]; len(h) == 0 || math.Abs(h[len(h)-1]-c.start) >= 0.05 {
					history[i] = append(h, c.start)
				}
			}
			prev = served
		}
		if g == last {
			if m != nil {
				row.state = m.State
				if m.Live {
					row.state += "~"
				}
				row.offsets = strings.ReplaceAll(offsetsText(m.Fit), " ", ",")
			}
			row.cues = len(served)
			row.stacked = servedStacks(served)
			row.inverted = servedInverted(s.cues, served)
			r := residualOf(served, speech, 0, dur)
			row.within, row.chance, row.median, row.n = r.within, r.chance, r.median, r.n
			name := f.name[:8] + "_" + strings.ReplaceAll(s.name, "/", "_") + ".vtt"
			if d := os.Getenv("NFX_SERVED_DUMP"); d != "" {
				if err := os.WriteFile(filepath.Join(d, name), renderVTT(served, false), 0o644); err != nil {
					t.Error(err)
				}
			}
			if d := os.Getenv("NFX_SERVED_CMP"); d != "" {
				if b, err := os.ReadFile(filepath.Join(d, name)); err == nil {
					if was := vttCues(b); len(was) == len(served) {
						var a, c []liveCue
						for i := range served {
							if math.Abs(served[i].start-was[i].start) >= 1 {
								a, c = append(a, was[i]), append(c, served[i])
							}
						}
						ra, rc := residualOf(a, speech, 0, dur), residualOf(c, speech, 0, dur)
						row.movedN = len(c)
						row.movedWas = int(math.Round(ra.within * float64(ra.n)))
						row.movedHits = int(math.Round(rc.within * float64(rc.n)))
					}
				}
			}
		}
	}
	return row
}

func TestSnapshotServed(t *testing.T) {
	dir := snapshotDir(t)
	files, subs := loadSnapshot(t)
	perFile := 1
	if v, err := strconv.Atoi(os.Getenv("NFX_SERVED_SUBS")); err == nil && v > 0 {
		perFile = v
	}
	step, _ := strconv.Atoi(os.Getenv("NFX_SERVED_STEP"))
	only := os.Getenv("NFX_SERVED_ONLY")
	log.SetOutput(io.Discard)
	t.Cleanup(func() { log.SetOutput(os.Stderr) })
	type job struct {
		f    *snapFile
		s    *snapSub
		alts []*snapSub
		raw  []byte
	}
	var jobs []job
	var listed map[string][]string
	if p := os.Getenv("NFX_SERVED_PAIRS"); p != "" {
		b, err := os.ReadFile(p)
		if err != nil {
			t.Fatal(err)
		}
		listed = map[string][]string{}
		for _, l := range strings.Split(string(b), "\n") {
			if fs := strings.Split(l, "\t"); len(fs) >= 2 {
				listed[fs[0]] = append(listed[fs[0]], fs[1])
			}
		}
	}
	for _, f := range files {
		if only != "" && !strings.HasPrefix(f.name, only) {
			continue
		}
		raw, err := os.ReadFile(filepath.Join(dir, "synclive", f.name+".json"))
		if err != nil {
			continue
		}
		var saved liveSaved
		if json.Unmarshal(raw, &saved) != nil || len(saved.Windows) < 6 {
			continue
		}
		if listed != nil {
			var here []*snapSub
			for _, name := range listed[f.name[:8]] {
				for _, s := range subs {
					if s.name == name {
						here = append(here, s)
					}
				}
			}
			for _, s := range here {
				j := job{f: f, s: s, raw: raw}
				if os.Getenv("NFX_SERVED_ALTS") != "" {
					for _, a := range here {
						if a != s {
							j.alts = append(j.alts, a)
						}
					}
				}
				jobs = append(jobs, j)
			}
			continue
		}
		dur := f.ref.Probe.Duration
		ws := prepare(mergeWindows(f.ref.Windows, f.live))
		type cand struct {
			s     *snapSub
			gain  float64
			steps bool
		}
		var cands []cand
		var mu sync.Mutex
		var wg sync.WaitGroup
		sem := make(chan struct{}, 4)
		for _, s := range subs {
			lastStart := s.spans[len(s.spans)-1].start
			if lastStart < 0.75*dur || lastStart > 1.1*dur {
				continue
			}
			wg.Add(1)
			sem <- struct{}{}
			go func() {
				defer wg.Done()
				defer func() { <-sem }()
				fit, ok := alignWith(clampSpans(s.spans, dur), append([]prepared(nil), ws...), syncScales[:1], syncSplitPenalty, -1)
				if ok && fit.Score-fit.Base >= syncMinGain && fit.Peak >= syncMinPeak {
					mu.Lock()
					cands = append(cands, cand{s, fit.Score - fit.Base, len(fit.Segments) > 1})
					mu.Unlock()
				}
			}()
		}
		wg.Wait()
		sort.Slice(cands, func(i, j int) bool { return cands[i].gain > cands[j].gain })
		// The best fitting one, and the best with offset steps (a subtitle made for another cut)
		n, stepped := 0, false
		for _, c := range cands {
			if n < perFile || c.steps && !stepped {
				jobs = append(jobs, job{f: f, s: c.s, raw: raw})
				n++
				stepped = stepped || c.steps
			}
		}
	}
	t.Logf("%d pairs", len(jobs))
	if p := os.Getenv("NFX_SERVED_PAIRS_OUT"); p != "" {
		var b strings.Builder
		for _, j := range jobs {
			fmt.Fprintf(&b, "%s\t%s\n", j.f.name[:8], j.s.name)
		}
		if err := os.WriteFile(p, []byte(b.String()), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	rows := make([]servedRow, len(jobs))
	var wg sync.WaitGroup
	sem := make(chan struct{}, 4)
	for i, j := range jobs {
		wg.Add(1)
		sem <- struct{}{}
		go func() {
			defer wg.Done()
			defer func() { <-sem }()
			rows[i] = replayAnswers(t, t.TempDir(), j.raw, j.f, j.s, j.alts, step)
		}()
	}
	wg.Wait()
	var out io.Writer = io.Discard
	if p := os.Getenv("NFX_SERVED_OUT"); p != "" {
		fh, err := os.Create(p)
		if err != nil {
			t.Fatal(err)
		}
		defer fh.Close()
		out = fh
	}
	type sum struct {
		pairs, stacked, stackedPairs, inverted, good, soft, atChance, shown25, shown50, flips int
		movedN, movedWas, movedHits, swaps, backs                                             int
		within, chance                                                                        float64
		states                                                                                map[string]int
	}
	groups := map[string]*sum{}
	for _, r := range rows {
		fmt.Fprintf(out, "%s\t%s\t%.0f\t%s\t%s\tcues=%d\tstacked=%d\tinverted=%d\twithin=%.2f\tchance=%.2f\tmedian=%+.2f\tn=%d\tgood=%d\tsoft=%d\tatchance=%d\tshown25=%d\tshown50=%d\tflips=%d\tmoved=%d\tmovedwas=%d\tmovedhits=%d\tswaps=%d\tbacks=%d\n",
			r.file, r.sub, r.dur, r.state, r.offsets, r.cues, r.stacked, r.inverted, r.within, r.chance, r.median, r.n, r.good, r.soft, r.atChance, r.shown25, r.shown50, r.flips, r.movedN, r.movedWas, r.movedHits, r.swaps, r.backs)
		g := "series"
		if r.dur >= servedFilmSeconds {
			g = "films"
		}
		for _, name := range []string{g, "all"} {
			s := groups[name]
			if s == nil {
				s = &sum{states: map[string]int{}}
				groups[name] = s
			}
			s.pairs++
			s.stacked += r.stacked
			if r.stacked > 0 {
				s.stackedPairs++
			}
			s.inverted += r.inverted
			s.good += r.good
			s.soft += r.soft
			s.atChance += r.atChance
			s.shown25 += r.shown25
			s.shown50 += r.shown50
			s.flips += r.flips
			s.movedN += r.movedN
			s.movedWas += r.movedWas
			s.movedHits += r.movedHits
			s.swaps += r.swaps
			s.backs += r.backs
			s.within += r.within
			s.chance += r.chance
			s.states[r.state]++
		}
	}
	for _, name := range []string{"series", "films", "all"} {
		if s := groups[name]; s != nil {
			t.Logf("SUMMARY %s: %d pairs %v; stacked %d (%d pairs), inverted %d; within %.3f chance %.3f; stretches good %d soft %d chance %d; shown moves >=0.25 %d >=0.5 %d, flips %d; lines moved %d on speech %d -> %d; swaps %d, back %d",
				name, s.pairs, s.states, s.stacked, s.stackedPairs, s.inverted, s.within/float64(s.pairs), s.chance/float64(s.pairs), s.good, s.soft, s.atChance, s.shown25, s.shown50, s.flips, s.movedN, s.movedWas, s.movedHits, s.swaps, s.backs)
		}
	}
}

// TestSnapshotWriteServedFixture writes testdata/sync/<name>.json of one pair: NFX_FIXTURE_OUT, and
// NFX_FIXTURE_PAIR "<name> <file8> <subtitle> [<alternative>]" (subtitles as loadSnapshot names them).
func TestSnapshotWriteServedFixture(t *testing.T) {
	out := os.Getenv("NFX_FIXTURE_OUT")
	p := strings.Fields(os.Getenv("NFX_FIXTURE_PAIR"))
	if out == "" || len(p) < 3 {
		t.Skip("NFX_FIXTURE_OUT or NFX_FIXTURE_PAIR not set")
	}
	dir := snapshotDir(t)
	files, subs := loadSnapshot(t)
	f, s := findFile(files, p[1]), findSub(subs, p[2])
	if f == nil || s == nil {
		t.Fatal("pair not in the snapshot")
	}
	raw, err := os.ReadFile(filepath.Join(dir, "synclive", f.name+".json"))
	if err != nil {
		t.Fatal(err)
	}
	var live liveSaved
	if err := json.Unmarshal(raw, &live); err != nil {
		t.Fatal(err)
	}
	fx := syncFixture{Duration: f.ref.Probe.Duration, Codec: f.ref.Probe.SubCodec, Check: live.Check}
	for _, w := range f.ref.Windows {
		fx.Subs = append(fx.Subs, fixtureWindowOf(w))
	}
	for _, w := range f.ref.Speech {
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
	for _, c := range s.spans {
		fx.Sub = append(fx.Sub, [2]float64{math.Round(c.start*1000) / 1000, math.Round(c.end*1000) / 1000})
	}
	if len(p) > 3 {
		a := findSub(subs, p[3])
		if a == nil {
			t.Fatal("alternative not in the snapshot")
		}
		for _, c := range a.spans {
			fx.Alt = append(fx.Alt, [2]float64{math.Round(c.start*1000) / 1000, math.Round(c.end*1000) / 1000})
		}
	}
	b, err := json.Marshal(fx)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(out, p[0]+".json"), b, 0o644); err != nil {
		t.Fatal(err)
	}
	t.Logf("%d bytes, %d read windows, %d live, %d own, %d cues", len(b), len(fx.Subs), len(fx.Live), len(fx.Own), len(fx.Sub))
}
