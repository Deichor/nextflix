package subtitles

import (
	"context"
	"math"
	"math/rand"
	"sync"
	"testing"
	"time"
)

// Regression tests of the timing robustness (see sync_eval_test.go for the real-data harness).

// speechWindows is what a speech detector makes of the file's lines in live windows: speech from
// lag (± jitter) after each line's start to its end, miss of the lines lost, as many false bursts.
func speechWindows(file []span, starts []float64, lag, jitter, miss float64, seed int64) []refWindow {
	rng := rand.New(rand.NewSource(seed))
	var out []refWindow
	for _, s := range starts {
		w := refWindow{Start: s, Kind: "live", Speech: make([]byte, int(syncWindowLen*syncRate))}
		mark := func(a, b float64) {
			for i := max(int((a-s)*syncRate), 0); i < int((b-s)*syncRate) && i < len(w.Speech); i++ {
				w.Speech[i] = 1
			}
		}
		for _, c := range file {
			if c.end < s || c.start >= s+syncWindowLen {
				continue
			}
			if rng.Float64() < miss {
				x := s + rng.Float64()*syncWindowLen
				mark(x, x+0.5+rng.Float64())
				continue
			}
			mark(c.start+lag+(rng.Float64()*2-1)*jitter, c.end)
		}
		out = append(out, w)
	}
	return out
}

// Avatar: Seven Havens S01E02 (real data): a WEB-DL subtitle on the WEB-DL file has one offset
// (0.00 by the file's own English track). The played audio's speech starts 0.25 s after the
// track's lines, and the old Viterbi bridged that gap through one live window 10-14 s away,
// serving minutes of cues 10-14 s off. Every re-fit must now serve offsets within 0.35 s of the
// truth, with and without the read windows.
func TestAvatarLiveNoFalseSegments(t *testing.T) {
	f, sub := loadFixture(t, "avatar_s01e02.json")
	for _, withRead := range []bool{true, false} {
		fitted := 0
		steps := replayLive(t, f, sub, withRead, nil)
		for _, st := range steps {
			if !st.fits {
				continue
			}
			fitted++
			for _, s := range st.served.Segments {
				if math.Abs(s.Offset) > 0.35 || math.Abs(st.served.Scale-1) > 1e-9 {
					t.Fatalf("read=%v minute %d: %s", withRead, st.minute, describeFit(st.served))
				}
			}
		}
		last := steps[len(steps)-1]
		if fitted < len(steps)-2 || withRead && math.Abs(last.served.Segments[0].Offset) > 0.05 {
			t.Fatalf("read=%v: %d of %d steps fitted, last %s", withRead, fitted, len(steps), describeFit(last.served))
		}
	}
}

// A bridge: offsets 0.23 then 0.00 (closer than syncMinChange) with one window between them at
// +13 s that scores less there than at its neighbours' offsets. It goes; real runs stay.
func TestMergeUnsupportedBridge(t *testing.T) {
	const lo = 0
	curve := func(peaks map[int]float64) []float64 {
		c := make([]float64, 3000)
		for q, v := range peaks {
			c[q] = v
		}
		return c
	}
	// states: 100 = +0.23 s, 77 = 0.00 s, 1400 = +13 s
	curves := [][]float64{
		curve(map[int]float64{100: 9, 77: 6}),
		curve(map[int]float64{100: 9.1, 1400: 8.3, 77: 7}),
		curve(map[int]float64{77: 18, 100: 11}),
		curve(map[int]float64{77: 12, 100: 7}),
	}
	path := []int{100, 1400, 77, 77}
	mergeUnsupported(path, curves, lo, syncSplitPenalty)
	if path[1] == 1400 || path[0] != 100 || path[2] != 77 || path[3] != 77 {
		t.Fatalf("path %v", path)
	}
	// a supported run is kept
	curves[1] = curve(map[int]float64{1400: 20, 100: 3, 77: 3})
	path = []int{100, 1400, 77, 77}
	mergeUnsupported(path, curves, lo, syncSplitPenalty)
	if path[1] != 1400 {
		t.Fatalf("supported run merged: %v", path)
	}
}

// The penalty of an offset change no longer grows with the number of live windows: the teaser
// before a title sequence cut differently (a few windows) keeps its offset when the whole
// episode was heard (it was dropped at about 25 windows before).
func TestAlignLiveKeepsTheTeaser(t *testing.T) {
	for seed := int64(1); seed <= 4; seed++ {
		file := breakFile(400+seed, episode, 150)
		sub := subtitleFor(file, 1, func(u float64) float64 {
			if u < 155 {
				return 1.2
			}
			return 3.2
		})
		ws := speechWindows(file, gridStarts(0, episode), 0, 0.15, 0.2, seed)
		f, ok := alignLive(sub, ws, syncScales)
		if !ok || !f.good() || len(f.Segments) != 2 || math.Abs(f.Segments[0].Offset-1.2) > 0.15 ||
			math.Abs(f.Segments[1].Offset-3.2) > 0.15 {
			t.Fatalf("seed %d: %s", seed, describeFit(f))
		}
	}
}

// A subtitle made for 25 fps on a 23.976 fps file is fitted from contiguous played windows (the
// drift shows within minutes); before, other rates waited for windows spread over 25 minutes.
func TestAlignLiveOtherRate(t *testing.T) {
	k := 25 / 23.976
	for seed := int64(1); seed <= 3; seed++ {
		file := dialogue(500+seed, episode)
		sub := subtitleFor(file, k, func(float64) float64 { return 0.8 })
		ws := speechWindows(file, gridStarts(120, 120+10*syncWindowLen), 0, 0.15, 0.2, seed)
		f, ok := alignLive(sub, ws, syncScales)
		if !ok || !f.good() || math.Abs(f.Scale-k) > 1e-9 || mappedWithin(f, sub, file, 0.15) < 0.95 {
			t.Fatalf("seed %d: %s", seed, describeFit(f))
		}
	}
}

// With the other rates tried on contiguous windows, another episode's subtitle still does not fit.
func TestAlignLiveOtherRateRejectsAnotherEpisode(t *testing.T) {
	wrong := 0
	for i := int64(0); i < 60; i++ {
		rng := rand.New(rand.NewSource(i))
		file := dialogue(600+i, episode)
		n := 8 + int(i%13)
		from := math.Round(rng.Float64() * (episode - float64(n)*syncWindowLen - 60))
		ws := speechWindows(file, gridStarts(from, from+float64(n)*syncWindowLen), 0.1, 0.15, 0.2, i)
		if f, ok := alignLive(dialogue(700+i, episode), ws, syncScales); ok && f.good() {
			wrong++
			t.Logf("case %d (%d windows): %s", i, n, describeFit(f))
		}
	}
	if wrong > 0 {
		t.Fatalf("another episode fitted %d times of 60", wrong)
	}
}

// Speech that starts a constant lag after the lines of the file's own track does not turn into
// offset changes between the two kinds of windows: the lag is fitted.
func TestAlignMixedKindsFitsTheLag(t *testing.T) {
	file := dialogue(800, episode)
	sub := subtitleFor(file, 1, func(float64) float64 { return -2 })
	var ws []refWindow
	for _, w := range truthWindows(file, []float64{20, 440, 860, 1280, 1700, 2120}, 0, 0, 1) {
		ws = append(ws, w)
	}
	for _, w := range speechWindows(file, gridStarts(60, 1200), 0.3, 0.1, 0.15, 2) {
		if !overlapsHalf(w, ws) {
			ws = append(ws, w)
		}
	}
	f, ok := alignLive(sub, ws, syncScales)
	if !ok || !f.good() || len(f.Segments) != 1 || math.Abs(f.Segments[0].Offset+2) > 0.05 || math.Abs(f.Lag-0.3) > 0.05 {
		t.Fatalf("%s lag %.2f", describeFit(f), f.Lag)
	}
}

// The first fit may come from 2 minutes of played audio (liveMinCover): another episode's
// subtitle still never fits there, this episode's mostly does.
func TestAlignEarlyLive(t *testing.T) {
	right, tried, wrong := 0, 0, 0
	for i := int64(0); i < 300; i++ {
		rng := rand.New(rand.NewSource(1000 + i))
		file := dialogue(1000+i, episode)
		from := math.Round(rng.Float64() * 600)
		heard := liveMinCover + float64(i%3)*30
		var ws []refWindow
		for _, w := range speechWindows(file, gridStarts(from, from+heard+syncWindowLen), 0.1, 0.15, 0.2, i) {
			if n := int(math.Min(syncWindowLen, from+heard-w.Start) * syncRate); n >= liveMinRun {
				w.Speech = w.Speech[:n]
				ws = append(ws, w)
			}
		}
		if windowSeconds(ws) < liveMinCover {
			continue
		}
		off := math.Round((rng.Float64()*120-60)*100) / 100
		if f, ok := alignLive(subtitleFor(file, 1, func(float64) float64 { return off }), ws, syncScales); ok {
			tried++
			if f.good() && math.Abs(offsetAt(f.Segments, from+heard/2)-off-0.1) < 0.2 {
				right++
			}
		}
		if f, ok := alignLive(dialogue(2000+i, episode), ws, syncScales); ok && f.good() {
			wrong++
			t.Logf("case %d: another episode fits: %s", i, describeFit(f))
		}
	}
	if wrong > 0 || tried < 30 || right < tried*8/10 {
		t.Fatalf("right %d/%d, another episode fitted %d times of 300", right, tried, wrong)
	}
	t.Logf("right %d/%d", right, tried)
}

// A file above the byte budget with a text track: the first play stream reads its own track's
// lines of the first two minutes from where playback starts (once), and they time the subtitle
// with a minute of played audio.
func TestLiveReadsEarlyOwnTrack(t *testing.T) {
	svc := newLiveTest(t, episode)
	y := svc.sync
	y.probe = func(context.Context, string) (mediaProbe, error) {
		return mediaProbe{Duration: episode, FPS: 23.976, Audio: 1, TextSub: 2, Bitrate: 2 << 20}, nil
	}
	file := dialogue(1100, episode)
	var mu sync.Mutex
	var starts []float64
	y.readSubs = func(_ context.Context, _ string, _ mediaProbe, start, _ float64) (refWindow, error) {
		mu.Lock()
		starts = append(starts, start)
		mu.Unlock()
		return truthWindows(file, []float64{start}, 0, 0, 1)[0], nil
	}
	grown := make(chan struct{}, 4)
	y.onGrow = func(*liveFile) { grown <- struct{}{} }
	inner := cacheProviderSub(t, svc, "1|early|2|10|1", subtitleFor(file, 1, func(float64) float64 { return 2.5 }))
	id := wrapSyncID(inner, syncHash, 1)
	ctx := context.Background()
	if a, _ := svc.FileFor(ctx, id, 0); a.Sync != syncStatePending {
		t.Fatalf("first: %q", a.Sync)
	}
	tap := y.openSpeech(syncHash, 1, 300.4)
	select {
	case <-grown:
	case <-time.After(5 * time.Second):
		t.Fatal("no early read")
	}
	tap.Close()
	mu.Lock()
	got := append([]float64(nil), starts...)
	mu.Unlock()
	if len(got) != liveEarlyWindows || got[0] != 300 || got[1] != 360 {
		t.Fatalf("read at %v", got)
	}
	f := y.liveFile(syncHash, 1)
	injectLive(f, file, []int{7})
	y.refitFile(f)
	key, _ := syncKey(inner, syncHash, 1)
	m := y.meta(key)
	if m == nil || !m.fits() || len(m.Fit.Segments) != 1 || math.Abs(m.Fit.Segments[0].Offset-2.5) > 0.05 {
		t.Fatalf("meta %+v", m)
	}
	// once per file
	y.openSpeech(syncHash, 1, 900).Close()
	time.Sleep(50 * time.Millisecond)
	mu.Lock()
	defer mu.Unlock()
	if len(starts) != liveEarlyWindows {
		t.Fatalf("read again: %v", starts)
	}
}

// A provider subtitle "made for the playing release" (here subf2m's S02.WEBRip.Amazon on an
// AMZN WEB-DL, which still needed +1.24 s / +3.26 s on The Mentalist S02E10) only ranks ahead:
// it gets a file id, so it is timed like any other; only a hash match is served as it is.
func TestSameReleaseSubtitleIsTimed(t *testing.T) {
	svc := NewService(nil, Config{CacheDir: t.TempDir(), TorrServerURL: "http://torrserver"})
	q := Query{MediaType: "tv", Season: 2, Episode: 10, Hash: syncHash, Index: 1, Langs: []string{"tr"},
		Title: "The Mentalist S02E10 1080p AMZN WEB-DL DDP5 1 H 264-ViSUM"}
	same := encodeID(SourceSubf2m, "tr", "52|amazon")
	hashed := encodeID(SourceOpenSubtitles, "tr", "53|hash")
	other := encodeID(SourceTurkceAltyazi, "tr", "54|hdtv")
	subs := svc.rank([]candidate{
		{Sub: Sub{ID: same, Source: SourceSubf2m, Language: "tr"}, Releases: []string{"The.Mentalist.S02.WEBRip.Amazon"}, Pack: true},
		{Sub: Sub{ID: hashed, Source: SourceOpenSubtitles, Language: "tr", Synced: true}},
		{Sub: Sub{ID: other, Source: SourceTurkceAltyazi, Language: "tr"}, Releases: []string{"S02 pack HDTV"}, Pack: true},
	}, q)
	timed := map[string]bool{}
	for _, s := range subs {
		if in, _, _, ok := unwrapSyncID(s.ID); ok {
			timed[in] = true
		}
	}
	if !timed[same] || !timed[other] || timed[hashed] {
		t.Fatalf("timed %v", timed)
	}
	if len(subs) < 2 || subs[1].ID != wrapSyncID(same, syncHash, 1) {
		t.Fatalf("the same release's subtitle should rank right after the hash match: %+v", subs)
	}
}

// A provisional fit stays served while a re-fit only moves cues without matching clearly more
// lines (the cues moved back and forth between re-fits before); a clearly better one replaces it.
func TestKeepStableFit(t *testing.T) {
	file := dialogue(900, episode)
	sub := subtitleFor(file, 1, func(float64) float64 { return 2 })
	ws := speechWindows(file, gridStarts(0, 900), 0, 0.15, 0.2, 1)
	fit, ok := alignLive(sub, ws, syncScales[:1])
	if !ok || !fit.good() {
		t.Fatalf("fit %s", describeFit(fit))
	}
	m := syncMeta{State: syncStateSynced, Fit: fit, Live: true}
	base := vttOf(sub)
	// the served fit differs by a nudge of one stretch: kept
	o := fit.Segments[0].Offset
	nudged := syncMeta{State: syncStateSynced, Live: true, Fit: syncFit{Scale: 1, Segments: []syncSegment{{Offset: o}, {From: 600, Offset: o + 0.06}}}}
	if !keepStable(&nudged, m, base, episode, false) {
		t.Fatal("a nudge replaced the served fit")
	}
	// the served fit is 1.5 s off: replaced
	off := syncMeta{State: syncStateSynced, Live: true, Fit: syncFit{Scale: 1, Segments: []syncSegment{{Offset: o + 1.5}}}}
	if keepStable(&off, m, base, episode, false) {
		t.Fatal("a fit 1.5 s off was kept")
	}
	// once the file is covered the new result counts
	if keepStable(&nudged, m, base, episode, true) {
		t.Fatal("kept a provisional fit of a covered file")
	}
}
