package tmdb

import (
	"fmt"
	"log"
	"net/url"
	"strconv"
	"sync"
	"time"
)

const listLimit = 20

// GetLatestMovie returns the newest movie entry on TMDB (not cached).
func (c *Client) GetLatestMovie() (*MovieDetails, error) {
	var movie MovieDetails
	if err := c.Get("/movie/latest", nil, &movie); err != nil {
		return nil, err
	}
	return &movie, nil
}

func (c *Client) GetPopularMovies(page int) ([]Movie, error) {
	page = max(page, 1)
	return Cached(c, fmt.Sprintf("popular_movies_page_%d", page), func() ([]Movie, error) {
		var res MovieResults
		if err := c.Get("/movie/popular", lang("page", strconv.Itoa(page)), &res); err != nil {
			return nil, err
		}
		return res.Results, nil
	})
}

// DiscoverMovies forwards params to /discover/movie (language and page default to en-US and 1).
func (c *Client) DiscoverMovies(params map[string]string) ([]Movie, error) {
	q := url.Values{}
	for k, v := range params {
		q.Set(k, v)
	}
	if _, ok := params["language"]; !ok {
		q.Set("language", "en-US")
	}
	if _, ok := params["page"]; !ok {
		q.Set("page", "1")
	}
	var res MovieResults
	if err := c.Get("/discover/movie", q, &res); err != nil {
		return nil, err
	}
	return res.Results, nil
}

// GetNowPlayingMovies returns in-theater movies with their videos attached.
func (c *Client) GetNowPlayingMovies(page int) ([]MovieWithVideos, error) {
	page = max(page, 1)
	return Cached(c, fmt.Sprintf("now_playing_movies_page_%d", page), func() ([]MovieWithVideos, error) {
		var res MovieResults
		if err := c.Get("/movie/now_playing", lang("page", strconv.Itoa(page)), &res); err != nil {
			return nil, err
		}
		return c.withVideos(res.Results), nil
	})
}

// GetUpcomingMoviesList returns upcoming movies with their videos attached.
func (c *Client) GetUpcomingMoviesList(page int) ([]MovieWithVideos, error) {
	page = max(page, 1)
	return Cached(c, fmt.Sprintf("upcoming_movies_list_page_%d", page), func() ([]MovieWithVideos, error) {
		var res MovieResults
		if err := c.Get("/movie/upcoming", lang("page", strconv.Itoa(page)), &res); err != nil {
			return nil, err
		}
		return c.withVideos(res.Results), nil
	})
}

// withVideos fetches the video list of every movie (a few at a time); failures leave Videos empty.
func (c *Client) withVideos(movies []Movie) []MovieWithVideos {
	out := make([]MovieWithVideos, len(movies))
	sem := make(chan struct{}, 5)
	var wg sync.WaitGroup
	for i, m := range movies {
		out[i].Movie = m
		wg.Add(1)
		go func(i, id int) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			var v Videos
			if err := c.Get(fmt.Sprintf("/movie/%d/videos", id), lang(), &v); err != nil {
				log.Printf("tmdb: videos for movie %d: %v", id, err)
				return
			}
			out[i].Videos = v
		}(i, m.ID)
	}
	wg.Wait()
	return out
}

// GetUpcomingMovies bundles daily/weekly trending, now playing and upcoming movies.
// A failing sub-list becomes empty instead of failing the whole call.
func (c *Client) GetUpcomingMovies() (*UpcomingMovies, error) {
	if !c.Enabled() {
		return nil, ErrNoAPIKey
	}
	return Cached(c, "upcoming_movies_combined", func() (*UpcomingMovies, error) {
		return &UpcomingMovies{
			TrendingDaily:  orEmpty(c.fetchMovies("/trending/movie/day", url.Values{"page": {"1"}})),
			TrendingWeekly: orEmpty(c.fetchMovies("/trending/movie/week", url.Values{"page": {"1"}})),
			NowPlaying:     orEmpty(c.fetchMovies("/movie/now_playing", lang("page", "1"))),
			Upcoming:       orEmpty(c.fetchMovies("/movie/upcoming", lang("page", "1"))),
			CachedAt:       time.Now(),
		}, nil
	})
}

// GetUpcomingTVSeries bundles airing today, on the air and daily/weekly trending TV shows.
func (c *Client) GetUpcomingTVSeries() (*UpcomingTVSeries, error) {
	if !c.Enabled() {
		return nil, ErrNoAPIKey
	}
	return Cached(c, "upcoming_tv_series_combined", func() (*UpcomingTVSeries, error) {
		return &UpcomingTVSeries{
			AiringToday:    orEmpty(c.fetchTV("/tv/airing_today", lang("page", "1"))),
			OnTheAir:       orEmpty(c.fetchTV("/tv/on_the_air", lang("page", "1"))),
			TrendingDaily:  orEmpty(c.fetchTV("/trending/tv/day", url.Values{"page": {"1"}})),
			TrendingWeekly: orEmpty(c.fetchTV("/trending/tv/week", url.Values{"page": {"1"}})),
			CachedAt:       time.Now(),
		}, nil
	})
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
