package streaming

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
)

// Warm-up bookkeeping. A title (resolve key) is warmed only on intent: the detail page's
// prefetch, Play, or the next episode near the end of the current one. Its tsWarm records the
// torrents it added to TorrServer, so they can be dropped again:
//   - after a pick: every candidate that is neither the pick, an upgrade being warmed nor a
//     spare (afterPick); a failed check drops its torrent at once
//   - spares (alternatives for a sidegrade): 60 s after the pick without playback, or 3 min
//     into playback (counted from the spare's arrival or the player's last draining poll
//     when those are later)
//   - POST /release: all of the title's torrents, optionally after a grace (detail page
//     left), and its background work (resolve job, upgrade warm-up, late indexers) is cancelled
//   - a title held 15 min without playback is released by itself
// A torrent that is playing, or that another live title holds, is never dropped. Dropped
// torrents of chosen sources stay in TorrServer's DB (metadata only), so reopening them skips
// the metadata exchange.

// Vars so tests can shorten them.
var (
	tsHoldTime        = 15 * time.Minute // a warmed title without playback is released after this
	tsHoldPing        = 20 * time.Second // stat ping that keeps TorrServer from closing a held torrent
	tsReleaseSettle   = 2 * time.Second  // lets a closing player's stream end before its torrent is dropped
	tsReleaseGraceMax = 2 * time.Minute  // longest ?delay= of a release
	tsRecentUse       = 30 * time.Second // a delayed release spares torrents used this recently
	tsSpareIdle       = 60 * time.Second // spares are dropped when playback does not start within this
	tsSpareWindow     = 3 * time.Minute  // ... and this long after it started
	tsSpareFresh      = 45 * time.Second // a spare measurement this old still counts for a sidegrade
	tsSidegradeEvery  = 2 * time.Minute  // at most one sidegrade per title in this time
	tsSideSearchEvery = 3 * time.Minute  // a draining player starts a search for alternatives at most this often
	tsJanitorTick     = 5 * time.Second
)

const (
	tsSpareMax        = 2
	tsTorrentCategory = "nextflix" // TorrServer category of the torrents Nextflix saved
	tsDBKeep          = tsSourceTTL
)

type tsWarm struct {
	key    string
	ctx    context.Context // background work of the title; cancelled on release
	cancel context.CancelFunc

	// guarded by Service.warmMu
	hashes    map[string]bool // torrents added for this title and not dropped yet
	source    string          // the picked / cached source
	health    *tsHealth       // measured health of source (nil = unknown)
	spares    map[string]*tsSpare
	holdUntil time.Time
	releaseAt time.Time // pending delayed release (zero = none)
	pickedAt  time.Time
	playedAt  time.Time // first playback of one of its torrents
	pingedAt  time.Time
	sideAt    time.Time // last sidegrade offered
	measuring bool      // spares are being measured
	drainAt   time.Time // last sidegrade poll of a draining player
	searchAt  time.Time // last search for alternatives (searchSidegrade)
	searching bool
	released  bool

	// Playback priority (priority.go), guarded by Service.warmMu
	next       bool                   // warmed as the next episode of a playing show
	intentAt   time.Time              // last player request for it (resolve, upgrade, sidegrade)
	paused     bool                   // waits for another title's playback
	pausedAt   time.Time              // when it was paused
	pauseState map[string]tsPauseStep // how far each torrent got while paused
	yieldAt    time.Time              // last time its work waited for playback (quiet gate)
	deferred   *tsResolveQuery        // a prefetch put off while paused, started on resume
}

type tsSpare struct {
	res        tsResolveResult
	measuredAt time.Time
	addedAt    time.Time
}

// tsSideOpts is what the player tells sidegrade about its buffer.
type tsSideOpts struct {
	draining bool // ?draining=1: the buffer drains, the source starves whatever resolve measured
	critical bool // ?critical=1: under 6 s left, a lower resolution that keeps up is fine too
}

// holdWarm returns the live warm state of key (creating it) and records intent: a pending
// delayed release is cancelled and the hold is extended.
func (s *Service) holdWarm(key string) *tsWarm {
	s.janitorOnce.Do(func() { go s.janitor() })
	now := time.Now()
	s.warmMu.Lock()
	defer s.warmMu.Unlock()
	w := s.warms[key]
	if w == nil {
		w = &tsWarm{key: key, hashes: map[string]bool{}, spares: map[string]*tsSpare{}}
		// The title travels with its work, so the playback priority knows whose it is
		w.ctx, w.cancel = context.WithCancel(context.WithValue(context.Background(), tsWarmCtxKey{}, w))
		s.warms[key] = w
	}
	w.releaseAt = time.Time{}
	w.holdUntil = now.Add(tsHoldTime)
	return w
}

// setSource marks hash as the title's source and returns the previous one.
func (s *Service) setSource(w *tsWarm, hash string) string {
	if w == nil || hash == "" {
		return ""
	}
	s.warmMu.Lock()
	defer s.warmMu.Unlock()
	s.noteTitleLocked(w.key, hash)
	prev := w.source
	if prev != hash {
		w.health = nil
	}
	w.source = hash
	if w.pickedAt.IsZero() {
		w.pickedAt = time.Now()
	}
	if !w.released {
		w.hashes[hash] = true
		delete(w.spares, hash)
	}
	return prev
}

// useSource is setSource with the source's measured health (compared by sidegrades).
func (s *Service) useSource(w *tsWarm, r *tsResolveResult) string {
	prev := s.setSource(w, r.Hash)
	if w != nil && r.Health != nil {
		s.warmMu.Lock()
		if w.source == r.Hash {
			h := *r.Health
			w.health = &h
		}
		s.warmMu.Unlock()
	}
	return prev
}

// trackTorrent records that the title added hash to TorrServer. On a released title it is
// dropped again right away.
func (s *Service) trackTorrent(w *tsWarm, hash string) {
	if w == nil || !tsHashPattern.MatchString(hash) {
		return
	}
	s.warmMu.Lock()
	s.noteTitleLocked(w.key, hash)
	released := w.released
	if !released {
		w.hashes[hash] = true
	}
	s.warmMu.Unlock()
	if released {
		settle := tsReleaseSettle
		go func() {
			time.Sleep(settle)
			s.dropIfFree(hash, false)
		}()
	}
}

// dropFrom gives up the title's claim on hash and drops it unless it is the title's source,
// plays, or another title holds it.
func (s *Service) dropFrom(w *tsWarm, hash string) {
	if !tsHashPattern.MatchString(hash) {
		return
	}
	if w != nil {
		s.warmMu.Lock()
		keep := w.source == hash && !w.released
		if !keep {
			delete(w.hashes, hash)
			delete(w.spares, hash)
		}
		s.warmMu.Unlock()
		if keep {
			return
		}
	}
	go s.dropIfFree(hash, false)
}

// canDrop: hash is not playing, no live title holds it and, for a delayed release, it was
// not used recently. Needs s.warmMu.
func (s *Service) canDropLocked(hash string, recent bool) bool {
	if s.playing[hash] > 0 {
		return false
	}
	if recent && time.Since(s.lastUse[hash]) < tsRecentUse {
		return false
	}
	for _, w := range s.warms {
		if !w.released && w.hashes[hash] {
			return false
		}
	}
	return true
}

func (s *Service) dropIfFree(hash string, recent bool) bool {
	s.warmMu.Lock()
	ok := s.canDropLocked(hash, recent)
	s.warmMu.Unlock()
	return ok && s.dropTorrent(hash)
}

// tsSpareWorthy: a measured alternative that may replace a starving pick of resolution
// floor: alive, of a fitting length and the same resolution (higher only where an HEVC
// client without a cap could play it).
func tsSpareWorthy(r *tsResolveResult, floor int, q tsResolveQuery) bool {
	if r == nil || (r.Health != nil && r.Health.Dead) || !r.durationOK() {
		return false
	}
	rq := tsQualityHeight(r.Quality)
	return rq == floor || (rq > floor && q.hevc && q.maxQ == 0)
}

// tsLowerSpare: a lower resolution that keeps up may replace a pick that starves (sidegrade).
func tsLowerSpare(r, pick *tsResolveResult) bool {
	return r != nil && pick != nil && !pick.keepsUp() && r.Health != nil && r.Health.KeepsUp && r.durationOK() &&
		r.unfit == "" && tsQualityHeight(r.Quality) < tsQualityHeight(pick.Quality)
}

const (
	tsClearlyFaster  = 1.5 // how much faster (rate / need) an alternative must be than the playing source
	tsFasterMinRatio = 0.5 // ... when it delivers at least this share of its bitrate
)

// tsFaster: r is clearly faster than a source of health cur that does not keep up.
func tsFaster(r, cur *tsHealth) bool {
	if r == nil || cur == nil || cur.KeepsUp || r.Dead {
		return false
	}
	return r.ratio() >= tsFasterMinRatio && r.ratio() > cur.ratio() && r.ratio() >= tsClearlyFaster*cur.ratio()
}

// tsSidegradeWorthy: a measured spare of a fitting length that keeps up and, when the
// playing source's last measurement (cur) kept up too, delivers tsClearlyFaster times its
// rate / need. Against an unmeasured, starving or dead source keeping up is enough. A spare
// that does not keep up is never offered: a switch restarts or reloads the player.
func tsSidegradeWorthy(r *tsResolveResult, cur *tsHealth) bool {
	if r.Health == nil || !r.Health.KeepsUp || r.Health.Dead || !r.durationOK() {
		return false
	}
	return cur == nil || !cur.KeepsUp || cur.Dead || r.Health.ratio() >= tsClearlyFaster*cur.ratio()
}

// addSpare keeps r as a spare of w for a sidegrade (never one unfit for the client); false
// when it was not taken.
func (s *Service) addSpare(w *tsWarm, r *tsResolveResult) bool {
	if w == nil || !tsHashPattern.MatchString(r.Hash) || r.unfit != "" {
		return false
	}
	s.warmMu.Lock()
	if w.released || w.source == r.Hash {
		s.warmMu.Unlock()
		return false
	}
	// When full, one that keeps up replaces a starving one
	evicted := ""
	if len(w.spares) >= tsSpareMax {
		for h, sp := range w.spares {
			if r.keepsUp() && !sp.res.keepsUp() {
				evicted = h
				break
			}
		}
		if evicted == "" {
			s.warmMu.Unlock()
			return false
		}
		delete(w.spares, evicted)
		delete(w.hashes, evicted)
	}
	now := time.Now()
	s.noteTitleLocked(w.key, r.Hash)
	w.spares[r.Hash] = &tsSpare{res: *r, measuredAt: now, addedAt: now}
	w.hashes[r.Hash] = true
	s.warmMu.Unlock()
	if evicted != "" {
		go s.dropIfFree(evicted, false)
	}
	return true
}

// releaseWarm ends a title: its background work is cancelled and, after tsReleaseSettle, its
// torrents are dropped (not the playing ones; with recent also not recently used ones).
func (s *Service) releaseWarm(w *tsWarm, recent bool) {
	s.warmMu.Lock()
	if w.released {
		s.warmMu.Unlock()
		return
	}
	if s.warms[w.key] == w {
		delete(s.warms, w.key)
	}
	w.released = true
	hashes := make([]string, 0, len(w.hashes))
	for h := range w.hashes {
		hashes = append(hashes, h)
	}
	w.hashes, w.spares = map[string]bool{}, map[string]*tsSpare{}
	s.warmMu.Unlock()
	w.cancel()
	if len(hashes) == 0 {
		return
	}
	sort.Strings(hashes)
	settle := tsReleaseSettle
	go func() {
		time.Sleep(settle)
		var dropped []string
		for _, h := range hashes {
			if s.dropIfFree(h, recent) {
				dropped = append(dropped, h)
			}
		}
		log.Printf("warm: released %s, dropped %d of %d torrents", w.key, len(dropped), len(hashes))
	}()
}

// releaseTitle releases every capability variant of the title base (now, or after delay);
// a title whose resolve a player is waiting for is left alone. While a player reads one of
// the title's torrents an immediate release only becomes a delayed one (tsReleaseRecheck),
// so a closing player still releases it and a stray release of a playing title is ignored
// (warmTick). Returns how many matched.
func (s *Service) releaseTitle(base string, delay time.Duration) int {
	s.warmMu.Lock()
	var now []*tsWarm
	n := 0
	streaming := s.streamingLocked(base)
	if streaming != "" && delay < tsReleaseRecheck {
		log.Printf("warm: release of %s while %s plays: checked again in %s", base, tsShortHash(streaming), tsReleaseRecheck)
		delay = tsReleaseRecheck
	}
	for k, w := range s.warms {
		if k != base && !strings.HasPrefix(k, base+"|") {
			continue
		}
		n++
		if delay > 0 {
			w.releaseAt = time.Now().Add(delay)
		} else {
			now = append(now, w)
		}
	}
	s.warmMu.Unlock()
	for _, w := range now {
		if !s.flights.awaited(w.key) {
			s.releaseWarm(w, false)
		}
	}
	return n
}

// releaseHashes drops torrents a closing player used, after tsReleaseSettle, unless they
// play again by then. Titles that held them give them up.
func (s *Service) releaseHashes(hashes []string) {
	s.warmMu.Lock()
	// A torrent a player reads right now is not given up (a stray release)
	kept := hashes[:0:0]
	for _, h := range hashes {
		if s.readerLocked(h) {
			log.Printf("warm: keeping %s: playing (release ignored)", tsShortHash(h))
			continue
		}
		kept = append(kept, h)
	}
	hashes = kept
	for _, w := range s.warms {
		for _, h := range hashes {
			delete(w.hashes, h)
			delete(w.spares, h)
			if w.source == h {
				w.source = ""
			}
		}
	}
	s.warmMu.Unlock()
	settle := tsReleaseSettle
	go func() {
		time.Sleep(settle)
		for _, h := range hashes {
			s.dropIfFree(h, false)
		}
	}()
}

// playBegin / playEnd count the running play streams of a torrent.
func (s *Service) playBegin(hash string) {
	now := time.Now()
	s.warmMu.Lock()
	defer s.warmMu.Unlock()
	s.playing[hash]++
	s.lastUse[hash] = now
	s.playStart[hash] = now
	s.playFlowing[hash] = false
	delete(s.playEndAt, hash)
	delete(s.dropLater, hash)
	s.linkCachedLocked(hash)
	for _, w := range s.warms {
		if w.hashes[hash] {
			w.releaseAt = time.Time{}
			if w.playedAt.IsZero() {
				w.playedAt = now
			}
		}
	}
}

func (s *Service) playEnd(hash string) {
	s.warmMu.Lock()
	defer s.warmMu.Unlock()
	if s.playing[hash]--; s.playing[hash] <= 0 {
		delete(s.playing, hash)
		delete(s.playStart, hash)
		delete(s.playFlowing, hash)
		s.playEndAt[hash] = time.Now()
		s.streamEnd[hash] = time.Now()
	}
	s.lastUse[hash] = time.Now()
}

// playFlows records that the latest play stream of a torrent sent its first bytes.
func (s *Service) playFlows(hash string) {
	s.warmMu.Lock()
	if s.playing[hash] > 0 {
		s.playFlowing[hash] = true
	}
	s.warmMu.Unlock()
}

// probeBegin / probeEnd mark a player probe: its stream is about to start (quiet.go).
func (s *Service) probeBegin(hash string) {
	s.warmMu.Lock()
	s.starting[hash]++
	s.warmMu.Unlock()
}

func (s *Service) probeEnd(hash string) {
	s.warmMu.Lock()
	if s.starting[hash]--; s.starting[hash] <= 0 {
		delete(s.starting, hash)
	}
	s.warmMu.Unlock()
}

// touch records a request for a torrent (files, probe, seekpoint).
func (s *Service) touch(hash string) {
	if !tsHashPattern.MatchString(hash) {
		return
	}
	s.warmMu.Lock()
	s.lastUse[hash] = time.Now()
	s.warmMu.Unlock()
}

func (s *Service) janitor() {
	t := time.NewTicker(tsJanitorTick)
	defer t.Stop()
	for now := range t.C {
		for _, h := range s.warmTick(now) {
			go s.pingTorrent(h)
		}
		s.priorityTick(now)
		s.retryDrops(now)
		s.pruneState(now)
	}
}

// warmTick runs the timed rules: delayed releases and expired holds, spare windows. It
// returns the held torrents to ping so TorrServer keeps them open.
func (s *Service) warmTick(now time.Time) (ping []string) {
	type drop struct {
		w    *tsWarm
		hash string
	}
	var release []*tsWarm
	var drops []drop
	var ignored []string
	s.warmMu.Lock()
	for _, w := range s.warms {
		playing := false
		for h := range w.hashes {
			playing = playing || s.playing[h] > 0
		}
		// A player reads a torrent of the title (any variant): never released now
		if !playing {
			playing = s.streamingLocked(w.key) != ""
		}
		if playing {
			w.holdUntil = now.Add(tsHoldTime)
			if !w.releaseAt.IsZero() && !now.Before(w.releaseAt) {
				w.releaseAt = time.Time{}
				ignored = append(ignored, w.key)
			}
		}
		if (!w.releaseAt.IsZero() && !now.Before(w.releaseAt)) || (!playing && now.After(w.holdUntil)) {
			release = append(release, w)
			continue
		}
		if len(w.spares) > 0 && !w.pickedAt.IsZero() {
			idle := w.playedAt.IsZero() && now.Sub(w.pickedAt) >= tsSpareIdle
			for h, sp := range w.spares {
				// Into playback the window counts from the latest of the start, the spare's
				// arrival and the last draining poll
				since := w.playedAt
				for _, t := range []time.Time{w.drainAt, sp.addedAt} {
					if t.After(since) {
						since = t
					}
				}
				if idle || (!w.playedAt.IsZero() && now.Sub(since) >= tsSpareWindow) {
					drops = append(drops, drop{w, h})
				}
			}
		}
		if now.Sub(w.pingedAt) >= tsHoldPing && !w.paused {
			w.pingedAt = now
			for h := range w.hashes {
				if s.playing[h] == 0 {
					ping = append(ping, h)
				}
			}
		}
	}
	s.warmMu.Unlock()
	sort.Strings(ignored)
	for _, k := range ignored {
		log.Printf("warm: ignoring the release of %s: a player reads it", k)
	}
	for _, w := range release {
		if s.flights.awaited(w.key) {
			continue
		}
		s.releaseWarm(w, true)
	}
	for _, d := range drops {
		s.dropFrom(d.w, d.hash)
	}
	sort.Strings(ping)
	return ping
}

// sidegradeFor returns a spare of the title that keeps up now (measured within
// tsSpareFresh), is not current or in q.avoid and has at least resolution curQ; nil
// otherwise, and then it measures the spares in the background so a later call can answer.
// Only a spare that keeps up and beats the source's last measurement is offered
// (tsSidegradeWorthy). A draining player (opt) with no spare left to measure starts a search
// for alternatives (searchSidegrade).
func (s *Service) sidegradeFor(key string, q tsResolveQuery, current string, curQ int, opt tsSideOpts) *tsResolveResult {
	now := time.Now()
	s.warmMu.Lock()
	w := s.warms[key]
	if w == nil || w.released {
		s.warmMu.Unlock()
		return nil
	}
	if opt.draining {
		w.drainAt = now
	}
	if now.Sub(w.sideAt) < tsSidegradeEvery {
		s.warmMu.Unlock()
		return nil
	}
	var cur *tsHealth // the playing source's measured health
	if w.source == current {
		cur = w.health
	}
	// Lower resolutions only for a starving source (or a nearly empty buffer), and only
	// when they keep up
	lowerOK := opt.critical || (cur != nil && !cur.KeepsUp)
	avoid := map[string]bool{}
	for _, h := range q.avoid {
		avoid[h] = true
	}
	var best *tsResolveResult
	var stale []tsResolveResult
	var avoided []string
	for h, sp := range w.spares {
		r := sp.res
		if avoid[h] {
			avoided = append(avoided, h) // the player found it slow
			continue
		}
		lower := tsQualityHeight(r.Quality) < curQ
		if h == current || (lower && (!lowerOK || r.Health == nil || !r.Health.KeepsUp)) {
			continue
		}
		if now.Sub(sp.measuredAt) > tsSpareFresh {
			stale = append(stale, r)
			continue
		}
		// The playing resolution (or higher) first, then the fastest
		better := best == nil || (!lower && tsQualityHeight(best.Quality) < curQ) ||
			(lower == (tsQualityHeight(best.Quality) < curQ) && r.Health.ratio() > best.Health.ratio())
		if tsSidegradeWorthy(&r, cur) && better {
			res := r
			best = &res
		}
	}
	if best != nil {
		w.sideAt = now
		delete(w.spares, best.Hash)
	}
	measure := best == nil && len(stale) > 0 && !w.measuring
	if measure {
		w.measuring = true
	}
	search := best == nil && opt.draining && len(stale) == 0 && !w.measuring && !w.searching &&
		now.Sub(w.searchAt) >= tsSideSearchEvery
	if search {
		w.searching, w.searchAt = true, now
	}
	s.warmMu.Unlock()
	for _, h := range avoided {
		s.dropFrom(w, h)
	}
	if best != nil {
		log.Printf("warm: sidegrade from %s to %q: %s", tsShortHash(current), best.TorrentTitle, best.Health)
		s.storeSource(key, *best) // the next play starts with it, too
		if prev := s.useSource(w, best); prev != "" && prev != best.Hash && prev != current {
			s.dropFrom(w, prev)
		}
		return best
	}
	if measure {
		go s.measureSpares(w, stale)
	}
	if search {
		go s.searchSidegrade(w, q, current, curQ, lowerOK)
	}
	return nil
}

// searchSidegrade looks for a faster source than a draining current one: the title is
// resolved again in the background (its checks wait while a stream starts, quiet.go),
// capped at the playing resolution, without current and the avoided torrents. A pick that
// keeps up with a fitting length becomes a spare, which a later sidegrade poll offers; a
// lower resolution only with lowerOK.
func (s *Service) searchSidegrade(w *tsWarm, q tsResolveQuery, current string, curQ int, lowerOK bool) {
	defer func() {
		s.warmMu.Lock()
		w.searching = false
		s.warmMu.Unlock()
	}()
	if s.flights.running(w.key) {
		return // a resolve of the title runs anyway
	}
	ctx, cancel := context.WithTimeout(tsBackground(w.ctx), tsResolveJobTime)
	defer cancel()
	q.warm = w
	q.upgradeFrom = 0
	if curQ > 0 {
		q.maxQ = curQ // higher ones are the upgrade path
	}
	avoid := map[string]bool{current: true}
	for _, h := range q.avoid {
		avoid[h] = true
	}
	q.avoid = append(append([]string(nil), q.avoid...), current)
	log.Printf("resolve %q: the player drains, searching for a faster source (up to %dp, %d avoided)", q.title, curQ, len(avoid))
	search := s.sideSearchFn
	if search == nil {
		search = s.findSource
	}
	res, seed, err := search(ctx, q)
	if err != nil {
		log.Printf("resolve %q: no faster source: %v", q.title, err)
		return
	}
	if seed != nil {
		seed.cancel() // checks still running end and drop their torrents
		for _, r := range seed.pick.results {
			if r != nil && r.Hash != res.Hash {
				s.dropFrom(w, r.Hash)
			}
		}
		if seed.pick.live {
			// A snapshot of a check that was still running: it was just cancelled
			log.Printf("resolve %q: no faster source (nothing finished good)", q.title)
			return
		}
	}
	rq := tsQualityHeight(res.Quality)
	if res.good() && res.Health != nil && res.unfit == "" && !avoid[res.Hash] && (rq >= curQ || lowerOK) && s.addSpare(w, res) {
		log.Printf("resolve %q: faster source %q (%s) ready for a sidegrade: %s", q.title, res.TorrentTitle, res.Quality, res.Health)
		return
	}
	log.Printf("resolve %q: no faster source (best %q: %s)", q.title, res.TorrentTitle, res.Health)
	s.dropFrom(w, res.Hash)
}

func (s *Service) measureSpares(w *tsWarm, spares []tsResolveResult) {
	ctx := tsBackground(w.ctx)
	defer func() {
		s.warmMu.Lock()
		w.measuring = false
		s.warmMu.Unlock()
	}()
	done := make(chan struct{}, len(spares))
	for i, r := range spares {
		go func(round int, r tsResolveResult) {
			defer func() { done <- struct{}{} }()
			if !s.warmSlot(ctx, r.Hash, "spare measurement") {
				return
			}
			need := tsNeedRate(r.Size, 0, "")
			if r.Health != nil && r.Health.Need > 0 {
				need = r.Health.Need
			}
			h := s.measureWarm(ctx, r.Hash, r.Index, tsMidOffset(r.Size, 2+round), need, tsWarmWindow, tsWarmHooks{})
			<-s.warmRounds
			if h == nil {
				return
			}
			log.Printf("warm: spare %q: %s", r.TorrentTitle, h)
			s.warmMu.Lock()
			if sp := w.spares[r.Hash]; sp != nil {
				sp.res.Health, sp.measuredAt = h, time.Now()
			}
			s.warmMu.Unlock()
		}(i, r)
	}
	for range spares {
		<-done
	}
}

// rewarm brings a cached source of a held title back into TorrServer (its metadata comes from
// TorrServer's DB) and reads its header, which connects peers and fills the probe cache.
func (s *Service) rewarm(w *tsWarm, res *tsResolveResult) {
	s.warmMu.Lock()
	warm := w.source == res.Hash && w.hashes[res.Hash]
	s.warmMu.Unlock()
	if warm || res.Magnet == "" {
		return
	}
	if s.debridOwns(res) {
		s.debridRewarm(w, res)
		return
	}
	s.setSource(w, res.Hash)
	go func() {
		ctx, cancel := context.WithTimeout(w.ctx, 40*time.Second)
		defer cancel()
		var st tsTorrentStat
		if err := s.torrentStat(ctx, res.Magnet, &st); err != nil {
			log.Printf("warm: %s: %v", res.TorrentTitle, err)
			return
		}
		if _, err := s.probeFile(ctx, res.Hash, res.Index); err != nil {
			log.Printf("warm: probe of %s: %v", res.TorrentTitle, err)
		}
	}()
}

// tsNextWarmDue: the next episode is resolved and warmed once 10 minutes of this one are
// left, or at 85 % for episodes shorter than 25 minutes.
func tsNextWarmDue(position, duration float64) bool {
	if duration <= 0 || position <= 0 {
		return false
	}
	if duration < 25*60 {
		return position >= duration*0.85
	}
	return duration-position <= 10*60
}

func (s *Service) torrServerPost(ctx context.Context, body map[string]any, out any) error {
	b, _ := json.Marshal(body)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, s.torrServerURL()+"/torrents", bytes.NewReader(b))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("torrents %v: HTTP %d", body["action"], resp.StatusCode)
	}
	if out != nil {
		return json.NewDecoder(resp.Body).Decode(out)
	}
	return nil
}

// dropTorrent closes a torrent in TorrServer; one saved in its DB stays there (metadata only).
func (s *Service) dropTorrent(hash string) bool {
	if !s.guardDrop(hash) {
		return false
	}
	if s.dropFn != nil {
		s.dropFn(hash)
		return true
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := s.torrServerPost(ctx, map[string]any{"action": "drop", "hash": hash}, nil); err != nil {
		log.Printf("warm: drop %s: %v", hash, err)
	}
	return true
}

func (s *Service) pingTorrent(hash string) {
	if s.pingFn != nil {
		s.pingFn(hash)
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	var st struct{}
	_ = s.torrentStat(ctx, hash, &st)
}

// persistTorrent saves a chosen source in TorrServer's DB, so after a drop (or a TorrServer
// restart) it opens without the metadata exchange.
func (s *Service) persistTorrent(res tsResolveResult) {
	if s.debridOwns(&res) {
		return // TorBox plays it, TorrServer never loads it
	}
	s.debridAddUncached(res)
	if !tsHashPattern.MatchString(res.Hash) || !strings.HasPrefix(res.Magnet, "magnet:?") || s.dropFn != nil {
		return
	}
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		body := map[string]any{"action": "add", "link": res.Magnet, "title": res.TorrentTitle, "category": tsTorrentCategory, "save_to_db": true}
		if err := s.torrServerPost(ctx, body, nil); err != nil {
			log.Printf("warm: save %s to the TorrServer DB: %v", res.Hash, err)
		}
	}()
}

// pruneTorrServerDB removes Nextflix's saved torrents that are closed and older than the
// source cache.
func (s *Service) pruneTorrServerDB(ctx context.Context) {
	var list []struct {
		Hash      string `json:"hash"`
		Category  string `json:"category"`
		Stat      int    `json:"stat"`
		Timestamp int64  `json:"timestamp"`
	}
	if err := s.torrServerPost(ctx, map[string]any{"action": "list"}, &list); err != nil {
		log.Printf("warm: list TorrServer torrents: %v", err)
		return
	}
	cutoff := time.Now().Add(-tsDBKeep).Unix()
	n := 0
	for _, t := range list {
		if t.Category == tsTorrentCategory && t.Stat == 5 && t.Timestamp > 0 && t.Timestamp < cutoff && s.idle(strings.ToLower(t.Hash)) {
			if err := s.torrServerPost(ctx, map[string]any{"action": "rem", "hash": t.Hash}, nil); err == nil {
				n++
			}
		}
	}
	if n > 0 {
		log.Printf("warm: removed %d old torrents from the TorrServer DB", n)
	}
}

// release (POST /release) drops a title's torrents and stops its background work.
//   - resolve query (type, title, ...): every capability variant of that title / episode;
//     delay=<s> (at most 120) waits first, and any new intent (prefetch, resolve, play) cancels it
//   - hash=<h>,<h>: torrents a closing player used, dropped unless they play again
//
// Always 202; meant for navigator.sendBeacon.
func (h handler) release(c *gin.Context) {
	var hashes []string
	for _, v := range strings.Split(c.Query("hash"), ",") {
		if v = strings.ToLower(strings.TrimSpace(v)); tsHashPattern.MatchString(v) && len(hashes) < 10 {
			hashes = append(hashes, v)
		}
	}
	titled := c.Query("type") != "" || c.Query("title") != ""
	if !titled && len(hashes) == 0 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "a resolve query or hash is required"})
		return
	}
	out := gin.H{"status": "released"}
	if titled {
		q, ok := h.parseQuery(c)
		if !ok {
			return
		}
		delay, _ := strconv.Atoi(c.Query("delay"))
		// Clamped before the conversion: a huge delay would overflow to a negative duration
		d := time.Duration(min(max(delay, 0), int(tsReleaseGraceMax/time.Second))) * time.Second
		out["titles"] = h.s.releaseTitle(tsResolveBaseKey(q), d)
		if d > 0 {
			out["status"] = "scheduled"
		}
	}
	if len(hashes) > 0 {
		// why=<reason>: why the player left these torrents (a switch to them failed, or how the
		// upgrade that replaced them went)
		if why := tsPlayerReason(c.Query("why")); why != "" {
			log.Printf("player: dropping %s: %s", tsShortHash(hashes[0]), why)
		}
		h.s.releaseHashes(hashes)
		out["hashes"] = len(hashes)
	}
	c.JSON(http.StatusAccepted, out)
}

// sidegrade answers 200 with a different source of at least the playing resolution that
// keeps up now (a spare of the resolve), else 204. Same query as /upgrade, plus draining=1
// (the buffer drains), critical=1 (nearly empty: lower resolutions too) and avoid=.
func (h handler) sidegrade(c *gin.Context) {
	q, ok := h.parseQuery(c)
	if !ok {
		return
	}
	current := strings.ToLower(c.Query("current"))
	if !tsHashPattern.MatchString(current) {
		c.JSON(http.StatusBadRequest, gin.H{"error": "current must be the playing torrent's hash"})
		return
	}
	curQ, _ := strconv.Atoi(c.Query("current_q"))
	key := tsResolveKey(q)
	if curQ <= 0 {
		if cached, ok := h.s.cachedSource(key); ok {
			curQ = tsQualityHeight(cached.Quality)
		}
	}
	// A fallback above the cap plays: spares and searches stay within the cap
	if q.maxQ > 0 && curQ > q.maxQ {
		curQ = q.maxQ
	}
	q.warm = h.s.holdWarm(key)
	h.s.markIntent(q.warm)
	opt := tsSideOpts{critical: c.Query("critical") == "1"}
	opt.draining = opt.critical || c.Query("draining") == "1"
	if res := h.s.sidegradeFor(key, q, current, curQ, opt); res != nil {
		c.JSON(http.StatusOK, res)
		return
	}
	c.Status(http.StatusNoContent)
}
