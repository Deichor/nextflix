package top10

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

const (
	countriesURL = "https://www.netflix.com/tudum/top10/data/all-weeks-countries.tsv"
	globalURL    = "https://www.netflix.com/tudum/top10/data/all-weeks-global.tsv"
	// waybackPrefix serves the newest archived copy; used when netflix.com is unreachable.
	waybackPrefix = "https://web.archive.org/web/29991231000000id_/"

	Global = "global"

	defaultRefresh = 12 * time.Hour
	retryAfter     = time.Hour
	downloadLimit  = 10 * time.Minute
	noMatchRetry   = 3 * 24 * time.Hour
	matchMaxAge    = 30 * 24 * time.Hour
	listSize       = 10
)

type Config struct {
	Countries     []string // ISO2 codes, default TR
	CountriesURLs []string // tried in order; default netflix.com, then the Wayback Machine
	GlobalURLs    []string
	RefreshEvery  time.Duration
	HTTPClient    *http.Client
	// Localizer gives titles in other languages (nil: lists are English only)
	Localizer Localizer
}

type listKey struct{ region, mediaType string }

type Service struct {
	db     *gorm.DB
	search Searcher
	cfg    Config

	mu    sync.RWMutex
	lists map[listKey]List

	refreshMu sync.Mutex
}

func NewService(db *gorm.DB, search Searcher, cfg Config) *Service {
	if len(cfg.Countries) == 0 {
		cfg.Countries = []string{"TR"}
	}
	for i, c := range cfg.Countries {
		cfg.Countries[i] = strings.ToUpper(c)
	}
	if len(cfg.CountriesURLs) == 0 {
		cfg.CountriesURLs = []string{countriesURL, waybackPrefix + countriesURL}
	}
	if len(cfg.GlobalURLs) == 0 {
		cfg.GlobalURLs = []string{globalURL, waybackPrefix + globalURL}
	}
	if cfg.RefreshEvery <= 0 {
		cfg.RefreshEvery = defaultRefresh
	}
	if cfg.HTTPClient == nil {
		cfg.HTTPClient = &http.Client{Transport: &http.Transport{
			Proxy:                 http.ProxyFromEnvironment,
			DialContext:           (&net.Dialer{Timeout: 15 * time.Second}).DialContext,
			TLSHandshakeTimeout:   15 * time.Second,
			ResponseHeaderTimeout: 90 * time.Second,
		}}
	}
	return &Service{db: db, search: search, cfg: cfg, lists: map[listKey]List{}}
}

// Region normalizes a region query value; ok is false for unsupported regions.
func (s *Service) Region(raw string) (string, bool) {
	r := strings.TrimSpace(raw)
	if strings.EqualFold(r, Global) {
		return Global, true
	}
	r = strings.ToUpper(r)
	for _, c := range s.cfg.Countries {
		if c == r {
			return r, true
		}
	}
	return "", false
}

// Get returns the last good list of a region ("TR", "global") and type ("movie", "tv").
func (s *Service) Get(region, mediaType string) (List, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	l, ok := s.lists[listKey{region, mediaType}]
	return l, ok
}

// GetIn is Get with titles, overviews and artwork in lang ("en" or "tr"); ranks are unchanged.
// Titles that cannot be looked up keep their English text.
func (s *Service) GetIn(region, mediaType, lang string) (List, bool) {
	l, ok := s.Get(region, mediaType)
	if !ok || lang == "" || lang == "en" || s.cfg.Localizer == nil {
		return l, ok
	}
	items := make([]Item, len(l.Items))
	copy(items, l.Items)
	var wg sync.WaitGroup
	for i := range items {
		wg.Add(1)
		go func(it *Item) {
			defer wg.Done()
			loc, ok := s.cfg.Localizer.Localize(lang, it.MediaType, it.TmdbID)
			if !ok {
				return
			}
			it.Title = firstNonEmpty(loc.Title, it.Title)
			it.Overview = firstNonEmpty(loc.Overview, it.Overview)
			it.PosterPath = firstNonEmpty(loc.PosterPath, it.PosterPath)
			it.BackdropPath = firstNonEmpty(loc.BackdropPath, it.BackdropPath)
		}(&items[i])
	}
	wg.Wait()
	l.Items = items
	return l, true
}

func firstNonEmpty(a, b string) string {
	if a != "" {
		return a
	}
	return b
}

// Start loads the stored lists and refreshes them now when stale, then every RefreshEvery.
func (s *Service) Start(ctx context.Context) {
	stale := s.load()
	if s.search == nil {
		log.Printf("top10: TMDB_API_KEY not set, lists are not refreshed")
		return
	}
	go func() {
		wait := time.Duration(0)
		if !stale {
			wait = s.cfg.RefreshEvery
		}
		for {
			select {
			case <-ctx.Done():
				return
			case <-time.After(wait):
			}
			if err := s.Refresh(ctx); err != nil {
				log.Printf("top10: refresh: %v", err)
				wait = retryAfter
			} else {
				wait = s.cfg.RefreshEvery
			}
		}
	}()
}

// load reads the stored lists and reports whether any expected list is missing or stale.
func (s *Service) load() bool {
	var rows []storedList
	if err := s.db.Find(&rows).Error; err != nil {
		log.Printf("top10: load stored lists: %v", err)
		return true
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	fresh := map[listKey]bool{}
	for _, r := range rows {
		var items []Item
		if json.Unmarshal([]byte(r.Items), &items) != nil {
			continue
		}
		k := listKey{r.Region, r.MediaType}
		s.lists[k] = List{Region: r.Region, Week: r.Week, Type: r.MediaType, Items: items}
		fresh[k] = time.Since(r.FetchedAt) < s.cfg.RefreshEvery
	}
	for _, region := range append([]string{Global}, s.cfg.Countries...) {
		for _, t := range []string{"movie", "tv"} {
			if !fresh[listKey{region, t}] {
				return true
			}
		}
	}
	return false
}

// Refresh downloads both files, maps the titles to TMDB and stores the new lists.
// Lists that fail keep their previous data.
func (s *Service) Refresh(ctx context.Context) error {
	s.refreshMu.Lock()
	defer s.refreshMu.Unlock()

	var errs []error
	var countries map[string]*weekData
	err := s.download(ctx, s.cfg.CountriesURLs, func(r io.Reader) error {
		var err error
		countries, err = parseCountries(r, s.cfg.Countries)
		return err
	})
	if err != nil {
		errs = append(errs, fmt.Errorf("countries: %w", err))
	}
	for _, iso := range s.cfg.Countries {
		wd := countries[iso]
		if wd == nil || wd.Week == "" {
			continue
		}
		errs = append(errs, s.publish(ctx, iso, "movie", wd.Week, wd.Films))
		errs = append(errs, s.publish(ctx, iso, "tv", wd.Week, wd.TV))
	}

	var global *globalData
	err = s.download(ctx, s.cfg.GlobalURLs, func(r io.Reader) error {
		var err error
		global, err = parseGlobal(r)
		return err
	})
	if err != nil {
		errs = append(errs, fmt.Errorf("global: %w", err))
	} else if global.Week != "" {
		errs = append(errs, s.publish(ctx, Global, "movie", global.Week, mergeGlobal(global.FilmsEN, global.FilmsNonEN)))
		errs = append(errs, s.publish(ctx, Global, "tv", global.Week, mergeGlobal(global.TVEN, global.TVNonEN)))
	}
	return errors.Join(errs...)
}

// download streams the first URL that answers 200 into parse; a failed parse tries the next URL.
func (s *Service) download(ctx context.Context, urls []string, parse func(io.Reader) error) error {
	ctx, cancel := context.WithTimeout(ctx, downloadLimit)
	defer cancel()
	var errs []error
	for _, u := range urls {
		err := s.downloadOne(ctx, u, parse)
		if err == nil {
			log.Printf("top10: loaded %s", u)
			return nil
		}
		log.Printf("top10: %s: %v", u, err)
		errs = append(errs, err)
		if ctx.Err() != nil {
			break
		}
	}
	return errors.Join(errs...)
}

func (s *Service) downloadOne(ctx context.Context, u string, parse func(io.Reader) error) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return err
	}
	req.Header.Set("User-Agent", "Nextflix/1.0 (+self-hosted)")
	resp, err := s.cfg.HTTPClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("HTTP %d", resp.StatusCode)
	}
	return parse(resp.Body)
}

// publish maps entries to TMDB and stores the list when at least one title matched. A week
// older than the stored list (an old archived copy) keeps the stored one.
func (s *Service) publish(ctx context.Context, region, mediaType, week string, entries []entry) error {
	if old, ok := s.Get(region, mediaType); ok && week < old.Week {
		log.Printf("top10: %s %s: week %s is older than the stored %s, kept", region, mediaType, week, old.Week)
		return nil
	}
	items, searchErr := s.mapEntries(ctx, mediaType, entries)
	if err := ctx.Err(); err != nil {
		return err
	}
	if len(items) == 0 {
		if searchErr != nil {
			return fmt.Errorf("%s %s %s: no titles matched: %w", region, mediaType, week, searchErr)
		}
		log.Printf("top10: %s %s %s: no titles matched", region, mediaType, week)
		return nil
	}
	raw, err := json.Marshal(items)
	if err != nil {
		return err
	}
	row := storedList{Region: region, MediaType: mediaType, Week: week, Items: string(raw), FetchedAt: time.Now()}
	if err := s.db.Clauses(clause.OnConflict{UpdateAll: true}).Create(&row).Error; err != nil {
		return err
	}
	s.mu.Lock()
	s.lists[listKey{region, mediaType}] = List{Region: region, Week: week, Type: mediaType, Items: items}
	s.mu.Unlock()
	return nil
}

// mapEntries resolves the top entries to TMDB in rank order; unmatched titles are skipped and
// a show listed with several seasons appears once, at its best rank. err is the first failed
// TMDB search.
func (s *Service) mapEntries(ctx context.Context, mediaType string, entries []entry) (items []Item, err error) {
	items = make([]Item, 0, listSize)
	seen := map[int]bool{}
	for _, e := range entries {
		if ctx.Err() != nil || len(items) == listSize {
			break
		}
		m, ok, serr := s.resolve(mediaType, e)
		if serr != nil && err == nil {
			err = serr
		}
		if !ok || seen[m.TmdbID] {
			continue
		}
		seen[m.TmdbID] = true
		items = append(items, m.item(e.Rank))
	}
	return items, err
}

// resolve returns the cached TMDB match of a title, searching TMDB when the cache has none.
// err is set when the search failed and no cached match was left to fall back on.
func (s *Service) resolve(mediaType string, e entry) (mappedTitle, bool, error) {
	key := normTitle(e.Title)
	var cached mappedTitle
	err := s.db.Where("media_type = ? AND query = ?", mediaType, key).First(&cached).Error
	if err == nil {
		age := time.Since(cached.UpdatedAt)
		if cached.TmdbID > 0 && age < matchMaxAge {
			return cached, true, nil
		}
		if cached.TmdbID == 0 && age < noMatchRetry {
			return cached, false, nil
		}
	}

	m, found, err := s.searchTitle(mediaType, e)
	if err != nil {
		log.Printf("top10: search %q: %v", e.Title, err)
		if cached.TmdbID > 0 {
			return cached, true, nil
		}
		return mappedTitle{}, false, err
	}
	row := mappedTitle{MediaType: mediaType, Query: key, NetflixTitle: e.Title, UpdatedAt: time.Now()}
	if found {
		row.TmdbID, row.Title, row.Overview = m.TmdbID, m.Title, m.Overview
		row.PosterPath, row.BackdropPath = m.PosterPath, m.BackdropPath
		row.VoteAverage, row.Date = m.VoteAverage, m.Date
	}
	err = s.db.Clauses(clause.OnConflict{
		Columns:   []clause.Column{{Name: "media_type"}, {Name: "query"}},
		DoUpdates: clause.AssignmentColumns([]string{"netflix_title", "tmdb_id", "title", "overview", "poster_path", "backdrop_path", "vote_average", "date", "updated_at"}),
	}).Create(&row).Error
	if err != nil {
		log.Printf("top10: save match %q: %v", e.Title, err)
	}
	return row, found, nil
}

// searchTitle answers with the first query that has usable results (exact match preferred).
// Later, shorter queries only run when earlier ones found nothing, so "Demon Slayer: ... Infinity
// Castle" never falls back to an exact but unrelated "Demon Slayer".
func (s *Service) searchTitle(mediaType string, e entry) (Match, bool, error) {
	for _, q := range queries(e, mediaType) {
		var hits []Match
		var err error
		if mediaType == "tv" {
			hits, err = s.search.SearchTV(q)
		} else {
			hits, err = s.search.SearchMovies(q)
		}
		if err != nil {
			return Match{}, false, err
		}
		if m, found, _ := pickMatch(q, hits); found {
			return m, true, nil
		}
	}
	return Match{}, false, nil
}
