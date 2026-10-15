package streaming

import (
	"context"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func shortStart(t *testing.T, ahead, steady, gap time.Duration) {
	oldAhead, oldSteady, oldGap := tsStartAhead, tsStartSteady, tsStartGap
	tsStartAhead, tsStartSteady, tsStartGap = ahead, steady, gap
	t.Cleanup(func() { tsStartAhead, tsStartSteady, tsStartGap = oldAhead, oldSteady, oldGap })
}

// A stream is past its start once it delivered tsStartAhead of media ahead of real time (at
// the file's rate); a stream that flows without gaps but falls behind still starts.
func TestStartEndsWhenAhead(t *testing.T) {
	ms := time.Millisecond
	shortQuiet(t, time.Minute)
	shortStart(t, 30*time.Second, 50*ms, time.Second)
	s := NewService(nil, Config{})
	other := hx('o')
	st := s.playBegin(hx('p'))
	defer s.playDone(hx('p'), st)
	st.rate.Store(1000) // bytes per second of media
	w := &tsFirstWrite{w: discard{}, stream: st}
	for range 8 {
		_, _ = w.Write(make([]byte, 1)) // a trickle: far behind real time
		time.Sleep(10 * ms)
	}
	if s.startingStream(other) != hx('p') {
		t.Fatal("a stream that falls behind got past its start")
	}
	_, _ = w.Write(make([]byte, 31*1000)) // 31 s of media
	if s.startingStream(other) != "" {
		t.Fatal("a stream 30 s ahead still holds background work")
	}
	// A seek after that is a new start
	st2 := s.playBegin(hx('p'))
	defer s.playDone(hx('p'), st2)
	if s.startingStream(other) != hx('p') {
		t.Fatal("a seek after the start is not a new start")
	}
}

// Without a rate (re-encoded video) the start ends after tsStartSteady of flow without a gap;
// a gap starts the count again.
func TestStartGapRestartsSteadyCount(t *testing.T) {
	ms := time.Millisecond
	shortQuiet(t, time.Minute)
	shortStart(t, time.Minute, 100*ms, 40*ms)
	s := NewService(nil, Config{})
	other := hx('o')
	st := s.playBegin(hx('p'))
	defer s.playDone(hx('p'), st)
	w := &tsFirstWrite{w: discard{}, stream: st}
	flow := func(d time.Duration) {
		for end := time.Now().Add(d); time.Now().Before(end); time.Sleep(10 * ms) {
			_, _ = w.Write([]byte{1})
		}
	}
	flow(70 * ms)
	time.Sleep(60 * ms) // a stall
	if s.startingStream(other) != hx('p') {
		t.Fatal("a stalled stream got past its start")
	}
	flow(60 * ms)
	if s.startingStream(other) != hx('p') {
		t.Fatal("the flow before the gap still counted")
	}
	flow(60 * ms)
	if s.startingStream(other) != "" {
		t.Fatal("a stream that flows steadily still holds background work")
	}
}

// A torrent whose stream keeps restarting (stalls, silent restarts) holds background work for
// tsStartQuietMax from its first stream, not from every restart.
func TestStartCapSpansRestarts(t *testing.T) {
	ms := time.Millisecond
	shortQuiet(t, 120*ms)
	s := NewService(nil, Config{})
	other := hx('o')
	s.playBegin(hx('p'))
	time.Sleep(70 * ms)
	s.playEnd(hx('p'))
	s.playBegin(hx('p')) // the restart
	defer s.playEnd(hx('p'))
	if s.startingStream(other) != hx('p') {
		t.Fatal("the restart does not start")
	}
	time.Sleep(70 * ms)
	if s.startingStream(other) != "" {
		t.Fatal("restarts hold background work beyond tsStartQuietMax")
	}
	// The stall watchdog restarts the starving stream again: no new start
	s.playEnd(hx('p'))
	st := s.playBegin(hx('p'))
	if s.startingStream(other) != "" {
		t.Fatal("a restart after the timeout holds background work again")
	}
	// Once a stream got healthy, a seek is a new start
	st.rate.Store(1000)
	_, _ = (&tsFirstWrite{w: discard{}, stream: st}).Write(make([]byte, 40*1000))
	s.startingStream(other)
	s.playEnd(hx('p'))
	s.playBegin(hx('p'))
	if s.startingStream(other) != hx('p') {
		t.Fatal("a seek after a healthy stream does not start anew")
	}
}

// The media rate of a stream is what it carries: the copied video and the copied audio track
// it plays (not the file's other tracks); the file's bitrate only without per-stream rates.
func TestStreamRateOfItsTracks(t *testing.T) {
	p := &tsProbe{Streams: []tsProbeStream{
		{CodecType: "video", CodecName: "h264", BitRate: "8000000"},
		{CodecType: "audio", CodecName: "truehd", Tags: map[string]string{"BPS-eng": "4000000"}},
		{CodecType: "audio", CodecName: "ac3", BitRate: "640000"},
	}}
	p.Format.BitRate = "20000000"
	copyV := tsVideoPlan{copy: true}
	for _, c := range []struct {
		name  string
		plan  tsVideoPlan
		aplan tsAudioPlan
		audio int
		track tsTrack
		want  int64
	}{
		{"muxed, copied audio 1", copyV, tsAudioPlan{copy: true}, 1, tsTrackBoth, (8000000 + 640000) / 8},
		{"muxed, re-encoded audio", copyV, tsAudioPlan{}, 0, tsTrackBoth, 8000000 / 8},
		{"split video", copyV, tsAudioPlan{copy: true}, 0, tsTrackVideo, 8000000 / 8},
		{"split audio (BPS tag)", tsVideoPlan{}, tsAudioPlan{copy: true}, 0, tsTrackAudio, 4000000 / 8},
		{"re-encoded video", tsVideoPlan{}, tsAudioPlan{copy: true}, 0, tsTrackBoth, 0},
	} {
		st := &tsPlayStream{}
		st.setRate(p, c.plan, c.aplan, c.audio, c.track)
		if got := st.rate.Load(); got != c.want {
			t.Errorf("%s: rate %d, want %d", c.name, got, c.want)
		}
	}
	p.Streams[0].BitRate = ""
	st := &tsPlayStream{}
	st.setRate(p, copyV, tsAudioPlan{copy: true}, 0, tsTrackBoth)
	if st.rate.Load() != 20000000/8 {
		t.Fatalf("without a video rate: %d, want the file's", st.rate.Load())
	}
}

// While a player waits for a new resolve of the title (a step down, a handover with avoid),
// the title's background work from the earlier resolve waits; its foreground work, other
// titles and a resolve nobody waits for do not hold it.
func TestBackgroundWaitsForPlayerResolve(t *testing.T) {
	s, _ := newWarmService(t)
	q := tsResolveQuery{mediaType: "movie", title: "Night of the Living Dead", year: 1968, hevc: true}
	key := tsResolveKey(q)
	w := s.holdWarm(key)
	bg := tsBackground(w.ctx)
	step := q
	step.maxQ = 1080
	release := make(chan struct{})
	f, _ := s.flights.start(tsResolveKey(step), func() (*tsResolveResult, error) {
		<-release
		return nil, errNoSource
	})
	defer close(release)
	if other, _ := s.yieldReason(bg, hx('a')); other != "" {
		t.Fatalf("waits for %q while nobody waits for the resolve", other)
	}
	f.waiters.Add(1)
	defer f.waiters.Add(-1)
	if other, why := s.yieldReason(bg, hx('a')); other == "" || !strings.Contains(why, "resolves") {
		t.Fatalf("background work goes on next to the player's resolve (%q %q)", other, why)
	}
	if other, _ := s.yieldReason(w.ctx, hx('a')); other != "" {
		t.Fatal("foreground work waits for the player's resolve")
	}
	ow := s.holdWarm(tsResolveKey(tsResolveQuery{mediaType: "movie", title: "Carnival of Souls", year: 1962}))
	if other, _ := s.yieldReason(tsBackground(ow.ctx), hx('b')); other != "" {
		t.Fatal("another title waits for the player's resolve")
	}
}

// The subtitle timing's reads wait for any starting stream, the file's own included.
func TestWaitStartQuietOwnStream(t *testing.T) {
	ms := time.Millisecond
	shortQuiet(t, 80*ms)
	s := NewService(nil, Config{})
	s.playBegin(hx('p'))
	defer s.playEnd(hx('p'))
	start := time.Now()
	if !s.WaitStartQuiet(context.Background(), strings.ToUpper(hx('p'))) || time.Since(start) < 60*ms {
		t.Fatalf("the read went on after %s while its own stream started", time.Since(start))
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if s.WaitStartQuiet(ctx, hx('p')) {
		t.Fatal("a cancelled wait reports success")
	}
}

// Once resolve answered, the duration probe of a candidate that was not picked stops while
// another torrent starts and runs again afterwards.
func TestDurationProbeYieldsToStart(t *testing.T) {
	ms := time.Millisecond
	shortQuiet(t, time.Minute)
	s := NewService(nil, Config{})
	var calls, cancelled atomic.Int32
	s.probeFn = func(ctx context.Context, hash string, index int) (*tsProbe, error) {
		if calls.Add(1) == 1 {
			<-ctx.Done() // a slow header
			cancelled.Add(1)
			return nil, ctx.Err()
		}
		p := &tsProbe{}
		p.Format.Duration = "6000"
		return p, nil
	}
	go func() {
		time.Sleep(50 * ms)
		s.probeBegin(hx('p')) // a player starts another torrent
		time.Sleep(150 * ms)
		s.probeEnd(hx('p'))
	}()
	c := tsCandidate{raw: JackettResult{Title: "Detour 1945 1080p"}, hash: hx('a'), quality: 1080}
	start := time.Now()
	dur, ok, err := s.checkDuration(tsBackground(context.Background()), c, tsResolveQuery{mediaType: "movie", title: "Detour", runtime: 100},
		hx('a'), 1, nil, nil)
	if err != nil || !ok || dur != 6000 {
		t.Fatalf("got %v %v %v", dur, ok, err)
	}
	if cancelled.Load() != 1 || calls.Load() != 2 || time.Since(start) < 190*ms {
		t.Fatalf("probes %d (cancelled %d) in %s, want the first stopped and one after the start",
			calls.Load(), cancelled.Load(), time.Since(start))
	}
}
