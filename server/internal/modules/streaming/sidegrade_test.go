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
