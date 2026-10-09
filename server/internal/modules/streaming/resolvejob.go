package streaming

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/gin-gonic/gin"

	"nextflix/internal/platform/httpserver"
)

// A resolve runs as one background job per title/episode. GET /resolve and POST /prefetch
// for the same request share that job, so a prefetch and a click never search twice.

const (
	tsResolveJobTime     = 90 * time.Second
	tsPrefetchMax        = 3               // concurrent background prefetch jobs
	tsPrefetchRetryAfter = 5 * time.Minute // a failed request is not prefetched again before this
	tsAvoidMax           = 5               // hashes in ?avoid=
)

// tsResolveError carries the HTTP status and a message that is safe to show.
type tsResolveError struct {
	status int
	msg    string
}

func (e *tsResolveError) Error() string { return e.msg }

var errNoSource = &tsResolveError{status: http.StatusNotFound, msg: "No playable source found"}

type tsFlight struct {
	done    chan struct{}
	res     *tsResolveResult
	err     error
	waiters atomic.Int32 // GET /resolve requests waiting for it
}

// tsFlightGroup runs at most one job per key; later callers wait for the running one.
type tsFlightGroup struct {
	mu sync.Mutex
	m  map[string]*tsFlight
}

// start runs fn in the background unless a job for key is already running.
// started reports whether this call started it.
func (g *tsFlightGroup) start(key string, fn func() (*tsResolveResult, error)) (f *tsFlight, started bool) {
	g.mu.Lock()
	if f, ok := g.m[key]; ok {
		g.mu.Unlock()
		return f, false
	}
	if g.m == nil {
		g.m = map[string]*tsFlight{}
	}
	f = &tsFlight{done: make(chan struct{})}
	g.m[key] = f
	g.mu.Unlock()

	go func() {
		defer func() {
			if r := recover(); r != nil {
				f.res, f.err = nil, fmt.Errorf("resolve panic: %v", r)
			}
			g.mu.Lock()
			delete(g.m, key)
			g.mu.Unlock()
			close(f.done)
		}()
		f.res, f.err = fn()
	}()
	return f, true
}

// awaited reports whether a player is waiting for the job of key.
func (g *tsFlightGroup) awaited(key string) bool {
	g.mu.Lock()
	defer g.mu.Unlock()
	f, ok := g.m[key]
	return ok && f.waiters.Load() > 0
}

func (g *tsFlightGroup) running(key string) bool {
	g.mu.Lock()
	defer g.mu.Unlock()
	_, ok := g.m[key]
	return ok
}

// startResolve starts (or joins) the resolve job for q; a found source is cached before the job
// ends. The job belongs to the title's warm state: a release cancels it.
func (s *Service) startResolve(key string, q tsResolveQuery) (*tsFlight, bool) {
	q.warm = s.holdWarm(key)
	return s.flights.start(key, func() (*tsResolveResult, error) {
		ctx, cancel := context.WithTimeout(q.warmCtx(), tsResolveJobTime)
		defer cancel()
		started := time.Now()
		res, seed, err := s.findSource(ctx, q)
		if err != nil {
			// A released title did not fail; one that gave way to playback runs again on resume
			if q.warmCtx().Err() == nil {
				if s.pausedSince(q.warm, started) {
					s.deferPrefetch(q.warm, q)
				} else {
					s.failedAt.Store(key, time.Now())
				}
			}
			log.Printf("resolve %q: failed after %s: %v", q.title, time.Since(started).Round(time.Millisecond), err)
			return nil, err
		}
		s.failedAt.Delete(key)
		s.storeSource(key, *res)
		// After the store, so a quick upgrade is never overwritten by this pick
		s.afterPick(key, res, seed)
		go s.mergeLate(key, q, res, seed)
		capped := ""
		if q.maxQ > 0 {
			capped = fmt.Sprintf(" (max %dp)", q.maxQ)
		}
		log.Printf("resolve %q%s: picked %q in %s, health: %s", q.title, capped, res.TorrentTitle,
			time.Since(started).Round(time.Millisecond), res.Health)
		return res, nil
	})
}

// clientHEVC reports whether the browser said it decodes HEVC (?hevc=1).
func clientHEVC(c *gin.Context) bool { return c.Query("hevc") == "1" }

// parseResolveQuery reads type/title/year/season/episode, the client capability (hevc, hdr,
// dv, atmos, acodecs),
// the optional tmdb_id, runtime in minutes (used when there is no TMDB runtime) and quality
// cap (maxq=1080|720) and answers 400 when they are invalid.
func parseResolveQuery(c *gin.Context) (tsResolveQuery, bool) {
	q := tsResolveQuery{
		mediaType: c.Query("type"),
		title:     strings.TrimSpace(c.Query("title")),
	}
	q.year, _ = strconv.Atoi(c.Query("year"))
	q.season, _ = strconv.Atoi(c.Query("season"))
	q.episode, _ = strconv.Atoi(c.Query("episode"))
	q.hevc = clientHEVC(c)
	q.hdr = q.hevc && c.Query("hdr") == "1"
	q.dv = q.hevc && c.Query("dv") == "1"
	q.atmos = c.Query("atmos") == "1"
	q.acodecs = clientAudioCodecs(c)
	// A cinema recording of a film still in cinemas (cinema.go)
	q.cinema = c.Query("cinema") == "1"
	q.lang = httpserver.Lang(c) // the profile language: its dub is fine too (see lang.go)
	if rt, err := strconv.Atoi(c.Query("runtime")); err == nil && rt >= 1 && rt <= 600 {
		q.runtime = rt
	}
	if id, err := strconv.Atoi(c.Query("tmdb_id")); err == nil && id > 0 {
		q.tmdbID = id
	}
	// Only the caps the player steps down to; anything else means no cap
	if mq, _ := strconv.Atoi(c.Query("maxq")); mq == 1080 || mq == 720 {
		q.maxQ = mq
	}
	// Torrents the player found broken or of the wrong length
	for _, h := range strings.Split(c.Query("avoid"), ",") {
		if h = strings.ToLower(strings.TrimSpace(h)); tsHashPattern.MatchString(h) && len(q.avoid) < tsAvoidMax {
			q.avoid = append(q.avoid, h)
		}
	}
	if q.mediaType != "movie" && q.mediaType != "tv" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "type must be movie or tv"})
		return q, false
	}
	if len(tsNormalize(q.title)) == 0 || len(q.title) > 200 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "title is required"})
		return q, false
	}
	if q.mediaType == "tv" && (q.season < 0 || q.episode < 1) {
		c.JSON(http.StatusBadRequest, gin.H{"error": "season and episode are required for tv"})
		return q, false
	}
	if q.mediaType == "movie" {
		q.season, q.episode = 0, 0
	}
	return q, true
}

// parseQuery is parseResolveQuery with the English title of the tmdb_id (see englishTitle).
func (h handler) parseQuery(c *gin.Context) (tsResolveQuery, bool) {
	q, ok := parseResolveQuery(c)
	if ok {
		h.s.englishTitle(&q)
	}
	return q, ok
}

// resolve finds the best playable torrent for a movie or episode.
// ?refresh=1 skips the cache and avoids the previously cached torrent.
func (h handler) resolve(c *gin.Context) {
	s := h.s
	q, ok := h.parseQuery(c)
	if !ok {
		return
	}
	if c.Query("dry") == "1" {
		s.dryResolve(c, q)
		return
	}
	key := tsResolveKey(q)
	// cached=1 only reports a known source; it never searches, joins a swarm or holds the title
	if c.Query("cached") == "1" {
		if res, ok := s.cachedFor(key, q); ok {
			res.Cached = true
			c.JSON(http.StatusOK, res)
		} else {
			c.JSON(http.StatusNotFound, gin.H{"error": "not cached"})
		}
		return
	}
	refresh := c.Query("refresh") == "1" || c.Query("refresh") == "true"
	res, ok := s.cachedFor(key, q)
	if ok && !refresh && tsHashIn(res.Hash, q.avoid) {
		// The player left the cached torrent (too slow, broken): search without it
		log.Printf("resolve %q: the cached %s is avoided, searching again", q.title, tsShortHash(res.Hash))
		s.forgetSource(key)
		ok = false
	}
	if ok && !refresh {
		// Play is about to open it: the title is held, and released with the player
		w := s.holdWarm(key)
		s.markIntent(w) // a player asks: the title never gives way to another one's playback
		s.useSource(w, res)
		res.Cached = true
		c.JSON(http.StatusOK, res)
		return
	}
	// Titles that are not out yet (or only in cinemas) are not searched
	if !s.flights.running(key) {
		if a, blocked := s.notOut(q); blocked {
			c.JSON(statusTooEarly, tsNotOutBody(a))
			return
		}
	}
	if refresh {
		if prev, ok := s.cachedSource(key); ok {
			q.avoid = append(q.avoid, prev.Hash)
		}
		s.forgetSource(key)
	}

	s.markIntent(s.holdWarm(key))
	f, _ := s.startResolve(key, q)
	f.waiters.Add(1)
	defer f.waiters.Add(-1)
	select {
	case <-f.done:
	case <-c.Request.Context().Done():
		return // the viewer gave up; the job keeps running until the player releases the title
	}
	if f.err != nil {
		// The search may have found that the film is only in cinemas after all (homedur.go)
		if errors.Is(f.err, errNoSource) {
			if a, blocked := s.notOut(q); blocked {
				c.JSON(statusTooEarly, tsNotOutBody(a))
				return
			}
		}
		var re *tsResolveError
		if errors.As(f.err, &re) {
			c.JSON(re.status, gin.H{"error": re.msg})
		} else {
			c.JSON(http.StatusNotFound, gin.H{"error": errNoSource.msg})
		}
		return
	}
	c.JSON(http.StatusOK, f.res)
}

// prefetch starts a background resolve so a later Play starts instantly. Always answers 202.
func (h handler) prefetch(c *gin.Context) {
	s := h.s
	q, ok := h.parseQuery(c)
	if !ok {
		return
	}
	key := tsResolveKey(q)
	// The next episode is only warmed near the end of the current one (?next_pos=&next_dur=)
	if pos, dur := c.Query("next_pos"), c.Query("next_dur"); pos != "" || dur != "" {
		p, _ := strconv.ParseFloat(pos, 64)
		d, _ := strconv.ParseFloat(dur, 64)
		if !tsNextWarmDue(p, d) {
			c.JSON(http.StatusAccepted, gin.H{"status": "early"})
			return
		}
	}
	if !s.cachedOrRunning(key, q) {
		if a, blocked := s.notOut(q); blocked {
			c.JSON(http.StatusAccepted, gin.H{"status": "not_released", "availability": a.Status,
				"release_date": tsDateOrNil(a.ReleaseDate), "digital_date": tsDateOrNil(a.DigitalDate)})
			return
		}
	}
	w := s.holdWarm(key)
	if c.Query("next_pos") != "" {
		s.markNext(w) // the next episode of a playing show is never paused
	}
	// While another title plays, this one waits; it starts when the playback ends
	if s.warmYield(w) != "" && !s.flights.running(key) {
		s.deferPrefetch(w, q)
		c.JSON(http.StatusAccepted, gin.H{"status": "deferred"})
		return
	}
	c.JSON(http.StatusAccepted, gin.H{"status": s.prefetchStart(key, q, w)})
}

// prefetchStart warms a cached source again or starts a background resolve (at most
// tsPrefetchMax at once) and returns the prefetch status.
func (s *Service) prefetchStart(key string, q tsResolveQuery, w *tsWarm) string {
	switch {
	case s.cachedOrRunning(key, q):
		if res, ok := s.cachedFor(key, q); ok {
			s.rewarm(w, res)
		}
		return "ready"
	case s.failedRecently(key):
		return "failed_recently"
	}
	select {
	case s.prefetchSlots <- struct{}{}:
		f, started := s.startResolve(key, q)
		if !started {
			<-s.prefetchSlots
			return "running"
		}
		go func() {
			<-f.done
			<-s.prefetchSlots
		}()
		return "started"
	default:
		return "busy"
	}
}

// tsHashIn reports whether hash is one of hashes (lower case).
func tsHashIn(hash string, hashes []string) bool {
	hash = strings.ToLower(hash)
	for _, h := range hashes {
		if h == hash {
			return true
		}
	}
	return false
}

func (s *Service) cachedOrRunning(key string, q tsResolveQuery) bool {
	if _, ok := s.cachedFor(key, q); ok {
		return true
	}
	return s.flights.running(key)
}

func (s *Service) failedRecently(key string) bool {
	v, ok := s.failedAt.Load(key)
	return ok && time.Since(v.(time.Time)) < tsPrefetchRetryAfter
}
