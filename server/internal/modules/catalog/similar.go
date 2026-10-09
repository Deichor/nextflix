package catalog

import (
	"errors"
	"log"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/gin-gonic/gin"

	"nextflix/internal/platform/httpserver"
	"nextflix/internal/platform/tmdb"
)

// "More Like This": candidates from TMDB recommendations (2 pages), similar, the collection,
// the director's / creator's and top-billed cast's notable works and keyword discover, scored
// in two stages (list data, then the top candidates' keywords and details) and cached 24 h per
// title and language. Profile adjustments are applied on every request.

const (
	simCacheTTL   = 24 * time.Hour
	simCacheMax   = 2000
	simFactsTop   = 60 // candidates whose details are fetched for the second stage
	simKeywordsTo = 4  // keyword discover lists
	simKwMinW     = 0.7
	simTasteW     = 0.6 // × mean genre affinity of the profile
	simStartedW   = 0.5
	simFinishedW  = 1.5
)

// SimilarItem is one "More Like This" title. Reason: "collection" (part of the same
// collection), "spin_off" (same franchise) or "match".
type SimilarItem struct {
	tmdb.SearchResult
	Reason string `json:"reason"`
	score  float64
	why    string // score terms (debug)
}

type simState struct {
	fetch simFetcher

	mu       sync.Mutex
	results  map[string]expiring[[]SimilarItem]
	inflight map[string]*simCall
}

type simCall struct {
	done  chan struct{}
	items []SimilarItem
	err   error
}

func newSimState(f simFetcher) *simState {
	return &simState{fetch: f, results: map[string]expiring[[]SimilarItem]{}, inflight: map[string]*simCall{}}
}

// Similar returns the "More Like This" list of a title in lang, without profile adjustments.
// Cached 24 h; concurrent callers share one computation.
func (s *Service) Similar(lang, mediaType string, id int) ([]SimilarItem, error) {
	cl := s.similarCall(lang, mediaType, id)
	<-cl.done
	return cl.items, cl.err
}

// SimilarWithin is Similar with a time limit; ok is false when the list is not ready in time
// (it is still computed and cached for the next caller).
func (s *Service) SimilarWithin(lang, mediaType string, id int, budget time.Duration) (items []SimilarItem, ok bool) {
	cl := s.similarCall(lang, mediaType, id)
	select {
	case <-cl.done:
		return cl.items, cl.err == nil
	case <-time.After(budget):
		return nil, false
	}
}

func (s *Service) similarCall(lang, mediaType string, id int) *simCall {
	lang = tmdb.NormalizeLang(lang)
	key := lang + ":" + resultKey(mediaType, id)
	st := s.sim
	st.mu.Lock()
	defer st.mu.Unlock()
	if e, ok := st.results[key]; ok && time.Now().Before(e.expires) {
		cl := &simCall{done: make(chan struct{}), items: e.v}
		close(cl.done)
		return cl
	}
	if cl, ok := st.inflight[key]; ok {
		return cl
	}
	cl := &simCall{done: make(chan struct{})}
	st.inflight[key] = cl
	go func() {
		start := time.Now()
		cl.items, cl.err = buildSimilar(st.fetch, lang, mediaType, id, simToday())
		if cl.err != nil {
			log.Printf("catalog: similar %s: %v", key, cl.err)
		} else if d := time.Since(start); d > 3*time.Second {
			log.Printf("catalog: similar %s: %d titles in %s", key, len(cl.items), d.Round(time.Millisecond))
		}
		st.mu.Lock()
		delete(st.inflight, key)
		if e, ok := st.results[key]; ok && (cl.err != nil || len(cl.items) == 0) {
			// TMDB failed: the expired list is better than none (tried again in a minute)
			cl.items, cl.err = e.v, nil
			st.results[key] = expiring[[]SimilarItem]{e.v, time.Now().Add(time.Minute)}
		} else if cl.err == nil && len(cl.items) > 0 {
			st.results = prune(st.results, simCacheMax)
			st.results[key] = expiring[[]SimilarItem]{cl.items, time.Now().Add(simCacheTTL)}
		}
		st.mu.Unlock()
		close(cl.done)
	}()
	return cl
}

// buildSimilar collects the candidates of a title and ranks them.
func buildSimilar(f simFetcher, lang, mediaType string, id int, today string) ([]SimilarItem, error) {
	src, err := f.source(mediaType, id)
	if err != nil {
		return nil, err
	}
	pool := map[string]*simCand{}
	var mu sync.Mutex
	get := func(r tmdb.SearchResult) *simCand { // mu held
		k := resultKey(r.MediaType, r.ID)
		c := pool[k]
		if c == nil {
			c = newSimCand(r)
			pool[k] = c
		}
		return c
	}
	var wg sync.WaitGroup
	run := func(fn func()) {
		wg.Add(1)
		go func() {
			defer wg.Done()
			fn()
		}()
	}

	var listErrs []error
	addList := func(kind string, page int) {
		list, err := f.related(lang, mediaType, kind, id, page)
		mu.Lock()
		defer mu.Unlock()
		if err != nil {
			listErrs = append(listErrs, err)
			return
		}
		for i, r := range list {
			c, pos := get(r), (page-1)*20+i
			if kind == "recommendations" && (c.recPos < 0 || pos < c.recPos) {
				c.recPos = pos
			} else if kind == "similar" && (c.simPos < 0 || pos < c.simPos) {
				c.simPos = pos
			}
		}
	}
	run(func() { addList("recommendations", 1) })
	run(func() { addList("recommendations", 2) })
	run(func() { addList("similar", 1) })

	if mediaType == "movie" && src.Collection > 0 {
		run(func() {
			parts, err := f.collection(lang, src.Collection)
			if err != nil {
				log.Printf("catalog: similar: collection %d: %v", src.Collection, err)
				return
			}
			mu.Lock()
			for _, r := range parts {
				get(r).collection = true
			}
			mu.Unlock()
		})
	}
	person := func(pid int, creator bool) {
		w, err := f.personWorks(lang, pid)
		if err != nil || w == nil {
			return
		}
		mu.Lock()
		defer mu.Unlock()
		if creator {
			for _, r := range w.Made {
				get(r).byCreator = true
			}
			return
		}
		for _, r := range w.Lead {
			get(r).castHits++
		}
	}
	for _, p := range src.Creators {
		run(func() { person(p, true) })
	}
	for _, p := range src.Cast {
		run(func() { person(p, false) })
	}

	// Keyword weights from title counts, then the most distinctive keywords' best titles
	run(func() {
		kws := src.Keywords
		if len(kws) > simKeywordMax {
			kws = kws[:simKeywordMax]
		}
		df := make(map[int]int, len(kws))
		var kmu sync.Mutex
		var kwg sync.WaitGroup
		for _, k := range kws {
			if genericKeywords[strings.ToLower(src.KeywordNames[k])] {
				continue
			}
			kwg.Add(1)
			go func() {
				defer kwg.Done()
				n, err := f.keywordDF(mediaType, k)
				if err != nil {
					n = -1
				}
				kmu.Lock()
				df[k] = n
				kmu.Unlock()
			}()
		}
		kwg.Wait()
		src.prepare(func(k int) int {
			if n, ok := df[k]; ok {
				return n
			}
			return -1
		})
		genre := src.mainGenreRaw()
		for _, k := range src.topKeywords(simKeywordsTo, simKwMinW) {
			kwg.Add(1)
			go func() {
				defer kwg.Done()
				list, err := f.discover(lang, mediaType, simQuery{Keyword: k, Genre: genre, MinVotes: max(10, src.floor/2)})
				if err != nil {
					return
				}
				mu.Lock()
				for _, r := range list {
					get(r).kwLists++
				}
				mu.Unlock()
			}()
		}
		// Non-English titles: the best known titles of the language and main genre too (TMDB's
		// lists lean towards English titles)
		if src.Language != "en" && src.Language != "" {
			for page := 1; page <= 2; page++ {
				kwg.Add(1)
				go func() {
					defer kwg.Done()
					list, err := f.discover(lang, mediaType, simQuery{Genre: genre, Language: src.Language,
						MinVotes: max(10, src.floor/2), Page: page})
					if err != nil {
						return
					}
					mu.Lock()
					for _, r := range list {
						get(r)
					}
					mu.Unlock()
				}()
			}
		}
		kwg.Wait()
	})
	wg.Wait()
	if len(listErrs) == 3 {
		return nil, errors.Join(listErrs...)
	}

	// Stage 1: list data only
	type scored struct {
		c *simCand
		s float64
	}
	var first []scored
	var keep []*simCand
	for _, c := range pool {
		c.related = isSpinOff(src, c, c.res.Title, c.res.OriginalTitle)
		if c.collection || c.related {
			keep = append(keep, c)
			continue
		}
		if p, ok := scoreCand(src, c, today); ok {
			first = append(first, scored{c, p.sum()})
		}
	}
	sort.Slice(first, func(i, j int) bool {
		if first[i].s != first[j].s {
			return first[i].s > first[j].s
		}
		return first[i].c.res.ID < first[j].c.res.ID
	})
	for i := 0; i < len(first) && i < simFactsTop; i++ {
		keep = append(keep, first[i].c)
	}

	// Stage 2: keywords, runtime, collection, countries and English text of the best ones
	var fwg sync.WaitGroup
	for _, c := range keep {
		fwg.Add(1)
		go func() {
			defer fwg.Done()
			if fa, err := f.facts(c.res.MediaType, c.res.ID); err == nil {
				c.facts = fa
			}
		}()
	}
	fwg.Wait()
	for _, c := range keep {
		if c.facts != nil && !c.related {
			c.related = isSpinOff(src, c, c.facts.Title, c.facts.OriginalTitle)
		}
	}

	ranked := rankSimilar(src, keep, today, simLimit)
	out := make([]SimilarItem, 0, len(ranked))
	for _, c := range ranked {
		it := SimilarItem{SearchResult: c.res, Reason: "match", score: c.score, why: c.parts.String()}
		switch {
		case c.collection:
			it.Reason = "collection"
		case c.related:
			it.Reason = "spin_off"
		}
		if fa := c.facts; fa != nil {
			if len(it.GenreIDs) == 0 {
				it.GenreIDs = fa.Genres
			}
			if lang != tmdb.LangEN && tmdb.MissingText(lang, it.Title, it.OriginalTitle, it.OriginalLanguage, it.Overview) {
				tmdb.FillText(lang, &it.Title, &it.Overview, it.OriginalTitle, it.OriginalLanguage, fa.Title, fa.Overview)
			}
		}
		if it.GenreIDs == nil {
			it.GenreIDs = []int{}
		}
		out = append(out, it)
	}
	return out, nil
}

// isSpinOff: one title contains the other, and the creator, the collection or TMDB's lists
// link them too.
func isSpinOff(src *simSource, c *simCand, title, original string) bool {
	if c.collection || !(c.byCreator || c.recPos >= 0 || c.simPos >= 0) {
		return false
	}
	for _, a := range []string{src.Title, src.OriginalTitle} {
		for _, b := range []string{title, original} {
			if a != "" && b != "" && spinOff(a, b) {
				return true
			}
		}
	}
	return false
}

// personalizeSimilar removes titles the profile disliked, moves watched ones down and nudges
// the profile's genres up. The collection and spin-off block keeps its place.
func (s *Service) personalizeSimilar(items []SimilarItem, profileID uint) []SimilarItem {
	if profileID == 0 || len(items) == 0 {
		return items
	}
	taste := s.taste.affinity(profileID)
	var marks map[string]profileMark
	if st := s.local.get(); st != nil {
		marks = st.profiles[profileID]
	}
	if len(taste) == 0 && len(marks) == 0 {
		return items
	}
	out := make([]SimilarItem, 0, len(items))
	block := 0
	for _, it := range items {
		m := marks[resultKey(it.MediaType, it.ID)]
		if m.disliked {
			continue
		}
		if len(taste) > 0 && len(it.GenreIDs) > 0 {
			sum := 0.0
			for _, g := range it.GenreIDs {
				sum += taste[commonGenre(it.MediaType, g)]
			}
			it.score += simTasteW * sum / float64(len(it.GenreIDs))
		}
		switch {
		case m.finished:
			it.score -= simFinishedW
		case m.started:
			it.score -= simStartedW
		}
		if it.Reason != "match" {
			block++
		}
		out = append(out, it)
	}
	rest := out[block:]
	sort.SliceStable(rest, func(i, j int) bool { return rest[i].score > rest[j].score })
	return out
}

// similarFor is the personalized list, or TMDB's merged lists when the matcher fails.
func (s *Service) similarFor(c *gin.Context, mediaType string, id, limit int) ([]any, error) {
	items, err := s.Similar(httpserver.Lang(c), mediaType, id)
	if err != nil {
		res, ferr := s.client(c).GetRelatedMedia(id, mediaType, limit, 0)
		if ferr != nil {
			return nil, errors.Join(err, ferr)
		}
		out := make([]any, 0, len(res))
		for _, r := range res {
			out = append(out, r)
		}
		return out, nil
	}
	items = s.personalizeSimilar(items, profileOf(c))
	if len(items) > limit {
		items = items[:limit]
	}
	debug := c.Query("debug") == "1"
	out := make([]any, 0, len(items))
	for _, it := range items {
		if debug {
			out = append(out, struct {
				SimilarItem
				Score float64 `json:"score"`
				Why   string  `json:"why"`
			}{it, it.score, it.why})
			continue
		}
		out = append(out, it)
	}
	return out, nil
}

// profileOf reads profile_id, else the X-User-ID header.
func profileOf(c *gin.Context) uint {
	if v, err := strconv.ParseUint(c.Query("profile_id"), 10, 64); err == nil {
		return uint(v)
	}
	return httpserver.ProfileID(c)
}

// GET /api/catalog/:type/:id/similar?profile_id=&limit= – "More Like This" (limit 1-40, default 24).
func (s *Service) similar(c *gin.Context) {
	mediaType := c.Param("type")
	id, err := strconv.Atoi(c.Param("id"))
	if (mediaType != "movie" && mediaType != "tv") || err != nil || id <= 0 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Use /api/catalog/movie|tv/<id>/similar"})
		return
	}
	limit := simLimit
	if l, err := strconv.Atoi(c.Query("limit")); err == nil && l > 0 && l <= 40 {
		limit = l
	}
	results, err := s.similarFor(c, mediaType, id, limit)
	if err != nil {
		fail(c, http.StatusBadGateway, "Failed to get similar titles", err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"results": results, "total_results": len(results), "media_type": mediaType, "media_id": id})
}
