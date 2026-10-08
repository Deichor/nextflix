package streaming

import (
	"context"
	"log"
	"sort"
	"strings"
	"time"
)

// Playback priority: while a play stream runs, the warm-up of every other title waits and
// its torrents leave TorrServer, so the playing file gets the whole connection. Exempt are
// the titles of the playing torrents (their spares, upgrade and sidegrade candidates, and any
// other episode of the same show), a next-episode prefetch, and a title a player asked for in
// the last tsPlayIntent (Play, upgrade and sidegrade polls). The rule is applied in two ways:
//   - live, in the quiet gate (waitQuiet, measureOnce): the title's reads, probes and checks
//     wait, also before its resolve answered (a detail page prefetch)
//   - by the janitor (priorityTick): the title is paused, after tsReleaseSettle its torrents
//     are saved in TorrServer's DB (metadata only) and dropped one tick later; once no
//     stream has played for tsPlayGrace it resumes, its torrents are added again (from the
//     DB) and a prefetch that was put off starts

const (
	tsPlayGrace  = 20 * time.Second // a stream that ended this recently still counts (seek, next episode)
	tsPlayIntent = 2 * time.Minute  // a title a player asked for this recently is never paused
)

type tsWarmCtxKey struct{}

// tsPauseStep is how far a paused torrent got: saved in TorrServer's DB, then dropped.
type tsPauseStep int8

const (
	tsPauseSaved tsPauseStep = iota + 1
	tsPauseDropped
)

// tsTitleGroup is the movie / show of a resolve key ("v10|tv|title|year|season|episode|caps"
// -> "tv|title|year"), so every episode and capability variant of a show is one group.
func tsTitleGroup(key string) string {
	parts := strings.SplitN(key, "|", 5)
	if len(parts) < 4 {
		return key
	}
	return strings.Join(parts[1:4], "|")
}

func tsWarmOf(ctx context.Context) *tsWarm {
	w, _ := ctx.Value(tsWarmCtxKey{}).(*tsWarm)
	return w
}

// markIntent records that a player asked for the title.
func (s *Service) markIntent(w *tsWarm) {
	if w == nil {
		return
	}
	s.warmMu.Lock()
	w.intentAt = time.Now()
	s.warmMu.Unlock()
}

// markNext records that the title is warmed as the next episode of a playing show.
func (s *Service) markNext(w *tsWarm) {
	if w == nil {
		return
	}
	s.warmMu.Lock()
	w.next = true
	s.warmMu.Unlock()
}

// activePlayLocked lists the torrents that play now or stopped less than tsPlayGrace ago.
// Needs s.warmMu.
func (s *Service) activePlayLocked(now time.Time) []string {
	var out []string
	for h, n := range s.playing {
		if n > 0 {
			out = append(out, h)
		}
	}
	for h, at := range s.playEndAt {
		if now.Sub(at) >= s.playGrace {
			delete(s.playEndAt, h)
		} else if s.playing[h] <= 0 {
			out = append(out, h)
		}
	}
	sort.Strings(out)
	return out
}

// yieldLocked returns the playing torrent the work of w must wait for ("" = none).
// Needs s.warmMu.
func (s *Service) yieldLocked(w *tsWarm, now time.Time) string {
	if w == nil || w.released || w.next || now.Sub(w.intentAt) < s.playIntent {
		return ""
	}
	active := s.activePlayLocked(now)
	if len(active) == 0 {
		return ""
	}
	// A stream of any torrent the title (any capability variant) holds, resolved, prefetched
	// or cached is its own playback: a seamless switch, a sidegrade, a re-resolve
	match := sameGroup(w.key)
	for _, h := range active {
		if w.hashes[h] || w.source == h || s.ownsLocked(h, match) {
			return ""
		}
	}
	return active[0]
}

// playbackYield returns the playing torrent that work on hash under ctx waits for: ctx
// belongs to a title that must give way (see yieldLocked) and hash itself does not play.
func (s *Service) playbackYield(ctx context.Context, hash string) string {
	w := tsWarmOf(ctx)
	if w == nil {
		return ""
	}
	s.warmMu.Lock()
	defer s.warmMu.Unlock()
	if hash != "" && s.playing[hash] > 0 {
		return ""
	}
	now := time.Now()
	other := s.yieldLocked(w, now)
	if other != "" {
		w.yieldAt = now
	}
	return other
}

// warmYield is yieldLocked for a handler (a prefetch put off while another title plays).
func (s *Service) warmYield(w *tsWarm) string {
	s.warmMu.Lock()
	defer s.warmMu.Unlock()
	return s.yieldLocked(w, time.Now())
}

// deferPrefetch remembers a prefetch of a paused title; it starts when the title resumes.
func (s *Service) deferPrefetch(w *tsWarm, q tsResolveQuery) {
	s.warmMu.Lock()
	w.deferred = &q
	s.warmMu.Unlock()
}

// pausedSince reports whether the title gave way to playback since t (its resolve may have
// run out of time without failing on its own).
func (s *Service) pausedSince(w *tsWarm, t time.Time) bool {
	if w == nil {
		return false
	}
	s.warmMu.Lock()
	defer s.warmMu.Unlock()
	return w.paused || w.pausedAt.After(t) || w.yieldAt.After(t)
}

// warmSlot waits for the quiet gate and then for a background measurement slot, so work
// that has to wait never holds one of the few slots.
func (s *Service) warmSlot(ctx context.Context, hash, what string) bool {
	for {
		if !s.waitQuiet(ctx, hash, what) {
			return false
		}
		select {
		case s.warmRounds <- struct{}{}:
		case <-ctx.Done():
			return false
		}
		if other, _ := s.yieldReason(ctx, hash); other == "" {
			return true
		}
		<-s.warmRounds // the gate closed while it waited for the slot
	}
}

type tsPauseWork struct {
	w    *tsWarm
	hash string
}

// priorityTick pauses the titles that must give way to playback and resumes the others.
// A paused title's torrents are saved in TorrServer's DB (tsReleaseSettle after the pause,
// when its reads have stopped) and dropped on the next tick; a torrent that plays or that an
// unpaused title holds stays.
func (s *Service) priorityTick(now time.Time) {
	var saves, drops []tsPauseWork
	var paused, resumed []*tsWarm
	var readd [][]string
	var start []*tsResolveQuery
	reason := map[*tsWarm]string{}
	s.warmMu.Lock()
	for _, w := range s.warms {
		if w.released {
			continue
		}
		other := s.yieldLocked(w, now)
		switch {
		case other != "" && !w.paused:
			w.paused, w.pausedAt = true, now
			w.pauseState = map[string]tsPauseStep{}
			paused = append(paused, w)
			reason[w] = other
		case other == "" && w.paused:
			var back []string
			for h, st := range w.pauseState {
				if st == tsPauseDropped && w.hashes[h] {
					back = append(back, h)
				}
			}
			sort.Strings(back)
			w.paused, w.pauseState = false, nil
			resumed = append(resumed, w)
			readd = append(readd, back)
			start = append(start, w.deferred)
			w.deferred = nil
		}
		if !w.paused || now.Sub(w.pausedAt) < s.pauseSettle {
			continue
		}
		for h := range w.hashes {
			if s.playing[h] > 0 || s.heldUnpausedLocked(h) || s.streamBusyLocked(h, now) != "" {
				continue
			}
			switch w.pauseState[h] {
			case 0:
				w.pauseState[h] = tsPauseSaved
				saves = append(saves, tsPauseWork{w, h})
			case tsPauseSaved:
				w.pauseState[h] = tsPauseDropped
				drops = append(drops, tsPauseWork{w, h})
			}
		}
	}
	s.warmMu.Unlock()

	for _, w := range paused {
		log.Printf("warm: playback priority: pausing %s (%d torrents) while %s plays", w.key, len(w.hashes), tsShortHash(reason[w]))
	}
	for _, p := range saves {
		go s.saveTorrent(p.hash)
	}
	byTitle := map[string][]string{}
	for _, p := range drops {
		s.dropTorrent(p.hash)
		byTitle[p.w.key] = append(byTitle[p.w.key], tsShortHash(p.hash))
	}
	for key, hs := range byTitle {
		sort.Strings(hs)
		log.Printf("warm: playback priority: dropped %s of %s (kept in TorrServer's DB)", strings.Join(hs, ","), key)
	}
	for i, w := range resumed {
		log.Printf("warm: playback priority: resuming %s (%d torrents back)", w.key, len(readd[i]))
		for _, h := range readd[i] {
			go s.pingTorrent(h) // the stat adds it again, from TorrServer's DB
		}
		if q := start[i]; q != nil {
			go s.startDeferred(w, *q)
		}
	}
}

// heldUnpausedLocked: a title that is not paused holds hash. Needs s.warmMu.
func (s *Service) heldUnpausedLocked(hash string) bool {
	for _, w := range s.warms {
		if !w.released && !w.paused && w.hashes[hash] {
			return true
		}
	}
	return false
}

// startDeferred runs a prefetch that was put off while the title was paused.
func (s *Service) startDeferred(w *tsWarm, q tsResolveQuery) {
	s.warmMu.Lock()
	live := !w.released && s.warms[w.key] == w
	s.warmMu.Unlock()
	if !live {
		return
	}
	log.Printf("warm: playback priority: starting the deferred prefetch of %q", q.title)
	s.prefetchStart(w.key, q, w)
}

// saveTorrent stores a loaded torrent's metadata in TorrServer's DB (category nextflix, so
// pruneTorrServerDB removes it after tsDBKeep), so adding it again after a drop is instant.
func (s *Service) saveTorrent(hash string) {
	if _, ok := s.debrid.hashSource(hash); ok {
		return // a debrid source never goes into TorrServer
	}
	if s.saveFn != nil {
		s.saveFn(hash)
		return
	}
	if s.dropFn != nil {
		return // tests without a TorrServer
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	body := map[string]any{"action": "add", "link": hash, "category": tsTorrentCategory, "save_to_db": true}
	if err := s.torrServerPost(ctx, body, nil); err != nil {
		log.Printf("warm: save %s to the TorrServer DB: %v", tsShortHash(hash), err)
	}
}
