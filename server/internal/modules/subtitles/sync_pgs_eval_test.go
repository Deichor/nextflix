package subtitles

import (
	"context"
	"encoding/json"
	"math"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Evaluation of the timing with a file's own PGS track (picture subtitles: cue times from packet
// times) on real data, NFX_TESTDATA (the snapshot) and NFX_GT (the track's cue times as the cue
// tap and the window reads give them, "start --> end" lines); skipped without them. Every line
// is scored against the PGS line starts (linePairs, the same pairs for every case): as served
// (the speech of read and played audio only), and timed with the PGS track: read like a new
// file (planned windows plus up to 2 rounds of extra ones), the speech read before plus the cue
// tap of the stretches that were played, the PGS read plus that tap, and the whole track.

var pgsEvalCases = []struct {
	file, times, note string
	subs, served      []string // subtitles and their served results (synced/<prefix>)
	watched           [2]float64
}{
	{"a9de75c7", "s03e02_pgs.times", "Mentalist S03E02 d3g", []string{"subf2m/67061622", "subf2m/cf45ce69"},
		[]string{"b7691037", "3808e97f"}, [2]float64{1132, 2500}},
}

// servedResult reads a served result of the snapshot (synced/<prefix>*: its cues and fit).
func servedResult(t *testing.T, prefix string) ([]liveCue, syncFit) {
	ps, _ := filepath.Glob(filepath.Join(snapshotDir(t), "synced", prefix+"*.vtt"))
	if len(ps) != 1 {
		return nil, syncFit{}
	}
	b, err := os.ReadFile(ps[0])
	if err != nil {
		return nil, syncFit{}
	}
	var m syncMeta
	if j, err := os.ReadFile(strings.TrimSuffix(ps[0], ".vtt") + ".json"); err == nil {
		_ = json.Unmarshal(j, &m)
	}
	return vttCues(b), m.Fit
}

// ownOf are the own windows of track at starts with enough lines (a window read with fewer
// would be a speech window; left out here).
func ownOf(track []span, starts []float64) []refWindow {
	var out []refWindow
	for _, w := range ownWindows(track, starts) {
		if len(ownStartsOf([]refWindow{w})) >= ownMinStarts {
			out = append(out, w)
		}
	}
	return out
}

// pgsReadWindows are the PGS windows a new file's first timing reads: the planned starts, then
// up to 2 rounds of extra windows where the fit's offset changes.
func pgsReadWindows(track, spans []span, duration float64, maxWindows int) []refWindow {
	read := ownOf(track, windowStarts(duration, syncWindowLen, maxWindows))
	timed := clampSpans(spans, duration)
	fit, ok := alignWith(timed, prepare(read), syncScales, syncSplitPenalty, -1)
	for round := 0; ok && round < 2; round++ {
		extra := fit.extraWindows(syncWindowLen, maxWindows-len(read))
		if len(extra) == 0 {
			break
		}
		read = append(read, ownOf(track, extra)...)
		fit, ok = alignWith(timed, prepare(read), syncScales, syncSplitPenalty, -1)
	}
	return read
}

// pgsPairs pairs the subtitle's cues with the PGS line starts, predicted by the fit with the
// whole track (the best estimate; the pairs depend on the cue only).
func pgsPairs(spans, track []span, read []refWindow, duration float64) ([]linePair, bool) {
	full, _, ok := fitAsServed(spans, read, tapWindows(track, nil, duration), duration, nil)
	if !ok {
		return nil, false
	}
	var starts []float64
	for _, s := range track {
		starts = append(starts, s.start)
	}
	pred := func(i int) float64 { return full.mapTime(spans[i].start) }
	var pairs []linePair
	for _, w := range ownWindows(track, gridStarts(0, duration+syncWindowLen)) {
		pairs = append(pairs, linePairs(len(spans), pred, starts, w.Start, w.end())...)
	}
	return pairs, true
}

// lineErrs are the errors of the paired cues served at starts whose PGS line lies in [lo, hi).
func lineErrs(pairs []linePair, starts []float64, lo, hi float64) []float64 {
	var out []float64
	for _, p := range pairs {
		if p.want >= lo && p.want < hi {
			out = append(out, starts[p.cue]-p.want)
		}
	}
	return out
}

func TestSnapshotPGS(t *testing.T) {
	files, subs := loadSnapshot(t)
	for _, c := range pgsEvalCases {
		f := findFile(files, c.file)
		track := loadFullOwn(t, c.times)
		if f == nil || len(track) == 0 {
			t.Logf("%s: no data", c.note)
			continue
		}
		dur := f.ref.Probe.Duration
		var heard []float64
		for _, w := range f.live {
			heard = append(heard, w.Start)
		}
		tapHeard := ownOf(track, heard) // the played grid windows as the cue tap reads them
		tapAll := tapWindows(track, nil, dur)
		t.Logf("%s: %d PGS lines, %d read windows today (%s), %d live windows; tap of the played stretches %d own windows, whole track %d",
			c.note, len(track), len(f.ref.Windows), f.ref.kind(), len(f.live), len(tapHeard), len(tapAll))
		for k, name := range c.subs {
			s := findSub(subs, name)
			if s == nil {
				t.Logf("%s: no subtitle", name)
				continue
			}
			pgsRead := pgsReadWindows(track, s.spans, dur, f.ref.windowCap())
			pairs, ok := pgsPairs(s.spans, track, pgsRead, dur)
			if !ok {
				t.Logf("%s %s: no fit with the whole track", c.note, name)
				continue
			}
			type result struct {
				label         string
				fit           syncFit
				fitted, final []float64
			}
			var rows []result
			if served, fit := servedResult(t, c.served[k]); len(served) == len(s.spans) {
				starts := make([]float64, len(served))
				for i, sc := range served {
					starts[i] = sc.start
				}
				rows = append(rows, result{"served (speech)", fit, nil, starts})
			} else {
				t.Logf("%s %s: served result has %d cues of %d", c.note, name, len(served), len(s.spans))
			}
			for _, sc := range []struct {
				label      string
				read, live []refWindow
			}{
				{"speech read", f.ref.Windows, nil},
				{"PGS read", pgsRead, nil},
				{"speech read + PGS tap played", f.ref.Windows, tapHeard},
				{"PGS read + PGS tap played", pgsRead, tapHeard},
				{"PGS read + whole track", pgsRead, tapAll},
			} {
				fit, _, ok := fitAsServed(s.spans, sc.read, sc.live, dur, nil)
				if !ok {
					t.Logf("%s %-16s %-30s not accepted (%s)", c.note, name, sc.label, describeFit(fit))
					continue
				}
				fitted, refined := servedStarts(s.spans, fit, sc.read, sc.live)
				rows = append(rows, result{sc.label, fit, fitted, refined})
			}
			for _, r := range rows {
				t.Logf("%s %-16s %-30s %s", c.note, name, r.label, segText(r.fit))
				if r.fitted != nil {
					t.Logf("%s %-16s %-30s fitted  %s", c.note, "", "", signedOf(lineErrs(pairs, r.fitted, 0, dur), nil))
				}
				t.Logf("%s %-16s %-30s served  %s", c.note, "", "", signedOf(lineErrs(pairs, r.final, 0, dur), nil))
				t.Logf("%s %-16s %-30s watched %s", c.note, "", "", signedOf(lineErrs(pairs, r.final, c.watched[0], c.watched[1]), nil))
			}
		}
	}
}

// replayServed replays the played windows a grid window at a time through the real re-fits
// (decide; read reference ref), with the cue tap's own windows of track for every window heard
// (nil track: speech only), and returns what is served after each step: the cues' starts (as
// served, refined) and the end of the window heard last.
func replayServed(t *testing.T, f *snapFile, ref syncRef, track, sub []span, order []int) (heard []float64, served [][]float64, changes int) {
	svc := NewService(nil, Config{CacheDir: t.TempDir(), TorrServerURL: "http://torrserver", LiveTiming: true})
	y := svc.sync
	y.onGrow = func(*liveFile) {}
	hash := f.name
	y.probe = func(context.Context, string) (mediaProbe, error) { return ref.Probe, nil }
	inner := cacheProviderSub(t, svc, "eval|"+f.name[:8], sub)
	key, _ := syncKey(inner, hash, 1)
	base, _ := svc.File(context.Background(), inner)
	lf := y.liveFile(hash, 1)
	lf.setDuration(ref.Probe.Duration)
	var prev *syncMeta
	for i := range order {
		lf.mu.Lock()
		lf.done, lf.ownDone = map[int]liveWin{}, map[int]liveWin{}
		for _, j := range order[:i+1] {
			w := f.live[j]
			g := int(math.Round(w.Start / syncWindowLen))
			lf.done[g] = liveWin{Bits: packBits(w.Speech), Frames: len(w.Speech)}
			if track == nil {
				continue
			}
			own := ownWindows(track, []float64{w.Start})[0].Speech[:len(w.Speech)]
			var starts []int
			for k, v := range own {
				if v == 2 {
					starts = append(starts, k)
				}
			}
			lf.ownDone[g] = liveWin{Bits: packBits(own), Frames: len(own), Starts: starts}
		}
		lf.complete = false
		lf.checkComplete()
		lf.mu.Unlock()
		y.decide(context.Background(), inner, hash, 1, base, ref, false)
		m := y.meta(key)
		if m != nil && prev != nil && fitChanged(prev, *m) {
			changes++
		}
		prev = m
		vtt := base
		if a, ok := y.result(key, base); ok && m != nil && (m.fits() || m.State == syncStateReplaced) {
			vtt = a.VTT
		}
		cues := vttCues(vtt)
		starts := make([]float64, len(sub))
		for j := range starts {
			starts[j] = math.NaN()
			if len(cues) == len(sub) {
				starts[j] = cues[j].start
			}
		}
		heard = append(heard, f.live[order[i]].end())
		served = append(served, starts)
	}
	return heard, served, changes
}

// TestSnapshotPGSReplay: what the viewer watches while playing (the minute 90-150 s behind the
// audio heard, as replayed a grid window at a time), scored line by line against the PGS track:
// the speech of the played audio only (today), the cue tap of the PGS track on today's speech
// read, and a new file's PGS read with the tap; played from the start and resumed at 1132 s
// (the viewing of 2026-10-16).
func TestSnapshotPGSReplay(t *testing.T) {
	files, subs := loadSnapshot(t)
	for _, c := range pgsEvalCases {
		f := findFile(files, c.file)
		track := loadFullOwn(t, c.times)
		if f == nil || len(track) == 0 {
			continue
		}
		dur := f.ref.Probe.Duration
		// the snapshot's probe is from before picture tracks counted; a timing probes it again
		speechRef := f.ref
		speechRef.Probe.OwnSub, speechRef.Probe.SubCodec = 2, "hdmv_pgs_subtitle"
		resume := 0
		for i, w := range f.live {
			if w.end() <= c.watched[0] {
				resume = i + 1
			}
		}
		var start, resumed []int
		for i := range f.live {
			start = append(start, i)
			resumed = append(resumed, (resume+i)%len(f.live))
		}
		for _, name := range c.subs {
			s := findSub(subs, name)
			if s == nil {
				continue
			}
			pgsRead := pgsReadWindows(track, s.spans, dur, f.ref.windowCap())
			pairs, ok := pgsPairs(s.spans, track, pgsRead, dur)
			if !ok {
				continue
			}
			pgsRef := syncRef{Probe: speechRef.Probe, Windows: pgsRead, MaxWindows: f.ref.MaxWindows}
			for _, w := range pgsRead { // the speech read with each window (as played there)
				if sp, ok := speechAt(f.live, w.Start, len(w.Speech)); ok {
					pgsRef.Speech = append(pgsRef.Speech, refWindow{Start: w.Start, Kind: "audio", Speech: sp})
				}
			}
			for _, o := range []struct {
				label string
				order []int
			}{{"from the start", start}, {"resumed at 1132 s", resumed}} {
				for _, sc := range []struct {
					label string
					ref   syncRef
					track []span
				}{
					{"speech read + speech", f.ref, nil},
					{"speech read + PGS tap", speechRef, track},
					{"PGS read + PGS tap", pgsRef, track},
				} {
					heard, served, changes := replayServed(t, f, sc.ref, sc.track, s.spans, o.order)
					var watched []float64
					untimed := 0
					for i, h := range heard {
						for _, e := range lineErrs(pairs, served[i], h-150, h-90) {
							if math.IsNaN(e) {
								untimed++
								continue
							}
							watched = append(watched, e)
						}
					}
					t.Logf("%s %-16s %-18s %-22s %d served changes, watched %s", c.note, name, o.label, sc.label, changes,
						signedOf(watched, nil))
					if untimed > 0 {
						t.Logf("%s %-16s %-18s %-22s %d watched lines not served timed", c.note, "", "", "", untimed)
					}
				}
			}
		}
	}
}
