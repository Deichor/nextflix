package subtitles

import (
	"context"
	"math"
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
// track's while that is not known only set aside (where the speech does not tell); for another
// cut the speech read with them and heard stands in, and only a tight track refines lines.
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
	// a text track's windows are set aside (no speech stands in here: the fit takes them, as too
	// little speech; no line refinement while not known)
	ref.Probe.SubCodec = "subrip"
	got, _ = y.checkOwnTrack(syncHash, 1, ref, nil)
	if got.Own.State != "" || !got.OwnAside || len(got.Windows) != 0 || refineWindows(got, nil) != nil {
		t.Fatalf("unchecked text: %+v, %d windows (%s)", got.Own, len(got.Windows), got.kind())
	}
	if r := y.fitRef("", syncHash, 1, ref, nil, nil); r.ref.kind() != "subs" || len(r.ref.Windows) != len(own) || !r.setAside {
		t.Fatalf("unchecked text, too little speech: %d windows (%s)", len(r.ref.Windows), r.ref.kind())
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

// The Mentalist S03E09 (NF): a text track timed to the file whose lines start 0.5 s before their
// speech. Its windows time subtitles on the speech, not 0.5 s early, and the timing stays when
// the check calls the track another cut (the speech of the same stretches stands in).
func TestOwnTrackLeadIsNotServed(t *testing.T) {
	file := dialogue(74, 1500)
	starts := windowStarts(1500, syncWindowLen, 8)
	speech := speechWindows(file, starts, 0, 0.05, 0.1, 4)
	for i := range speech {
		speech[i].Kind = "audio"
	}
	ref := syncRef{Probe: mediaProbe{Duration: 1500, FPS: 23.976, Audio: 1, OwnSub: 2, SubCodec: "subrip"},
		Windows: truthWindows(shiftSpans(file, -0.5), starts, 0, 0, 1), Speech: speech, Paired: true}
	sub := subtitleFor(file, 1, func(float64) float64 { return 2 })
	fit := func(state string) (syncMeta, ownCheck) {
		svc := newLiveTest(t, 1500)
		y := svc.sync
		f := y.liveFile(syncHash, 1)
		if state != "" {
			f.mu.Lock()
			f.check, f.checkN = ownCheck{State: state, Pairs: 1 << 20}, 1<<20
			f.mu.Unlock()
		}
		ctx := context.Background()
		inner := cacheProviderSub(t, svc, "1|lead|2|9|1", sub)
		base, _ := svc.File(ctx, inner)
		key, _ := syncKey(inner, syncHash, 1)
		if !y.decide(ctx, inner, syncHash, 1, base, ref, false) {
			t.Fatal("too little to tell")
		}
		m := y.meta(key)
		if m == nil || !m.fits() {
			t.Fatalf("%s: %+v", state, m)
		}
		return *m, f.ownChecked()
	}
	timed, c := fit("")
	if c.State != ownTimed || math.Abs(c.Fit.Segments[0].Offset-0.5) > 0.1 {
		t.Fatalf("check %+v", c)
	}
	other, _ := fit(ownOther)
	for _, m := range []syncMeta{timed, other} {
		if len(m.Fit.Segments) != 1 || math.Abs(m.Fit.Segments[0].Offset-2) > 0.1 {
			t.Fatalf("%s reference: %s", m.Ref, describeFit(m.Fit))
		}
	}
	if timed.Ref != "subs" || other.Ref != "audio" || math.Abs(timed.Fit.Segments[0].Offset-other.Fit.Segments[0].Offset) > 0.1 {
		t.Fatalf("timed %s (%s), other %s (%s)", describeFit(timed.Fit), timed.Ref, describeFit(other.Fit), other.Ref)
	}
}

// looseTrack is a loose own track leading its speech by 0.4 s, by 0.65 s from 900 s, with its
// grid starts and pairs.
func looseTrack() (own []span, starts []float64, pairs []ownPair) {
	file := dialogue(76, 1500)
	starts = gridStarts(0, 1500)
	rng := rand.New(rand.NewSource(8))
	for _, c := range file {
		d := 0.4 + (rng.Float64()*2-1)*0.4 // loose
		if c.start >= 900 {
			d += 0.25 // a stretch that leads more, by less than an offset change
		}
		own = append(own, span{c.start - d, c.end - d})
	}
	return own, starts, checkPairs(own, file, starts, 0, 0.05, 6)
}

// checkStretchMoves checks that c moves looseTrack's windows by their lead.
func checkStretchMoves(t *testing.T, c ownCheck, own []span, starts []float64) {
	t.Helper()
	var early, late []float64
	for _, w := range c.onSpeech(ownWindows(own, starts)) {
		if _, n := w.lineStarts(); n < ownMinStarts {
			continue // (no use to a fit)
		}
		at := math.Round(w.Start/syncWindowLen) * syncWindowLen
		if at < 900 {
			early = append(early, w.Start-at)
		} else {
			late = append(late, w.Start-at)
		}
	}
	if m := median(early); math.Abs(m-0.4) > 0.12 {
		t.Errorf("before 900 s: moved %.2f s (median), want 0.4", m)
	}
	if m := median(late); math.Abs(m-0.65) > 0.12 {
		t.Errorf("after 900 s: moved %.2f s (median), want 0.65", m)
	}
}

// A loose track's lead changes by stretch (S03E09: 0.45 to 0.87 s); its windows move by their
// own offsets to the speech of the same stretch, a tight track's by the check's.
func TestLooseTrackStretchOffsets(t *testing.T) {
	own, starts, pairs := looseTrack()
	c := checkOwn(pairs)
	if c.State != ownTimed || c.Tight {
		t.Fatalf("check %s tight=%v near %.2f: %s", c.State, c.Tight, c.Near, describeFit(c.Fit))
	}
	c.local = localOffsets(pairs, c)
	checkStretchMoves(t, c, own, starts)
	c.Tight = true
	if l := localOffsets(pairs, c); l != nil {
		t.Errorf("a tight track: %v", l)
	}
}

// A check that found no fit (NoFit) is kept with the live windows like a decided one, a timed
// track's offsets too: after a restart (or the live file dropped from memory) a stand-in
// reference (its read runs) takes them, and the track's windows stay set aside.
func TestOwnCheckNoFitKept(t *testing.T) {
	svc := newLiveTest(t, 1500)
	y := svc.sync
	f := y.liveFile(syncHash, 1)
	f.mu.Lock()
	f.check, f.checkN, f.dirty = ownCheck{NoFit: true, Pairs: 6, Lag: 0.3}, 6, true
	f.mu.Unlock()
	y.saveLive(f)
	again := NewService(nil, Config{CacheDir: svc.cfg.CacheDir, LiveTiming: true}).sync
	if g := again.liveFile(syncHash, 1); g.check.State != "" || !g.check.NoFit || g.check.Pairs != 6 || g.check.Lag != 0.3 || g.checkN != 6 {
		t.Fatalf("reloaded %+v (n %d)", g.check, g.checkN)
	}
	file := dialogue(75, 1500)
	ref := syncRef{Probe: mediaProbe{Duration: 1500, OwnSub: 2, SubCodec: "subrip", Audio: 1},
		Windows: truthWindows(file, windowStarts(1500, syncWindowLen, 6), 0, 0, 1), Busy: true}
	if got, _ := again.checkOwnTrack(syncHash, 1, ref, nil); !got.Own.NoFit || !got.OwnAside || hasOwnWindows(got.Windows) {
		t.Fatalf("stand-in: %+v, %s", got.Own, refKind(got, got.Windows))
	}
	f.mu.Lock()
	f.check, f.dirty = ownCheck{State: ownTimed, Pairs: 8, Fit: syncFit{Segments: []syncSegment{{Offset: 0.42}}}}, true
	f.mu.Unlock()
	y.saveLive(f)
	g := NewService(nil, Config{CacheDir: svc.cfg.CacheDir, LiveTiming: true}).sync.liveFile(syncHash, 1)
	if c := g.ownChecked(); c.State != ownTimed || len(c.Fit.Segments) != 1 || c.Fit.Segments[0].Offset != 0.42 || c.NoFit {
		t.Fatalf("timed reloaded %+v", c)
	}
}

// A loose track's offsets per stretch are kept with the file's check, so every use of the check
// (sameCutWindows reads another file's) moves its windows by them; after a restart they are
// found again from the stretches.
func TestLooseTrackStretchOffsetsKept(t *testing.T) {
	own, starts, pairs := looseTrack()
	svc := newLiveTest(t, 1500)
	y := svc.sync
	f := y.liveFile(syncHash, 1)
	if c := checkOwnCached(f, pairs, true, syncHash, 1); c.State != ownTimed || c.Tight {
		t.Fatalf("check %s tight=%v", c.State, c.Tight)
	}
	checkStretchMoves(t, f.ownChecked(), own, starts)
	y.saveLive(f)
	g := NewService(nil, Config{CacheDir: svc.cfg.CacheDir, LiveTiming: true}).sync.liveFile(syncHash, 1)
	if c := checkOwnCached(g, pairs, true, syncHash, 1); c.State != ownTimed || g.checkN != len(distinctPairs(pairs)) {
		t.Fatalf("reloaded %s (n %d)", c.State, g.checkN)
	}
	checkStretchMoves(t, g.ownChecked(), own, starts)
}

// A timed check kept before its offsets were (PR #16) is checked again with the next fit: the
// track's windows then go onto the speech (The Mentalist S03E09's lines lead it by 0.5 s), and
// the offsets are kept.
func TestOwnCheckWithoutOffsetsCheckedAgain(t *testing.T) {
	file := dialogue(74, 1500)
	starts := windowStarts(1500, syncWindowLen, 8)
	speech := speechWindows(file, starts, 0, 0.05, 0.1, 4)
	for i := range speech {
		speech[i].Kind = "audio"
	}
	ref := syncRef{Probe: mediaProbe{Duration: 1500, FPS: 23.976, Audio: 1, OwnSub: 2, SubCodec: "subrip"},
		Windows: truthWindows(shiftSpans(file, -0.5), starts, 0, 0, 1), Speech: speech, Paired: true}
	sub := subtitleFor(file, 1, func(float64) float64 { return 2 })
	svc := newLiveTest(t, 1500)
	y := svc.sync
	old := []byte(`{"windows":{},"check":{"state":"timed","pairs":8}}`)
	if err := svc.files.write("synclive", syncFileKey(syncHash, 1), ".json", old); err != nil {
		t.Fatal(err)
	}
	f := y.liveFile(syncHash, 1)
	if c := f.ownChecked(); c.State != ownTimed || len(c.Fit.Segments) != 0 || f.checkN != 0 {
		t.Fatalf("restored %+v (n %d)", c, f.checkN)
	}
	ctx := context.Background()
	inner := cacheProviderSub(t, svc, "1|oldcheck|2|9|1", sub)
	base, _ := svc.File(ctx, inner)
	key, _ := syncKey(inner, syncHash, 1)
	if !y.decide(ctx, inner, syncHash, 1, base, ref, false) {
		t.Fatal("too little to tell")
	}
	if m := y.meta(key); m == nil || !m.fits() || len(m.Fit.Segments) != 1 || math.Abs(m.Fit.Segments[0].Offset-2) > 0.1 {
		t.Fatalf("fit %+v, want +2 s (the lines' lead not served)", m)
	}
	if c := f.ownChecked(); c.State != ownTimed || len(c.Fit.Segments) == 0 || math.Abs(c.Fit.Segments[0].Offset-0.5) > 0.1 {
		t.Fatalf("checked again: %+v", c)
	}
	g := NewService(nil, Config{CacheDir: svc.cfg.CacheDir, LiveTiming: true}).sync.liveFile(syncHash, 1)
	if c := g.ownChecked(); len(c.Fit.Segments) == 0 || g.checkN != c.Pairs {
		t.Fatalf("not kept: %+v (n %d)", c, g.checkN)
	}
}

// A fit that missed moves the set-aside windows by its offset only when more line starts than by
// chance lie on an onset at that offset (review: the best share at any lag was tested).
func TestNoFitLag(t *testing.T) {
	file := dialogue(77, 600)
	pairs := checkPairs(file, file, gridStarts(0, 600), 0.3, 0, 9)
	at := func(off float64) syncFit { return syncFit{Segments: []syncSegment{{Offset: off}}} }
	if near, _ := nearShare(pairs, 0.3); near < ownChanceShare {
		t.Fatalf("at the lag %.2f", near)
	}
	if near, _ := nearShare(pairs, -0.4); near >= ownChanceShare {
		t.Fatalf("away from the lag %.2f", near)
	}
	if l := noFitLag(pairs, at(0.3)); math.Abs(l-0.3) > 1e-9 {
		t.Errorf("on the lag: %.2f", l)
	}
	if l := noFitLag(pairs, at(-0.4)); l != 0 {
		t.Errorf("chance at its offset: %.2f", l)
	}
	if l := noFitLag(pairs, at(0.9)); l != 0 {
		t.Errorf("outside the lag: %.2f", l)
	}
}
