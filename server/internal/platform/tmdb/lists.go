package tmdb

import (
	"fmt"
	"log"
	"net/url"
	"strconv"
	"sync"
	"sync/atomic"
	"time"
)

const listLimit = 20

// GetLatestMovie returns the newest movie entry on TMDB (not cached).
func (c *Client) GetLatestMovie() (*MovieDetails, error) {
	var movie MovieDetails
	if err := c.Get("/movie/latest", c.params(), &movie); err != nil {
		return nil, err
	}
	return &movie, nil
}

func (c *Client) GetPopularMovies(page int) ([]Movie, error) {
	page = max(page, 1)
	return Cached(c, fmt.Sprintf("popular_movies_page_%d", page), func() ([]Movie, error) {
		var res MovieResults
		if err := c.Get("/movie/popular", c.params("page", strconv.Itoa(page)), &res); err != nil {
			return nil, err
		}
		fillList(c, res.Results, movieText, func() ([]Movie, error) { return c.english().GetPopularMovies(page) })
		return res.Results, nil
	})
}

// DiscoverMovies forwards params to /discover/movie (language defaults to the client's, page to 1).
// Cached for 24h per query: callers must pass validated params, not raw client input.
func (c *Client) DiscoverMovies(params map[string]string) ([]Movie, error) {
	q := url.Values{}
	for k, v := range params {
		q.Set(k, v)
	}
	if _, ok := params["language"]; !ok {
		q.Set("language", c.Language())
	}
	if _, ok := params["page"]; !ok {
		q.Set("page", "1")
	}
	return Cached(c, "discover_movies_"+q.Encode(), func() ([]Movie, error) {
		var res MovieResults
		if err := c.Get("/discover/movie", q, &res); err != nil {
			return nil, err
		}
		if _, ok := params["language"]; !ok {
			fillList(c, res.Results, movieText, func() ([]Movie, error) { return c.english().DiscoverMovies(params) })
		}
		return res.Results, nil
	})
}

// GetNowPlayingMovies returns in-theater movies with their videos attached.
func (c *Client) GetNowPlayingMovies(page int) ([]MovieWithVideos, error) {
	page = max(page, 1)
	return Cached(c, fmt.Sprintf("now_playing_movies_page_%d", page), func() ([]MovieWithVideos, error) {
		var res MovieResults
		if err := c.Get("/movie/now_playing", c.params("page", strconv.Itoa(page)), &res); err != nil {
			return nil, err
		}
		out, err := c.withVideos(res.Results)
		fillList(c, out, movieVideosText, func() ([]MovieWithVideos, error) { return c.english().GetNowPlayingMovies(page) })
		return out, err
	})
}

// GetUpcomingMoviesList returns upcoming movies with their videos attached.
func (c *Client) GetUpcomingMoviesList(page int) ([]MovieWithVideos, error) {
	page = max(page, 1)
	return Cached(c, fmt.Sprintf("upcoming_movies_list_page_%d", page), func() ([]MovieWithVideos, error) {
		var res MovieResults
		if err := c.Get("/movie/upcoming", c.params("page", strconv.Itoa(page)), &res); err != nil {
			return nil, err
		}
		out, err := c.withVideos(res.Results)
		fillList(c, out, movieVideosText, func() ([]MovieWithVideos, error) { return c.english().GetUpcomingMoviesList(page) })
		return out, err
	})
}

// withVideos fetches the video list of every movie (a few at a time); failures leave Videos
// empty and return ErrPartial.
func (c *Client) withVideos(movies []Movie) ([]MovieWithVideos, error) {
	out := make([]MovieWithVideos, len(movies))
	sem := make(chan struct{}, 5)
	var failed atomic.Bool
	var wg sync.WaitGroup
	for i, m := range movies {
		out[i].Movie = m
		wg.Add(1)
		go func(i, id int) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			var v Videos
			if err := c.Get(fmt.Sprintf("/movie/%d/videos", id), c.withVideoLanguages(c.params()), &v); err != nil {
				log.Printf("tmdb: videos for movie %d: %v", id, err)
				failed.Store(true)
				return
			}
			out[i].Videos = v
		}(i, m.ID)
	}
	wg.Wait()
	if failed.Load() {
		return out, ErrPartial
	}
	return out, nil
}

// GetUpcomingMovies bundles daily/weekly trending, now playing and upcoming movies, fetched at once.
// A failing sub-list becomes empty instead of failing the whole call (see ErrPartial).
func (c *Client) GetUpcomingMovies() (*UpcomingMovies, error) {
	if !c.Enabled() {
		return nil, ErrNoAPIKey
	}
	return Cached(c, "upcoming_movies_combined", func() (*UpcomingMovies, error) {
		if c.localized() {
			// The gap fallbacks below read the English lists; Cached shares this fetch with them
			go c.english().GetUpcomingMovies()
		}
		lists, errs := fetchAll([]string{"/trending/movie/day", "/trending/movie/week", "/movie/now_playing", "/movie/upcoming"},
			func(path string) ([]Movie, error) { return c.fetchMovies(path, c.params("page", "1")) })
		var p partial
		u := &UpcomingMovies{
			TrendingDaily:  p.movies(lists[0], errs[0]),
			TrendingWeekly: p.movies(lists[1], errs[1]),
			NowPlaying:     p.movies(lists[2], errs[2]),
			Upcoming:       p.movies(lists[3], errs[3]),
			CachedAt:       time.Now(),
		}
		if c.localized() {
			en := func(pick func(*UpcomingMovies) []Movie) func() ([]Movie, error) {
				return func() ([]Movie, error) {
					e, err := c.english().GetUpcomingMovies()
					if err != nil {
						return nil, err
					}
					return pick(e), nil
				}
			}
			fillList(c, u.TrendingDaily, movieText, en(func(e *UpcomingMovies) []Movie { return e.TrendingDaily }))
			fillList(c, u.TrendingWeekly, movieText, en(func(e *UpcomingMovies) []Movie { return e.TrendingWeekly }))
			fillList(c, u.NowPlaying, movieText, en(func(e *UpcomingMovies) []Movie { return e.NowPlaying }))
			fillList(c, u.Upcoming, movieText, en(func(e *UpcomingMovies) []Movie { return e.Upcoming }))
		}
		return u, p.err()
	})
}

// GetUpcomingTVSeries bundles airing today, on the air and daily/weekly trending TV shows, fetched at once.
func (c *Client) GetUpcomingTVSeries() (*UpcomingTVSeries, error) {
	if !c.Enabled() {
		return nil, ErrNoAPIKey
	}
	return Cached(c, "upcoming_tv_series_combined", func() (*UpcomingTVSeries, error) {
		if c.localized() {
			go c.english().GetUpcomingTVSeries()
		}
		lists, errs := fetchAll([]string{"/tv/airing_today", "/tv/on_the_air", "/trending/tv/day", "/trending/tv/week"},
			func(path string) ([]TV, error) { return c.fetchTV(path, c.params("page", "1")) })
		var p partial
		u := &UpcomingTVSeries{
			AiringToday:    p.tv(lists[0], errs[0]),
			OnTheAir:       p.tv(lists[1], errs[1]),
			TrendingDaily:  p.tv(lists[2], errs[2]),
			TrendingWeekly: p.tv(lists[3], errs[3]),
			CachedAt:       time.Now(),
		}
		if c.localized() {
			en := func(pick func(*UpcomingTVSeries) []TV) func() ([]TV, error) {
				return func() ([]TV, error) {
					e, err := c.english().GetUpcomingTVSeries()
					if err != nil {
						return nil, err
					}
					return pick(e), nil
				}
			}
			fillList(c, u.AiringToday, tvText, en(func(e *UpcomingTVSeries) []TV { return e.AiringToday }))
			fillList(c, u.OnTheAir, tvText, en(func(e *UpcomingTVSeries) []TV { return e.OnTheAir }))
			fillList(c, u.TrendingDaily, tvText, en(func(e *UpcomingTVSeries) []TV { return e.TrendingDaily }))
			fillList(c, u.TrendingWeekly, tvText, en(func(e *UpcomingTVSeries) []TV { return e.TrendingWeekly }))
		}
		return u, p.err()
	})
}

// fetchAll fetches every path at once; results and errors keep the order of paths.
func fetchAll[T any](paths []string, fetch func(path string) ([]T, error)) ([][]T, []error) {
	lists := make([][]T, len(paths))
	errs := make([]error, len(paths))
	var wg sync.WaitGroup
	for i, path := range paths {
		wg.Add(1)
		go func() {
			defer wg.Done()
			lists[i], errs[i] = fetch(path)
		}()
	}
	wg.Wait()
	return lists, errs
}

func (c *Client) fetchMovies(path string, params url.Values) ([]Movie, error) {
	var res MovieResults
	if err := c.Get(path, params, &res); err != nil {
		return nil, err
	}
	return truncate(res.Results), nil
}

func (c *Client) fetchTV(path string, params url.Values) ([]TV, error) {
	var res TVResults
	if err := c.Get(path, params, &res); err != nil {
		return nil, err
	}
	return truncate(res.Results), nil
}

func truncate[T any](items []T) []T {
	if len(items) > listLimit {
		return items[:listLimit]
	}
	return items
}

// partial collects sub-list failures of a combined result.
type partial struct{ failed bool }

func (p *partial) movies(items []Movie, err error) []Movie {
	p.failed = p.failed || err != nil
	return orEmpty(items, err)
}

func (p *partial) tv(items []TV, err error) []TV {
	p.failed = p.failed || err != nil
	return orEmpty(items, err)
}

func (p *partial) err() error {
	if p.failed {
		return ErrPartial
	}
	return nil
}

func orEmpty[T any](items []T, err error) []T {
	if err != nil {
		log.Printf("tmdb: %v", err)
		return []T{}
	}
	if items == nil {
		return []T{}
	}
	return items
}
