package streaming

import (
	"context"
	"log"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/gin-gonic/gin"
)

// TMDB's digital dates often lag the real ones: a film it lists as only in cinemas can
// already have WEB-DL releases on the indexers. HomeReleaseOut searches like resolve does
// (no TorrServer) and tells whether a usable home release exists; the catalog then reports
// the film as available (catalog.HomeReleaseFinder, wired in main.go).

const (
	tsHomeWait       = 4 * time.Second  // the caller waits this long; the search goes on and is cached
	tsHomeSearchTime = 15 * time.Second // the whole search, slower indexers included
	tsHomeFoundTTL   = 6 * time.Hour
	tsHomeMissTTL    = time.Hour // only cinema recordings (or nothing) so far
	tsHomeFailTTL    = 5 * time.Minute
	tsHomeMax        = 3 // searches at once
	tsHomeLog        = 5 // candidates logged per verdict
	// Fake WEB-DLs of films in cinemas are common, so one uploader is not enough: a real home
	// release is re-released by several groups within hours
	tsHomeMinSeeders = 10
	tsHomeMinGroups  = 2
)

type tsHomeVerdict struct {
	found   bool
	best    []string
	expires time.Time
}

type tsHomeCall struct {
	done chan struct{}
	v    tsHomeVerdict
}

type tsHome struct {
	mu    sync.Mutex
	cache map[int]tsHomeVerdict
	calls map[int]*tsHomeCall
	slots chan struct{}
	// Hashes per movie whose length was wrong: never a home release again (homedur.go)
	rejected map[int]map[string]time.Time
	probing  chan struct{} // one background length check at a time
}

func newHome() *tsHome {
	return &tsHome{cache: map[int]tsHomeVerdict{}, calls: map[int]*tsHomeCall{}, slots: make(chan struct{}, tsHomeMax),
		rejected: map[int]map[string]time.Time{}, probing: make(chan struct{}, 1)}
}

// tsHomeCandidates are the ranked candidates of raw that count as a home release: a
// release naming a home source (web, bluray, hdtv, ...), never a cinema one, whose name starts
// with the title (after a site prefix: "George A. Romero's Resident Evil (2025)" is another
// film) and that has tsHomeMinSeeders. tsScore already rejected recordings (CAM, TS, TC, SCR),
// bonus material, namesakes and other years.
func tsHomeCandidates(raw []JackettResult, q tsResolveQuery) []tsCandidate {
	titles := tsTitleTokens(q)
	var out []tsCandidate
	for _, c := range tsRankAll(raw, q) {
		if src := tsSourceOf(c.rest); src == "" || src == "cinema" || c.raw.Seeders < tsHomeMinSeeders {
			continue
		}
		name := tsNormalize(tsSitePrefPattern.ReplaceAllString(c.raw.Title, ""))
		for _, t := range titles {
			if tsHasPrefix(name, t) {
				out = append(out, c)
				break
			}
		}
	}
	return out
}

// tsHomeFound: the home releases come from at least tsHomeMinGroups release groups (the last
// word of a name: "-Kitsune", "[QxR]", "YTS").
func tsHomeFound(cands []tsCandidate) bool {
	groups := map[string]bool{}
	for _, c := range cands {
		if t := tsNormalize(c.raw.Title); len(t) > 0 {
			groups[t[len(t)-1]] = true
		}
	}
	return len(groups) >= tsHomeMinGroups
}

// HomeReleaseOut reports whether a home release of the movie is on the indexers. It waits
// up to tsHomeWait; a slower search answers false now and caches its verdict for later.
func (s *Service) HomeReleaseOut(tmdbID int) bool {
	if tmdbID <= 0 {
		return false
	}
	h := s.home
	h.mu.Lock()
	if v, ok := h.cache[tmdbID]; ok && time.Now().Before(v.expires) {
		h.mu.Unlock()
		return v.found
	}
	call, running := h.calls[tmdbID]
	if !running {
		call = &tsHomeCall{done: make(chan struct{})}
		h.calls[tmdbID] = call
	}
	h.mu.Unlock()
	if !running {
		go func() {
			v, check := s.searchHomeRelease(tmdbID)
			h.mu.Lock()
			now := time.Now()
			for k, old := range h.cache {
				if now.After(old.expires) {
					delete(h.cache, k)
				}
			}
			h.cache[tmdbID] = v
			delete(h.calls, tmdbID)
			h.mu.Unlock()
			call.v = v
			close(call.done)
			if check != nil {
				check() // after the verdict is stored, so a wrong length can turn it
			}
		}()
	}
	select {
	case <-call.done:
		return call.v.found
	case <-time.After(tsHomeWait):
		log.Printf("home release of movie %d: still searching after %s", tmdbID, tsHomeWait)
		return false
	}
}

// searchHomeRelease searches the movie's home releases; check (nil unless found) probes the
// lengths of the TorBox-cached ones (probeHomeReleases).
func (s *Service) searchHomeRelease(tmdbID int) (v tsHomeVerdict, check func()) {
	h := s.home
	h.slots <- struct{}{}
	defer func() { <-h.slots }()
	started := time.Now()
	fail := tsHomeVerdict{expires: started.Add(tsHomeFailTTL)}
	q := tsResolveQuery{mediaType: "movie", tmdbID: tmdbID}
	s.englishTitle(&q)
	if len(tsNormalize(q.title)) == 0 {
		log.Printf("home release of movie %d: no title", tmdbID)
		return fail, nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), tsHomeSearchTime)
	defer cancel()
	// Releases whose length was wrong before never count
	rejected := h.rejectedHome(tmdbID)
	rank := func(raw []JackettResult, q tsResolveQuery) []tsCandidate {
		var out []tsCandidate
		for _, c := range tsHomeCandidates(raw, q) {
			if !rejected[strings.ToLower(c.hash)] {
				out = append(out, c)
			}
		}
		return out
	}
	q, _, cands, err := s.searchRanked(ctx, q, tsHomeFound, rank)
	if err != nil {
		log.Printf("home release %q: search failed: %v", q.title, err)
		return fail, nil
	}
	v = tsHomeVerdict{found: tsHomeFound(cands), expires: started.Add(tsHomeMissTTL)}
	if v.found {
		v.expires = started.Add(tsHomeFoundTTL)
	}
	for i, c := range cands {
		if i == tsHomeLog {
			break
		}
		v.best = append(v.best, c.raw.Title)
	}
	log.Printf("home release %q (%d): found=%v, %d home releases in %s, best %q", q.title, q.year, v.found, len(cands),
		time.Since(started).Round(time.Millisecond), v.best)
	if v.found {
		q.tmdbID = tmdbID
		check = func() { s.probeHomeReleases(q, cands) }
	}
	return v, check
}

// searchRanked searches a title like resolve does and ranks the results with rank. When
// enough(ranked) is false after the fast indexers it waits for the slower ones until ctx ends.
func (s *Service) searchRanked(ctx context.Context, q tsResolveQuery, enough func([]tsCandidate) bool,
	rank func([]JackettResult, tsResolveQuery) []tsCandidate) (tsResolveQuery, []JackettResult, []tsCandidate, error) {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel() // ends the slower indexers
	q.warm = &tsWarm{key: "search|" + tsResolveBaseKey(q), ctx: ctx, cancel: cancel}
	started := time.Now()
	runtime := s.lookupRuntime(q)
	names := s.titleNamesLater(q)
	sr, q, _, err := s.searchTitle(ctx, q, started, names)
	if err != nil {
		return q, nil, nil, err
	}
	q.runtime, _, _ = runtime()
	raw := sr.raw
	cands := rank(raw, q)
	for late := sr.late; late != nil && !enough(cands); {
		select {
		case batch, ok := <-late:
			if !ok {
				late = nil
				break
			}
			raw = append(raw, batch...)
			cands = rank(raw, q)
		case <-ctx.Done():
			late = nil
		}
	}
	return q, raw, cands, nil
}

// dryResolve answers resolve?dry=1: the ranked candidates of a search, without TorrServer,
// TorBox, the source cache or any hold. For checking the ranking of a title.
func (s *Service) dryResolve(c *gin.Context, q tsResolveQuery) {
	if a, blocked := s.notOut(q); blocked {
		c.JSON(statusTooEarly, tsNotOutBody(a))
		return
	}
	ctx, cancel := context.WithTimeout(c.Request.Context(), tsHomeSearchTime)
	defer cancel()
	started := time.Now()
	q, raw, cands, err := s.searchRanked(ctx, q, func(c []tsCandidate) bool { return len(c) >= tsFewCandidates }, tsRankAll)
	if err != nil {
		c.JSON(http.StatusBadGateway, gin.H{"error": err.Error()})
		return
	}
	type item struct {
		Title   string  `json:"title"`
		Seeders int     `json:"seeders"`
		Size    int64   `json:"size"`
		Quality int     `json:"quality"`
		Source  string  `json:"source"`
		Score   float64 `json:"score"`
		Parts   string  `json:"parts"`
	}
	out := []item{}
	for i, cand := range cands {
		if i == 100 {
			break
		}
		src := tsSourceOf(cand.rest)
		if cand.cinema {
			src = "cinema" // a recording (?cinema=1)
		}
		out = append(out, item{Title: cand.raw.Title, Seeders: cand.raw.Seeders, Size: cand.raw.Size, Quality: cand.quality,
			Source: src, Score: cand.score, Parts: cand.parts.String()})
	}
	c.JSON(http.StatusOK, gin.H{"title": q.title, "year": q.year, "runtime": q.runtime, "results": len(raw),
		"usable": len(cands), "took_ms": time.Since(started).Milliseconds(), "candidates": out})
}
