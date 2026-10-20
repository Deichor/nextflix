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
	s.useSource(w, result(hx('a'), "1080p", 0.9)) // starving (x0.9) when resolve measured it
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
		t.Fatal("a spare that does not keep up was offered")
	}
	if got := s.sidegradeFor(key, tsResolveQuery{}, hx('a'), 1080, tsSideOpts{draining: true}); got != nil {
		t.Fatalf("draining: got %v, want none", got)
	}
}

// The Mentalist S03E03 on an iPhone: Safari blocked autoplay, so the paused player's buffer was
// empty and it polled with critical=1, and a TorBox spare at x9.34 replaced the source at
// x3.05 27 s after the pick (restart at 0:00, another encode, subtitles off). While a keeps-up
// verdict is recent (tsSideTrust) the source is never left, whatever the player reports:
// nothing is offered and nothing searched. An older one gives way to a draining player (it may
// have slowed down since) under the old rules: a clearly faster spare, a lower resolution only
// with critical=1. A starving, dead or unmeasured source always gets the spare.
func TestSidegradeKeepsSourceThatKeepsUp(t *testing.T) {
	ok := measured("cur", "1080p", 3.05, 1).Health
	fast := measured(hx('b'), "1080p", 9.34, 1)
	drain, crit := tsSideOpts{draining: true}, tsSideOpts{draining: true, critical: true}
	old := tsSideTrust + time.Second
	for _, tc := range []struct {
		name          string
		cur           *tsHealth
		age           time.Duration
		opt           tsSideOpts
		spare         *tsResolveResult
		offer, search bool
	}{
		{"keeps up", ok, 27 * time.Second, tsSideOpts{}, fast, false, false},
		{"keeps up, draining", ok, 27 * time.Second, drain, fast, false, false},
		{"keeps up, critical", ok, 27 * time.Second, crit, fast, false, false},
		{"kept up long ago", ok, old, tsSideOpts{}, fast, false, false},
		{"kept up long ago, draining", ok, old, drain, fast, true, true},
		{"kept up long ago, draining, spare not clearly faster", ok, old, drain, measured(hx('b'), "1080p", 3.5, 1), false, true},
		{"kept up long ago, draining, lower spare", ok, old, drain, measured(hx('b'), "720p", 5, 1), false, true},
		{"kept up long ago, critical, lower spare", ok, old, crit, measured(hx('b'), "720p", 5, 1), true, true},
		{"starving", measured("cur", "1080p", 0.9, 1).Health, 0, tsSideOpts{}, fast, true, false},
		{"starving, draining", measured("cur", "1080p", 0.9, 1).Health, 0, drain, fast, true, true},
		{"dead", &tsHealth{Need: tsMB, Dead: true}, 0, drain, fast, true, true},
		{"not measured", nil, 0, drain, fast, true, true},
		// A pick answered at the cap (before its verdict) at x2.00 is no starving source
		{"before its verdict", early(2, 5*time.Second), 0, tsSideOpts{}, fast, false, false},
		{"before its verdict, draining", early(2, 5*time.Second), 0, drain, fast, true, true},
		{"before its verdict, draining, spare slower", early(2, 5*time.Second), 0, drain, measured(hx('b'), "1080p", 1.5, 1), false, true},
		{"before its verdict, lower spare", early(2, 5*time.Second), 0, drain, measured(hx('b'), "720p", 5, 1), false, true},
		{"below its need before its verdict", early(0.6, 5*time.Second), 0, tsSideOpts{}, fast, true, false},
		{"below its need before its verdict, lower spare", early(0.6, 5*time.Second), 0, tsSideOpts{}, measured(hx('b'), "720p", 5, 1), false, false},
		{"a second of samples", early(0.6, time.Second), 0, tsSideOpts{}, fast, false, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, _ := newWarmService(t)
			q := tsResolveQuery{mediaType: "tv", title: "The Mentalist", season: 3, episode: 3}
			key := tsResolveKey(q)
			w := s.holdWarm(key)
			s.setSource(w, hx('a'))
			s.warmMu.Lock()
			w.health, w.healthAt = tc.cur, time.Now().Add(-tc.age)
			s.warmMu.Unlock()
			s.addSpare(w, tc.spare)
			var calls atomic.Int32
			playedLong(t, s, hx('a'))
			s.sideSearchFn = func(ctx context.Context, q tsResolveQuery) (*tsResolveResult, *tsUpgradeSeed, error) {
				calls.Add(1)
				return nil, nil, errNoSource
			}
			got := s.sidegradeFor(key, q, hx('a'), 1080, tc.opt)
			if (got != nil) != tc.offer {
				t.Fatalf("sidegrade = %v, want an offer: %v", got, tc.offer)
			}
			if c, _ := s.cachedSource(key); !tc.offer && c != nil {
				t.Fatalf("stored %v as the next source", c)
			}
			// The next poll without spares searches only for a source that may be left
			s.warmMu.Lock()
			w.sideAt, w.spares = time.Time{}, map[string]*tsSpare{}
			s.warmMu.Unlock()
			s.sidegradeFor(key, q, hx('a'), 1080, tc.opt)
			time.Sleep(20 * time.Millisecond)
			if searched := calls.Load() > 0; searched != tc.search {
				t.Fatalf("searched %v, want %v", searched, tc.search)
			}
		})
	}
}

// Lower resolutions for an unmeasured source only when the buffer is nearly empty
// (critical=1); avoided spares never.
func TestSidegradeCriticalAndAvoid(t *testing.T) {
	s, rec, key, w := drainService(t)
	s.addSpare(w, result(hx('b'), "720p", 5))
	s.addSpare(w, result(hx('c'), "1080p", 3))
	q := tsResolveQuery{avoid: []string{hx('c')}}
	s.warmMu.Lock()
	w.health = nil          // a starving one would take the lower resolution anyway
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

// early is a live health before its verdict: ratio x need so far, over sampled.
func early(ratio float64, sampled time.Duration) *tsHealth {
	h := newHealth(int64(ratio*mbps), int64(mbps), 3, 2)
	h.partial, h.KeepsUp, h.sampled = true, false, sampled
	return h
}

// A spare the player found slow is dropped also while the source is not left.
func TestSidegradeDropsAvoidedSpareWhileSourceKeepsUp(t *testing.T) {
	s, rec, key, w := drainService(t)
	s.addSpare(w, result(hx('b'), "1080p", 5))
	s.addSpare(w, result(hx('c'), "1080p", 5))
	s.warmMu.Lock()
	w.health, w.healthAt = measured(hx('a'), "1080p", 3, 1).Health, time.Now()
	s.warmMu.Unlock()
	q := tsResolveQuery{avoid: []string{hx('c')}}
	if got := s.sidegradeFor(key, q, hx('a'), 1080, tsSideOpts{draining: true}); got != nil {
		t.Fatalf("got %v, want none while the source keeps up", got)
	}
	if got := rec.list(); got != "c" {
		t.Fatalf("dropped %q, want the avoided spare", got)
	}
}

// A health of unknown age (a cached TorBox source) is never trusted as recent.
func TestSidegradeUnknownHealthAgeGivesWayToDrain(t *testing.T) {
	s, _, key, w := drainService(t)
	s.addSpare(w, result(hx('b'), "1080p", 9))
	s.useSourceAt(w, result(hx('a'), "1080p", 3), time.Time{})
	if got := s.sidegradeFor(key, tsResolveQuery{}, hx('a'), 1080, tsSideOpts{draining: true}); got == nil || got.Hash != hx('b') {
		t.Fatalf("got %v, want the faster spare", got)
	}
}
