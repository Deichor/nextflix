package catalog

import (
	"fmt"
	"log"
	"math"
	"net/url"
	"slices"
	"sort"
	"sync"
	"time"

	"nextflix/internal/platform/tmdb"
)

// Filter-only browsing (genre chips without a query): a wider TMDB discover pool (popular,
// most voted and best rated titles) re-ranked by quality, engagement, recency and what the
// profiles of this server watched and rated; later pages continue with TMDB's popularity order.

const (
	browsePopPages   = 5 // popularity.desc pages in the pool; the fallback continues after them
	browseVotePages  = 3 // vote_count.desc
	browseRatedPages = 3 // vote_average.desc with at least browseRatedVotes votes
	browseRatedVotes = "200"
	browseFetchers   = 6
	// Ranked page size: type=all used to merge a movie and a TV page
	browsePageAll  = 40
	browsePageSide = 20
	localTTL       = 5 * time.Minute
	browseOrderTTL = 10 * time.Minute // since the last page asked
	browseOrderMax = 64
)

// Browse score weights
const (
	wrWeight       = 1.0  // Bayesian weighted rating (0-10)
	bVoteWeight    = 0.35 // × log10(1 + vote_count)
	bPopWeight     = 0.3  // × log10(1 + popularity)
	bRecentBoost   = 0.4  // × (1 - age/3 years)
	watchersWeight = 0.4  // × log2(1 + profiles that watched it)
	positiveWeight = 0.5  // per profile that loved or liked it
	dislikesWeight = 0.8  // per profile that disliked it
	bTasteWeight   = 0.6  // × mean genre affinity of the profile
	ownStarted     = 0.8  // the profile started it
	ownFinished    = 1.5  // the profile finished it
)

// LocalTitle is one title a profile watched or rated (fed from progress and ratings in main.go).
type LocalTitle struct {
	MediaType string
	TmdbID    int
	Watched   bool
	Finished  bool
	Rating    string // "love", "like", "dislike" or ""
}

// LocalSignalsSource lists the watched and rated titles of every profile.
type LocalSignalsSource func() (map[uint][]LocalTitle, error)

type titleStats struct{ watchers, positive, dislikes int }

type profileMark struct{ started, finished, disliked bool }

type localStats struct {
	titles   map[string]titleStats
	profiles map[uint]map[string]profileMark
}

type localCache struct {
	mu      sync.Mutex
	source  LocalSignalsSource
	stats   *localStats
	expires time.Time
}

// SetLocalSignals enables the server-wide watch and rating signals of browsing.
func (s *Service) SetLocalSignals(src LocalSignalsSource) {
	s.local.mu.Lock()
	s.local.source, s.local.stats = src, nil
	s.local.mu.Unlock()
}

func (l *localCache) invalidate() {
	l.mu.Lock()
	l.stats = nil
	l.mu.Unlock()
}

// get returns the aggregated signals, refreshed every 5 minutes (nil without a source).
func (l *localCache) get() *localStats {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.source == nil {
		return nil
	}
	if l.stats != nil && time.Now().Before(l.expires) {
		return l.stats
	}
	all, err := l.source()
	if err != nil {
		log.Printf("catalog: local signals: %v", err)
		return l.stats
	}
	l.stats, l.expires = aggregateLocal(all), time.Now().Add(localTTL)
	return l.stats
}

func aggregateLocal(all map[uint][]LocalTitle) *localStats {
	st := &localStats{titles: map[string]titleStats{}, profiles: map[uint]map[string]profileMark{}}
	for pid, titles := range all {
		marks := map[string]profileMark{}
		for _, t := range titles {
			k := resultKey(t.MediaType, t.TmdbID)
			m := marks[k]
			m.started = m.started || t.Watched || t.Finished
			m.finished = m.finished || t.Finished
			m.disliked = m.disliked || t.Rating == "dislike"
			marks[k] = m
			ts := st.titles[k]
			if t.Watched || t.Finished {
				ts.watchers++
			}
			switch t.Rating {
			case "love", "like":
				ts.positive++
			case "dislike":
				ts.dislikes++
			}
			st.titles[k] = ts
		}
		st.profiles[pid] = marks
	}
	return st
}

// bayes holds the pool constants of the IMDb weighted rating WR = v/(v+m)·R + m/(v+m)·C.
type bayes struct{ c, m float64 }

// newBayes takes C = the mean rating of the pool's rated titles and m = its 75th percentile
// vote count.
func newBayes(pool []tmdb.SearchResult) bayes {
	var votes []int
	sum, n := 0.0, 0
	for _, r := range pool {
		if r.VoteCount > 0 {
			sum += r.VoteAverage
			n++
		}
		votes = append(votes, r.VoteCount)
	}
	b := bayes{c: 6.5, m: 100}
	if n > 0 {
		b.c = sum / float64(n)
	}
	if len(votes) > 0 {
		sort.Ints(votes)
		b.m = math.Max(float64(votes[len(votes)*3/4]), 1)
	}
	return b
}

func (b bayes) rating(r tmdb.SearchResult) float64 {
	v := float64(max(r.VoteCount, 0))
	return v/(v+b.m)*r.VoteAverage + b.m/(v+b.m)*b.c
}

type browseInput struct {
	b       bayes
	local   *localStats
	profile uint
	taste   map[int]float64
	now     time.Time
}

// browseScore blends quality, engagement, recency, the server's profiles and the current
// profile's taste. disliked: the profile disliked it (ranked after everything else).
func (in browseInput) score(r tmdb.SearchResult) (score float64, disliked bool) {
	score = wrWeight*in.b.rating(r) +
		bVoteWeight*math.Log10(1+float64(max(r.VoteCount, 0))) +
		bPopWeight*math.Log10(1+max(r.Popularity, 0))
	if d, err := time.Parse("2006-01-02", r.ReleaseDate); err == nil {
		if age := in.now.Sub(d).Hours() / 24 / 365; age >= 0 && age < 3 {
			score += bRecentBoost * (1 - age/3)
		}
	}
	if len(in.taste) > 0 && len(r.GenreIDs) > 0 {
		sum := 0.0
		for _, g := range r.GenreIDs {
			sum += in.taste[commonGenre(r.MediaType, g)]
		}
		score += bTasteWeight * sum / float64(len(r.GenreIDs))
	}
	if in.local == nil {
		return score, false
	}
	k := resultKey(r.MediaType, r.ID)
	ts := in.local.titles[k]
	score += watchersWeight*math.Log2(1+float64(ts.watchers)) +
		positiveWeight*float64(ts.positive) - dislikesWeight*float64(ts.dislikes)
	mark := in.local.profiles[in.profile][k]
	switch {
	case mark.finished:
		score -= ownFinished
	case mark.started:
		score -= ownStarted
	}
	return score, mark.disliked
}

// rankBrowse orders the pool by browse score; the profile's disliked titles go last.
func rankBrowse(in browseInput, pool []tmdb.SearchResult) []tmdb.SearchResult {
	type item struct {
		r        tmdb.SearchResult
		score    float64
		disliked bool
	}
	items := make([]item, len(pool))
	for i, r := range pool {
		s, d := in.score(r)
		items[i] = item{r, s, d}
	}
	sort.SliceStable(items, func(i, j int) bool {
		if items[i].disliked != items[j].disliked {
			return !items[i].disliked
		}
		return items[i].score > items[j].score
	})
	out := make([]tmdb.SearchResult, len(items))
	for i, it := range items {
		out[i] = it.r
	}
	return out
}

// browsePool fetches the candidate pool of a filter: popularity, vote count and rating pages
// of every media type the filter allows (each page cached 24 h by the TMDB client). It also
// returns TMDB's popularity totals (pages and results) for the fallback pages.
func (s *Service) browsePool(tm *tmdb.Client, f Filter) (pool []tmdb.SearchResult, popPages, popResults int, err error) {
	type job struct {
		side string
		q    url.Values
		pop  bool
	}
	var jobs []job
	for _, side := range []string{"movie", "tv"} {
		add := func(sortBy string, pages int, minVotes string) {
			for p := 1; p <= pages; p++ {
				q, ok := f.discoverParams(side, p)
				if !ok {
					return
				}
				q.Set("sort_by", sortBy)
				if minVotes != "" {
					q.Set("vote_count.gte", minVotes)
				}
				jobs = append(jobs, job{side, q, sortBy == "popularity.desc"})
			}
		}
		add("popularity.desc", browsePopPages, "")
		add("vote_count.desc", browseVotePages, "")
		add("vote_average.desc", browseRatedPages, browseRatedVotes)
	}
	if len(jobs) == 0 {
		return nil, 0, 0, nil
	}

	results := make([][]tmdb.SearchResult, len(jobs))
	errs := make([]error, len(jobs))
	var (
		wg     sync.WaitGroup
		mu     sync.Mutex
		sem    = make(chan struct{}, browseFetchers)
		totals = map[string][2]int{}
	)
	for i, j := range jobs {
		wg.Add(1)
		go func() {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			var list []tmdb.SearchResult
			var pages, count int
			if j.side == "movie" {
				res, err := tm.DiscoverMoviePage(j.q)
				if err != nil {
					errs[i] = err
					return
				}
				for _, m := range res.Results {
					list = append(list, tmdb.MovieResult(m))
				}
				pages, count = res.TotalPages, res.TotalResults
			} else {
				res, err := tm.DiscoverTVPage(j.q)
				if err != nil {
					errs[i] = err
					return
				}
				for _, t := range res.Results {
					list = append(list, tmdb.TVResult(t))
				}
				pages, count = res.TotalPages, res.TotalResults
			}
			results[i] = list
			if j.pop && j.q.Get("page") == "1" {
				mu.Lock()
				totals[j.side] = [2]int{pages, count}
				mu.Unlock()
			}
		}()
	}
	wg.Wait()

	ok := 0
	for i := range jobs {
		if errs[i] == nil {
			ok++
		} else if err == nil {
			err = errs[i]
		}
	}
	if ok == 0 {
		return nil, 0, 0, err
	}
	seen := map[string]bool{}
	for _, list := range results {
		for _, r := range list {
			k := resultKey(r.MediaType, r.ID)
			if seen[k] || !usable(r) {
				continue
			}
			seen[k] = true
			pool = append(pool, r)
		}
	}
	for _, t := range totals {
		popPages = max(popPages, t[0])
		popResults += t[1]
	}
	return pool, popPages, popResults, nil
}

// browseOrder is the ranked pool of one browse (profile, filter, language). Later pages slice
// the order page 1 made, so a taste or signals change mid-scroll causes no repeats or gaps.
type browseOrder struct {
	ranked               []tmdb.SearchResult
	popPages, popResults int
	expires              time.Time
}

type browseOrders struct {
	mu    sync.Mutex
	items map[string]browseOrder
}

// get returns a live order and extends its life.
func (c *browseOrders) get(key string, now time.Time) (browseOrder, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	o, ok := c.items[key]
	if !ok || !now.Before(o.expires) {
		return browseOrder{}, false
	}
	o.expires = now.Add(browseOrderTTL)
	c.items[key] = o
	return o, true
}

func (c *browseOrders) put(key string, o browseOrder, now time.Time) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.items == nil {
		c.items = map[string]browseOrder{}
	}
	for k, old := range c.items {
		if !now.Before(old.expires) {
			delete(c.items, k)
		}
	}
	for len(c.items) >= browseOrderMax {
		oldest := ""
		for k, old := range c.items {
			if oldest == "" || old.expires.Before(c.items[oldest].expires) {
				oldest = k
			}
		}
		delete(c.items, oldest)
	}
	o.expires = now.Add(browseOrderTTL)
	c.items[key] = o
}

func browseKey(tm *tmdb.Client, f Filter, profileID uint) string {
	genres := slices.Sorted(slices.Values(f.Genres))
	return fmt.Sprintf("%d|%s|%s|%v|%v", profileID, tm.Lang(), f.Type, genres, f.Anime)
}

// Browse lists the titles of a filter: the ranked pool first (pages of 40 for type all, 20
// for one type), then TMDB's popularity pages after the ones in the pool, without repeats.
// Page 1 ranks the pool anew; later pages reuse that order while it is cached.
func (s *Service) Browse(tm *tmdb.Client, f Filter, page int, profileID uint) (*DiscoverPage, error) {
	page = min(max(page, 1), maxDiscoverPages)
	key, now := browseKey(tm, f, profileID), time.Now()
	o, ok := s.browse.get(key, now)
	if !ok || page == 1 {
		pool, popPages, popResults, err := s.browsePool(tm, f)
		if err != nil {
			return nil, err
		}
		in := browseInput{b: newBayes(pool), local: s.local.get(), profile: profileID,
			taste: s.taste.affinity(profileID), now: now}
		o = browseOrder{ranked: rankBrowse(in, pool), popPages: popPages, popResults: popResults}
		s.browse.put(key, o, now)
	}
	pool, popPages := o.ranked, o.popPages
	size := browsePageAll
	if f.Type != "all" {
		size = browsePageSide
	}
	rankedPages := (len(pool) + size - 1) / size
	total := min(rankedPages+max(popPages-browsePopPages, 0), maxDiscoverPages)
	out := &DiscoverPage{Page: page, TotalPages: total, TotalResults: max(o.popResults, len(pool)),
		Results: []tmdb.SearchResult{}}
	if page <= rankedPages {
		out.Results = slices.Clone(pool[(page-1)*size : min(page*size, len(pool))])
		return out, nil
	}
	// Past the pool: TMDB's popularity order, minus what the pool already showed
	rest, err := s.browsePopular(tm, f, page-rankedPages+browsePopPages)
	if err != nil {
		return nil, err
	}
	inPool := make(map[string]bool, len(pool))
	for _, r := range pool {
		inPool[resultKey(r.MediaType, r.ID)] = true
	}
	for _, r := range rest.Results {
		if !inPool[resultKey(r.MediaType, r.ID)] {
			out.Results = append(out.Results, r)
		}
	}
	return out, nil
}
