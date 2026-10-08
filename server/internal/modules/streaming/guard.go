package streaming

import (
	"fmt"
	"log"
	"sort"
	"strings"
	"time"
)

// Stream guard: a torrent a player uses is never dropped, whoever asks (playback priority,
// release, hold expiry, resolve / upgrade / spare cleanup). Every drop goes through
// dropTorrent, which consults streamBusyLocked; a vetoed drop is retried by the janitor
// once the torrent is free again (and only if no live title holds it by then).

const (
	tsStreamKeep     = 60 * time.Second // a play stream that ended this recently still guards its torrent
	tsTitleMemory    = 6 * time.Hour    // how long a torrent stays linked to the titles that resolved it
	tsReleaseRecheck = 5 * time.Second  // a release that arrives while the title streams is checked again after this
)

// streamBusyLocked says why hash must stay loaded ("" = free): a play stream runs, one ended
// less than streamKeep ago, or a player's probe or seekpoint runs. Needs s.warmMu.
func (s *Service) streamBusyLocked(hash string, now time.Time) string {
	switch {
	case s.playing[hash] > 0:
		return "a stream reads it"
	case s.starting[hash] > 0:
		return "a player probes it"
	case s.seeking[hash] > 0:
		return "a player asks for a seek point"
	}
	if at, ok := s.streamEnd[hash]; ok {
		if d := now.Sub(at); d < s.streamKeep {
			return fmt.Sprintf("its stream ended %s ago", d.Round(time.Second))
		}
		delete(s.streamEnd, hash)
	}
	return ""
}

// readerLocked: a player reads hash right now (stream, probe or seekpoint). Needs s.warmMu.
func (s *Service) readerLocked(hash string) bool {
	return s.playing[hash] > 0 || s.starting[hash] > 0 || s.seeking[hash] > 0
}

// guardDrop reports whether hash may be dropped now; a vetoed drop is logged and retried later.
func (s *Service) guardDrop(hash string) bool {
	s.warmMu.Lock()
	why := s.streamBusyLocked(hash, time.Now())
	if why != "" {
		s.dropLater[hash] = true
	}
	s.warmMu.Unlock()
	if why != "" {
		log.Printf("warm: keeping %s: playing (%s)", tsShortHash(hash), why)
		return false
	}
	return true
}

// retryDrops drops the vetoed torrents that are free now and that no live title holds.
func (s *Service) retryDrops(now time.Time) {
	var free []string
	s.warmMu.Lock()
	for h := range s.dropLater {
		if s.streamBusyLocked(h, now) == "" {
			delete(s.dropLater, h)
			free = append(free, h)
		}
	}
	for h, groups := range s.hashTitles {
		for g, at := range groups {
			if now.Sub(at) > tsTitleMemory {
				delete(groups, g)
			}
		}
		if len(groups) == 0 {
			delete(s.hashTitles, h)
		}
	}
	s.warmMu.Unlock()
	sort.Strings(free)
	for _, h := range free {
		s.dropIfFree(h, false)
	}
}

// tsKeyBase is a resolve key without its capability suffix ("v10|tv|title|year|s|e").
func tsKeyBase(key string) string {
	parts := strings.SplitN(key, "|", 7)
	if len(parts) < 6 {
		return key
	}
	return strings.Join(parts[:6], "|")
}

// noteTitleLocked links hash to the title / episode of resolve key, whatever its capability
// suffix, so its stream counts as that title's playback. Needs s.warmMu.
func (s *Service) noteTitleLocked(key, hash string) {
	if !tsHashPattern.MatchString(hash) {
		return
	}
	g := s.hashTitles[hash]
	if g == nil {
		g = map[string]time.Time{}
		s.hashTitles[hash] = g
	}
	g[tsKeyBase(key)] = time.Now()
}

func (s *Service) noteTitle(key, hash string) {
	s.warmMu.Lock()
	s.noteTitleLocked(key, hash)
	s.warmMu.Unlock()
}

// linkCachedLocked links a torrent that starts playing without having been resolved in this
// run (a saved source) to the titles whose cached source it is. Needs s.warmMu.
func (s *Service) linkCachedLocked(hash string) {
	if len(s.hashTitles[hash]) > 0 {
		return
	}
	s.resolveCache.Range(func(k, v any) bool {
		if e, ok := v.(tsCacheEntry); ok && e.res.Hash == hash {
			s.noteTitleLocked(k.(string), hash)
		}
		return true
	})
}

// ownsLocked: hash belongs to the title (any capability variant) a resolve key matches: a
// live warm of it holds hash, or it resolved, prefetched or cached hash. match gets resolve
// keys and bases. Needs s.warmMu.
func (s *Service) ownsLocked(hash string, match func(key string) bool) bool {
	for base := range s.hashTitles[hash] {
		if match(base) {
			return true
		}
	}
	for _, o := range s.warms {
		if !o.released && (o.hashes[hash] || o.source == hash) && match(o.key) {
			return true
		}
	}
	return false
}

// sameGroup matches every episode and variant of the movie / show of key (playback priority).
func sameGroup(key string) func(string) bool {
	g := tsTitleGroup(key)
	return func(k string) bool { return tsTitleGroup(k) == g }
}

// sameTitle matches every capability variant of the title / episode of key (release).
func sameTitle(key string) func(string) bool {
	b := tsKeyBase(key)
	return func(k string) bool { return tsKeyBase(k) == b }
}

// streamingLocked returns a torrent of the title / episode of key that a player reads right
// now ("" = none). Needs s.warmMu.
func (s *Service) streamingLocked(key string) string {
	var hs []string
	for _, m := range []map[string]int{s.playing, s.starting, s.seeking} {
		for h, n := range m {
			if n > 0 {
				hs = append(hs, h)
			}
		}
	}
	sort.Strings(hs)
	match := sameTitle(key)
	for _, h := range hs {
		if s.ownsLocked(h, match) {
			return h
		}
	}
	return ""
}

// seekBegin / seekEnd mark a running seekpoint request.
func (s *Service) seekBegin(hash string) {
	s.warmMu.Lock()
	s.seeking[hash]++
	s.warmMu.Unlock()
}

func (s *Service) seekEnd(hash string) {
	s.warmMu.Lock()
	if s.seeking[hash]--; s.seeking[hash] <= 0 {
		delete(s.seeking, hash)
	}
	s.warmMu.Unlock()
}

// idle: no player uses hash (see streamBusyLocked).
func (s *Service) idle(hash string) bool {
	s.warmMu.Lock()
	defer s.warmMu.Unlock()
	return s.streamBusyLocked(hash, time.Now()) == ""
}
