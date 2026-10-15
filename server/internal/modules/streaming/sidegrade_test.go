package streaming

import (
	"context"
	"sync/atomic"
	"testing"
	"time"
)

func drainService(t *testing.T) (*Service, *dropRecorder, string, *tsWarm) {
	s, rec := newWarmService(t)
	q := tsResolveQuery{mediaType: "movie", title: "Carnival of Souls", year: 1962}
	key := tsResolveKey(q)
	w := s.holdWarm(key)
	s.useSource(w, result(hx('a'), "1080p", 3)) // kept up when resolve measured it
	return s, rec, key, w
}

// A spare that does not keep up is never offered, also when the player drains: a switch
// reloads or restarts the player.
func TestSidegradeNeverOffersStarvingSpare(t *testing.T) {
	s, _, key, w := drainService(t)
	s.addSpare(w, result(hx('b'), "1080p", 0.8))
	s.warmMu.Lock()
	w.searchAt = time.Now() // no search in this test
	s.warmMu.Unlock()
	if s.sidegradeFor(key, tsResolveQuery{}, hx('a'), 1080, tsSideOpts{}) != nil {
		t.Fatal("a starving spare replaced a source that kept up")
	}
	if got := s.sidegradeFor(key, tsResolveQuery{}, hx('a'), 1080, tsSideOpts{draining: true}); got != nil {
		t.Fatalf("draining: got %v, want none", got)
	}
}

// Frieren on the TV: a 2160p pack measured 17.83 MB/s plays and the player reports a draining
// buffer. The background search only finds a 1080p at 0.51 MB/s (its cap answer, "best so
// far"): nothing is offered. A 1080p spare that keeps up but has less headroom than the
// playing source is not offered either; one clearly faster is.
func TestSidegradeDrainingKeepsFastSource(t *testing.T) {
	s, _ := newWarmService(t)
	q := tsResolveQuery{mediaType: "tv", title: "Frieren: Beyond Journey's End", season: 1, episode: 1, hevc: true}
	key := tsResolveKey(q)
	w := s.holdWarm(key)
	playing := measured(hx('a'), "2160p", 17.83, 8) // x2.23
	s.useSource(w, playing)
	slow := measured(hx('b'), "1080p", 0.51, 1.02)
	slow.DurationVerified, slow.durationChecked = true, true
	var calls atomic.Int32
	playedLong(t, s, hx('a'))
	s.sideSearchFn = func(ctx context.Context, q tsResolveQuery) (*tsResolveResult, *tsUpgradeSeed, error) {
		calls.Add(1)
		_, cancel := context.WithCancel(ctx)
		return slow, &tsUpgradeSeed{pick: &tsPick{live: true}, cancel: cancel}, nil
	}
	if got := s.sidegradeFor(key, q, hx('a'), 2160, tsSideOpts{draining: true}); got != nil {
		t.Fatalf("draining: got %v before any search", got)
	}
	deadline := time.Now().Add(time.Second)
	for calls.Load() == 0 || func() bool { s.warmMu.Lock(); defer s.warmMu.Unlock(); return w.searching }() {
		if time.Now().After(deadline) {
			t.Fatal("the search did not run")
		}
		time.Sleep(5 * time.Millisecond)
	}
	if got := s.sidegradeFor(key, q, hx('a'), 2160, tsSideOpts{draining: true}); got != nil {
		t.Fatalf("offered the search's best-so-far answer %v", got)
	}
	s.addSpare(w, measured(hx('c'), "2160p", 16, 8)) // keeps up, x2.0: not clearly faster
	if got := s.sidegradeFor(key, q, hx('a'), 2160, tsSideOpts{draining: true}); got != nil {
		t.Fatalf("offered %v, slower than the playing source", got)
	}
	s.addSpare(w, measured(hx('d'), "2160p", 30, 8)) // x3.75
	if got := s.sidegradeFor(key, q, hx('a'), 2160, tsSideOpts{draining: true}); got == nil || got.Hash != hx('d') {
		t.Fatalf("got %v, want the clearly faster spare", got)
	}
}

// Lower resolutions only when the buffer is nearly empty (critical=1); avoided spares never.
func TestSidegradeCriticalAndAvoid(t *testing.T) {
	s, rec, key, w := drainService(t)
	s.addSpare(w, result(hx('b'), "720p", 5))
	s.addSpare(w, result(hx('c'), "1080p", 3))
	q := tsResolveQuery{avoid: []string{hx('c')}}
	s.warmMu.Lock()
	w.searchAt = time.Now() // no search in this test
	s.warmMu.Unlock()
	if s.sidegradeFor(key, q, hx('a'), 1080, tsSideOpts{draining: true}) != nil {
		t.Fatal("draining alone offered a lower resolution or an avoided torrent")
	}
	if got := rec.list(); got != "c" {
		t.Fatalf("dropped %q, want the avoided spare", got)
	}
	if got := s.sidegradeFor(key, q, hx('a'), 1080, tsSideOpts{draining: true, critical: true}); got == nil || got.Hash != hx('b') {
		t.Fatalf("critical: got %v, want the 720p that keeps up", got)
	}
}

// Without spares a draining player starts one background search (rate-limited); its pick
// becomes a spare that the next poll offers.
func TestSidegradeSearchesWhenNoSpareLeft(t *testing.T) {
	s, _, key, w := drainService(t)
	var calls atomic.Int32
	var asked atomic.Value
	found := result(hx('d'), "1080p", 5) // clearly faster than the source (x3)
	found.DurationVerified, found.durationChecked = true, true
	playedLong(t, s, hx('a'))
	s.sideSearchFn = func(ctx context.Context, q tsResolveQuery) (*tsResolveResult, *tsUpgradeSeed, error) {
		calls.Add(1)
		asked.Store(q)
		return found, nil, nil
	}
	q := tsResolveQuery{mediaType: "movie", title: "Carnival of Souls", year: 1962, avoid: []string{hx('e')}}
	if s.sidegradeFor(key, q, hx('a'), 1080, tsSideOpts{draining: true}) != nil {
		t.Fatal("offered a source before the search found one")
	}
	deadline := time.Now().Add(time.Second)
	for {
		s.warmMu.Lock()
		busy := w.searching
		s.warmMu.Unlock()
		if !busy || time.Now().After(deadline) {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	sq, _ := asked.Load().(tsResolveQuery)
	if calls.Load() != 1 || sq.maxQ != 1080 || len(sq.avoid) != 2 || sq.avoid[1] != hx('a') {
		t.Fatalf("%d searches, query maxQ=%d avoid=%v", calls.Load(), sq.maxQ, sq.avoid)
	}
	if got := s.sidegradeFor(key, q, hx('a'), 1080, tsSideOpts{draining: true}); got == nil || got.Hash != hx('d') {
		t.Fatalf("got %v, want the source the search found", got)
	}
	// The next drain within tsSideSearchEvery does not search again
	s.warmMu.Lock()
	w.sideAt = time.Time{}
	s.warmMu.Unlock()
	s.sidegradeFor(key, q, hx('d'), 1080, tsSideOpts{draining: true})
	time.Sleep(20 * time.Millisecond)
	if calls.Load() != 1 {
		t.Fatalf("%d searches within tsSideSearchEvery", calls.Load())
	}
}

// Spares outlive the 3 minute window while the player keeps reporting a draining buffer, and
// a spare that arrived late gets its own window.
func TestSpareWindowFollowsDrain(t *testing.T) {
	s, rec, _, w := drainService(t)
	s.addSpare(w, result(hx('b'), "1080p", 3))
	now := time.Now()
	s.warmMu.Lock()
	w.playedAt = now.Add(-4 * time.Minute)
	w.spares[hx('b')].addedAt = now.Add(-4 * time.Minute)
	w.drainAt = now.Add(-time.Minute)
	s.warmMu.Unlock()
	s.warmTick(now)
	if got := rec.list(); got != "" {
		t.Fatalf("dropped %q while the player drains", got)
	}
	s.warmMu.Lock()
	w.drainAt = time.Time{}
	s.warmMu.Unlock()
	s.addSpare(w, result(hx('c'), "1080p", 3)) // arrived just now
	s.warmTick(now)
	if got := rec.list(); got != "b" {
		t.Fatalf("dropped %q, want only the old spare", got)
	}
}

// playedLong marks hash as a stream that has played for two minutes.
func playedLong(t *testing.T, s *Service, hash string) {
	t.Helper()
	s.playBegin(hash)
	t.Cleanup(func() { s.playEnd(hash) })
	s.warmMu.Lock()
	s.playRuns[hash].firstAt = time.Now().Add(-2 * time.Minute)
	s.warmMu.Unlock()
}

// The player's critical=1 during its start buffer starts no search: not before the stream
// played a minute, not while a player resolves the title (its step down), never without the
// playing resolution (the search is capped at it).
func TestSidegradeSearchWaitsForPlayback(t *testing.T) {
	s, _, key, w := drainService(t)
	var calls atomic.Int32
	s.sideSearchFn = func(ctx context.Context, q tsResolveQuery) (*tsResolveResult, *tsUpgradeSeed, error) {
		calls.Add(1)
		return nil, nil, errNoSource
	}
	q := tsResolveQuery{mediaType: "movie", title: "Carnival of Souls", year: 1962}
	crit := tsSideOpts{draining: true, critical: true}
	searched := func() bool {
		time.Sleep(30 * time.Millisecond)
		s.warmMu.Lock()
		defer s.warmMu.Unlock()
		return calls.Load() > 0 || w.searching || !w.searchAt.IsZero()
	}
	s.playBegin(hx('a')) // no bytes yet
	defer s.playEnd(hx('a'))
	s.sidegradeFor(key, q, hx('a'), 1080, crit)
	if searched() {
		t.Fatal("searched during the stream's start")
	}
	s.playFlows(hx('a'))
	s.sidegradeFor(key, q, hx('a'), 1080, crit)
	if searched() {
		t.Fatal("searched right after the first bytes")
	}
	s.warmMu.Lock()
	s.playRuns[hx('a')].firstAt = time.Now().Add(-2 * time.Minute)
	s.warmMu.Unlock()
	// A player waits for the step down (maxq=720, another capability variant of the title)
	step := q
	step.maxQ = 720
	f, _ := s.flights.start(tsResolveKey(step), func() (*tsResolveResult, error) {
		time.Sleep(200 * time.Millisecond)
		return nil, errNoSource
	})
	f.waiters.Add(1)
	s.sidegradeFor(key, q, hx('a'), 1080, crit)
	if searched() {
		t.Fatal("searched while a player resolves the title")
	}
	<-f.done
	f.waiters.Add(-1)
	s.sidegradeFor(key, q, hx('a'), 0, crit)
	if searched() {
		t.Fatal("searched without a cap (playing resolution unknown)")
	}
	s.sidegradeFor(key, q, hx('a'), 1080, crit)
	if !searched() {
		t.Fatal("no search once the stream played a minute")
	}
}

// Night of the Living Dead on a 28 Mbit line: the pick (x0.11) never got past its start, and
// the search's check of a source that kept up waited behind it until the start cap. While the
// title's source starves, its sidegrade search (and spare measurements) pass the start-quiet
// gate, start without the minute, and still end when a player resolves the title.
func TestRescuePassesStartOfStarvingSource(t *testing.T) {
	shortQuiet(t, time.Minute)
	s, _, key, w := drainService(t)
	s.useSource(w, result(hx('a'), "1080p", 0.11))
	s.playBegin(hx('a')) // starting: no bytes for a while
	defer s.playEnd(hx('a'))
	s.playFlows(hx('a'))
	if s.startingStream(hx('b')) != hx('a') {
		t.Fatal("the starving stream does not start")
	}
	type seen struct{ background, yields, ended bool }
	got := make(chan seen, 1)
	s.sideSearchFn = func(ctx context.Context, q tsResolveQuery) (*tsResolveResult, *tsUpgradeSeed, error) {
		other, _ := s.yieldReason(ctx, hx('b'))
		v := seen{background: tsInBackground(ctx), yields: other != ""}
		select {
		case <-ctx.Done():
			v.ended = true
		case <-time.After(time.Second):
		}
		got <- v
		return nil, nil, errNoSource
	}
	q := tsResolveQuery{mediaType: "movie", title: "Carnival of Souls", year: 1962}
	s.sidegradeFor(key, q, hx('a'), 1080, tsSideOpts{draining: true})
	time.Sleep(50 * time.Millisecond)
	// The player steps down meanwhile: the search ends
	step := q
	step.maxQ = 720
	release := make(chan struct{})
	defer close(release)
	f, _ := s.flights.start(tsResolveKey(step), func() (*tsResolveResult, error) { <-release; return nil, errNoSource })
	f.waiters.Add(1)
	defer f.waiters.Add(-1)
	select {
	case v := <-got:
		if v.background || v.yields || !v.ended {
			t.Fatalf("search: background %v, waits for the start %v, ended by the step down %v", v.background, v.yields, v.ended)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("no search while the source starves")
	}
	// A healthy title's rescue work stays background
	ok := s.holdWarm(tsResolveKey(tsResolveQuery{mediaType: "movie", title: "Detour", year: 1945}))
	s.useSource(ok, result(hx('c'), "1080p", 3))
	if !tsInBackground(s.rescueCtx(ok, "spare measurement")) {
		t.Fatal("a title whose source keeps up skips the start-quiet gate")
	}
}

// Resolve's early measurement below half the need stops counting as starving once the
// source's stream got healthy; a draining player still counts.
func TestRescueEndsWhenStreamHealthy(t *testing.T) {
	shortQuiet(t, time.Minute)
	shortStart(t, 30*time.Second, time.Minute, time.Second)
	s, _, _, w := drainService(t)
	s.useSource(w, result(hx('a'), "1080p", 0.41))
	st := s.playBegin(hx('a'))
	defer s.playDone(hx('a'), st)
	if tsInBackground(s.rescueCtx(w, "spare measurement")) {
		t.Fatal("a source measured at 0.41 does not count as starving while it starts")
	}
	st.rate.Store(1000)
	_, _ = (&tsFirstWrite{w: discard{}, stream: st}).Write(make([]byte, 31*1000))
	if !tsInBackground(s.rescueCtx(w, "spare measurement")) {
		t.Fatal("a healthy stream still counts as starving by resolve's measurement")
	}
	s.warmMu.Lock()
	w.drainAt = time.Now()
	s.warmMu.Unlock()
	if tsInBackground(s.rescueCtx(w, "spare measurement")) {
		t.Fatal("a draining player does not count")
	}
}
