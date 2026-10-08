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

func shortCap(t *testing.T, d time.Duration) {
	old, extra := tsResolveAnswerCap, tsResolveCapExtra
	tsResolveAnswerCap, tsResolveCapExtra = d, d
	t.Cleanup(func() { tsResolveAnswerCap, tsResolveCapExtra = old, extra })
}

// Weak swarms: nothing keeps up, so resolve answers at the cap with the best so far instead
// of waiting for every check.
func TestPickAnswersAtCapWithBestSoFar(t *testing.T) {
	ms := time.Millisecond
	shortCap(t, 80*ms)
	slow := measured("slow", "1080p", 0.2, 1)  // starving, finished early
	better := measured("bet", "1080p", 0.6, 1) // starving too, but closer
	res := []*tsResolveResult{slow, better, measured("late", "1080p", 3, 1)}
	delays := []time.Duration{5 * ms, 20 * ms, 5 * time.Second}
	keep := func(i int, pick *tsResolveResult) bool { return true }
	start := time.Now()
	p := tsPickBestKeep(context.Background(), nil, 3, func(int) time.Duration { return time.Second }, keep,
		func(ctx context.Context, i int) (*tsResolveResult, error) {
			select {
			case <-time.After(delays[i]):
				return res[i], nil
			case <-ctx.Done():
				return nil, ctx.Err()
			}
		}, newLive(3))
	took := time.Since(start)
	if p.best != 1 || p.live || took < 70*ms || took > 400*ms {
		t.Fatalf("got #%d live=%v after %s, want the closest finished one at the cap", p.best, p.live, took)
	}
	if !p.kept[2] {
		t.Fatal("the running check must keep measuring")
	}
}

// At the cap a running check's snapshot counts; that check keeps running and its outcome
// arrives later.
func TestPickAnswersWithSnapshot(t *testing.T) {
	ms := time.Millisecond
	shortCap(t, 60*ms)
	live := newLive(2)
	finalRes := measured("b", "1080p", 0.9, 1)
	p := tsPickBestKeep(context.Background(), nil, 2, func(int) time.Duration { return time.Second }, nil,
		func(ctx context.Context, i int) (*tsResolveResult, error) {
			if i == 0 {
				<-ctx.Done() // never answers in time, no snapshot either
				return nil, ctx.Err()
			}
			time.Sleep(10 * ms)
			live.set(1, measured("b", "1080p", 0.5, 1)) // duration checked, health so far
			select {
			case <-time.After(300 * ms):
				return finalRes, nil
			case <-ctx.Done():
				return nil, ctx.Err()
			}
		}, live)
	if p.best != 1 || !p.live || !p.kept[1] || p.res.Health.ratio() > 0.6 {
		t.Fatalf("got #%d live=%v kept=%v res=%v, want the snapshot of #1", p.best, p.live, p.kept, p.res)
	}
	if o := <-p.later; o.i != 0 || o.err == nil {
		t.Fatalf("first later outcome %+v, want the cancelled #0", o)
	}
	if o := <-p.later; o.i != 1 || o.res != finalRes {
		t.Fatalf("later outcome %+v, want #1's final result", o)
	}

	// Nothing at all by the cap: the first snapshot answers as soon as it is there
	live = newLive(1)
	start := time.Now()
	p = tsPickBestKeep(context.Background(), nil, 1, func(int) time.Duration { return time.Second }, nil,
		func(ctx context.Context, i int) (*tsResolveResult, error) {
			time.Sleep(120 * ms)
			live.set(0, measured("a", "1080p", 0.4, 1))
			<-ctx.Done()
			return nil, ctx.Err()
		}, live)
	if took := time.Since(start); p.best != 0 || !p.live || took > 400*ms {
		t.Fatalf("got #%d live=%v after %s", p.best, p.live, took)
	}
	p.stop(0)
}

// A healthy answer within the cap is unchanged: the better-ranked 4K wins within its grace.
// The cap is a hard wall though: a grace that would end past it is cut short.
func TestCapIsHardWall(t *testing.T) {
	ms := time.Millisecond
	res := []*tsResolveResult{measured("4k", "2160p", 6, 2), measured("hd", "1080p", 4, 1)}
	delays := []time.Duration{100 * ms, 5 * ms}
	check := func(ctx context.Context, i int) (*tsResolveResult, error) {
		select {
		case <-time.After(delays[i]):
			return res[i], nil
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	shortCap(t, time.Second)
	if i, _, _ := tsPickBest(context.Background(), 2, 300*ms, check); i != 0 {
		t.Fatalf("got %d, want the better-ranked 4K within the grace", i)
	}
	shortCap(t, 40*ms)
	start := time.Now()
	i, _, _ := tsPickBest(context.Background(), 2, 300*ms, check)
	if took := time.Since(start); i != 1 || took > 90*ms {
		t.Fatalf("got %d after %s, want the good 1080p at the cap", i, took)
	}
}

// partialSnap is a snapshot of a running check before its verdict (rate < 0: not measured yet).
func partialSnap(name string, rate float64, peers, seeders int) *tsResolveResult {
	r := &tsResolveResult{Hash: name, Quality: "1080p", Seeders: seeders, durationChecked: true, partial: true}
	if rate >= 0 {
		r.Health = newHealth(int64(rate*mbps), int64(mbps), peers, 0)
		r.Health.partial, r.Health.KeepsUp = true, false
	}
	return r
}

func TestChoosePartial(t *testing.T) {
	for _, tc := range []struct {
		name  string
		snaps []*tsResolveResult
		want  int
	}{
		{"rate first", []*tsResolveResult{partialSnap("a", 0.2, 9, 50), partialSnap("b", 0.6, 1, 2)}, 1},
		{"then active peers", []*tsResolveResult{partialSnap("a", 0, 1, 50), partialSnap("b", 0, 4, 2)}, 1},
		{"unmeasured: jackett seeders", []*tsResolveResult{partialSnap("a", -1, 0, 3), nil, partialSnap("c", -1, 0, 8)}, 2},
		{"tie: better-ranked", []*tsResolveResult{partialSnap("a", -1, 0, 3), partialSnap("b", -1, 0, 3)}, 0},
		{"none", []*tsResolveResult{nil, nil}, -1},
	} {
		if got := tsChoosePlayable(tc.snaps); got != tc.want {
			t.Errorf("%s: got %d, want %d", tc.name, got, tc.want)
		}
	}
	if partialSnap("a", 2, 3, 3).good() || partialSnap("a", -1, 0, 3).keepsUp() {
		t.Fatal("a snapshot without a verdict never counts as good")
	}
}

// Weak swarms whose checks started late (slow metadata, probe first): no check has a
// verdict by the cap, yet the cap answers with the best partial snapshot.
func TestCapAnswersWithPartialSnapshot(t *testing.T) {
	ms := time.Millisecond
	shortCap(t, 60*ms)
	live := newLive(3)
	start := time.Now()
	p := tsPickBestKeep(context.Background(), nil, 3, func(int) time.Duration { return time.Second }, nil,
		func(ctx context.Context, i int) (*tsResolveResult, error) {
			switch i {
			case 0:
				live.set(0, partialSnap("a", -1, 0, 40)) // metadata only
			case 1:
				live.set(1, partialSnap("b", 0.3, 2, 5)) // a few samples
			}
			<-ctx.Done()
			return nil, ctx.Err()
		}, live)
	if took := time.Since(start); p.best != 1 || !p.live || !p.res.partial || took > 200*ms {
		t.Fatalf("got #%d live=%v after %s, want #1's partial snapshot at the cap", p.best, p.live, took)
	}
	p.stop(1)

	// A snapshot with a verdict beats partial ones, even a slower one
	live = newLive(2)
	p = tsPickBestKeep(context.Background(), nil, 2, func(int) time.Duration { return time.Second }, nil,
		func(ctx context.Context, i int) (*tsResolveResult, error) {
			if i == 0 {
				live.set(0, partialSnap("a", 0.9, 5, 40))
			} else {
				live.set(1, measured("b", "1080p", 0.4, 1))
			}
			<-ctx.Done()
			return nil, ctx.Err()
		}, live)
	if p.best != 1 || p.res.partial {
		t.Fatalf("got #%d (partial %v), want the measured snapshot", p.best, p.res.partial)
	}
	p.stop(1)
}

// Before the cap partial snapshots never answer: a grace ends with the finished good one,
// not with a running check that only has its metadata.
func TestGraceIgnoresPartialSnapshots(t *testing.T) {
	ms := time.Millisecond
	shortCap(t, time.Second)
	live := newLive(2)
	good := measured("hd", "1080p", 4, 1)
	p := tsPickBestKeep(context.Background(), nil, 2, func(int) time.Duration { return 50 * ms }, nil,
		func(ctx context.Context, i int) (*tsResolveResult, error) {
			if i == 0 {
				snap := partialSnap("4k", -1, 0, 40)
				snap.durationChecked = false // unknown runtime: durationOK
				live.set(0, snap)
				<-ctx.Done()
				return nil, ctx.Err()
			}
			time.Sleep(5 * ms)
			return good, nil
		}, live)
	if p.best != 1 || p.live || p.res != good {
		t.Fatalf("got #%d live=%v, want the finished good one", p.best, p.live)
	}
}

// Answered with a snapshot: afterPick stops the pick's own measurement, and a faster
// alternative of the same resolution becomes a spare and the next play's source.
func TestAfterPickSnapshotPick(t *testing.T) {
	s, _ := newWarmService(t)
	q := tsResolveQuery{mediaType: "movie", title: "The Little Shop of Horrors", year: 1960}
	key := tsResolveKey(q)
	q.warm = s.holdWarm(key)
	pickRes := result(hx('a'), "1080p", 0.2)
	alt := result(hx('b'), "1080p", 0.9) // starving too, but 4.5x faster
	var stopped, cancelled atomic.Bool
	later := make(chan tsOutcome, 2)
	p := &tsPick{best: 0, res: pickRes, live: true, results: []*tsResolveResult{nil, nil}, kept: []bool{true, true},
		errs: make([]error, 2), later: later, cancels: []context.CancelFunc{func() { cancelled.Store(true) }, func() {}},
		measures: []context.CancelFunc{func() { stopped.Store(true) }, func() {}}}
	s.storeSource(key, *pickRes)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	s.afterPick(key, pickRes, &tsUpgradeSeed{q: q, cands: []tsCandidate{{quality: 1080}, {quality: 1080}}, pick: p, ctx: ctx, cancel: cancel})
	if !stopped.Load() || cancelled.Load() {
		t.Fatal("the pick's own measurement keeps running, or its duration check was cancelled")
	}
	later <- tsOutcome{i: 0, err: context.Canceled} // the stopped pick: ignored
	later <- tsOutcome{i: 1, res: alt}
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		if c, _ := s.cachedSource(key); c != nil && c.Hash == alt.Hash {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	if c, _ := s.cachedSource(key); c == nil || c.Hash != alt.Hash {
		t.Fatalf("cached source %v, want the faster alternative", c)
	}
	s.warmMu.Lock()
	_, spare := q.warm.spares[alt.Hash]
	s.warmMu.Unlock()
	if !spare {
		t.Fatal("the faster alternative is no spare")
	}
	// The player stalls and asks for a sidegrade: a spare that does not keep up is never offered
	if got := s.sidegradeFor(key, tsResolveQuery{}, hx('a'), 1080, tsSideOpts{}); got != nil {
		t.Fatalf("sidegrade = %v, want none", got)
	}
}

func TestSidegradeWorthy(t *testing.T) {
	cur := measured("cur", "1080p", 0.2, 1).Health
	for _, tc := range []struct {
		rate float64
		want bool
	}{{1.5, true}, {0.9, false}, {0.4, false}, {0.25, false}} {
		r := measured("r", "1080p", tc.rate, 1)
		if got := tsSidegradeWorthy(r, cur); got != tc.want {
			t.Errorf("%.2f vs 0.2: %v", tc.rate, got)
		}
	}
	// Against a source that keeps up only one that keeps up and is clearly faster
	ok := measured("cur", "1080p", 2, 1).Health
	for _, tc := range []struct {
		rate float64
		want bool
	}{{0.9, false}, {2.5, false}, {3, true}} {
		if got := tsSidegradeWorthy(measured("r", "1080p", tc.rate, 1), ok); got != tc.want {
			t.Errorf("%.2f vs a healthy 2: %v", tc.rate, got)
		}
	}
}

// Background work waits while another torrent's stream starts; work before an answer does not.
func TestQuietGate(t *testing.T) {
	ms := time.Millisecond
	shortQuiet(t, 150*ms)
	s := NewService(nil, Config{})
	playing, other := hx('p'), hx('o')
	s.playBegin(playing)
	defer s.playEnd(playing)

	if !s.waitQuiet(context.Background(), other, "check") {
		t.Fatal("foreground work must not wait")
	}
	bg := tsBackground(context.Background())
	start := time.Now()
	if !s.waitQuiet(bg, playing, "check") || time.Since(start) > 50*ms {
		t.Fatal("work on the starting torrent itself waited")
	}
	if !s.waitQuiet(bg, other, "check") || time.Since(start) < 120*ms {
		t.Fatalf("background work went on after %s while a stream started", time.Since(start))
	}
	// A seek is a new stream: quiet again
	s.playBegin(playing)
	defer s.playEnd(playing)
	if s.startingStream(other) != playing {
		t.Fatal("a new stream of the torrent does not count as starting")
	}
	ctx, cancel := context.WithCancel(bg)
	cancel()
	if s.waitQuiet(ctx, other, "check") {
		t.Fatal("a cancelled wait reports success")
	}
}

// A background measurement that a starting stream interrupts before its verdict starts over
// once the stream is past its start.
func TestMeasureYieldsToStartingStream(t *testing.T) {
	fastWarm(t)
	ms := time.Millisecond
	shortQuiet(t, 250*ms)
	ts := fakeTorrServer(200<<10, 0) // thin: never keeps up early
	defer ts.Close()
	s := NewService(nil, Config{TorrServerURL: ts.URL})
	hash := strings.Repeat("a", 40)
	go func() {
		time.Sleep(60 * ms) // before the verdict (skip 75 ms + 150 ms)
		s.playBegin(hx('p'))
	}()
	start := time.Now()
	h := s.measureWarm(tsBackground(context.Background()), hash, 1, 0, int64(mbps), 600*ms, tsWarmHooks{})
	if h == nil || time.Since(start) < 300*ms {
		t.Fatalf("health %v after %s, want a full measurement after the stream's start", h, time.Since(start))
	}
}

// A check that turns background (its resolve answered) ends with its verdict when a stream
// starts, instead of competing for the rest of its window.
func TestMeasureCutShortOnceBackground(t *testing.T) {
	fastWarm(t)
	ms := time.Millisecond
	old := tsStartQuiet
	tsStartQuiet = time.Minute
	t.Cleanup(func() { tsStartQuiet = old })
	ts := fakeTorrServer(200<<10, 0)
	defer ts.Close()
	s := NewService(nil, Config{TorrServerURL: ts.URL})
	phase := &tsPhase{}
	ctx := tsWithPhase(context.Background(), phase)
	go func() {
		time.Sleep(350 * ms) // the verdict exists by now
		s.playBegin(hx('p'))
		phase.background.Store(true)
	}()
	start := time.Now()
	h := s.measureWarm(ctx, strings.Repeat("a", 40), 1, 0, int64(mbps), 3*time.Second, tsWarmHooks{})
	if took := time.Since(start); h == nil || h.KeepsUp || took > time.Second {
		t.Fatalf("health %v after %s, want the starving verdict right after the stream started", h, took)
	}
}

// checkCandidate reports a snapshot as soon as the file is picked, before the duration
// check and the health verdict, so the cap can answer with it; full snapshots follow.
func TestCheckCandidateEarlySnapshot(t *testing.T) {
	fastWarm(t)
	hash := strings.Repeat("c", 40)
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if _, ok := r.URL.Query()["stat"]; ok {
			fmt.Fprintf(w, `{"hash":%q,"name":"Sintel","file_stats":[{"id":1,"path":"Sintel/Sintel.2010.1080p.mkv","length":%d}],`+
				`"download_speed":0,"active_peers":3,"connected_seeders":2}`, hash, 2*tsGB)
			return
		}
		w.WriteHeader(http.StatusPartialContent)
		chunk := make([]byte, (8<<20)/20)
		for {
			if _, err := w.Write(chunk); err != nil {
				return
			}
			w.(http.Flusher).Flush()
			select {
			case <-time.After(50 * time.Millisecond):
			case <-r.Context().Done():
				return
			}
		}
	}))
	defer ts.Close()
	s := NewService(nil, Config{TorrServerURL: ts.URL})
	c := tsCandidate{raw: JackettResult{Title: "Sintel 2010 1080p WEB x264", Seeders: 9}, magnet: "magnet:?xt=urn:btih:" + hash, hash: hash, quality: 1080}
	var mu sync.Mutex
	var snaps []tsResolveResult
	res, err := s.checkCandidate(context.Background(), c, tsResolveQuery{mediaType: "movie", title: "Sintel"}, "", 600*time.Millisecond, nil,
		func(r *tsResolveResult) {
			mu.Lock()
			snaps = append(snaps, *r)
			mu.Unlock()
		})
	if err != nil || res.Health == nil || !res.Health.KeepsUp {
		t.Fatalf("check = %+v, %v", res, err)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(snaps) < 2 || !snaps[0].partial || snaps[0].Health != nil || snaps[0].FileName != "Sintel.2010.1080p.mkv" {
		t.Fatalf("first snapshot %+v of %d, want the picked file without health", snaps[0], len(snaps))
	}
	last := snaps[len(snaps)-1]
	if last.partial || last.Health == nil || !last.Health.KeepsUp {
		t.Fatalf("last snapshot %+v, want the verdict", last)
	}
}
