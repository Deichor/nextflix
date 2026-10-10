package catalog

import (
	"log"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/gin-gonic/gin"

	"nextflix/internal/platform/httpserver"
	"nextflix/internal/platform/tmdb"
)

const (
	animationGenre = 16
	animeLanguage  = "ja"
	// TMDB answers at most 500 pages
	maxDiscoverPages = 500
	searchCacheTTL   = 15 * time.Minute
	searchCacheMax   = 200
	minDiscoverVotes = "10"
)

// genrePair is a filter genre's TMDB id for movies and for TV (0: no counterpart).
type genrePair struct{ movie, tv int }

// filterGenres maps the genre ids clients send (TMDB movie ids, plus the TV-only ones) to both
// media types. TV has combined genres (Action & Adventure, Sci-Fi & Fantasy, War & Politics),
// so Action, Adventure, Sci-Fi, Fantasy and War match those; TV has no Thriller, which maps to
// Mystery. History, Horror, Music, Romance and TV Movie have no TV genre; Reality, News, Soap
// and Talk none for movies; Kids is Family for movies.
var filterGenres = map[int]genrePair{
	28: {28, 10759}, 12: {12, 10759}, 16: {16, 16}, 35: {35, 35}, 80: {80, 80}, 99: {99, 99},
	18: {18, 18}, 10751: {10751, 10751}, 14: {14, 10765}, 36: {36, 0}, 27: {27, 0},
	10402: {10402, 0}, 9648: {9648, 9648}, 10749: {10749, 0}, 878: {878, 10765},
	10770: {10770, 0}, 53: {53, 9648}, 10752: {10752, 10768}, 37: {37, 37},
	10759: {28, 10759}, 10762: {10751, 10762}, 10763: {0, 10763}, 10764: {0, 10764},
	10765: {878, 10765}, 10766: {0, 10766}, 10767: {0, 10767}, 10768: {10752, 10768},
}

// Filter is a genre/type selection of the search page. Genres combine with AND.
type Filter struct {
	Type   string // "all", "movie" or "tv"
	Genres []int
	Anime  bool // Animation + original language Japanese
}

// ParseFilter reads type, genres (comma-separated ids) and anime from a query.
func ParseFilter(q url.Values) Filter {
	f := Filter{Type: "all"}
	switch t := strings.ToLower(q.Get("type")); t {
	case "movie", "tv":
		f.Type = t
	}
	seen := map[int]bool{}
	for _, part := range strings.Split(q.Get("genres"), ",") {
		id, err := strconv.Atoi(strings.TrimSpace(part))
		if err != nil || id <= 0 || seen[id] {
			continue
		}
		seen[id] = true
		f.Genres = append(f.Genres, id)
	}
	switch strings.ToLower(q.Get("anime")) {
	case "1", "true", "yes":
		f.Anime = true
	}
	return f
}

// Empty reports whether nothing narrows the results.
func (f Filter) Empty() bool { return f.Type == "all" && len(f.Genres) == 0 && !f.Anime }

// sideGenres returns the TMDB genre ids mediaType must all have, or ok=false when a selected
// genre has no counterpart for it (that media type cannot match).
func (f Filter) sideGenres(mediaType string) (ids []int, ok bool) {
	if f.Type != "all" && f.Type != mediaType {
		return nil, false
	}
	seen := map[int]bool{}
	add := func(id int) {
		if !seen[id] {
			seen[id] = true
			ids = append(ids, id)
		}
	}
	for _, g := range f.Genres {
		p, known := filterGenres[g]
		id := p.movie
		if mediaType == "tv" {
			id = p.tv
		}
		if !known || id == 0 {
			return nil, false
		}
		add(id)
	}
	if f.Anime {
		add(animationGenre)
	}
	return ids, true
}

// Match reports whether a search result passes the filter: its type is selected, it has every
// selected genre (mapped to its media type) and, for Anime, Animation and Japanese origin.
func (f Filter) Match(r tmdb.SearchResult) bool {
	if r.Adult || (r.MediaType != "movie" && r.MediaType != "tv") {
		return false
	}
	ids, ok := f.sideGenres(r.MediaType)
	if !ok {
		return false
	}
	have := map[int]bool{}
	for _, g := range r.GenreIDs {
		have[g] = true
	}
	for _, id := range ids {
		if !have[id] {
			return false
		}
	}
	return !f.Anime || r.OriginalLanguage == animeLanguage
}

// discoverParams is the TMDB /discover query of the filter for one media type (ok=false: skip it).
func (f Filter) discoverParams(mediaType string, page int) (url.Values, bool) {
	ids, ok := f.sideGenres(mediaType)
	if !ok {
		return nil, false
	}
	q := url.Values{}
	q.Set("sort_by", "popularity.desc")
	q.Set("include_adult", "false")
	q.Set("vote_count.gte", minDiscoverVotes)
	q.Set("page", strconv.Itoa(page))
	if len(ids) > 0 {
		parts := make([]string, len(ids))
		for i, id := range ids {
			parts[i] = strconv.Itoa(id)
		}
		// comma = AND in TMDB
		q.Set("with_genres", strings.Join(parts, ","))
	}
	if f.Anime {
		q.Set("with_original_language", animeLanguage)
	}
	return q, true
}

// DiscoverPage is the answer of GET /api/catalog/discover.
type DiscoverPage struct {
	Page         int                 `json:"page"`
	TotalPages   int                 `json:"total_pages"`
	TotalResults int                 `json:"total_results"`
	Results      []tmdb.SearchResult `json:"results"`
	// A search only: the corrected query whose results were merged in (typo)
	DidYouMean string `json:"did_you_mean,omitempty"`
}

// usable drops adult titles and titles without any artwork.
func usable(r tmdb.SearchResult) bool {
	return !r.Adult && (r.PosterPath != "" || r.BackdropPath != "")
}

// mergeByPopularity merges movie and TV pages, most popular first.
func mergeByPopularity(lists ...[]tmdb.SearchResult) []tmdb.SearchResult {
	out := []tmdb.SearchResult{}
	for _, l := range lists {
		for _, r := range l {
			if usable(r) {
				out = append(out, r)
			}
		}
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].Popularity > out[j].Popularity })
	return out
}

// browsePopular lists titles of the filter through TMDB discover (by popularity). With type
// "all", a page holds the movie page and the TV page of that number.
func (s *Service) browsePopular(tm *tmdb.Client, f Filter, page int) (*DiscoverPage, error) {
	page = min(max(page, 1), maxDiscoverPages)
	var (
		wg                sync.WaitGroup
		movies, shows     []tmdb.SearchResult
		mTotal, tTotal    int
		mCount, tCount    int
		mErr, tErr        error
		wantMovie, wantTV bool
	)
	if q, ok := f.discoverParams("movie", page); ok {
		wantMovie = true
		wg.Add(1)
		go func() {
			defer wg.Done()
			res, err := tm.DiscoverMoviePage(q)
			if err != nil {
				mErr = err
				return
			}
			for _, m := range res.Results {
				movies = append(movies, tmdb.MovieResult(m))
			}
			mTotal, mCount = res.TotalPages, res.TotalResults
		}()
	}
	if q, ok := f.discoverParams("tv", page); ok {
		wantTV = true
		wg.Add(1)
		go func() {
			defer wg.Done()
			res, err := tm.DiscoverTVPage(q)
			if err != nil {
				tErr = err
				return
			}
			for _, t := range res.Results {
				shows = append(shows, tmdb.TVResult(t))
			}
			tTotal, tCount = res.TotalPages, res.TotalResults
		}()
	}
	wg.Wait()
	// Fail only when every requested side failed
	if (wantMovie || wantTV) && (!wantMovie || mErr != nil) && (!wantTV || tErr != nil) {
		if mErr != nil {
			return nil, mErr
		}
		return nil, tErr
	}
	return &DiscoverPage{
		Page:         page,
		TotalPages:   min(max(mTotal, tTotal), maxDiscoverPages),
		TotalResults: mCount + tCount,
		Results:      mergeByPopularity(movies, shows),
	}, nil
}

type searchEntry struct {
	results []tmdb.SearchResult
	expires time.Time
}

// searchPool returns the first n TMDB multi-search pages of query (people and titles without
// artwork dropped), in TMDB's order and cached for 15 minutes per language, so changing
// filters does not search again. The pages are asked at once; only page 1 must answer.
func (s *Service) searchPool(tm *tmdb.Client, query string, n int) ([]tmdb.SearchResult, error) {
	key := tm.Lang() + "|" + strconv.Itoa(n) + "|" + strings.ToLower(query)
	now := time.Now()
	s.searchMu.Lock()
	if e, ok := s.searches[key]; ok && now.Before(e.expires) {
		s.searchMu.Unlock()
		return e.results, nil
	}
	s.searchMu.Unlock()

	pages := make([][]tmdb.SearchResult, n)
	errs := make([]error, n)
	var wg sync.WaitGroup
	for p := 1; p <= n; p++ {
		wg.Add(1)
		go func(p int) {
			defer wg.Done()
			res, err := tm.SearchMulti(query, p)
			if err != nil {
				errs[p-1] = err
				if p > 1 {
					log.Printf("catalog: search %q page %d: %v", query, p, err)
				}
				return
			}
			pages[p-1] = res.Results
		}(p)
	}
	wg.Wait()
	if errs[0] != nil {
		return nil, errs[0]
	}

	seen := map[string]bool{}
	var out []tmdb.SearchResult
	for _, list := range pages {
		for _, r := range list {
			k := r.MediaType + ":" + strconv.Itoa(r.ID)
			if seen[k] || !usable(r) {
				continue
			}
			seen[k] = true
			out = append(out, r)
		}
	}
	s.learnTitles(tm.Lang(), out)

	s.searchMu.Lock()
	if len(s.searches) >= searchCacheMax {
		s.searches = map[string]searchEntry{}
	}
	s.searches[key] = searchEntry{results: out, expires: now.Add(searchCacheTTL)}
	s.searchMu.Unlock()
	return out, nil
}

// GET /api/catalog/discover?type=all|movie|tv&genres=16,878&anime=1&q=&page=
func (s *Service) discover(c *gin.Context) {
	f := ParseFilter(c.Request.URL.Query())
	tm := s.client(c)
	query := searchQuery(c)
	var (
		res *DiscoverPage
		err error
	)
	if query != "" {
		if pageParam(c) > 1 {
			c.JSON(http.StatusOK, DiscoverPage{Page: pageParam(c), TotalPages: 1, Results: []tmdb.SearchResult{}})
			return
		}
		res, err = s.RankedSearch(tm, query, f, httpserver.ProfileID(c))
	} else {
		res, err = s.Browse(tm, f, pageParam(c), httpserver.ProfileID(c))
	}
	if err != nil {
		log.Printf("catalog: discover (lang %s): %v", httpserver.Lang(c), err)
		c.JSON(http.StatusBadGateway, gin.H{"error": "Failed to discover titles"})
		return
	}
	c.JSON(http.StatusOK, res)
}
