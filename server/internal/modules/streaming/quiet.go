package streaming

import (
	"context"
	"log"
	"sync/atomic"
	"time"
)

// A starting stream gets the line to itself: once resolve has answered, its background work
// (candidate checks that keep running, upgrade warm-ups, spare measurements, late candidate
// checks and their probes) waits while another torrent starts: the player's probe runs, or
// its play stream is younger than tsStartQuiet (up to tsStartQuietMax while it has not sent
// its first bytes yet). Work before the answer (resolve, prefetch on the detail page) never
// waits for that; any work of a title that gives way to another title's playback does
// (priority.go).

// Vars so tests can shorten them.
var (
	tsStartQuiet    = 30 * time.Second       // a play stream this young defers background work
	tsStartQuietMax = 90 * time.Second       // ... and one without its first bytes up to this age
	tsQuietPoll     = 500 * time.Millisecond // how often deferred work looks again
)

type tsPhaseKey struct{}

// tsPhase marks the work of one resolve as background once it has answered.
type tsPhase struct{ background atomic.Bool }

func tsWithPhase(ctx context.Context, p *tsPhase) context.Context {
	return context.WithValue(ctx, tsPhaseKey{}, p)
}

// tsBackground marks ctx as background work from the start.
func tsBackground(ctx context.Context) context.Context {
	p := &tsPhase{}
	p.background.Store(true)
	return tsWithPhase(ctx, p)
}

func tsInBackground(ctx context.Context) bool {
	p, _ := ctx.Value(tsPhaseKey{}).(*tsPhase)
	return p != nil && p.background.Load()
}

// startingStream returns a torrent other than except that is starting: the player probes it,
// or its play stream started less than tsStartQuiet ago (tsStartQuietMax while no byte has
// been sent yet). "" = none.
func (s *Service) startingStream(except string) string {
	now := time.Now()
	s.warmMu.Lock()
	defer s.warmMu.Unlock()
	for h, n := range s.starting {
		if h != except && n > 0 {
			return h
		}
	}
	for h, at := range s.playStart {
		if h == except || s.playing[h] <= 0 {
			continue
		}
		if age := now.Sub(at); age < tsStartQuiet || (!s.playFlowing[h] && age < tsStartQuietMax) {
			return h
		}
	}
	return ""
}

// yieldReason returns the torrent that work on hash under ctx waits for and why: background
// work waits while another torrent's stream starts, and any work of a title that must give
// way to playback (priority.go) waits while another title plays. "" = none.
func (s *Service) yieldReason(ctx context.Context, hash string) (string, string) {
	if other := s.playbackYield(ctx, hash); other != "" {
		return other, "plays"
	}
	if tsInBackground(ctx) {
		if other := s.startingStream(hash); other != "" {
			return other, "starts"
		}
	}
	return "", ""
}

// waitQuiet holds work on hash (what names it in the log) while yieldReason says so. It
// returns false when ctx ended.
func (s *Service) waitQuiet(ctx context.Context, hash, what string) bool {
	var since time.Time
	for {
		other, why := s.yieldReason(ctx, hash)
		if other == "" {
			if !since.IsZero() {
				log.Printf("warm: resuming %s of %s after %s", what, tsShortHash(hash), time.Since(since).Round(time.Second))
			}
			return ctx.Err() == nil
		}
		if since.IsZero() {
			since = time.Now()
			log.Printf("warm: deferring %s of %s while %s %s", what, tsShortHash(hash), tsShortHash(other), why)
		}
		if !tsSleep(ctx, tsQuietPoll) {
			return false
		}
	}
}

func tsShortHash(h string) string {
	if h == "" {
		return "a new torrent"
	}
	if len(h) > 8 {
		return h[:8]
	}
	return h
}
