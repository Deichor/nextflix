package streaming

import (
	"context"
	"fmt"
	"log"
	"math"
	"strconv"
	"time"

	"github.com/gin-gonic/gin"

	"nextflix/internal/platform/tmdb"
)

// Resolve compares the duration of each candidate file with the TMDB runtime, so a mislabeled
// or incomplete release (an 86 minute file for a 172 minute film) is never picked.

const (
	tsRuntimeTTL     = 24 * time.Hour
	tsRuntimeMissTTL = 10 * time.Minute // a failed lookup is retried after this
	tsRuntimeWait    = 5 * time.Second  // longest wait for the lookup once the search is done
)

// Vars so tests can shorten them.
var (
	tsDurationProbe = 12 * time.Second // ffprobe of a candidate's header on any swarm
	tsDurationRetry = 25 * time.Second // ... and how much longer once the warm-up shows peers or data
)

// TMDBClient is the part of the shared TMDB client (*tmdb.Client) resolve needs for runtimes.
type TMDBClient interface {
	GetMovieDetails(id int) (*tmdb.MovieDetails, error)
	GetTVDetails(id int) (*tmdb.TVDetails, error)
	GetSeasonDetails(id, season int) (*tmdb.Season, error)
}

type tsRuntimeEntry struct {
	min     int
	expires time.Time
}

// expectedRuntime returns the runtime in minutes resolve checks files against: TMDB's when
// the request named a tmdb_id, else the runtime the client sent (0 = unknown).
func (s *Service) expectedRuntime(q tsResolveQuery) (int, string) {
	if q.tmdbID > 0 && s.cfg.TMDB != nil {
		if rt := s.tmdbRuntime(q); rt > 0 {
			return rt, "tmdb"
		}
	}
	if q.runtime > 0 {
		return q.runtime, "client"
	}
	return 0, "unknown"
}

func tsRuntimeKey(mediaType string, id, season, episode int) string {
	if mediaType == "movie" {
		return fmt.Sprintf("movie:%d", id)
	}
	return fmt.Sprintf("tv:%d:%d:%d", id, season, episode)
}

func (s *Service) storeRuntime(key string, min int) {
	ttl := tsRuntimeTTL
	if min <= 0 {
		ttl = tsRuntimeMissTTL
	}
	s.runtimes.Store(key, tsRuntimeEntry{min: min, expires: time.Now().Add(ttl)})
}

// tmdbRuntime looks up the movie runtime, or the episode runtime from the season details
// (falling back to the show's episode_run_time). Results are cached in memory.
func (s *Service) tmdbRuntime(q tsResolveQuery) int {
	key := tsRuntimeKey(q.mediaType, q.tmdbID, q.season, q.episode)
	if v, ok := s.runtimes.Load(key); ok {
		if e := v.(tsRuntimeEntry); time.Now().Before(e.expires) {
			return e.min
		}
	}

	t := s.cfg.TMDB
	rt := 0
	if q.mediaType == "movie" {
		if d, err := t.GetMovieDetails(q.tmdbID); err == nil && d != nil {
			rt = d.Runtime
		} else if err != nil {
			log.Printf("resolve %q: tmdb movie %d: %v", q.title, q.tmdbID, err)
		}
	} else {
		if season, err := t.GetSeasonDetails(q.tmdbID, q.season); err == nil && season != nil {
			// One fetch fills the whole season
			for _, ep := range season.Episodes {
				if ep.Runtime > 0 {
					s.storeRuntime(tsRuntimeKey("tv", q.tmdbID, q.season, ep.EpisodeNumber), ep.Runtime)
				}
				if ep.EpisodeNumber == q.episode {
					rt = ep.Runtime
				}
			}
		} else if err != nil {
			log.Printf("resolve %q: tmdb season %d/%d: %v", q.title, q.tmdbID, q.season, err)
		}
		if rt <= 0 {
			if d, err := t.GetTVDetails(q.tmdbID); err == nil && d != nil {
				for _, r := range d.EpisodeRunTime {
					if r > 0 {
						rt = r
						break
					}
				}
			}
		}
	}
	s.storeRuntime(key, rt)
	return rt
}

// lookupRuntime runs expectedRuntime in the background; wait returns its result and how
// long the lookup took, or the client's runtime when TMDB is slower than tsRuntimeWait.
func (s *Service) lookupRuntime(q tsResolveQuery) (wait func() (int, string, time.Duration)) {
	type result struct {
		min  int
		from string
		took time.Duration
	}
	start := time.Now()
	ch := make(chan result, 1)
	go func() {
		rt, from := s.expectedRuntime(q)
		ch <- result{rt, from, time.Since(start)}
	}()
	return func() (int, string, time.Duration) {
		select {
		case r := <-ch:
			return r.min, r.from, r.took
		case <-time.After(tsRuntimeWait):
			if q.runtime > 0 {
				return q.runtime, "client (tmdb timed out)", time.Since(start)
			}
			return 0, "unknown (tmdb timed out)", time.Since(start)
		}
	}
}

// tsDurationMismatch reports whether a file of durSec seconds clearly is not the title of
// runtimeMin minutes. Movies may be off by max(10 min, 12 %), episodes by max(8 min, 25 %).
// Releases tagged as a longer cut (extended, director's cut, ...) may run up to 40 % longer.
// Unknown values never mismatch.
func tsDurationMismatch(durSec float64, runtimeMin int, mediaType string, longerCut bool) bool {
	if durSec <= 0 || runtimeMin <= 0 {
		return false
	}
	rt := float64(runtimeMin * 60)
	floor, share := 10*60.0, 0.12
	if mediaType == "tv" {
		floor, share = 8*60.0, 0.25
	}
	diff := durSec - rt
	if diff > 0 && longerCut {
		share = math.Max(share, 0.40)
	}
	return math.Abs(diff) > math.Max(floor, share*rt)
}

var tsLongerCutTokens = map[string]bool{
	"extended": true, "directors": true, "uncut": true, "unrated": true, "ultimate": true,
}

// tsLongerCut reports whether a release name says it is a longer cut than the theatrical one.
func tsLongerCut(title string) bool {
	for _, t := range tsNormalize(title) {
		if tsLongerCutTokens[t] {
			return true
		}
	}
	return false
}

// checkDuration probes the header of a candidate file (the result is cached for play) and
// returns an error when its duration does not fit the expected runtime. verified is true
// when it fits. A cold swarm can be too slow for the header: after tsDurationProbe the same
// probe goes on (up to tsDurationRetry more) only once warm says the swarm delivers (true);
// false or nil ends it. A probe that failed early is tried again on a warm swarm.
// headerRead (optional) is called once the probe ended, also when the runtime is unknown.
func (s *Service) checkDuration(ctx context.Context, c tsCandidate, q tsResolveQuery, hash string, index int, warm <-chan bool,
	headerRead func()) (dur float64, verified bool, err error) {
	ended := func() {
		if headerRead != nil {
			headerRead()
		}
	}
	if q.runtime <= 0 {
		log.Printf("resolve %q: duration of %q not checked: runtime unknown", q.title, c.raw.Title)
		// Still probe in the background, so the player's "Reading video info" is a cache hit
		go func() {
			_, _ = s.probeFileTimeout(context.WithoutCancel(ctx), hash, index, tsDurationRetry)
			ended()
		}()
		return 0, false, nil
	}
	started := time.Now()
	type outcome struct {
		p   *tsProbe
		err error
	}
	got := make(chan outcome, 1)
	pctx, cancel := context.WithCancel(ctx)
	defer cancel()
	go func() {
		p, err := s.probeFileTimeout(pctx, hash, index, tsDurationProbe+tsDurationRetry)
		ended()
		got <- outcome{p, err}
	}()
	alive := func() bool {
		if warm == nil {
			return false
		}
		select {
		case a := <-warm:
			return a && ctx.Err() == nil
		case <-ctx.Done():
			return false
		}
	}
	slow := time.NewTimer(tsDurationProbe)
	defer slow.Stop()
	var o outcome
	select {
	case o = <-got:
		if o.err != nil && ctx.Err() == nil && alive() {
			log.Printf("resolve %q: duration of %q: probing again on the warm swarm", q.title, c.raw.Title)
			o.p, o.err = s.probeFileTimeout(ctx, hash, index, tsDurationRetry)
		}
	case <-slow.C:
		// Killing the probe would throw away the pieces it asked for: it goes on while the
		// swarm delivers
		if !alive() {
			o.err = fmt.Errorf("no header after %s on a cold swarm", tsDurationProbe)
			break
		}
		log.Printf("resolve %q: duration of %q: still probing on the warm swarm", q.title, c.raw.Title)
		select {
		case o = <-got:
		case <-ctx.Done():
			return 0, false, nil
		}
	case <-ctx.Done():
		return 0, false, nil
	}
	if o.err != nil {
		if ctx.Err() == nil {
			log.Printf("resolve %q: duration of %q unknown (%v): unverified", q.title, c.raw.Title, o.err)
		}
		return 0, false, nil
	}
	return s.judgeDuration(c.raw.Title, q, o.p.duration(), started)
}

// tsDurationError: a probed file is not as long as the title (see judgeDuration).
type tsDurationError struct{ verdict string }

func (e *tsDurationError) Error() string { return e.verdict }

// judgeDuration compares a probed duration (seconds, 0 = unknown) with q.runtime.
func (s *Service) judgeDuration(release string, q tsResolveQuery, dur float64, started time.Time) (float64, bool, error) {
	if dur <= 0 {
		log.Printf("resolve %q: duration of %q unknown: unverified", q.title, release)
		return 0, false, nil
	}
	verdict := fmt.Sprintf("duration %dm vs runtime %dm", int(math.Round(dur/60)), q.runtime)
	if tsDurationMismatch(dur, q.runtime, q.mediaType, tsLongerCut(release)) {
		log.Printf("resolve %q: %q %s: rejected (%s)", q.title, release, verdict, time.Since(started).Round(time.Millisecond))
		return dur, false, &tsDurationError{verdict}
	}
	log.Printf("resolve %q: %q %s: ok (%s)", q.title, release, verdict, time.Since(started).Round(time.Millisecond))
	return dur, true, nil
}

// probeRuntimeCheck compares a probed duration with the expected runtime of the title named
// by ?type=movie|tv&tmdb_id=&season=&episode= (or ?runtime= minutes). ok is false when
// either value is unknown. A longer file is checked against the release name for a longer cut.
func (s *Service) probeRuntimeCheck(c *gin.Context, hash string, index int, dur float64) (runtimeMin int, mismatch, ok bool) {
	q := tsResolveQuery{mediaType: c.Query("type")}
	q.tmdbID, _ = strconv.Atoi(c.Query("tmdb_id"))
	q.season, _ = strconv.Atoi(c.Query("season"))
	q.episode, _ = strconv.Atoi(c.Query("episode"))
	if rt, err := strconv.Atoi(c.Query("runtime")); err == nil && rt >= 1 && rt <= 600 {
		q.runtime = rt
	}
	if (q.mediaType != "movie" && q.mediaType != "tv") || dur <= 0 || (q.tmdbID <= 0 && q.runtime <= 0) {
		return 0, false, false
	}
	if q.mediaType == "tv" && q.episode < 1 {
		return 0, false, false
	}
	rt, _ := s.expectedRuntime(q)
	if rt <= 0 {
		return 0, false, false
	}
	mismatch = tsDurationMismatch(dur, rt, q.mediaType, false)
	if mismatch && dur > float64(rt*60) {
		mismatch = tsDurationMismatch(dur, rt, q.mediaType, tsLongerCut(s.releaseName(c.Request.Context(), hash, index)))
	}
	if mismatch {
		log.Printf("probe %s:%d: duration %dm vs runtime %dm: wrong length", hash, index, int(math.Round(dur/60)), rt)
	}
	return rt, mismatch, true
}

// releaseName is the torrent name plus the file path from TorrServer ("" when unknown).
func (s *Service) releaseName(ctx context.Context, hash string, index int) string {
	ctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	var st tsTorrentStat
	if err := s.torrentStat(ctx, hash, &st); err != nil {
		return ""
	}
	name := st.Name
	for _, f := range st.FileStats {
		if f.ID == index {
			name += " " + f.Path
		}
	}
	return name
}
