package streaming

import (
	"testing"
	"time"
)

func releasedOf(s *Service, w *tsWarm) bool {
	s.warmMu.Lock()
	defer s.warmMu.Unlock()
	return w.released
}

// A release that arrives while the title plays (a stray release from a remounted or
// navigating page) drops nothing and cancels nothing; the closing player's release still
// works once its stream ended.
func TestReleaseWhilePlayingKeepsTitle(t *testing.T) {
	s, rec := newWarmService(t)
	q := tsResolveQuery{mediaType: "movie", title: "Hoppers", year: 2026, hevc: true, maxQ: 1080}
	w := s.holdWarm(tsResolveKey(q))
	s.setSource(w, hx('a'))
	s.trackTorrent(w, hx('b')) // a check that keeps measuring
	s.playBegin(hx('a'))

	if n := s.releaseTitle(tsResolveBaseKey(q), 0); n != 1 {
		t.Fatalf("matched %d titles", n)
	}
	if got := rec.list(); got != "" {
		t.Fatalf("dropped %q while the title plays", got)
	}
	if releasedOf(s, w) || w.ctx.Err() != nil {
		t.Fatal("the playing title was released")
	}
	// Still playing when the recheck is due: the release is ignored
	s.warmTick(time.Now().Add(tsReleaseRecheck + time.Second))
	if got := rec.list(); got != "" || releasedOf(s, w) {
		t.Fatalf("dropped %q / released after the recheck while playing", got)
	}
	// A delayed release (detail page left under the player) is ignored too
	s.releaseTitle(tsResolveBaseKey(q), time.Millisecond)
	s.warmTick(time.Now().Add(time.Second))
	if releasedOf(s, w) {
		t.Fatal("a delayed release ended the playing title")
	}

	// The player closes: its stream ends, the release goes through, the played torrent is
	// kept by the stream guard and dropped once that ran out
	s.streamKeep = 40 * time.Millisecond
	s.playEnd(hx('a'))
	s.releaseTitle(tsResolveBaseKey(q), 0)
	if !releasedOf(s, w) || w.ctx.Err() == nil {
		t.Fatal("the closed title was not released")
	}
	if got := rec.list(); got != "b" {
		t.Fatalf("dropped %q, want only the unused check", got)
	}
	s.retryDrops(time.Now())
	if got := rec.list(); got != "a,b" {
		t.Fatalf("dropped %q, want the played torrent after the guard", got)
	}
}

// A stream of a torrent that only another capability variant of the title resolved (the
// hidden seamless-switch stream, a re-resolve after a step down) is the title's own
// playback: the title is never paused for it and its torrents stay.
func TestPriorityKeepsSeamlessPrepOfSameTitle(t *testing.T) {
	s, drops := newWarmService(t)
	shortPriority(s, 10*time.Millisecond)
	saves := newHashRecorder()
	s.saveFn = saves.add
	capped := tsResolveQuery{mediaType: "movie", title: "The Creator", year: 2023, hevc: true, maxQ: 1080}
	w := warmWith(s, capped, hx('a'))
	s.setSource(w, hx('a'))
	// Another variant resolved b (its warm is gone by now)
	full := tsResolveQuery{mediaType: "movie", title: "The Creator", year: 2023, hevc: true}
	other := warmWith(s, full, hx('b'))
	s.releaseWarm(other, false)
	time.Sleep(20 * time.Millisecond)
	drops.reset()

	s.playBegin(hx('a'))
	s.playBegin(hx('b')) // the hidden slot prepares b
	s.playEnd(hx('a'))   // the switch: a stops, b plays
	defer s.playEnd(hx('b'))
	time.Sleep(20 * time.Millisecond) // a left the playback grace: only b plays
	for i := 0; i < 3; i++ {
		s.priorityTick(time.Now())
	}
	if pausedOf(s, w) || s.playbackYield(w.ctx, "") != "" {
		t.Fatal("The Creator paused for its own stream")
	}
	if got := drops.list(); got != "" {
		t.Fatalf("dropped %q", got)
	}
	if got := saves.list(); got != "" {
		t.Fatalf("saved %q", got)
	}
}

// A title whose key differs only in the capability suffix from the one that cached the
// playing torrent (a saved source played without resolving) is not paused either.
func TestPriorityCapsInsensitiveAssociation(t *testing.T) {
	s, drops := newWarmService(t)
	shortPriority(s, time.Second)
	s.saveFn = func(string) {}
	hdr := tsResolveQuery{mediaType: "movie", title: "Hoppers", year: 2026, hevc: true, hdr: true}
	// Cached by an earlier run (no warm, no noteTitle)
	s.resolveCache.Store(tsResolveKey(hdr), tsCacheEntry{res: tsResolveResult{Hash: hx('a')}, expires: time.Now().Add(time.Hour)})
	capped := tsResolveQuery{mediaType: "movie", title: "Hoppers", year: 2026, hevc: true, maxQ: 1080}
	w := warmWith(s, capped, hx('c')) // a detail page prefetch of the 1080p variant
	stranger := warmWith(s, tsResolveQuery{mediaType: "movie", title: "Charade", year: 1963}, hx('d'))

	s.playBegin(hx('a'))
	defer s.playEnd(hx('a'))
	s.priorityTick(time.Now())
	s.priorityTick(time.Now())
	if pausedOf(s, w) {
		t.Fatal("Hoppers paused while its own torrent plays")
	}
	if !pausedOf(s, stranger) {
		t.Fatal("another title must still give way")
	}
	if got := drops.list(); got != "d" {
		t.Fatalf("dropped %q, want only the other title's torrent", got)
	}
}

// The stream guard covers every drop path: delayed release, hold expiry, cleanup of a
// running probe or seekpoint, and playback priority.
func TestDropGuardCoversAllPaths(t *testing.T) {
	s, rec := newWarmService(t)
	shortPriority(s, 10*time.Millisecond)
	s.saveFn = func(string) {}
	q := tsResolveQuery{mediaType: "movie", title: "Night of the Living Dead", year: 1968}
	w := warmWith(s, q, hx('a'), hx('9'))
	// a's stream ended 45 s ago: older than tsRecentUse, inside the stream guard
	s.warmMu.Lock()
	s.streamEnd[hx('a')] = time.Now().Add(-45 * time.Second)
	s.lastUse[hx('a')] = time.Now().Add(-45 * time.Second)
	s.warmMu.Unlock()

	// Delayed release
	s.releaseTitle(tsResolveBaseKey(q), time.Millisecond)
	s.warmTick(time.Now().Add(time.Second))
	if !releasedOf(s, w) {
		t.Fatal("not released")
	}
	if got := rec.list(); got != "9" {
		t.Fatalf("dropped %q, want 9 only (a's stream ended < 60 s ago)", got)
	}

	// Hold expiry (TTL)
	rec.reset()
	w2 := warmWith(s, q, hx('a'))
	s.warmTick(time.Now().Add(tsHoldTime + time.Minute))
	if !releasedOf(s, w2) {
		t.Fatal("hold did not expire")
	}
	if got := rec.list(); got != "" {
		t.Fatalf("hold expiry dropped %q", got)
	}

	// A running probe and a running seekpoint
	w3 := warmWith(s, q, hx('7'), hx('8'))
	s.probeBegin(hx('7'))
	s.seekBegin(hx('8'))
	s.dropFrom(w3, hx('7'))
	s.dropFrom(w3, hx('8'))
	if got := rec.list(); got != "" {
		t.Fatalf("dropped %q during a probe / seekpoint", got)
	}
	s.probeEnd(hx('7'))
	s.seekEnd(hx('8'))

	// Playback priority: a paused title never drops a torrent whose stream just ended
	other := warmWith(s, tsResolveQuery{mediaType: "movie", title: "Charade", year: 1963}, hx('e'))
	s.playBegin(hx('e'))
	s.playEnd(hx('e'))
	s.playBegin(hx('6')) // another title plays
	defer s.playEnd(hx('6'))
	time.Sleep(20 * time.Millisecond)
	s.priorityTick(time.Now())
	s.priorityTick(time.Now())
	if !pausedOf(s, other) {
		t.Fatal("Charade not paused")
	}
	if got := rec.list(); got != "" {
		t.Fatalf("playback priority dropped %q", got)
	}

	// Once the guard ran out the vetoed drops go through (a, 7, 8: no live title holds them)
	s.releaseWarm(w3, false)
	time.Sleep(20 * time.Millisecond)
	s.warmMu.Lock()
	s.streamEnd[hx('a')] = time.Now().Add(-2 * tsStreamKeep)
	s.warmMu.Unlock()
	s.retryDrops(time.Now())
	if got := rec.list(); got != "7,8,a" {
		t.Fatalf("dropped %q after the guard, want 7,8,a", got)
	}
}

// A thumbnail job keeps its torrent loaded but is no player: the title's release goes through
// at once, the hold is not extended, and the vetoed drop happens when the job ends.
func TestThumbJobIsNoReader(t *testing.T) {
	s, rec := newWarmService(t)
	q := tsResolveQuery{mediaType: "movie", title: "Hoppers", year: 2026}
	w := s.holdWarm(tsResolveKey(q))
	s.setSource(w, hx('a'))
	s.thumbBegin(hx('a'))
	s.warmMu.Lock()
	reader, streaming := s.readerLocked(hx('a')), s.streamingLocked(tsResolveKey(q))
	busy := s.streamBusyLocked(hx('a'), time.Now())
	s.warmMu.Unlock()
	if reader || streaming != "" || busy == "" {
		t.Fatalf("reader %v, streaming %q, busy %q", reader, streaming, busy)
	}
	s.releaseTitle(tsResolveBaseKey(q), 0)
	if !releasedOf(s, w) {
		t.Fatal("release ignored during a thumbnail job")
	}
	if got := rec.list(); got != "" {
		t.Fatalf("dropped %q under ffmpeg", got)
	}
	s.thumbEnd(hx('a'))
	if got := rec.list(); got != "a" {
		t.Fatalf("dropped %q after the job, want a", got)
	}

	// A closing player's hash release is not ignored either
	rec.reset()
	w2 := warmWith(s, q, hx('b'))
	s.thumbBegin(hx('b'))
	s.releaseHashes([]string{hx('b')})
	s.warmMu.Lock()
	held := w2.hashes[hx('b')]
	s.warmMu.Unlock()
	if held {
		t.Fatal("hash release ignored during a thumbnail job")
	}
	s.thumbEnd(hx('b'))
	if got := rec.list(); got != "b" {
		t.Fatalf("dropped %q, want b", got)
	}

	// Nor does a job keep an idle title's hold alive
	w3 := warmWith(s, q, hx('c'))
	s.thumbBegin(hx('c'))
	defer s.thumbEnd(hx('c'))
	s.warmTick(time.Now().Add(tsHoldTime + time.Minute))
	if !releasedOf(s, w3) {
		t.Fatal("a thumbnail job extended the hold")
	}
}

func TestPruneStateDropsOldEntries(t *testing.T) {
	s, _ := newWarmService(t)
	now := time.Now()
	old := now.Add(-time.Hour)
	s.warmMu.Lock()
	s.lastUse[hx('a')] = old
	s.lastUse[hx('b')] = now
	s.streamEnd[hx('c')] = old
	s.warmMu.Unlock()
	pa, pe := hx('d')+":0", hx('e')+":0"
	s.probeCache.Store(pa, &tsProbe{})
	s.probeCache.Store(pe, &tsProbe{})
	s.pruneState(now) // adopts the probes
	s.probeMu.Lock()
	s.probeAt[pa], s.probeAt[pe] = now.Add(-2*tsProbeKeep), now.Add(-2*tsProbeKeep)
	s.probeMu.Unlock()
	s.playBegin(hx('e')) // in use: its probe stays
	defer s.playEnd(hx('e'))
	s.pruneState(now)

	s.warmMu.Lock()
	_, a := s.lastUse[hx('a')]
	_, b := s.lastUse[hx('b')]
	_, c := s.streamEnd[hx('c')]
	s.warmMu.Unlock()
	if a || !b || c {
		t.Fatalf("lastUse a %v b %v, streamEnd c %v", a, b, c)
	}
	_, d := s.probeCache.Load(pa)
	_, e := s.probeCache.Load(pe)
	s.probeMu.Lock()
	_, dAt := s.probeAt[pa]
	s.probeMu.Unlock()
	if d || dAt || !e {
		t.Fatalf("probe d %v (%v), e %v", d, dAt, e)
	}
}
