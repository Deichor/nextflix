package catalog

import (
	"fmt"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"nextflix/internal/platform/tmdb"
)

// TMDB side of the "More Like This" matcher. Lists (recommendations, similar, collections,
// person credits, keyword discover) go through the shared TMDB cache in the request language;
// per-title facts and keyword counts are language-independent and kept in a compact cache of
// their own (thousands of candidates would otherwise flood the shared cache).

const (
	simFactsTTL   = 7 * 24 * time.Hour
	simFactsMax   = 15000
	simDFTTL      = 14 * 24 * time.Hour
	simDFMax      = 20000
	simTMDBSlots  = 12 // TMDB requests of the matcher in flight, server-wide
	simPersonTop  = 25 // best voted works kept per person and role
	simKeywordMax = 30 // source keywords whose title counts are looked up
)

// simPersonWorks are the notable works of one person.
type simPersonWorks struct {
	Lead []tmdb.SearchResult // lead roles: movie cast order <= 3, 6+ episodes of a show
	Made []tmdb.SearchResult // directed or created
}

// simFetcher is the TMDB data the matcher needs (faked in tests).
type simFetcher interface {
	source(mediaType string, id int) (*simSource, error)
	facts(mediaType string, id int) (*simFacts, error)
	// related returns page of TMDB "recommendations" or "similar" (kind) in lang.
	related(lang, mediaType, kind string, id, page int) ([]tmdb.SearchResult, error)
	collection(lang string, id int) ([]tmdb.SearchResult, error)
	personWorks(lang string, personID int) (*simPersonWorks, error)
	// keywordDF returns how many titles of mediaType carry the keyword.
	keywordDF(mediaType string, kw int) (int, error)
	// discover returns the most voted titles of a TMDB discover query.
	discover(lang, mediaType string, q simQuery) ([]tmdb.SearchResult, error)
}

type expiring[T any] struct {
	v       T
	expires time.Time
}

// tmdbSim implements simFetcher on the shared TMDB client.
type tmdbSim struct {
	tm    *tmdb.Client
	slots chan struct{}

	mu        sync.Mutex
	factCache map[string]expiring[*simFacts]
	df        map[string]expiring[int]
	sources   map[string]expiring[*simDetails] // kept past expiry: served when TMDB fails
}

func newTMDBSim(tm *tmdb.Client) *tmdbSim {
	return &tmdbSim{tm: tm, slots: make(chan struct{}, simTMDBSlots),
		factCache: map[string]expiring[*simFacts]{}, df: map[string]expiring[int]{},
		sources: map[string]expiring[*simDetails]{}}
}

// call runs fn in one of the matcher's TMDB slots.
func (t *tmdbSim) call(fn func() error) error {
	t.slots <- struct{}{}
	defer func() { <-t.slots }()
	return fn()
}

// prune drops expired entries of a full cache, or all of them when none expired.
func prune[T any](m map[string]expiring[T], limit int) map[string]expiring[T] {
	if len(m) < limit {
		return m
	}
	now := time.Now()
	for k, e := range m {
		if now.After(e.expires) {
			delete(m, k)
		}
	}
	if len(m) >= limit {
		return map[string]expiring[T]{}
	}
	return m
}

type simKeyword struct {
	ID   int    `json:"id"`
	Name string `json:"name"`
}

// simDetails is a /movie or /tv answer with keywords (and credits for the source).
type simDetails struct {
	ID               int            `json:"id"`
	Title            string         `json:"title"`
	Name             string         `json:"name"`
	OriginalTitle    string         `json:"original_title"`
	OriginalName     string         `json:"original_name"`
	Overview         string         `json:"overview"`
	ReleaseDate      string         `json:"release_date"`
	FirstAirDate     string         `json:"first_air_date"`
	PosterPath       string         `json:"poster_path"`
	OriginalLanguage string         `json:"original_language"`
	Genres           []tmdb.Genre   `json:"genres"`
	Runtime          int            `json:"runtime"`
	EpisodeRunTime   []int          `json:"episode_run_time"`
	Type             string         `json:"type"`
	OriginCountry    []string       `json:"origin_country"`
	Production       []tmdb.Country `json:"production_countries"`
	Collection       *struct {
		ID int `json:"id"`
	} `json:"belongs_to_collection"`
	CreatedBy []struct {
		ID int `json:"id"`
	} `json:"created_by"`
	VoteAverage float64 `json:"vote_average"`
	VoteCount   int     `json:"vote_count"`
	Popularity  float64 `json:"popularity"`
	Adult       bool    `json:"adult"`
	Video       bool    `json:"video"`
	Keywords    struct {
		Keywords []simKeyword `json:"keywords"` // movies
		Results  []simKeyword `json:"results"`  // TV
	} `json:"keywords"`
	Credits *tmdb.Credits `json:"credits"`
}

func (d *simDetails) toFacts(mediaType string) *simFacts {
	f := &simFacts{MediaType: mediaType, ID: d.ID, Overview: d.Overview, Language: d.OriginalLanguage,
		VoteAverage: d.VoteAverage, VoteCount: d.VoteCount, Popularity: d.Popularity, Adult: d.Adult,
		Video: d.Video, PosterPath: d.PosterPath, Countries: d.OriginCountry}
	if mediaType == "tv" {
		f.Title, f.OriginalTitle, f.Date, f.TVType = d.Name, d.OriginalName, d.FirstAirDate, d.Type
		if len(d.EpisodeRunTime) > 0 {
			f.Runtime = d.EpisodeRunTime[0]
		}
	} else {
		f.Title, f.OriginalTitle, f.Date, f.Runtime = d.Title, d.OriginalTitle, d.ReleaseDate, d.Runtime
		if d.Collection != nil {
			f.Collection = d.Collection.ID
		}
	}
	if len(f.Countries) == 0 {
		for _, c := range d.Production {
			f.Countries = append(f.Countries, c.ISO31661)
		}
	}
	for _, g := range d.Genres {
		f.Genres = append(f.Genres, g.ID)
	}
	for _, k := range append(d.Keywords.Keywords, d.Keywords.Results...) {
		f.Keywords = append(f.Keywords, k.ID)
	}
	return f
}

func (t *tmdbSim) details(mediaType string, id int, credits bool) (*simDetails, error) {
	extra := "keywords"
	if credits {
		extra = "keywords,credits"
	}
	var d simDetails
	path := fmt.Sprintf("/%s/%d", mediaType, id)
	err := t.call(func() error {
		return t.tm.Get(path, url.Values{"append_to_response": {extra}, "language": {"en-US"}}, &d)
	})
	if err != nil {
		return nil, err
	}
	return &d, nil
}

func (t *tmdbSim) source(mediaType string, id int) (*simSource, error) {
	key := resultKey(mediaType, id)
	t.mu.Lock()
	cached, ok := t.sources[key]
	t.mu.Unlock()
	d := cached.v
	if !ok || time.Now().After(cached.expires) {
		fresh, err := t.details(mediaType, id, true)
		switch {
		case err == nil:
			d = fresh
			t.mu.Lock()
			t.sources = prune(t.sources, simCacheMax)
			t.sources[key] = expiring[*simDetails]{d, time.Now().Add(simCacheTTL)}
			t.mu.Unlock()
		case !ok:
			return nil, err
		}
	}
	src := &simSource{simFacts: *d.toFacts(mediaType), KeywordNames: map[int]string{}}
	for _, k := range append(d.Keywords.Keywords, d.Keywords.Results...) {
		src.KeywordNames[k.ID] = k.Name
	}
	for _, c := range d.CreatedBy {
		src.Creators = append(src.Creators, c.ID)
	}
	if d.Credits != nil {
		if mediaType == "movie" {
			for _, c := range d.Credits.Crew {
				if c.Job == "Director" {
					src.Creators = append(src.Creators, c.ID)
				}
			}
		}
		cast := append([]tmdb.Cast(nil), d.Credits.Cast...)
		sort.SliceStable(cast, func(i, j int) bool { return cast[i].Order < cast[j].Order })
		for i := 0; i < len(cast) && i < 3; i++ {
			src.Cast = append(src.Cast, cast[i].ID)
		}
	}
	if len(src.Creators) > 2 {
		src.Creators = src.Creators[:2]
	}
	return src, nil
}

func (t *tmdbSim) facts(mediaType string, id int) (*simFacts, error) {
	key := resultKey(mediaType, id)
	t.mu.Lock()
	e, ok := t.factCache[key]
	t.mu.Unlock()
	if ok && time.Now().Before(e.expires) {
		return e.v, nil
	}
	d, err := t.details(mediaType, id, false)
	if err != nil {
		return nil, err
	}
	f := d.toFacts(mediaType)
	t.mu.Lock()
	t.factCache = prune(t.factCache, simFactsMax)
	t.factCache[key] = expiring[*simFacts]{f, time.Now().Add(simFactsTTL)}
	t.mu.Unlock()
	return f, nil
}

func movieResults(ms []tmdb.Movie) []tmdb.SearchResult {
	out := make([]tmdb.SearchResult, 0, len(ms))
	for _, m := range ms {
		if !m.Video {
			out = append(out, tmdb.MovieResult(m))
		}
	}
	return out
}

func tvResults(ts []tmdb.TV) []tmdb.SearchResult {
	out := make([]tmdb.SearchResult, 0, len(ts))
	for _, s := range ts {
		out = append(out, tmdb.TVResult(s))
	}
	return out
}

func (t *tmdbSim) related(lang, mediaType, kind string, id, page int) ([]tmdb.SearchResult, error) {
	tm := t.tm.WithLang(lang)
	var out []tmdb.SearchResult
	err := t.call(func() error {
		if mediaType == "tv" {
			fetch := tm.GetSimilarTVShows
			if kind == "recommendations" {
				fetch = tm.GetRecommendedTVShows
			}
			res, err := fetch(id, page)
			if err == nil {
				out = tvResults(res.Results)
			}
			return err
		}
		fetch := tm.GetSimilarMovies
		if kind == "recommendations" {
			fetch = tm.GetRecommendedMovies
		}
		res, err := fetch(id, page)
		if err == nil {
			out = movieResults(res.Results)
		}
		return err
	})
	return out, err
}

func (t *tmdbSim) collection(lang string, id int) ([]tmdb.SearchResult, error) {
	tm := t.tm.WithLang(lang)
	var out []tmdb.SearchResult
	err := t.call(func() error {
		var err error
		out, err = tmdb.Cached(tm, fmt.Sprintf("sim_collection_%d", id), func() ([]tmdb.SearchResult, error) {
			var res struct {
				Parts []tmdb.Movie `json:"parts"`
			}
			if err := tm.Get(fmt.Sprintf("/collection/%d", id), url.Values{"language": {tm.Language()}}, &res); err != nil {
				return nil, err
			}
			return movieResults(res.Parts), nil
		})
		return err
	})
	return out, err
}

type simCredit struct {
	tmdb.Movie
	MediaType    string `json:"media_type"`
	Name         string `json:"name"`
	OriginalName string `json:"original_name"`
	FirstAirDate string `json:"first_air_date"`
	Order        *int   `json:"order"`
	EpisodeCount int    `json:"episode_count"`
	Character    string `json:"character"`
	Job          string `json:"job"`
}

func (c simCredit) result() tmdb.SearchResult {
	if c.MediaType == "tv" {
		return tmdb.TVResult(tmdb.TV{ID: c.ID, Name: c.Name, OriginalName: c.OriginalName, Overview: c.Overview,
			FirstAirDate: c.FirstAirDate, PosterPath: c.PosterPath, BackdropPath: c.BackdropPath,
			GenreIDs: c.GenreIDs, VoteAverage: c.VoteAverage, VoteCount: c.VoteCount, Popularity: c.Popularity,
			Adult: c.Adult, OriginalLanguage: c.OriginalLanguage})
	}
	return tmdb.MovieResult(c.Movie)
}

// selfRole: talk show guests, cameos as themselves, voice-over narration.
func selfRole(character string) bool {
	c := strings.ToLower(character)
	return strings.Contains(c, "self") || strings.Contains(c, "narrator") || strings.Contains(c, "host")
}

func (t *tmdbSim) personWorks(lang string, personID int) (*simPersonWorks, error) {
	tm := t.tm.WithLang(lang)
	var out *simPersonWorks
	err := t.call(func() error {
		var err error
		out, err = tmdb.Cached(tm, fmt.Sprintf("sim_person_%d", personID), func() (*simPersonWorks, error) {
			var res struct {
				Cast []simCredit `json:"cast"`
				Crew []simCredit `json:"crew"`
			}
			path := fmt.Sprintf("/person/%d/combined_credits", personID)
			if err := tm.Get(path, url.Values{"language": {tm.Language()}}, &res); err != nil {
				return nil, err
			}
			return personWorksOf(res.Cast, res.Crew), nil
		})
		return err
	})
	return out, err
}

func personWorksOf(cast, crew []simCredit) *simPersonWorks {
	pick := func(list []simCredit, keep func(simCredit) bool) []tmdb.SearchResult {
		seen := map[string]bool{}
		var out []tmdb.SearchResult
		for _, c := range list {
			if c.MediaType != "movie" && c.MediaType != "tv" || c.Video || !keep(c) {
				continue
			}
			r := c.result()
			if k := resultKey(r.MediaType, r.ID); !seen[k] {
				seen[k] = true
				out = append(out, r)
			}
		}
		sort.SliceStable(out, func(i, j int) bool { return out[i].VoteCount > out[j].VoteCount })
		if len(out) > simPersonTop {
			out = out[:simPersonTop]
		}
		return out
	}
	return &simPersonWorks{
		Lead: pick(cast, func(c simCredit) bool {
			if selfRole(c.Character) {
				return false
			}
			if c.MediaType == "movie" {
				return c.Order != nil && *c.Order <= 3
			}
			return c.EpisodeCount >= 6
		}),
		Made: pick(crew, func(c simCredit) bool {
			return c.Job == "Director" && c.MediaType == "movie" || c.Job == "Creator" ||
				c.MediaType == "movie" && (c.Job == "Screenplay" || c.Job == "Writer")
		}),
	}
}

func (t *tmdbSim) keywordDF(mediaType string, kw int) (int, error) {
	key := mediaType + ":" + strconv.Itoa(kw)
	t.mu.Lock()
	e, ok := t.df[key]
	t.mu.Unlock()
	if ok && time.Now().Before(e.expires) {
		return e.v, nil
	}
	var res struct {
		TotalResults int `json:"total_results"`
	}
	err := t.call(func() error {
		return t.tm.Get("/discover/"+mediaType, url.Values{"with_keywords": {strconv.Itoa(kw)},
			"include_adult": {"false"}, "language": {"en-US"}}, &res)
	})
	if err != nil {
		return 0, err
	}
	t.mu.Lock()
	t.df = prune(t.df, simDFMax)
	t.df[key] = expiring[int]{res.TotalResults, time.Now().Add(simDFTTL)}
	t.mu.Unlock()
	return res.TotalResults, nil
}

// simQuery is one discover list of the matcher, sorted by vote count. Zero fields are not sent.
type simQuery struct {
	Keyword  int
	Genre    int
	Language string // original language
	MinVotes int
	Page     int
}

func (t *tmdbSim) discover(lang, mediaType string, q simQuery) ([]tmdb.SearchResult, error) {
	tm := t.tm.WithLang(lang)
	v := url.Values{"sort_by": {"vote_count.desc"}, "include_adult": {"false"},
		"vote_count.gte": {strconv.Itoa(q.MinVotes)}, "page": {strconv.Itoa(max(q.Page, 1))}}
	if q.Keyword > 0 {
		v.Set("with_keywords", strconv.Itoa(q.Keyword))
	}
	if q.Genre > 0 {
		v.Set("with_genres", strconv.Itoa(q.Genre))
	}
	if q.Language != "" {
		v.Set("with_original_language", q.Language)
	}
	var out []tmdb.SearchResult
	err := t.call(func() error {
		if mediaType == "tv" {
			res, err := tm.DiscoverTVPage(v)
			if err == nil {
				out = tvResults(res.Results)
			}
			return err
		}
		res, err := tm.DiscoverMoviePage(v)
		if err == nil {
			out = movieResults(res.Results)
		}
		return err
	})
	return out, err
}
