package streaming

import (
	"context"
	"errors"
	"log"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/gin-gonic/gin"
)

// Instant play hints (the "⚡" chip on title cards): does a title have a release TorBox has
// cached? Per title the resolve search runs without TorrServer (Jackett and Bitsearch, the
// TMDB names and year, the usual ranking filters), and the best ranked hashes go to one
// checkcached call. The endpoint only reads stored answers and queues missing ones for a
// small worker that gives way to playback. See docs/ARCHITECTURE.md (Instant play).

// Defaults; the TTLs, retry and poll are per tsInstant so tests can shorten them.
const (
	tsInstantHitTTL       = 6 * time.Hour    // a title with a cached release
	tsInstantMissTTL      = time.Hour        // none of its releases is cached (or it has none)
	tsInstantRetry        = 10 * time.Minute // an unknown answer (search or TorBox failed) is tried again after this
	tsInstantStale        = 24 * time.Hour   // an expired answer is still served (and checked again) this long
	tsInstantBusyPoll     = 2 * time.Second
	tsInstantLateWait     = 12 * time.Second // slower indexers may still add releases after the early cut
	tsInstantCheckTime    = 40 * time.Second
	tsInstantPrewarmFirst = 2 * time.Minute
	tsInstantPrewarmEvery = 10 * time.Minute
)

const (
	tsInstantWorkers        = 2   // titles checked at once
	tsInstantHashes         = 20  // best ranked hashes asked per title
	tsInstantMaxItems       = 100 // titles per request
	tsInstantQueueMax       = 300
	tsInstantPrewarmPerHour = 60
	tsInstantMaxBody        = 256 << 10 // request body bytes
)

var errInstantUnknown = errors.New("torbox did not answer for every hash")

// InstantItem is one title of POST /instant; season / episode pick the TV episode searched
// (S01E01 when missing).
type InstantItem struct {
	Type    string `json:"type"`
	TmdbID  int    `json:"tmdb_id"`
	Title   string `json:"title,omitempty"`
	Year    int    `json:"year,omitempty"`
	Season  int    `json:"season,omitempty"`
	Episode int    `json:"episode,omitempty"`
}

func (it InstantItem) key() string { return it.Type + ":" + strconv.Itoa(it.TmdbID) }

func (it InstantItem) valid() bool {
	return (it.Type == "movie" || it.Type == "tv") && it.TmdbID > 0 && len(it.Title) <= 200 &&
		it.Season >= 0 && it.Episode >= 0
}

// InstantFeed lists titles worth checking before anyone asks (Top 10, trending); wired in main.go.
type InstantFeed func() []InstantItem

type tsInstantAnswer struct {
	cached bool
	at     time.Time
}

type tsInstant struct {
	s *Service

	hitTTL, missTTL, retry, busyPoll time.Duration

	mu        sync.Mutex
	answers   map[string]tsInstantAnswer
	failedAt  map[string]time.Time // last unknown answer
	queued    map[string]bool      // waiting or running
	user      []InstantItem        // asked by a client, checked first
	warm      []InstantItem        // prewarm (Top 10, trending)
	prewarmAt []time.Time          // prewarm titles queued within the last hour
	running   int

	wake    chan struct{}
	once    sync.Once
	checkFn func(ctx context.Context, it InstantItem) (*bool, error) // tests replace the search
	busyFn  func() string                                            // tests replace the playback gate
}

func newInstant(s *Service) *tsInstant {
	in := &tsInstant{s: s, answers: map[string]tsInstantAnswer{}, failedAt: map[string]time.Time{}, queued: map[string]bool{},
		wake: make(chan struct{}, 1), hitTTL: tsInstantHitTTL, missTTL: tsInstantMissTTL, retry: tsInstantRetry, busyPoll: tsInstantBusyPoll}
	in.checkFn = s.instantCheck
	return in
}

func (in *tsInstant) ttl(a tsInstantAnswer) time.Duration {
	if a.cached {
		return in.hitTTL
	}
	return in.missTTL
}

// StartInstant runs the instant-play workers on ctx and, with a feed, checks its titles while
// the server is idle (at most tsInstantPrewarmPerHour an hour).
func (s *Service) StartInstant(ctx context.Context, feed InstantFeed) {
	s.instant.start(ctx)
	if feed != nil {
		go s.instant.prewarm(ctx, feed)
	}
}

func (in *tsInstant) start(ctx context.Context) {
	in.once.Do(func() {
		for i := 0; i < tsInstantWorkers; i++ {
			go in.run(ctx)
		}
	})
}

func (in *tsInstant) signal() {
	select {
	case in.wake <- struct{}{}:
	default:
	}
}

// lookup answers what is known (true, false, nil = unknown) and queues titles without a
// fresh answer. With TorBox off every answer is nil and nothing is queued.
func (in *tsInstant) lookup(items []InstantItem) map[string]*bool {
	out := make(map[string]*bool, len(items))
	if !in.s.debrid.enabled() {
		for _, it := range items {
			out[it.key()] = nil
		}
		return out
	}
	in.start(context.Background())
	now := time.Now()
	in.mu.Lock()
	added := false
	for _, it := range items {
		k := it.key()
		if _, dup := out[k]; dup {
			continue
		}
		var v *bool
		fresh := false
		if a, ok := in.answers[k]; ok && now.Sub(a.at) < tsInstantStale {
			c := a.cached
			v, fresh = &c, now.Sub(a.at) < in.ttl(a)
		}
		out[k] = v
		if !fresh && !in.queued[k] && now.Sub(in.failedAt[k]) >= in.retry {
			in.queued[k] = true
			in.user = append(in.user, it)
			added = true
		}
	}
	// A long queue drops its oldest titles; a client asks again for what it still shows
	if over := len(in.user) - tsInstantQueueMax; over > 0 {
		for _, it := range in.user[:over] {
			delete(in.queued, it.key())
		}
		in.user = append([]InstantItem(nil), in.user[over:]...)
	}
	in.pruneLocked(now)
	in.mu.Unlock()
	if added {
		in.signal()
	}
	return out
}

func (in *tsInstant) pruneLocked(now time.Time) {
	if len(in.answers) > 5000 {
		for k, a := range in.answers {
			if now.Sub(a.at) >= tsInstantStale {
				delete(in.answers, k)
			}
		}
	}
	if len(in.failedAt) > 5000 {
		for k, at := range in.failedAt {
			if now.Sub(at) >= in.retry {
				delete(in.failedAt, k)
			}
		}
	}
}

// next takes the next title, a client's first. ok is false once ctx ended.
func (in *tsInstant) next(ctx context.Context) (it InstantItem, prewarm, ok bool) {
	for {
		in.mu.Lock()
		switch {
		case len(in.user) > 0:
			it, in.user = in.user[0], in.user[1:]
		case len(in.warm) > 0:
			it, in.warm, prewarm = in.warm[0], in.warm[1:], true
		}
		if it.TmdbID > 0 {
			in.running++
			more := len(in.user)+len(in.warm) > 0
			in.mu.Unlock()
			if more {
				in.signal() // the other worker
			}
			return it, prewarm, true
		}
		in.mu.Unlock()
		select {
		case <-in.wake:
		case <-ctx.Done():
			return it, false, false
		}
	}
}

func (in *tsInstant) run(ctx context.Context) {
	for {
		it, prewarm, ok := in.next(ctx)
		if !ok {
			return
		}
		in.check(ctx, it, prewarm)
	}
}

// busy names why background checks wait: a stream plays or starts, a resolve runs (a viewer
// pressed Play or opened a detail page), or TorBox is paused after a failure. "" = idle.
func (in *tsInstant) busy() string {
	if in.busyFn != nil {
		return in.busyFn()
	}
	s := in.s
	s.warmMu.Lock()
	playing := len(s.activePlayLocked(time.Now())) > 0
	s.warmMu.Unlock()
	switch {
	case playing:
		return "a stream plays"
	case s.startingStream("") != "":
		return "a stream starts"
	case s.flights.count() > 0:
		return "a resolve runs"
	case !s.debrid.usable():
		return "torbox is paused"
	}
	return ""
}

func (g *tsFlightGroup) count() int {
	g.mu.Lock()
	defer g.mu.Unlock()
	return len(g.m)
}

// check runs one title once nothing else needs the line. A stream that starts meanwhile
// stops it; the title then goes back to the front of its queue.
func (in *tsInstant) check(ctx context.Context, it InstantItem, prewarm bool) {
	k := it.key()
	requeue := func() {
		in.mu.Lock()
		in.running--
		if prewarm {
			in.warm = append([]InstantItem{it}, in.warm...)
		} else {
			in.user = append([]InstantItem{it}, in.user...)
		}
		in.mu.Unlock()
	}
	var since time.Time
	for why := in.busy(); why != ""; why = in.busy() {
		if since.IsZero() {
			since = time.Now()
			log.Printf("instant: waiting while %s", why)
		}
		if !in.s.debrid.enabled() || !tsSleep(ctx, in.busyPoll) {
			in.finish(k, nil, false)
			return
		}
	}
	cctx, cancel := context.WithTimeout(ctx, tsInstantCheckTime)
	defer cancel()
	var stopped atomic.Value // why a running check was stopped (string)
	go func() {
		t := time.NewTicker(in.busyPoll / 2)
		defer t.Stop()
		for {
			select {
			case <-cctx.Done():
				return
			case <-t.C:
				if why := in.busy(); why != "" {
					stopped.Store(why)
					cancel()
					return
				}
			}
		}
	}()
	started := time.Now()
	v, err := in.checkFn(cctx, it)
	took := time.Since(started).Round(time.Millisecond)
	cancel()
	switch {
	case err == nil && v != nil:
		state := "not cached"
		if *v {
			state = "cached"
		}
		log.Printf("instant %s %q: %s (%s)", k, it.Title, state, took)
		in.finish(k, v, false)
	case stopped.Load() != nil && ctx.Err() == nil:
		log.Printf("instant %s %q: stopped after %s, %s", k, it.Title, took, stopped.Load())
		requeue()
	default:
		log.Printf("instant %s %q: unknown after %s: %v", k, it.Title, took, err)
		in.finish(k, nil, ctx.Err() == nil)
	}
}

func (in *tsInstant) finish(k string, v *bool, failed bool) {
	in.mu.Lock()
	defer in.mu.Unlock()
	in.running--
	delete(in.queued, k)
	now := time.Now()
	switch {
	case v != nil:
		in.answers[k] = tsInstantAnswer{cached: *v, at: now}
		delete(in.failedAt, k)
	case failed:
		in.failedAt[k] = now
	}
}

// prewarm queues the feed's titles without a fresh answer while nothing else runs.
func (in *tsInstant) prewarm(ctx context.Context, feed InstantFeed) {
	if !tsSleep(ctx, tsInstantPrewarmFirst) {
		return
	}
	for {
		in.prewarmOnce(feed)
		if !tsSleep(ctx, tsInstantPrewarmEvery) {
			return
		}
	}
}

func (in *tsInstant) prewarmOnce(feed InstantFeed) int {
	if !in.s.debrid.enabled() || in.busy() != "" {
		return 0
	}
	in.mu.Lock()
	idle := len(in.user)+len(in.warm) == 0 && in.running == 0
	in.mu.Unlock()
	if !idle {
		return 0
	}
	items := feed()
	now := time.Now()
	in.mu.Lock()
	defer in.mu.Unlock()
	recent := in.prewarmAt[:0]
	for _, at := range in.prewarmAt {
		if now.Sub(at) < time.Hour {
			recent = append(recent, at)
		}
	}
	in.prewarmAt = recent
	n := 0
	for _, it := range items {
		if len(in.prewarmAt) >= tsInstantPrewarmPerHour {
			break
		}
		k := it.key()
		if !it.valid() || in.queued[k] || now.Sub(in.failedAt[k]) < in.retry {
			continue
		}
		if a, ok := in.answers[k]; ok && now.Sub(a.at) < in.ttl(a) {
			continue
		}
		in.queued[k] = true
		in.warm = append(in.warm, it)
		in.prewarmAt = append(in.prewarmAt, now)
		n++
	}
	if n > 0 {
		log.Printf("instant: prewarming %d titles", n)
		in.signal()
	}
	return n
}

// instantCheck searches a title like resolve does (no TorrServer) and asks TorBox about the
// best ranked hashes: true when one is cached, false when none is (or nothing was found),
// nil with an error when it cannot tell.
func (s *Service) instantCheck(ctx context.Context, it InstantItem) (*bool, error) {
	yes, no := true, false
	q := tsResolveQuery{mediaType: it.Type, title: strings.TrimSpace(it.Title), year: it.Year, tmdbID: it.TmdbID}
	if q.mediaType == "tv" {
		q.season, q.episode = it.Season, it.Episode
		if q.episode < 1 {
			q.season, q.episode = 1, 1
		}
	}
	s.englishTitle(&q)
	if len(tsNormalize(q.title)) == 0 {
		return nil, errors.New("no title")
	}
	if _, blocked := s.notOut(q); blocked {
		return &no, nil
	}
	if !s.debrid.usable() {
		return nil, errDebridPaused
	}
	// Slower indexers keep searching on the warm context: they end with this check
	wctx, cancel := context.WithCancel(ctx)
	defer cancel()
	q.warm = &tsWarm{key: "instant|" + it.key(), ctx: wctx, cancel: cancel}
	started := time.Now()
	runtime := s.lookupRuntime(q)
	names := s.titleNamesLater(q)
	sr, q, _, err := s.searchTitle(wctx, q, started, names)
	if err != nil {
		return nil, err
	}
	q.runtime, _, _ = runtime()
	raw := sr.raw
	hit, known := s.instantAsk(wctx, raw, q)
	if !hit && sr.late != nil {
		before := len(raw)
		wait := time.NewTimer(tsInstantLateWait)
		defer wait.Stop()
	collect:
		for {
			select {
			case batch, ok := <-sr.late:
				if !ok {
					break collect
				}
				raw = append(raw, batch...)
			case <-wait.C:
				break collect
			case <-wctx.Done():
				break collect
			}
		}
		if len(raw) > before && wctx.Err() == nil {
			tsApplyAbsolute(&q, raw)
			hit, known = s.instantAsk(wctx, raw, q)
		}
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	switch {
	case hit:
		return &yes, nil
	case !known:
		return nil, errInstantUnknown
	}
	return &no, nil
}

// instantAsk asks TorBox about the best ranked hashes of raw: cinema recordings and
// releases below 720p left out. known is false when TorBox left one of them unanswered.
func (s *Service) instantAsk(ctx context.Context, raw []JackettResult, q tsResolveQuery) (hit, known bool) {
	var hashes []string
	seen := map[string]bool{}
	for _, c := range tsRankAll(raw, q) {
		h := strings.ToLower(c.hash)
		if h == "" || seen[h] || c.parts.Source <= tsLastResort/2 || (c.quality > 0 && c.quality < 720) {
			continue
		}
		seen[h] = true
		hashes = append(hashes, h)
		if len(hashes) >= tsInstantHashes {
			break
		}
	}
	if len(hashes) == 0 {
		log.Printf("instant %q: no usable release among %d results", q.title, len(raw))
		return false, true
	}
	n := len(s.debrid.cachedItems(ctx, hashes))
	log.Printf("instant %q: torbox has %d of the best %d releases cached (%d results)", q.title, n, len(hashes), len(raw))
	if n > 0 {
		return true, true
	}
	return false, s.debrid.allKnown(hashes)
}

// allKnown reports whether TorBox answered for every hash within tsDebridCacheTTL.
func (d *tsDebrid) allKnown(hashes []string) bool {
	now := time.Now()
	d.mu.Lock()
	defer d.mu.Unlock()
	for _, h := range hashes {
		if c, ok := d.checks[strings.ToLower(h)]; !ok || now.Sub(c.at) >= tsDebridCacheTTL {
			return false
		}
	}
	return true
}

// instant answers which titles have a release TorBox has cached (see tsInstant).
func (h handler) instant(c *gin.Context) {
	var body struct {
		Items []InstantItem `json:"items"`
	}
	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, tsInstantMaxBody)
	if err := c.ShouldBindJSON(&body); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "items must be a list of {type, tmdb_id}"})
		return
	}
	if len(body.Items) > tsInstantMaxItems {
		body.Items = body.Items[:tsInstantMaxItems]
	}
	items := body.Items[:0]
	for _, it := range body.Items {
		it.Title = strings.TrimSpace(it.Title)
		if it.valid() {
			items = append(items, it)
		}
	}
	c.JSON(http.StatusOK, gin.H{"enabled": h.s.debrid.enabled(), "results": h.s.instant.lookup(items)})
}
