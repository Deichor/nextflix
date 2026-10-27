package subtitles

import (
	"context"
	"fmt"
	"math"
	"strings"
	"testing"
)

// Real starts kept as fixtures (TestSnapshotWriteStartFixtures), replayed as they ran
// (replayStart): the TV's answers through the first 5 minutes against the file's final fit.

// timedWrong counts the cues of the first 5 minutes shown timed but more than 0.5 s off, and the
// first timing shown (seconds after the open; -1 none).
func timedWrong(r startRun) (int, float64) {
	first := -1.0
	for _, e := range r.timeline {
		if e.at >= 0 && !e.pending && first < 0 {
			first = e.at
		}
	}
	return r.wrong - r.untimed, first
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

// The Mentalist S03E15 (session of 2026-10-22 02:23): its pre-timed fit, from the speech (its own
// track found no fit to it and was set aside), was right; the first re-fit, with one played
// window, found no fit to the speech and timed the subtitle from the set-aside track's windows,
// 2 s early for 30 s. The speech fit stays, and shows once the played audio backs it.
func TestPreTimedSpeechFitIsNotReplacedByATrackSetAside(t *testing.T) {
	f, sub := loadStartFixture(t, "mentalist_s03e15_start.json")
	c := startCase{first: 6, pre: true, heard: [][2]float64{{5.7, 39}, {10.8, 49}, {31, 180}, {47, 240}, {400, 490}},
		truth: "-1.00 -3.35@237 -4.98@1076 -6.15@1523 -8.42@2131"}
	r := replayStart(t, c.spec(sub), f, f.tap, sub, false)
	wrong, first := timedWrong(r)
	if wrong > 0 || first < 0 || first > 25 {
		t.Fatalf("%d cues shown timed but off, first timing shown at %.0f s: %s", wrong, first, shownText(r))
	}
}

// The Mentalist S03E22 (2026-10-25 17:08): opened while its read ran; the cue tap's windows of its
// own track (another cut's, as the check found once the read was done) timed the subtitle 0.82 s
// early for minutes. Unchecked, the track gives way to the speech heard with it.
func TestUncheckedOwnTrackGivesWayToTheSpeech(t *testing.T) {
	f, sub := loadStartFixture(t, "mentalist_s03e22_start.json")
	c := startCase{first: 6, readAt: 80, heard: [][2]float64{{33, 142}, {36, 168}, {68, 240}, {140, 240}, {400, 500}},
		truth: "-0.82 -2.57@164 -3.90@973 -4.52@1637 -5.10@2140"}
	r := replayStart(t, c.spec(sub), f, f.tap, sub, false)
	wrong, first := timedWrong(r)
	if wrong > 0 || first < 0 || first > 40 {
		t.Fatalf("%d cues shown timed but off, first timing shown at %.0f s: %s", wrong, first, shownText(r))
	}
}

// The Mentalist S03E17 (2026-10-22): its subtitle was timed from the windows of its own track set
// aside, moved by the track's lag to the speech; that lag went +0.38, 0 and +0.37 s at 14, 15
// and 17 stretches, and the whole subtitle with it. It stays.
func TestSetAsideTrackLagDoesNotFlip(t *testing.T) {
	f, sub := loadStartFixture(t, "mentalist_s03e17_lag.json")
	truth := parseFit("-0.44 -2.05@214 -2.64@354 -3.80@989 -3.21@1122 -4.09@1188 -5.22@1519 -6.73@2101")
	r := replayMid(t, f, f.tap, sub, 10, gridOrder(0, 12), truth, false)
	if r.whole > 0 || len(r.near) == 0 || r.near[len(r.near)-1] > 0.1 {
		t.Fatalf("%d whole-track moves, %d changes, near the viewer %v", r.whole, r.moves, r.near)
	}
}

func TestSettleLag(t *testing.T) {
	file := dialogue(81, 1200)
	led := checkPairs(file, file, gridStarts(0, 1080), 0.3, 0.03, 1) // the track leads the speech by 0.3 s
	onTime := checkPairs(file, file, gridStarts(0, 1080), 0, 0.03, 2)
	prev := ownCheck{NoFit: true, Lag: 0.3, Pairs: 14}
	cases := []struct {
		name  string
		prev  ownCheck
		c     ownCheck
		pairs []ownPair
		lag   float64
	}{
		{"no lag found", prev, ownCheck{NoFit: true, Pairs: 15}, led, 0.3},
		{"another from one more stretch", prev, ownCheck{NoFit: true, Lag: 0.05, Pairs: 15}, led, 0.3},
		{"another from a third more", prev, ownCheck{NoFit: true, Lag: 0.05, Pairs: 19}, led, 0.05},
		{"a near one", prev, ownCheck{NoFit: true, Lag: 0.36, Pairs: 15}, led, 0.36},
		{"no longer on the speech", prev, ownCheck{NoFit: true, Pairs: 15}, onTime, 0},
		{"decided", prev, ownCheck{State: ownTimed, Pairs: 15}, led, 0},
	}
	for _, c := range cases {
		if got := settleLag(c.prev, c.c, c.pairs); math.Abs(got.Lag-c.lag) > 1e-9 {
			t.Errorf("%s: lag %.2f, want %.2f", c.name, got.Lag, c.lag)
		}
	}
	// a kept lag counts from the stretches it was found from
	kept := settleLag(prev, ownCheck{NoFit: true, Pairs: 15}, led)
	if got := settleLag(kept, ownCheck{NoFit: true, Lag: 0.05, Pairs: 18}, led); got.Lag != 0.05 {
		t.Errorf("a third more than 14: lag %.2f", got.Lag)
	}
}

// The played audio backs a pre-timed fit only at its offsets.
func TestBacksPre(t *testing.T) {
	file := dialogue(82, 900)
	sub := subtitleFor(file, 1, func(float64) float64 { return 2 })
	base := vttOf(sub)
	live := speechWindows(file, gridStarts(0, 180), 0.1, 0.05, 0.1, 3)
	pre := func(off float64) *syncMeta {
		return &syncMeta{State: syncStateSynced, Live: true, Pre: true, Fit: syncFit{Scale: 1, Segments: []syncSegment{{Offset: off}}}}
	}
	if !backsPre(pre(2), base, 900, live) {
		at, peak, gain := playBacking(pre(2).Fit, baseSpans(base, 900), live)
		t.Fatalf("the right offset is not backed: %.1f lines, peak %.1f, gain %.2f", at, peak, gain)
	}
	if backsPre(pre(3.4), base, 900, live) {
		t.Fatal("an offset 1.4 s off is backed")
	}
	shown := pre(2)
	shown.Pre = false
	if backsPre(shown, base, 900, live) || backsPre(pre(2), base, 900, nil) {
		t.Fatal("backed without a pre-timed fit or played audio")
	}
}

// Another frame rate is held to the hysteresis too: one that maps the cues as the served fit does
// gains nothing.
func TestKeepStableAnotherRate(t *testing.T) {
	file := dialogue(900, episode)
	sub := subtitleFor(file, 1, func(float64) float64 { return 2 })
	ws := speechWindows(file, gridStarts(0, 900), 0, 0.15, 0.2, 1)
	fit, ok := alignLive(sub, ws, syncScales[:1])
	if !ok || !fit.good() {
		t.Fatalf("fit %s", describeFit(fit))
	}
	m := syncMeta{State: syncStateSynced, Fit: fit, Live: true}
	other := fit
	other.Scale = 1 + 1e-7
	served := syncMeta{State: syncStateSynced, Live: true, Fit: other}
	if !keepStable(&served, m, vttOf(sub), episode, false) {
		t.Fatal("a fit at another rate mapping the cues the same was replaced")
	}
	// the same offsets as decide hands them over (not marked provisional yet) are stored: their
	// lines may be refined by the windows heard since
	same := syncMeta{State: syncStateSynced, Fit: fit}
	if keepStable(&syncMeta{State: syncStateSynced, Fit: fit, Live: true}, same, vttOf(sub), episode, false) {
		t.Fatal("the same fit kept the old result")
	}
}

// The first timing of a subtitle served in a viewing stores what the windows read give before it
// reads more where the offset changes.
func TestFirstTimingServesBeforeItReadsMore(t *testing.T) {
	svc := newLiveTest(t, 1500)
	y := svc.sync
	file := dialogue(83, 1500)
	sub := subtitleFor(file, 1, func(u float64) float64 {
		if u < 700 {
			return 1
		}
		return 3.5
	})
	ws := speechWindows(file, windowStarts(1500, syncWindowLen, 6), 0, 0.05, 0.1, 2)
	for i := range ws {
		ws[i].Kind = "audio"
	}
	probe := mediaProbe{Duration: 1500, FPS: 23.976, Audio: 1, OwnSub: -1, SubCodec: "none", Bitrate: 2 << 20}
	ref := syncRef{Probe: probe, Windows: ws, MaxWindows: syncMaxWindows, Paired: true}
	setRef(y, syncHash, 1, ref)
	ctx := context.Background()
	inner := cacheProviderSub(t, svc, "1|first|2|7|1", sub)
	base, _ := svc.File(ctx, inner)
	key, _ := syncKey(inner, syncHash, 1)
	y.noteServed(key, inner, syncHash, 1, 0)
	reads := 0
	var during *syncMeta
	y.read = func(_ context.Context, _ string, _ mediaProbe, at, _ float64) (refWindow, error) {
		if reads == 0 {
			during = y.meta(key)
		}
		reads++
		w := speechWindows(file, []float64{at}, 0, 0.05, 0.1, int64(at))[0]
		w.Kind = "audio"
		return w, nil
	}
	if !y.decideRead(ctx, inner, syncHash, 1, base, ref) {
		t.Fatal("too little to tell")
	}
	if reads == 0 {
		t.Fatal("no window read where the offset changes")
	}
	if during == nil || !during.fits() {
		t.Fatalf("served while the extra windows were read: %+v", during)
	}
	if m := y.meta(key); m == nil || !m.fits() || len(m.Fit.Segments) < 2 {
		t.Fatalf("after the reads: %+v", m)
	}
}
