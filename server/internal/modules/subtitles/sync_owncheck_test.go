package subtitles

import (
	"math/rand"
	"testing"
)

// trustOwnTrack marks the file's own track as checked, timed to the file and tight: for tests
// and replays of the aligner with own-track windows (the check itself is tested here).
func trustOwnTrack(f *liveFile) {
	f.mu.Lock()
	f.check, f.checkN = ownCheck{State: ownTimed, Tight: true}, 1<<30
	f.mu.Unlock()
}

// checkPairs pairs the own track's windows at the grid starts with speech of the file's lines.
func checkPairs(own, lines []span, starts []float64, lag, jitter float64, seed int64) []ownPair {
	speech := speechWindows(lines, starts, lag, jitter, 0.1, seed)
	var out []ownPair
	for i, w := range ownWindows(own, starts) {
		out = append(out, ownPair{w.Start, w.Speech, speech[i].Speech})
	}
	return out
}

// The check: a track on the file's lines is timed to it and tight; the same lines up to 0.9 s
// off each (loose captions) are timed but loose; another cut's lines (offsets changing by act)
// are not timed to the file; one stretch decides nothing.
func TestCheckOwn(t *testing.T) {
	file := dialogue(71, 1500)
	starts := gridStarts(0, 1500)
	rng := rand.New(rand.NewSource(5))
	loose := make([]span, len(file))
	for i, c := range file {
		d := (rng.Float64()*2 - 1) * 0.9
		loose[i] = span{c.start + d, c.end + d}
	}
	other := make([]span, len(file))
	for i, c := range file {
		d := 2.0 + 2*float64(int(c.start/300)) // another cut: +2 s more after every act break
		other[i] = span{c.start - d, c.end - d}
	}
	for _, c := range []struct {
		name  string
		own   []span
		state string
		tight bool
	}{
		{"own lines", file, ownTimed, true},
		{"loose captions", loose, ownTimed, false},
		{"another cut", other, ownOther, false},
	} {
		got := checkOwn(checkPairs(c.own, file, starts, 0.15, 0.08, 9))
		if got.State != c.state || got.Tight != c.tight {
			t.Errorf("%s: %s tight=%v (near %.2f of %d, fit %s)", c.name, got.State, got.Tight, got.Near, got.Matched,
				describeFit(got.Fit))
		}
		if c.tight && (got.Fit.Segments[0].Offset < 0.05 || got.Fit.Segments[0].Offset > 0.3) {
			t.Errorf("%s: offset %s, the speech starts 0.15 s after the lines", c.name, describeFit(got.Fit))
		}
	}
	if got := checkOwn(checkPairs(file, file, starts[:1], 0.15, 0.08, 9)); got.State != "" {
		t.Errorf("one stretch decided %q", got.State)
	}
	// (it needs liveMinLines speech line starts: 4 minutes of this dialogue)
	if got := checkOwn(checkPairs(file, file, starts[:4], 0.15, 0.08, 9)); got.State != ownTimed {
		t.Errorf("four stretches: %q (fit %s)", got.State, describeFit(got.Fit))
	}
}

// A decision stands: not against a check that does not decide (S02E22's captions went timed,
// not known, timed), nor against another decision from fewer than a third more stretches; a
// tight track stays tight down to 45 % of its line starts on an onset, a loose one needs 55 %.
func TestOwnCheckSettles(t *testing.T) {
	timed := ownCheck{State: ownTimed, Tight: true, Near: 0.6, Pairs: 9}
	cases := []struct {
		name      string
		prev, c   ownCheck
		state     string
		tight     bool
		fromFresh bool
	}{
		{"first decision", ownCheck{}, ownCheck{State: ownOther, Pairs: 2}, ownOther, false, true},
		{"not known", timed, ownCheck{Pairs: 13}, ownTimed, true, false},
		{"other from 11 stretches", timed, ownCheck{State: ownOther, Pairs: 11}, ownTimed, true, false},
		{"other from 12 stretches", timed, ownCheck{State: ownOther, Pairs: 12}, ownOther, false, true},
		{"tight at 48 %", timed, ownCheck{State: ownTimed, Near: 0.48, Pairs: 12}, ownTimed, true, true},
		{"loose at 40 %", timed, ownCheck{State: ownTimed, Near: 0.40, Pairs: 12}, ownTimed, false, true},
		{"loose stays at 50 %", ownCheck{State: ownTimed, Near: 0.4, Pairs: 9}, ownCheck{State: ownTimed, Near: 0.5, Pairs: 12},
			ownTimed, false, true},
	}
	for _, c := range cases {
		got := settleOwn(c.prev, c.c)
		if got.State != c.state || got.Tight != c.tight || (got.Pairs == c.c.Pairs) != c.fromFresh {
			t.Errorf("%s: %+v", c.name, got)
		}
	}
}

// The check is done again only from all stretches: a stand-in reference (its read runs) does
// not decide from the played ones alone. Once decided it is kept with the live windows, so a
// restart goes on from it; live files saved before have none.
func TestOwnCheckKept(t *testing.T) {
	svc := newLiveTest(t, 1500)
	y := svc.sync
	file := dialogue(73, 1500)
	f := y.liveFile(syncHash, 1)
	f.mu.Lock()
	for _, p := range checkPairs(file, file, gridStarts(0, 600), 0.15, 0.08, 9) {
		g := int(p.start / syncWindowLen)
		var starts []int
		for i, v := range p.own {
			if v == 2 {
				starts = append(starts, i)
			}
		}
		f.ownDone[g] = liveWin{Bits: packBits(p.own), Frames: len(p.own), Starts: starts}
		f.done[g] = liveWin{Bits: packBits(p.speech), Frames: len(p.speech)}
	}
	f.mu.Unlock()
	probe := mediaProbe{Duration: 1500, OwnSub: 2, SubCodec: "subrip", Audio: 1}
	if got, _ := y.checkOwnTrack(syncHash, 1, syncRef{Probe: probe, Skipped: true, Busy: true}, nil); got.Own.State != "" || f.checkN != 0 {
		t.Fatalf("a stand-in decided: %+v", got.Own)
	}
	got, _ := y.checkOwnTrack(syncHash, 1, syncRef{Probe: probe, Skipped: true}, nil)
	if got.Own.State != ownTimed || !got.Own.Tight || !f.dirty {
		t.Fatalf("from the played stretches: %+v", got.Own)
	}
	y.saveLive(f)
	again := NewService(nil, Config{CacheDir: svc.cfg.CacheDir, LiveTiming: true}).sync.liveFile(syncHash, 1)
	if c := again.check; c.State != ownTimed || !c.Tight || c.Pairs != got.Own.Pairs || again.checkN != c.Pairs {
		t.Fatalf("reloaded %+v (n %d), saved %+v", c, again.checkN, got.Own)
	}
	old := []byte(`{"windows":{"3":{"bits":"AQ==","n":8}},"complete":true}`)
	if err := svc.files.write("synclive", syncFileKey(testHash, 1), ".json", old); err != nil {
		t.Fatal(err)
	}
	if g := NewService(nil, Config{CacheDir: svc.cfg.CacheDir, LiveTiming: true}).sync.liveFile(testHash, 1); g.check.State != "" ||
		len(g.done) != 1 || !g.complete {
		t.Fatalf("an older live file: %+v", g)
	}
}

// A fit takes a PGS track's windows only once the check says it is timed to the file, a text
// track's also while that is not known (its speech may not tell); for another cut the speech
// read with them and heard stands in, and only a tight track refines lines.
func TestCheckOwnTrackWindows(t *testing.T) {
	svc := newLiveTest(t, 1500)
	y := svc.sync
	file := dialogue(72, 1500)
	starts := windowStarts(1500, syncWindowLen, 6)
	own := truthWindows(file, starts, 0, 0, 1)
	speech := speechWindows(file, starts, 0.15, 0.08, 0.1, 3)
	for i := range speech {
		speech[i].Kind = "audio"
	}
	ref := syncRef{Probe: mediaProbe{Duration: 1500, OwnSub: 2, SubCodec: "hdmv_pgs_subtitle", Audio: 1}, Windows: own}
	// without the speech: not known, no windows of a PGS track
	got, _ := y.checkOwnTrack(syncHash, 1, ref, nil)
	if got.Own.State != "" || len(got.Windows) != 0 || refineWindows(got, nil) != nil {
		t.Fatalf("unchecked PGS: %+v, %d windows", got.Own, len(got.Windows))
	}
	// a text track's windows are used (no line refinement while not known)
	ref.Probe.SubCodec = "subrip"
	got, _ = y.checkOwnTrack(syncHash, 1, ref, nil)
	if got.Own.State != "" || got.kind() != "subs" || len(got.Windows) != len(own) || refineWindows(got, nil) != nil {
		t.Fatalf("unchecked text: %+v, %d windows (%s)", got.Own, len(got.Windows), got.kind())
	}
	ref.Speech = speech
	got, _ = y.checkOwnTrack(syncHash, 1, ref, nil)
	if got.Own.State != ownTimed || !got.Own.Tight || got.kind() != "subs" || len(refineWindows(got, nil)) != len(own) {
		t.Fatalf("timed: %+v, %s", got.Own, got.kind())
	}
	// another cut: the speech stands in for every own window
	y2 := newLiveTest(t, 1500).sync
	shifted := make([]span, len(file))
	for i, c := range file {
		shifted[i] = span{c.start - 4, c.end - 4}
	}
	ref.Windows = truthWindows(shifted, starts, 0, 0, 1)
	got, _ = y2.checkOwnTrack(syncHash, 1, ref, nil)
	if got.Own.State != ownOther || got.kind() != "audio" || len(got.Windows) != len(starts) {
		t.Fatalf("another cut: %+v, %s, %d windows", got.Own, got.kind(), len(got.Windows))
	}
	if y2.liveFile(syncHash, 1).ownState() != ownOther {
		t.Fatal("the result is not kept for the file")
	}
	if _, s := y2.openOwnCues(syncHash, 1, 0, []OwnTrack{{Stream: 2, Lang: "eng", Codec: "subrip"}}); s != nil {
		t.Fatal("a cue tap of a track not timed to the file")
	}
}
