package subtitles

import (
	"context"
	"encoding/binary"
	"errors"
	"math"
	"math/rand"
	"net/http"
	"net/http/httptest"
	"sort"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
)

// speechPCM is 8 kHz s16le PCM of [from, from+length) seconds of a file whose dialogue is lines:
// voiced bursts (125 Hz with harmonics, a syllable rhythm) over room noise and a bass bed.
func speechPCM(lines []span, from, length float64, seed int64) []byte {
	const sr = syncSampleRate
	var voice [64]float64 // one period at 125 Hz
	for i := range voice {
		for h := 1; h <= 12; h++ {
			voice[i] += 3000 / float64(h) * math.Sin(2*math.Pi*float64(h*i)/64)
		}
	}
	sorted := append([]span(nil), lines...)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i].start < sorted[j].start })
	rng := rand.New(rand.NewSource(seed))
	n := int(length * sr)
	out := make([]byte, 2*n)
	j := 0
	first := int64(math.Round(from * sr))
	for i := 0; i < n; i++ {
		s := first + int64(i)
		t := float64(s) / sr
		for j < len(sorted) && sorted[j].end <= t {
			j++
		}
		v := rng.NormFloat64()*40 + 600*math.Sin(2*math.Pi*50*t)
		if j < len(sorted) && sorted[j].start <= t {
			v += (0.6 + 0.4*math.Sin(2*math.Pi*4*t)) * voice[s%64]
		}
		binary.LittleEndian.PutUint16(out[2*i:], uint16(int16(math.Max(-32768, math.Min(32767, v)))))
	}
	return out
}

// feed writes PCM to a sink in 1 s pieces from byte offset at.
func feed(s *SpeechSink, pcm []byte, at int64) {
	for i := 0; i < len(pcm); i += 16000 {
		s.Write(at+int64(i), pcm[i:min(i+16000, len(pcm))])
	}
}

// play opens a tap at from and feeds length seconds of the file's dialogue.
func play(t *testing.T, svc *Service, file []span, from, length float64) *SpeechSink {
	t.Helper()
	s := svc.OpenSpeech(syncHash, 1, from)
	if s == nil {
		t.Fatalf("no tap at %.0f", from)
	}
	feed(s, speechPCM(file, from, length, int64(from)), 0)
	return s
}

// newLiveTest is a service with live timing whose file is too dear to read (bitrate above the
// byte budget), so only the played audio times its subtitles. Re-fits run when the test asks.
func newLiveTest(t *testing.T, duration float64) *Service {
	saved := syncReadDelay
	syncReadDelay = 0
	t.Cleanup(func() { syncReadDelay = saved })
	svc := NewService(nil, Config{CacheDir: t.TempDir(), TorrServerURL: "http://torrserver", LiveTiming: true})
	svc.sync.probe = func(context.Context, string) (mediaProbe, error) {
		return mediaProbe{Duration: duration, FPS: 23.976, Audio: 1, OwnSub: -1, SubCodec: "none", Bitrate: 2 << 20}, nil
	}
	svc.sync.read = func(context.Context, string, mediaProbe, float64, float64) (refWindow, error) {
		t.Error("a window was read from a file above the byte budget")
		return refWindow{}, errors.New("no")
	}
	svc.sync.onGrow = func(*liveFile) {}
	return svc
}

func onsets(w refWindow) []float64 {
	var out []float64
	for i := 1; i < len(w.Speech); i++ {
		if w.Speech[i] != 0 && w.Speech[i-1] == 0 {
			out = append(out, w.Start+float64(i)/syncRate)
		}
	}
	return out
}

// Speech heard by a tap that starts between frames lands at the file's times.
func TestLiveSpeechTimestamps(t *testing.T) {
	svc := newLiveTest(t, 600)
	lines := []span{{40, 41.5}, {44.5, 46.5}, {50.2, 52}, {57, 58.2}, {63.3, 65}, {70.1, 72}, {78.4, 80}, {85, 87},
		{93, 95}, {101, 102.5}, {108, 110}, {115, 117}, {122, 124}}
	s := play(t, svc, lines, 37.25, 90)
	s.Close()
	ws, covered := svc.sync.liveFile(syncHash, 1).windows()
	if len(ws) != 2 || ws[0].Start != 37.25 || ws[1].Start != 60 || len(ws[1].Speech) != liveWindowFrames ||
		math.Abs(covered-82.75) > 0.02 {
		t.Fatalf("%d windows, covered %.2f: %+v", len(ws), covered, ws)
	}
	for _, l := range lines {
		for _, w := range ws {
			if l.start <= w.Start+0.5 || l.start >= w.end() {
				continue
			}
			found := false
			for _, o := range onsets(w) {
				found = found || math.Abs(o-l.start) <= 0.03
			}
			if !found {
				t.Fatalf("line at %.2f: onsets %v", l.start, onsets(w))
			}
		}
	}
	for _, w := range ws {
		for _, o := range onsets(w) {
			near := false
			for _, l := range lines {
				near = near || math.Abs(o-l.start) <= 0.05
			}
			if !near {
				t.Fatalf("speech at %.2f without a line", o)
			}
		}
	}
}

// Taps of several play streams (overlapping, out of order, with a gap) merge on the grid.
func TestLiveCoverageMerges(t *testing.T) {
	svc := newLiveTest(t, 2400)
	file := dialogue(71, 2400)
	a := play(t, svc, file, 300, 100)
	b := play(t, svc, file, 200, 150)
	c := svc.OpenSpeech(syncHash, 1, 500)
	pcm := speechPCM(file, 500, 30, 1)
	feed(c, pcm[:10*16000], 0)
	feed(c, pcm[12*16000:], 12*16000) // 2 s dropped by the tap
	for _, s := range []*SpeechSink{a, b, c} {
		s.Close()
	}
	if c.gaps != 2*16000 {
		t.Fatalf("gap %d bytes", c.gaps)
	}
	f := svc.sync.liveFile(syncHash, 1)
	ws, covered := f.windows()
	var starts []float64
	for _, w := range ws {
		starts = append(starts, w.Start)
	}
	// finished: 240-300, 300-360; parts: 200-240, 360-400, 500-530 (with the gap)
	want := []float64{200, 240, 300, 360, 500}
	if len(starts) != len(want) || math.Abs(covered-230) > 0.05 {
		t.Fatalf("starts %v covered %.2f", starts, covered)
	}
	for i := range want {
		if math.Abs(starts[i]-want[i]) > 0.01 {
			t.Fatalf("starts %v", starts)
		}
	}
	if len(f.done) != 2 || f.done[4].Frames != liveWindowFrames || f.done[5].Frames != liveWindowFrames {
		t.Fatalf("done %v", f.done)
	}
	// Kept on disk and read back by a new process
	svc.sync.saveLive(f)
	again := NewService(nil, Config{CacheDir: svc.cfg.CacheDir, LiveTiming: true})
	g := again.sync.liveFile(syncHash, 1)
	if len(g.done) != 2 || string(g.done[5].Bits) != string(f.done[5].Bits) {
		t.Fatalf("reloaded %d windows", len(g.done))
	}
}

// liveWindowsOf are live windows of the file's true dialogue (speech on/off, no marked starts).
func liveWindowsOf(file []span, starts []float64, noise float64, seed int64) []refWindow {
	ws := truthWindows(file, starts, noise, 0, seed)
	for i := range ws {
		ws[i].Kind = "live"
		for j, v := range ws[i].Speech {
			ws[i].Speech[j] = min(v, 1)
		}
	}
	return ws
}

func gridStarts(from, to float64) []float64 {
	var out []float64
	for s := from; s+syncWindowLen <= to; s += syncWindowLen {
		out = append(out, s)
	}
	return out
}

func breakFile(seed int64, length, at float64) []span {
	var file []span
	for _, c := range dialogue(seed, length) {
		if c.end < at-2 || c.start > at+15 {
			file = append(file, c)
		}
	}
	return file
}

// Contiguous windows over a whole episode (live timing) must not turn detector noise into
// offset changes, and must still find a real one.
func TestAlignDenseWindowsNoSpuriousChanges(t *testing.T) {
	for seed := int64(1); seed <= 4; seed++ {
		file := dialogue(80+seed, episode)
		sub := subtitleFor(file, 1, func(float64) float64 { return 2.3 })
		f, ok := alignLive(sub, liveWindowsOf(file, gridStarts(0, episode), 0.25, seed), syncScales)
		if !ok || !f.good() || len(f.Segments) != 1 || math.Abs(f.Segments[0].Offset-2.3) > 0.1 {
			t.Fatalf("seed %d constant: %s", seed, describeFit(f))
		}
		file = breakFile(90+seed, episode, 1300)
		sub = subtitleFor(file, 1, func(u float64) float64 {
			if u < 1305 {
				return -1.5
			}
			return -3.5
		})
		f, ok = alignLive(sub, liveWindowsOf(file, gridStarts(0, episode), 0.25, seed), syncScales)
		if !ok || !f.good() || len(f.Segments) != 2 || math.Abs(f.Segments[0].Offset+1.5) > 0.1 ||
			math.Abs(f.Segments[1].Offset+3.5) > 0.1 || mappedWithin(f, sub, file, 0.15) < 0.95 {
			t.Fatalf("seed %d split: %s", seed, describeFit(f))
		}
	}
}

// A few minutes of live windows (as soon as a fit may use them) never fit another episode's
// subtitle, and fit the right one.
func TestAlignFewLiveWindows(t *testing.T) {
	right, wrong, tried := 0, 0, 0
	for i := int64(0); i < 60; i++ {
		rng := rand.New(rand.NewSource(i))
		file := dialogue(200+i, episode)
		length := float64(4+i%5) * syncWindowLen
		from := math.Round(rng.Float64() * (episode - length - 60))
		ws := liveWindowsOf(file, gridStarts(from, from+length), 0.25, i)
		f, ok := alignLive(subtitleFor(file, 1, func(float64) float64 { return 4 }), ws, syncScales)
		if !ok {
			continue // too few line starts yet
		}
		tried++
		if f.good() && math.Abs(offsetAt(f.Segments, from+length/2)-4) < 0.2 {
			right++
		}
		if f, ok := alignLive(dialogue(300+i, episode), ws, syncScales); ok && f.good() {
			wrong++
		}
	}
	if wrong > 0 || tried < 25 || right < tried*9/10 {
		t.Fatalf("right %d/%d, another episode fitted %d times", right, tried, wrong)
	}
}

func lineShare(cues []liveCue, file []span, tol float64, in func(span) bool) float64 {
	n, ok := 0, 0
	for i, c := range file {
		if !in(c) {
			continue
		}
		n++
		if math.Abs(cues[i].start-c.start) <= tol {
			ok++
		}
	}
	return float64(ok) / float64(max(n, 1))
}

// The S02E10 case: a file above the byte budget is timed from what plays. The subtitle is
// 1.5 s late before an ad break and 3.5 s after it; the answers stay partial while the file
// plays and are right in the played part, and final once the whole file was heard.
func TestLiveRetimesProgressively(t *testing.T) {
	const length = 1500.0
	svc := newLiveTest(t, length)
	file := breakFile(7, length, 700)
	sub := subtitleFor(file, 1, func(u float64) float64 {
		if u < 708 {
			return -1.5
		}
		return -3.5
	})
	inner := cacheProviderSub(t, svc, "1|live|2|10|1", sub)
	id := wrapSyncID(inner, syncHash, 1)
	ctx := context.Background()

	a, err := svc.FileFor(ctx, id, 0)
	if err != nil || !a.Partial || a.Sync != syncStatePending {
		t.Fatalf("first answer partial=%v sync=%q err=%v", a.Partial, a.Sync, err)
	}
	f := svc.sync.liveFile(syncHash, 1)

	// The first 7 minutes play (the player buffered ahead to 420 s)
	first := play(t, svc, file, 0, 420)
	svc.sync.refitFile(f)
	a, _ = svc.FileFor(ctx, id, 0)
	if !a.Partial || a.Sync != syncStateLive {
		t.Fatalf("after 420 s: partial=%v sync=%q", a.Partial, a.Sync)
	}
	got := vttCues(a.VTT)
	if len(got) != len(file) {
		t.Fatalf("%d cues, want %d", len(got), len(file))
	}
	if share := lineShare(got, file, 0.3, func(c span) bool { return c.start > 30 && c.start < 400 }); share < 0.9 {
		t.Fatalf("played part: only %.2f of the lines on time", share)
	}

	// A seek to 1000 s, then back to 400 s: the stretches overlap and come out of order
	first.Close()
	second := play(t, svc, file, 1000, 500)
	second.Close()
	third := play(t, svc, file, 400, 610)
	third.Close()
	svc.sync.refitFile(f)
	a, _ = svc.FileFor(ctx, id, 0)
	if a.Partial || a.Sync != syncStateSynced {
		t.Fatalf("covered: partial=%v sync=%q", a.Partial, a.Sync)
	}
	got = vttCues(a.VTT)
	if share := lineShare(got, file, 0.3, func(span) bool { return true }); share < 0.95 {
		t.Fatalf("only %.2f of the lines on time", share)
	}
	key, _ := syncKey(inner, syncHash, 1)
	m := svc.sync.meta(key)
	if m.Live || len(m.Fit.Segments) != 2 || math.Abs(m.Fit.Segments[0].Offset+1.5) > 0.3 ||
		math.Abs(m.Fit.Segments[1].Offset+3.5) > 0.3 || m.Fit.Segments[1].From < 690 || m.Fit.Segments[1].From > 725 {
		t.Fatalf("fit %s (live %v)", describeFit(m.Fit), m.Live)
	}
	if svc.OpenSpeech(syncHash, 1, 0) != nil {
		t.Fatal("a covered file is still tapped")
	}
}

// injectLive marks grid windows of the file as heard.
func injectLive(f *liveFile, file []span, windows []int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, w := range windows {
		ws := liveWindowsOf(file, []float64{float64(w) * syncWindowLen}, 0.1, int64(w))
		f.done[w] = liveWin{Bits: packBits(ws[0].Speech), Frames: liveWindowFrames}
		f.dirty = true
	}
}

// A timing whose read failed (10 min lock-out) is still timed from live audio, and the
// answers carry the live state.
func TestLiveTimesAFailedTiming(t *testing.T) {
	gin.SetMode(gin.TestMode)
	svc := newLiveTest(t, 0)
	svc.sync.probe = func(context.Context, string) (mediaProbe, error) { return mediaProbe{}, errors.New("unreadable") }
	file := dialogue(13, episode)
	inner := cacheProviderSub(t, svc, "1|f|2|10|1", subtitleFor(file, 1, func(float64) float64 { return 2.5 }))
	id := wrapSyncID(inner, syncHash, 1)
	r := gin.New()
	Register(r.Group("/api"), svc)
	get := func(id string) *httptest.ResponseRecorder {
		w := httptest.NewRecorder()
		r.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/api/subtitles/file?id="+id, nil))
		return w
	}
	if w := get(id); w.Header().Get("X-Subtitle-Sync") != syncStatePending || w.Header().Get("X-Subtitle-Partial") != "1" {
		t.Fatalf("first: sync=%q partial=%q", w.Header().Get("X-Subtitle-Sync"), w.Header().Get("X-Subtitle-Partial"))
	}
	key, _ := syncKey(inner, syncHash, 1)
	if !svc.sync.recentlyFailed(key) {
		t.Fatal("the read did not fail")
	}
	tap := svc.OpenSpeech(syncHash, 1, 600) // playing
	f := svc.sync.liveFile(syncHash, 1)
	injectLive(f, file, []int{10, 11, 12, 13, 14, 15, 16, 17})
	svc.sync.refitFile(f)
	w := get(id)
	if w.Header().Get("X-Subtitle-Sync") != syncStateLive || w.Header().Get("X-Subtitle-Partial") != "1" ||
		w.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("live: sync=%q partial=%q cache=%q", w.Header().Get("X-Subtitle-Sync"),
			w.Header().Get("X-Subtitle-Partial"), w.Header().Get("Cache-Control"))
	}
	if got := vttCues(w.Body.Bytes()); len(got) != len(file) || math.Abs(got[100].start-file[100].start) > 0.1 {
		t.Fatal("not the timed cues")
	}
	if !svc.files.has("synclive", f.fk, ".json") {
		t.Fatal("live windows of a file with a timed subtitle not kept")
	}
	// Not playing any more: the provisional result is not partial (the players stop asking)
	tap.Close()
	f.mu.Lock()
	f.lastTap = time.Now().Add(-2 * livePlayingFor)
	f.mu.Unlock()
	svc.sync.mu.Lock()
	svc.sync.served[key].first = time.Now().Add(-time.Hour)
	svc.sync.mu.Unlock()
	if w := get(id); w.Header().Get("X-Subtitle-Sync") != syncStateLive || w.Header().Get("X-Subtitle-Partial") != "" {
		t.Fatalf("idle: sync=%q partial=%q", w.Header().Get("X-Subtitle-Sync"), w.Header().Get("X-Subtitle-Partial"))
	}
	// A subtitle that is never timed says so
	if w := get(inner); w.Header().Get("X-Subtitle-Sync") != syncStateOriginal {
		t.Fatalf("plain: sync=%q", w.Header().Get("X-Subtitle-Sync"))
	}
}

// The S02E10 sequence (real log): a provisional fit is accepted, a later re-fit is poor, then
// two offsets fit. The accepted fit stays served meanwhile (no alternatives tried), and the
// later good fit with other offsets replaces it.
func TestLiveKeepsAnAcceptedFit(t *testing.T) {
	svc := newLiveTest(t, episode)
	file := breakFile(29, episode, 200)
	sub := subtitleFor(file, 1, func(u float64) float64 {
		if u < 205 {
			return 0.66
		}
		return 3
	})
	inner := cacheProviderSub(t, svc, "301213|ta|2|10|1", sub)
	alt := encodeID(SourceTurkceAltyazi, "tr", "301216|ta|2|10|1") // not on disk: a fetch would fail
	svc.sync.listed([]Sub{{ID: inner, Source: SourceTurkceAltyazi, Language: "tr"},
		{ID: alt, Source: SourceTurkceAltyazi, Language: "tr"}}, map[string]bool{}, syncHash, 1)
	id := wrapSyncID(inner, syncHash, 1)
	key, _ := syncKey(inner, syncHash, 1)
	altKey, _ := syncKey(alt, syncHash, 1)
	ctx := context.Background()
	svc.FileFor(ctx, id, 0)
	f := svc.sync.liveFile(syncHash, 1)
	set := func(src []span, windows []int) {
		f.mu.Lock()
		f.done = map[int]liveWin{}
		f.mu.Unlock()
		injectLive(f, src, windows)
		svc.sync.refitFile(f)
	}

	// 1. The teaser (before the change at ~205 s): one offset, accepted
	set(file, []int{0, 1, 2})
	a, _ := svc.FileFor(ctx, id, 0)
	m := svc.sync.meta(key)
	if a.Sync != syncStateLive || !m.Live || len(m.Fit.Segments) != 1 || math.Abs(m.Fit.Segments[0].Offset-0.66) > 0.1 {
		t.Fatalf("step 1: %q %s", a.Sync, describeFit(m.Fit))
	}
	accepted := string(a.VTT)

	// 2. A re-fit that does not fit (here: windows of another episode): the accepted fit stays
	set(dialogue(31, episode), []int{0, 1, 2, 3, 4, 5})
	a, _ = svc.FileFor(ctx, id, 0)
	if a.Sync != syncStateLive || string(a.VTT) != accepted || svc.sync.meta(altKey) != nil {
		t.Fatalf("step 2: %q, same cues %v, alternative tried %v", a.Sync, string(a.VTT) == accepted, svc.sync.meta(altKey) != nil)
	}

	// 3. Seven windows over the change: two offsets fit and replace the kept fit
	set(file, []int{0, 1, 2, 3, 4, 5, 6})
	a, _ = svc.FileFor(ctx, id, 0)
	m = svc.sync.meta(key)
	if a.Sync != syncStateLive || len(m.Fit.Segments) != 2 || math.Abs(m.Fit.Segments[0].Offset-0.66) > 0.1 ||
		math.Abs(m.Fit.Segments[1].Offset-3) > 0.1 || string(a.VTT) == accepted {
		t.Fatalf("step 3: %q %s", a.Sync, describeFit(m.Fit))
	}
}

// Shaped like S02E10: +0.66 s in the teaser, +3.00 s after the title sequence (~214 s), the
// played audio arriving a minute at a time. The change is found within 5 windows and once a
// timing was served it never falls back to the untimed subtitle.
func TestLiveFindsTheTitleSequenceEarly(t *testing.T) {
	svc := newLiveTest(t, episode)
	file := breakFile(37, episode, 200)
	sub := subtitleFor(file, 1, func(u float64) float64 {
		if u < 205 {
			return 0.66
		}
		return 3
	})
	inner := cacheProviderSub(t, svc, "301213|shape|2|10|1", sub)
	id := wrapSyncID(inner, syncHash, 1)
	key, _ := syncKey(inner, syncHash, 1)
	ctx := context.Background()
	svc.FileFor(ctx, id, 0)
	f := svc.sync.liveFile(syncHash, 1)
	tap := svc.OpenSpeech(syncHash, 1, 0)
	defer tap.Close()
	found, served := 0, false
	for minute := 1; minute <= 10; minute++ {
		at := float64(minute-1) * 60
		feed(tap, speechPCM(file, at, 60, int64(minute)), int64(at*2*syncSampleRate))
		svc.sync.refitFile(f)
		a, _ := svc.FileFor(ctx, id, 0)
		m := svc.sync.meta(key)
		if served && a.Sync != syncStateLive {
			t.Fatalf("minute %d: fell back to %q", minute, a.Sync)
		}
		served = served || a.Sync == syncStateLive
		if m != nil && len(m.Fit.Segments) == 2 && math.Abs(m.Fit.Segments[0].Offset-0.66) < 0.2 &&
			math.Abs(m.Fit.Segments[1].Offset-3) < 0.2 && m.Fit.Segments[1].From > 190 && m.Fit.Segments[1].From < 225 {
			if found == 0 {
				found = minute
			}
		} else if found > 0 {
			t.Fatalf("minute %d: lost the change: %s", minute, describeFit(m.Fit))
		}
		if m != nil {
			t.Logf("minute %d: %s %s", minute, a.Sync, describeFit(m.Fit))
		}
	}
	if found == 0 || found > 5 {
		t.Fatalf("change found at minute %d", found)
	}
}

// Live audio re-fits by itself (debounced) once enough of it came in.
func TestLiveRefitIsScheduled(t *testing.T) {
	svc := newLiveTest(t, episode)
	svc.sync.refitDelay, svc.sync.refitGap, svc.sync.pendingGap = 10*time.Millisecond, 0, 0
	svc.sync.onGrow = svc.sync.scheduleRefit
	file := dialogue(17, episode)
	inner := cacheProviderSub(t, svc, "1|s|2|10|1", subtitleFor(file, 1, func(float64) float64 { return -2 }))
	id := wrapSyncID(inner, syncHash, 1)
	if a, _ := svc.FileFor(context.Background(), id, 0); a.Sync != syncStatePending {
		t.Fatalf("first: %q", a.Sync)
	}
	s := play(t, svc, file, 100, 260)
	deadline := time.Now().Add(20 * time.Second)
	for {
		a, _ := svc.FileFor(context.Background(), id, 0)
		if a.Sync == syncStateLive && a.Partial {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("no live timing: %q", a.Sync)
		}
		time.Sleep(20 * time.Millisecond)
	}
	s.Close()
	f := svc.sync.liveFile(syncHash, 1)
	for { // the re-fit Close asked for ends before the test's temp dir goes
		f.mu.Lock()
		busy := f.refit
		f.mu.Unlock()
		if !busy {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// Without live timing (no tap wired) results are final as before, and a file above the byte
// budget is served as it is.
func TestNoLiveTimingWithoutTap(t *testing.T) {
	svc := newLiveTest(t, episode)
	svc.cfg.LiveTiming = false
	if svc.OpenSpeech(syncHash, 1, 0) != nil {
		t.Fatal("tap opened without live timing")
	}
	inner := cacheProviderSub(t, svc, "1|n|2|10|1", dialogue(19, episode))
	a, _ := svc.FileFor(context.Background(), wrapSyncID(inner, syncHash, 1), 0)
	if a.Partial || a.Sync != syncStateFailed {
		t.Fatalf("partial=%v sync=%q", a.Partial, a.Sync)
	}
}

func TestMergeWindows(t *testing.T) {
	w := func(start float64, kind string) refWindow {
		return refWindow{Start: start, Kind: kind, Speech: make([]byte, liveWindowFrames)}
	}
	got := mergeWindows([]refWindow{w(20, "audio"), w(400, "audio"), w(130, "subs"), w(130, "subs"), w(400, "audio")},
		[]refWindow{w(0, "live"), w(120, "live"), w(180, "live")})
	var kinds []string
	for _, x := range got {
		kinds = append(kinds, x.Kind+":"+string(rune('0'+int(x.Start/60))))
	}
	// audio@20 loses to live@0, live@120 to subs@130, audio@400 has no live window; a window
	// read twice counts once
	want := []string{"live:0", "subs:2", "live:3", "audio:6"}
	if len(kinds) != len(want) {
		t.Fatalf("%v", kinds)
	}
	for i := range want {
		if kinds[i] != want[i] {
			t.Fatalf("%v", kinds)
		}
	}
}

// A stream that hears less than a minute ahead of the viewer (muxed 4K plays at its pace) re-fits
// every 10 s of audio, like an untimed viewing's first minutes; one far ahead (the TV's split
// stream), a closed one or an old position every 30 s. The player's position is the one its
// subtitle requests sent; with two streams the one nearest to the viewer counts, whichever heard last.
func TestNearViewerRefitsSooner(t *testing.T) {
	y := newLiveTest(t, episode).sync
	f := y.liveFile(syncHash, 1)
	y.noteServed("k", "sub", syncHash, 1, 960)
	step := func() float64 {
		v := y.fileViewer(f.fk)
		f.mu.Lock()
		defer f.mu.Unlock()
		return f.growStep(v)
	}
	hear := func(s *SpeechSink, at float64) {
		f.add(s, []liveFrame{{at: int(at * syncRate), v: 1}}, -1, -1)
	}
	near, far := &SpeechSink{}, &SpeechSink{}
	hear(near, 1000)
	if got := step(); got != livePendingGrowth {
		t.Fatalf("40 s ahead: %.0f s", got)
	}
	hear(far, 1100)
	if got := step(); got != livePendingGrowth {
		t.Fatalf("40 and 140 s ahead: %.0f s", got)
	}
	f.mu.Lock()
	delete(f.heads, near)
	f.mu.Unlock()
	if got := step(); got != liveRefitGrowth {
		t.Fatalf("140 s ahead: %.0f s", got)
	}
	f.mu.Lock()
	delete(f.heads, far)
	f.mu.Unlock()
	if got := step(); got != liveRefitGrowth {
		t.Fatalf("no stream: %.0f s", got)
	}
	hear(near, 1000)
	y.mu.Lock()
	y.served["k"].last = time.Now().Add(-3 * time.Minute)
	y.mu.Unlock()
	if got := step(); got != liveRefitGrowth {
		t.Fatalf("an old position: %.0f s", got)
	}
}
