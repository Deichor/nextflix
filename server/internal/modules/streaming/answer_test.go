package streaming

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func shortQuiet(t *testing.T, d time.Duration) {
	old, oldMax, oldPoll := tsStartQuiet, tsStartQuietMax, tsQuietPoll
	tsStartQuiet, tsStartQuietMax, tsQuietPoll = d, d, 10*time.Millisecond
	t.Cleanup(func() { tsStartQuiet, tsStartQuietMax, tsQuietPoll = old, oldMax, oldPoll })
}

// pickWith runs tsPickBestKeep over checks that set their snapshots and return after a delay.
func pickWith(t *testing.T, n int, grace time.Duration, check func(ctx context.Context, i int, live *tsLive) (*tsResolveResult, error)) (*tsPick, time.Duration) {
	t.Helper()
	live := newLive(n)
	start := time.Now()
	p := tsPickBestKeep(context.Background(), nil, n, func(int) time.Duration { return grace }, nil,
		func(ctx context.Context, i int) (*tsResolveResult, error) { return check(ctx, i, live) }, live)
	return p, time.Since(start)
}

func waitOr(ctx context.Context, d time.Duration) error {
	select {
	case <-time.After(d):
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// Charade: a 1080p keeps up with its duration verified while the better-ranked 4K checks
// still run far below their need. Resolve answers at once instead of waiting for a grace
// (or the cap); the 4K is left to the upgrade path.
func TestGoodAnswersAtOnceWithoutPromisingBetter(t *testing.T) {
	ms := time.Millisecond
	shortCap(t, 2*time.Second)
	good := measured("hd", "1080p", 5.46, 1.24)
	p, took := pickWith(t, 3, time.Second, func(ctx context.Context, i int, live *tsLive) (*tsResolveResult, error) {
		switch i {
		case 0:
			live.set(0, snap("4k", "2160p", 0.3*3, 3, 2*time.Second)) // 0.3 of its need
		case 1:
			live.set(1, snap("4k-b", "2160p", 0, 3, -1)) // metadata only, nothing measured
		case 2:
			if err := waitOr(ctx, 20*ms); err != nil {
				return nil, err
			}
			return good, nil
		}
		<-ctx.Done()
		return nil, ctx.Err()
	})
	if p.best != 2 || p.res != good || took > 300*ms {
		t.Fatalf("got #%d after %s, want the good 1080p at once", p.best, took)
	}
}

// A better-ranked 4K delivering 0.8 of its need so far gets its grace and wins once it keeps up.
func TestGoodWaitsForPromisingBetter(t *testing.T) {
	ms := time.Millisecond
	shortCap(t, 2*time.Second)
	uhd := measured("4k", "2160p", 4, 3)
	p, took := pickWith(t, 2, 500*ms, func(ctx context.Context, i int, live *tsLive) (*tsResolveResult, error) {
		if i == 0 {
			live.set(0, snap("4k", "2160p", 0.9*3, 3, 2*time.Second))
			if err := waitOr(ctx, 150*ms); err != nil {
				return nil, err
			}
			return uhd, nil
		}
		if err := waitOr(ctx, 20*ms); err != nil {
			return nil, err
		}
		return measured("hd", "1080p", 4, 1), nil
	})
	if p.best != 0 || p.res != uhd || took < 120*ms || took > 450*ms {
		t.Fatalf("got #%d after %s, want the promising 4K once it keeps up", p.best, took)
	}
}

// A promise that fades (the 4K's rate drops below 0.8 of its need) ends the grace at once.
func TestGoodAnswersWhenPromiseFades(t *testing.T) {
	ms := time.Millisecond
	shortCap(t, 2*time.Second)
	good := measured("hd", "1080p", 4, 1)
	p, took := pickWith(t, 2, time.Second, func(ctx context.Context, i int, live *tsLive) (*tsResolveResult, error) {
		if i == 0 {
			live.set(0, snap("4k", "2160p", 0.9*3, 3, 2*time.Second))
			if err := waitOr(ctx, 100*ms); err != nil {
				return nil, err
			}
			live.set(0, snap("4k", "2160p", 0.4*3, 3, 3*time.Second))
			<-ctx.Done()
			return nil, ctx.Err()
		}
		if err := waitOr(ctx, 20*ms); err != nil {
			return nil, err
		}
		return good, nil
	})
	if p.best != 1 || p.res != good || took < 80*ms || took > 500*ms {
		t.Fatalf("got #%d after %s, want the 1080p once the 4K falls behind", p.best, took)
	}
}

// Night of the Living Dead: at the cap a dead torrent (0 peers, 0 rate) of unknown length
// was picked over a starving one with 4 active peers, for its lower need.
func TestPlayableNeverDeadOverAlive(t *testing.T) {
	starving := snap("amiab", "1080p", 0.0, 1.75, -1)
	starving.Health.Rate, starving.Health.ActivePeers, starving.Health.Seeders = 0, 4, 3
	dead := measured("tigole", "1080p", 0, 0.53)
	dead.Health.Dead, dead.Health.ActivePeers, dead.Health.Seeders = true, 0, 0
	dead.durationChecked = true
	deadUHD := measured("yts4k", "2160p", 0, 0.77)
	deadUHD.Health.Dead, deadUHD.Health.ActivePeers = true, 0
	if i := tsChoosePlayable([]*tsResolveResult{deadUHD, starving, dead}); i != 1 {
		t.Fatalf("got %d, want the starving one with peers", i)
	}
	// Silent but not judged dead yet: still loses to one with peers
	silent := snap("silent", "1080p", 0, 0.5, 3*time.Second)
	silent.Health.ActivePeers = 0
	if i := tsChoosePlayable([]*tsResolveResult{silent, starving}); i != 1 {
		t.Fatalf("got %d, want the one with peers over a silent swarm", i)
	}
}

// The pick's check stops measuring but finishes its duration check (the probe the player
// waits for); stop cancels everything.
func TestStopMeasureKeepsCheck(t *testing.T) {
	ms := time.Millisecond
	shortCap(t, 50*ms)
	measureStopped := make(chan struct{})
	finished := make(chan error, 1)
	live := newLive(1)
	p := tsPickBestKeep(context.Background(), nil, 1, func(int) time.Duration { return time.Second }, nil,
		func(ctx context.Context, i int) (*tsResolveResult, error) {
			live.set(0, snap("hd", "1080p", 0.5, 1, 3*time.Second))
			<-tsMeasureCtx(ctx).Done()
			close(measureStopped)
			finished <- ctx.Err()
			return measured("hd", "1080p", 0.5, 1), nil
		}, live)
	if p.best != 0 || !p.live {
		t.Fatalf("got #%d live=%v", p.best, p.live)
	}
	p.stopMeasure(0)
	<-measureStopped
	if err := <-finished; err != nil {
		t.Fatalf("the check itself was cancelled: %v", err)
	}
}

// A probe already running for a file is joined, not started twice; a failed one is retried
// by a caller that still has time.
func TestProbeJoinsRunningProbe(t *testing.T) {
	ms := time.Millisecond
	s := NewService(nil, Config{})
	var calls atomic.Int32
	release := make(chan struct{})
	s.probeFn = func(ctx context.Context, hash string, index int) (*tsProbe, error) {
		calls.Add(1)
		select {
		case <-release:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
		return &tsProbe{}, nil
	}
	var wg sync.WaitGroup
	errs := make(chan error, 3)
	for k := 0; k < 3; k++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, err := s.probeFileTimeout(context.Background(), hx('a'), 1, time.Second)
			errs <- err
		}()
	}
	time.Sleep(30 * ms)
	close(release)
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	if n := calls.Load(); n != 1 {
		t.Fatalf("%d ffprobe runs, want 1", n)
	}

	// The first caller's probe dies with its check; the player's probe runs its own
	calls.Store(0)
	s.probeFn = func(ctx context.Context, hash string, index int) (*tsProbe, error) {
		if calls.Add(1) == 1 {
			<-ctx.Done()
			return nil, ctx.Err()
		}
		return &tsProbe{}, nil
	}
	checkCtx, cancel := context.WithCancel(context.Background())
	go func() { _, _ = s.probeFileTimeout(checkCtx, hx('b'), 1, time.Second) }()
	time.Sleep(20 * ms)
	got := make(chan error, 1)
	go func() {
		_, err := s.probeFileTimeout(context.Background(), hx('b'), 1, 5*time.Second)
		got <- err
	}()
	time.Sleep(20 * ms)
	cancel()
	if err := <-got; err != nil || calls.Load() != 2 {
		t.Fatalf("player probe: %v after %d runs", err, calls.Load())
	}
}

// A slow header on a warm swarm: the probe goes on past tsDurationProbe instead of being
// killed and started over.
func TestDurationProbeGoesOnWhileWarm(t *testing.T) {
	ms := time.Millisecond
	oldProbe, oldRetry := tsDurationProbe, tsDurationRetry
	tsDurationProbe, tsDurationRetry = 50*ms, 500*ms
	t.Cleanup(func() { tsDurationProbe, tsDurationRetry = oldProbe, oldRetry })
	s := NewService(nil, Config{})
	var calls atomic.Int32
	s.probeFn = func(ctx context.Context, hash string, index int) (*tsProbe, error) {
		calls.Add(1)
		if err := waitOr(ctx, 150*ms); err != nil {
			return nil, err
		}
		p := &tsProbe{}
		p.Format.Duration = "5400"
		return p, nil
	}
	q := tsResolveQuery{mediaType: "movie", title: "Detour", runtime: 90}
	c := tsCandidate{raw: JackettResult{Title: "Detour 1945 1080p BluRay H264 AAC RARBG"}}
	warm := make(chan bool, 1)
	warm <- true
	read := make(chan struct{})
	dur, ok, err := s.checkDuration(context.Background(), c, q, hx('d'), 1, warm, func() { close(read) })
	if err != nil || !ok || dur != 5400 || calls.Load() != 1 {
		t.Fatalf("duration %v verified=%v err=%v after %d probes", dur, ok, err, calls.Load())
	}
	<-read

	// A cold swarm (no peers, no data): the slow probe ends unverified at tsDurationProbe
	s.probeCache.Delete(hx('d') + ":1")
	cold := make(chan bool, 1)
	cold <- false
	start := time.Now()
	if _, ok, err := s.checkDuration(context.Background(), c, q, hx('d'), 1, cold, nil); ok || err != nil || time.Since(start) > 120*ms {
		t.Fatalf("cold swarm: verified=%v err=%v after %s", ok, err, time.Since(start))
	}
}

// checkCandidate reads the header first: the mid-file measurement starts once the probe
// ended (or tsProbeHeadStart passed), so the two do not compete for the same swarm.
func TestCheckCandidateProbesBeforeMeasuring(t *testing.T) {
	fastWarm(t)
	ms := time.Millisecond
	hash := strings.Repeat("e", 40)
	var firstRead atomic.Int64
	start := time.Now()
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if _, ok := r.URL.Query()["stat"]; ok {
			fmt.Fprintf(w, `{"hash":%q,"name":"Detour","file_stats":[{"id":1,"path":"Detour/Detour.1945.1080p.mp4","length":%d}],`+
				`"download_speed":0,"active_peers":3,"connected_seeders":2}`, hash, tsGB)
			return
		}
		firstRead.CompareAndSwap(0, int64(time.Since(start)))
		w.WriteHeader(http.StatusPartialContent)
		chunk := make([]byte, (8<<20)/20)
		for {
			if _, err := w.Write(chunk); err != nil {
				return
			}
			w.(http.Flusher).Flush()
			if waitOr(r.Context(), 50*ms) != nil {
				return
			}
		}
	}))
	defer ts.Close()
	s := NewService(nil, Config{TorrServerURL: ts.URL})
	var probed atomic.Int64
	s.probeFn = func(ctx context.Context, hash string, index int) (*tsProbe, error) {
		if err := waitOr(ctx, 200*ms); err != nil {
			return nil, err
		}
		probed.Store(int64(time.Since(start)))
		p := &tsProbe{}
		p.Format.Duration = "4080"
		return p, nil
	}
	c := tsCandidate{raw: JackettResult{Title: "Detour 1945 1080p BluRay H264 AAC RARBG"}, magnet: "magnet:?xt=urn:btih:" + hash, hash: hash, quality: 1080}
	res, err := s.checkCandidate(context.Background(), c, tsResolveQuery{mediaType: "movie", title: "Detour", runtime: 68}, "", 600*ms, nil, nil)
	if err != nil || !res.DurationVerified || res.Health == nil {
		t.Fatalf("check = %+v, %v", res, err)
	}
	if firstRead.Load() < probed.Load() {
		t.Fatalf("the measurement read at %s, before the probe ended at %s",
			time.Duration(firstRead.Load()), time.Duration(probed.Load()))
	}
}

// The player's probe and a play stream without its first bytes hold background work back;
// a stream that then flows steadily (rate unknown) ends the hold after tsStartSteady.
func TestQuietCoversProbeAndFirstBytes(t *testing.T) {
	ms := time.Millisecond
	shortQuiet(t, 50*ms)
	tsStartQuietMax = time.Minute
	oldSteady := tsStartSteady
	tsStartSteady = 50 * ms
	t.Cleanup(func() { tsStartSteady = oldSteady })
	s := NewService(nil, Config{})
	other := hx('o')
	s.probeBegin(hx('p'))
	if s.startingStream(other) != hx('p') {
		t.Fatal("a player probe does not count as a starting stream")
	}
	s.probeEnd(hx('p'))
	if s.startingStream(other) != "" {
		t.Fatal("a finished probe still holds background work")
	}
	st := s.playBegin(hx('p'))
	defer s.playEnd(hx('p'))
	time.Sleep(80 * ms)
	if s.startingStream(other) != hx('p') {
		t.Fatal("a stream without its first bytes stopped counting as starting")
	}
	w := &tsFirstWrite{w: discard{}, first: func() { s.playFlows(hx('p')) }, stream: st}
	if _, err := w.Write([]byte{1}); err != nil {
		t.Fatal(err)
	}
	if s.startingStream(other) != hx('p') {
		t.Fatal("the first bytes alone ended the start")
	}
	for range 4 {
		time.Sleep(20 * ms)
		if _, err := w.Write([]byte{1}); err != nil {
			t.Fatal(err)
		}
	}
	if s.startingStream(other) != "" {
		t.Fatal("a stream that flows steadily still holds background work")
	}
}

type discard struct{}

func (discard) Write(b []byte) (int, error) { return len(b), nil }

// A background probe that waits in the quiet gate (another torrent starts) is not registered
// yet, so the player's probe of the same file runs at once instead of joining it.
func TestPlayerProbeNeverJoinsDeferredProbe(t *testing.T) {
	s, _ := newWarmService(t)
	s.probeFn = func(ctx context.Context, hash string, index int) (*tsProbe, error) { return &tsProbe{}, nil }
	w := s.holdWarm("v10|movie|charade|1963|0|0")
	s.probeBegin(hx('z')) // another title's player probes its file
	defer s.probeEnd(hx('z'))
	bg, cancel := context.WithCancel(tsBackground(w.ctx))
	defer cancel()
	go func() { _, _ = s.probeFileTimeout(bg, hx('a'), 1, time.Second) }()
	time.Sleep(30 * time.Millisecond)
	done := make(chan error, 1)
	go func() {
		_, err := s.probeFileTimeout(context.Background(), hx('a'), 1, time.Second)
		done <- err
	}()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(300 * time.Millisecond):
		t.Fatal("the player's probe waited for a deferred background probe")
	}
}

// A title whose Play was pressed (resolve marks the intent) never gives way to another
// title's playback, before or after its answer; a detail-page prefetch without intent does.
func TestResolveNeverDeferredByOtherPlayback(t *testing.T) {
	s, _ := newWarmService(t)
	shortPriority(s, 20*time.Millisecond)
	playing := warmWith(s, tsResolveQuery{mediaType: "movie", title: "Night of the Living Dead", year: 1968}, hx('a'))
	s.setSource(playing, hx('a'))
	s.playBegin(hx('a'))
	defer s.playEnd(hx('a'))
	other := warmWith(s, tsResolveQuery{mediaType: "movie", title: "Charade", year: 1963})
	if s.playbackYield(other.ctx, hx('c')) == "" {
		t.Fatal("a prefetch without intent did not give way")
	}
	s.markIntent(other) // GET /resolve
	if got := s.playbackYield(other.ctx, hx('c')); got != "" {
		t.Fatalf("the resolve gave way to %s", got)
	}
	if !s.waitQuiet(other.ctx, hx('c'), "candidate check") {
		t.Fatal("candidate check of a resolve waited")
	}
}
