package streaming

import (
	"context"
	"log"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/gin-gonic/gin"
)

// 4K-first without stalls: when resolve has to answer with a lower resolution while a
// higher one is still warming up, it keeps warming that one in the background. Once it
// keeps up (and its duration is verified) it becomes the cached source and the player,
// which polls GET /upgrade, switches to it at the same position.

const (
	tsUpgradeWarmTime    = 90 * time.Second // longest background warm-up, from the start of the checks
	tsUpgradePause       = 3 * time.Second  // between two measurements of one candidate
	tsUpgradeMaxCands    = 4                // distinct torrents tried per title
	tsUpgradeFollowers   = 2                // ... warmed at once
	tsUpgradeYieldRounds = 2                // a starving one gives way to the next after this many rounds
	tsUpgradeMax         = 3                // titles warmed at once
	tsUpgradeRounds      = 2                // background measurements at once (they share the line with playback)
	tsUpgradeRetryAfter  = 10 * time.Minute // a title whose upgrade search found nothing is not searched again before this
	tsUpgradeForget      = time.Hour        // finished upgrade states are dropped after this
)

// tsUpgradeSeed is what findSource leaves for afterPick / watchUpgrade.
type tsUpgradeSeed struct {
	q      tsResolveQuery
	cands  []tsCandidate
	pick   *tsPick
	ctx    context.Context // bounds the background warm-up (tsUpgradeWarmTime)
	cancel context.CancelFunc
	raw    []JackettResult        // search results the candidates came from
	late   <-chan []JackettResult // results of indexers still searching (nil when none)
	base   string                 // Jackett URL that answered
}

// tsUpgrade is the upgrade state of one resolve key.
type tsUpgrade struct {
	mu      sync.Mutex
	ready   *tsResolveResult
	running bool
	ended   time.Time
}

func (u *tsUpgrade) result() *tsResolveResult {
	u.mu.Lock()
	defer u.mu.Unlock()
	return u.ready
}

// beginUpgrade claims the upgrade of key unless one is running or all slots are busy.
func (s *Service) beginUpgrade(key string) (*tsUpgrade, bool) {
	now := time.Now()
	s.upgrades.Range(func(k, v any) bool {
		u := v.(*tsUpgrade)
		u.mu.Lock()
		stale := !u.running && now.Sub(u.ended) > tsUpgradeForget
		u.mu.Unlock()
		if stale && k != key {
			s.upgrades.Delete(k)
		}
		return true
	})
	v, _ := s.upgrades.LoadOrStore(key, &tsUpgrade{})
	u := v.(*tsUpgrade)
	u.mu.Lock()
	defer u.mu.Unlock()
	if u.running {
		return u, false
	}
	select {
	case s.upgradeSlots <- struct{}{}:
	default:
		return u, false
	}
	u.running, u.ready = true, nil
	return u, true
}

func (s *Service) endUpgrade(u *tsUpgrade) {
	u.mu.Lock()
	u.running, u.ended = false, time.Now()
	u.mu.Unlock()
	<-s.upgradeSlots
}

// offerUpgrade makes up the ready upgrade of key; the first one wins. It becomes the cached
// source and the title's warm source; the source it replaces is dropped unless it plays.
func (s *Service) offerUpgrade(key string, q tsResolveQuery, up *tsResolveResult) bool {
	v, _ := s.upgrades.LoadOrStore(key, &tsUpgrade{})
	u := v.(*tsUpgrade)
	u.mu.Lock()
	stale := u.ready != nil && !u.running && time.Since(u.ended) > tsUpgradeWarmTime
	first := u.ready == nil || stale
	if first {
		up.Cached = false
		s.storeSource(key, *up) // before it is offered, so cache and offer agree
		u.ready = up
		if !u.running {
			u.ended = time.Now()
		}
	}
	u.mu.Unlock()
	if first {
		log.Printf("resolve %q: upgrade to %q (%s) ready: %s", q.title, up.TorrentTitle, up.Quality, up.Health)
		if prev := s.useSource(q.warm, up); prev != "" && prev != up.Hash {
			s.dropFrom(q.warm, prev)
		}
	}
	return first
}

// afterPick sorts out the other candidates of a finished resolve: higher resolutions keep
// warming for an upgrade (HEVC clients without a quality cap), up to tsSpareMax of the
// picked resolution stay as spares for a sidegrade, and every other torrent is dropped.
// Resolutions are the probed ones once a check read the file's header. While the pick
// starves, an alternative that keeps up (or is clearly faster) also becomes the cached source.
func (s *Service) afterPick(key string, res *tsResolveResult, seed *tsUpgradeSeed) {
	if seed == nil {
		return
	}
	q := seed.q
	s.useSource(q.warm, res)
	if seed.pick.live {
		// Answered with a snapshot: the pick plays now, measuring it further only competes.
		// Its duration check goes on, so the probe the player waits for is not thrown away
		seed.pick.stopMeasure(seed.pick.best)
	}
	floor := tsQualityHeight(res.Quality)
	var watch *tsUpgradeWatch
	if q.hevc && q.maxQ == 0 && floor < 2160 && seed.hasAbove(floor) {
		if u, ok := s.beginUpgrade(key); ok {
			watch = s.newUpgradeWatch(u, key, seed, floor)
		}
	}
	improve := func(r *tsResolveResult) {
		if res.keepsUp() || r.Health == nil || r.Health.Dead || !r.durationOK() || r.unfit != "" {
			return
		}
		// A lower resolution only replaces a starving pick when it keeps up
		if tsQualityHeight(r.Quality) < floor && !r.Health.KeepsUp {
			return
		}
		if !r.Health.KeepsUp && !tsFaster(r.Health, res.Health) {
			return
		}
		if cached, ok := s.cachedSource(key); ok && cached.Hash == res.Hash {
			s.storeSource(key, *r)
			log.Printf("resolve %q: %q is the source from now on (the pick starves): %s", q.title, r.TorrentTitle, r.Health)
		}
	}
	place := func(i int, r *tsResolveResult, tryWatch bool) {
		if r == nil || r.Hash == res.Hash {
			return
		}
		if tryWatch && watch != nil && tsQualityHeight(r.Quality) > floor && watch.offer(r) {
			return
		}
		improve(r)
		if (tsSpareWorthy(r, floor, q) || tsLowerSpare(r, res)) && s.addSpare(q.warm, r) {
			return
		}
		s.dropFrom(q.warm, r.Hash)
	}
	pending := 0
	for i := range seed.cands {
		if i == seed.pick.best {
			continue
		}
		if seed.pick.kept[i] {
			pending++
			if watch != nil && seed.cands[i].quality > floor {
				watch.expect()
			}
			continue
		}
		place(i, seed.pick.results[i], true)
	}
	if watch != nil {
		watch.start()
	}
	if pending == 0 {
		if watch == nil {
			seed.cancel()
		}
		return
	}
	go func() {
		if watch == nil {
			defer seed.cancel()
		}
		for pending > 0 {
			select {
			case o := <-seed.pick.later:
				if !seed.pick.kept[o.i] || o.i == seed.pick.best {
					continue
				}
				pending--
				var r *tsResolveResult
				if o.err != nil {
					log.Printf("resolve %q: candidate %q rejected: %v", q.title, seed.cands[o.i].raw.Title, o.err)
				} else {
					r = o.res
				}
				if watch != nil && seed.cands[o.i].quality > floor {
					up := r
					if r != nil && tsQualityHeight(r.Quality) <= floor {
						up = nil // its file is not of a higher resolution after all
					}
					if watch.arrived(up) {
						continue
					}
				}
				place(o.i, r, false)
			case <-seed.ctx.Done():
				return
			}
		}
	}()
}

// watchUpgrade follows the candidates of an upgrade search above floor until one keeps up,
// all are dead or seed.ctx ends. It owns u and the seed.
func (s *Service) watchUpgrade(u *tsUpgrade, key string, seed *tsUpgradeSeed, floor int) {
	watch := s.newUpgradeWatch(u, key, seed, floor)
	above := func(r *tsResolveResult) bool { return r != nil && tsQualityHeight(r.Quality) > floor }
	pending := 0
	for i, c := range seed.cands {
		if c.quality <= floor {
			continue
		}
		if seed.pick.kept[i] {
			pending++
			watch.expect()
		} else if r := seed.pick.results[i]; r != nil && !(above(r) && watch.offer(r)) {
			s.dropFrom(seed.q.warm, r.Hash)
		}
	}
	watch.start()
	if pending == 0 {
		return
	}
	go func() {
		for pending > 0 {
			select {
			case o := <-seed.pick.later:
				if !seed.pick.kept[o.i] {
					continue
				}
				pending--
				switch {
				case o.err != nil:
					log.Printf("resolve %q: upgrade candidate %q rejected: %v", seed.q.title, seed.cands[o.i].raw.Title, o.err)
					watch.arrived(nil)
				case !above(o.res):
					watch.arrived(nil)
					s.dropFrom(seed.q.warm, o.res.Hash)
				case !watch.arrived(o.res):
					s.dropFrom(seed.q.warm, o.res.Hash)
				}
			case <-seed.ctx.Done():
				return
			}
		}
	}()
}

// hasAbove reports whether a candidate above floor finished alive or is still running (not
// the pick itself, and finished ones by their probed resolution).
func (seed *tsUpgradeSeed) hasAbove(floor int) bool {
	for i, c := range seed.cands {
		if c.quality <= floor || i == seed.pick.best {
			continue
		}
		r := seed.pick.results[i]
		if seed.pick.kept[i] || (r != nil && tsQualityHeight(r.Quality) > floor && (r.Health == nil || !r.Health.Dead)) {
			return true
		}
	}
	return false
}

// tsUpgradeWatch warms the distinct torrents above floor, tsUpgradeFollowers at once and at
// most tsUpgradeMaxCands in all, best-ranked first. A starving one gives way to the next
// after tsUpgradeYieldRounds; the first that keeps up becomes the upgrade.
type tsUpgradeWatch struct {
	s      *Service
	u      *tsUpgrade
	key    string
	seed   *tsUpgradeSeed
	floor  int
	ctx    context.Context // ends with seed.ctx, or once an upgrade is ready
	cancel context.CancelFunc

	mu       sync.Mutex
	queue    []*tsResolveResult
	seen     map[string]bool
	active   int
	expected int // kept checks above floor that have not reported yet
	finished bool
}

func (s *Service) newUpgradeWatch(u *tsUpgrade, key string, seed *tsUpgradeSeed, floor int) *tsUpgradeWatch {
	ctx, cancel := context.WithCancel(seed.ctx)
	return &tsUpgradeWatch{s: s, u: u, key: key, seed: seed, floor: floor, ctx: ctx, cancel: cancel, seen: map[string]bool{}}
}

func (w *tsUpgradeWatch) expect() {
	w.mu.Lock()
	w.expected++
	w.mu.Unlock()
}

// arrived reports a kept check (r nil when it failed); true when r was queued.
func (w *tsUpgradeWatch) arrived(r *tsResolveResult) bool {
	w.mu.Lock()
	w.expected--
	w.mu.Unlock()
	taken := w.offer(r)
	w.pump()
	return taken
}

// offer queues r unless it is dead, a torrent already tried or over the limit.
func (w *tsUpgradeWatch) offer(r *tsResolveResult) bool {
	if r == nil || (r.Health != nil && r.Health.Dead) {
		return false
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.finished || w.seen[r.Hash] || len(w.seen) >= tsUpgradeMaxCands {
		return false
	}
	w.seen[r.Hash] = true
	w.queue = append(w.queue, r)
	return true
}

func (w *tsUpgradeWatch) start() {
	go func() {
		<-w.ctx.Done()
		w.pump()
	}()
	w.pump()
}

// pump starts followers for queued candidates and finishes once nothing is left.
func (w *tsUpgradeWatch) pump() {
	w.mu.Lock()
	over := w.ctx.Err() != nil
	for !w.finished && !over && w.active < tsUpgradeFollowers && len(w.queue) > 0 {
		r := w.queue[0]
		w.queue = w.queue[1:]
		w.active++
		go w.follow(r)
	}
	done := !w.finished && w.active == 0 && (over || (len(w.queue) == 0 && w.expected <= 0))
	var leftovers []*tsResolveResult
	if done {
		w.finished = true
		leftovers, w.queue = w.queue, nil
	}
	w.mu.Unlock()
	if !done {
		return
	}
	for _, r := range leftovers {
		w.s.dropFrom(w.seed.q.warm, r.Hash)
	}
	w.cancel()
	w.seed.cancel()
	if w.u.result() == nil {
		log.Printf("resolve %q: no upgrade above %dp", w.seed.q.title, w.floor)
	}
	w.s.endUpgrade(w.u)
}

func (w *tsUpgradeWatch) follow(r *tsResolveResult) {
	waiting := func() bool {
		w.mu.Lock()
		defer w.mu.Unlock()
		return len(w.queue) > 0
	}
	up := w.s.keepWarming(w.ctx, w.seed.q, *r, waiting)
	if up != nil && tsQualityHeight(up.Quality) > w.floor && w.s.offerUpgrade(w.key, w.seed.q, up) {
		w.cancel() // stop warming the others
	} else {
		w.s.dropFrom(w.seed.q.warm, r.Hash)
	}
	w.mu.Lock()
	w.active--
	w.mu.Unlock()
	w.pump()
}

// keepWarming measures r again and again (each time a fresh part of the file) until it keeps
// up with a verified duration (returned), its swarm is dead, its duration is wrong or ctx
// ends. A starving one gives up after tsUpgradeYieldRounds when yield (optional) says another
// candidate is waiting.
func (s *Service) keepWarming(ctx context.Context, q tsResolveQuery, r tsResolveResult, yield func() bool) *tsResolveResult {
	need := tsNeedRate(r.Size, q.runtime, q.mediaType)
	for round := 1; ctx.Err() == nil; round++ {
		if r.Health == nil || !r.Health.KeepsUp {
			if round > tsUpgradeYieldRounds && yield != nil && yield() {
				log.Printf("resolve %q: upgrade %q still starving, trying the next one", q.title, r.TorrentTitle)
				return nil
			}
			if round > 1 && !tsSleep(ctx, tsUpgradePause) {
				return nil
			}
			if !s.warmSlot(ctx, r.Hash, "upgrade warm-up") {
				return nil
			}
			h := s.measureWarm(ctx, r.Hash, r.Index, tsMidOffset(r.Size, round), need, tsWarmWindowTop, tsWarmHooks{})
			<-s.warmRounds
			if h == nil {
				continue
			}
			r.Health = h
			if h.Dead {
				log.Printf("resolve %q: upgrade %q: swarm dead", q.title, r.TorrentTitle)
				return nil
			}
			if !h.KeepsUp {
				continue
			}
		}
		if r.durationOK() {
			return &r
		}
		// The probe failed while the swarm was cold; it is warm now
		p, err := s.probeFileTimeout(ctx, r.Hash, r.Index, tsDurationRetry)
		if err != nil {
			if !tsSleep(ctx, tsUpgradePause) {
				return nil
			}
			continue
		}
		// A file without a duration in its header can never be verified
		if _, ok, _ := s.judgeDuration(r.TorrentTitle, q, p.duration(), time.Now()); !ok {
			return nil
		}
		s.applyProbedQuality(&r)
		r.DurationVerified = true
		return &r
	}
	return nil
}

func tsSleep(ctx context.Context, d time.Duration) bool {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-t.C:
		return true
	case <-ctx.Done():
		return false
	}
}

func tsQualityHeight(q string) int {
	switch q {
	case "2160p":
		return 2160
	case "1080p":
		return 1080
	case "720p":
		return 720
	case "480p":
		return 480
	}
	return 0
}

// upgradeFor returns a source better than the playing one (current, of curQ, 0 = unknown)
// once it keeps up, or nil. Without a watcher (the source came from the cache, or the
// watcher found nothing long enough ago) it starts a background upgrade search.
func (s *Service) upgradeFor(key string, q tsResolveQuery, current string, curQ int) *tsResolveResult {
	cached, hasCached := s.cachedSource(key)
	// The server knows the cached source's probed resolution; it wins over the client's guess
	if hasCached && cached.Hash == current && tsQualityHeight(cached.Quality) > 0 {
		curQ = tsQualityHeight(cached.Quality)
	}
	if curQ <= 0 {
		curQ = 1080
	}
	better := func(r *tsResolveResult) bool {
		return r != nil && r.Hash != current && tsQualityHeight(r.Quality) > curQ && r.Health != nil && r.Health.KeepsUp &&
			!r.Health.Dead && !r.partial && r.durationOK()
	}
	var u *tsUpgrade
	if v, ok := s.upgrades.Load(key); ok {
		u = v.(*tsUpgrade)
		if r := u.result(); better(r) {
			res := *r
			return &res
		}
	}
	if hasCached && better(cached) {
		return cached
	}
	if curQ >= 2160 || s.flights.running(key) {
		return nil
	}
	if u != nil {
		u.mu.Lock()
		recent := u.running || time.Since(u.ended) < tsUpgradeRetryAfter
		u.mu.Unlock()
		if recent {
			return nil
		}
	}
	s.startUpgradeSearch(key, q, current, curQ)
	return nil
}

// startUpgradeSearch searches the title again for resolutions above curQ only and warms
// them up like a resolve does; a winner lands in the source cache.
func (s *Service) startUpgradeSearch(key string, q tsResolveQuery, current string, curQ int) {
	u, ok := s.beginUpgrade(key)
	if !ok {
		return
	}
	go func() {
		ctx, cancel := context.WithTimeout(tsBackground(q.warmCtx()), tsResolveJobTime)
		defer cancel()
		q.upgradeFrom = curQ
		q.avoid = append(q.avoid, current)
		log.Printf("resolve %q: searching for an upgrade above %dp", q.title, curQ)
		_, seed, err := s.findSource(ctx, q)
		if err != nil {
			log.Printf("resolve %q: no upgrade above %dp: %v", q.title, curQ, err)
			s.endUpgrade(u)
			return
		}
		s.watchUpgrade(u, key, seed, curQ)
	}()
}

// upgrade answers 200 with a better source than ?current= once one keeps up, else 204.
// Takes the resolve query plus current=<hash> and optional current_q=2160|1080|720|480.
func (h handler) upgrade(c *gin.Context) {
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
	if tsQualityLabel(curQ) == "" {
		curQ = 0
	}
	if !q.hevc || q.maxQ > 0 {
		c.Status(http.StatusNoContent)
		return
	}
	key := tsResolveKey(q)
	q.warm = h.s.holdWarm(key) // a playing client asks: the title stays warm
	h.s.markIntent(q.warm)
	if res := h.s.upgradeFor(key, q, current, curQ); res != nil {
		c.JSON(http.StatusOK, res)
		return
	}
	c.Status(http.StatusNoContent)
}
