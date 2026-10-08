package tmdb

import (
	"errors"
	"fmt"
	"net/url"
	"sort"
	"strconv"
)

var errEmptyQuery = errors.New("search query cannot be empty")

func (c *Client) searchParams(query string, page int) url.Values {
	return c.params("query", query, "page", strconv.Itoa(max(page, 1)), "include_adult", "false")
}

// SearchMulti searches movies and TV shows; people are dropped and TV fields are normalized.
func (c *Client) SearchMulti(query string, page int) (*MultiSearchResponse, error) {
	if !c.Enabled() {
		return nil, ErrNoAPIKey
	}
	if query == "" {
		return nil, errEmptyQuery
	}
	var raw rawMultiSearchResponse
	if err := c.Get("/search/multi", c.searchParams(query, page), &raw); err != nil {
		return nil, err
	}

	var results []SearchResult
	for _, r := range raw.Results {
		if r.MediaType != "movie" && r.MediaType != "tv" {
			continue
		}
		n := SearchResult{
			ID:           r.ID,
			Overview:     r.Overview,
			PosterPath:   r.PosterPath,
			BackdropPath: r.BackdropPath,
			VoteAverage:  r.VoteAverage,
			VoteCount:    r.VoteCount,
			Popularity:   r.Popularity,
			MediaType:    r.MediaType,
			Adult:        r.Adult,
			GenreIDs:     r.GenreIDs,
		}
		if r.MediaType == "tv" {
			n.Title, n.OriginalTitle, n.ReleaseDate = r.Name, r.OriginalName, r.FirstAirDate
		} else {
			n.Title, n.OriginalTitle, n.ReleaseDate = r.Title, r.OriginalTitle, r.ReleaseDate
		}
		n.OriginalLanguage = r.OriginalLanguage
		results = append(results, n)
	}
	fillList(c, results, searchText, func() ([]SearchResult, error) {
		en, err := c.english().SearchMulti(query, page)
		if err != nil {
			return nil, err
		}
		return en.Results, nil
	})

	return &MultiSearchResponse{
		Page:         raw.Page,
		Results:      results,
		TotalPages:   raw.TotalPages,
		TotalResults: len(results),
	}, nil
}

func (c *Client) SearchMoviesOnly(query string, page int) (*MovieResults, error) {
	if !c.Enabled() {
		return nil, ErrNoAPIKey
	}
	if query == "" {
		return nil, errEmptyQuery
	}
	var res MovieResults
	if err := c.Get("/search/movie", c.searchParams(query, page), &res); err != nil {
		return nil, err
	}
	fillList(c, res.Results, movieText, func() ([]Movie, error) {
		en, err := c.english().SearchMoviesOnly(query, page)
		if err != nil {
			return nil, err
		}
		return en.Results, nil
	})
	return &res, nil
}

func (c *Client) SearchTVOnly(query string, page int) (*TVResults, error) {
	if !c.Enabled() {
		return nil, ErrNoAPIKey
	}
	if query == "" {
		return nil, errEmptyQuery
	}
	var res TVResults
	if err := c.Get("/search/tv", c.searchParams(query, page), &res); err != nil {
		return nil, err
	}
	fillList(c, res.Results, tvText, func() ([]TV, error) {
		en, err := c.english().SearchTVOnly(query, page)
		if err != nil {
			return nil, err
		}
		return en.Results, nil
	})
	return &res, nil
}

func (c *Client) GetSimilarMovies(movieID, page int) (*RelatedMovies, error) {
	return c.relatedMovies("similar", movieID, page)
}

func (c *Client) GetRecommendedMovies(movieID, page int) (*RelatedMovies, error) {
	return c.relatedMovies("recommendations", movieID, page)
}

func (c *Client) GetSimilarTVShows(tvID, page int) (*RelatedTV, error) {
	return c.relatedTV("similar", tvID, page)
}

func (c *Client) GetRecommendedTVShows(tvID, page int) (*RelatedTV, error) {
	return c.relatedTV("recommendations", tvID, page)
}

// relatedMovies returns a page of /movie/{id}/similar or /recommendations, cached for 24h.
func (c *Client) relatedMovies(kind string, id, page int) (*RelatedMovies, error) {
	page = max(page, 1)
	return Cached(c, fmt.Sprintf("movie_%s_%d_page_%d", kind, id, page), func() (*RelatedMovies, error) {
		var res RelatedMovies
		if err := c.Get(fmt.Sprintf("/movie/%d/%s", id, kind), c.params("page", strconv.Itoa(page)), &res); err != nil {
			return nil, err
		}
		fillList(c, res.Results, movieText, func() ([]Movie, error) {
			en, err := c.english().relatedMovies(kind, id, page)
			if err != nil {
				return nil, err
			}
			return en.Results, nil
		})
		return &res, nil
	})
}

// relatedTV returns a page of /tv/{id}/similar or /recommendations, cached for 24h.
func (c *Client) relatedTV(kind string, id, page int) (*RelatedTV, error) {
	page = max(page, 1)
	return Cached(c, fmt.Sprintf("tv_%s_%d_page_%d", kind, id, page), func() (*RelatedTV, error) {
		var res RelatedTV
		if err := c.Get(fmt.Sprintf("/tv/%d/%s", id, kind), c.params("page", strconv.Itoa(page)), &res); err != nil {
			return nil, err
		}
		fillList(c, res.Results, tvText, func() ([]TV, error) {
			en, err := c.english().relatedTV(kind, id, page)
			if err != nil {
				return nil, err
			}
			return en.Results, nil
		})
		return &res, nil
	})
}

// GetRelatedMedia merges similar and recommended titles (similar first, no duplicates, up to limit).
// With releaseYear > 0 the results are ordered by distance to that year.
func (c *Client) GetRelatedMedia(mediaID int, mediaType string, limit, releaseYear int) ([]SearchResult, error) {
	if !c.Enabled() {
		return nil, ErrNoAPIKey
	}
	if limit <= 0 {
		limit = 20
	}

	var results []SearchResult
	seen := map[int]bool{}
	add := func(r SearchResult) {
		if len(results) < limit && !seen[r.ID] {
			seen[r.ID] = true
			results = append(results, r)
		}
	}

	switch mediaType {
	case "movie":
		if similar, err := c.GetSimilarMovies(mediaID, 1); err == nil {
			for _, m := range similar.Results {
				add(movieResult(m))
			}
		}
		if len(results) < limit {
			if rec, err := c.GetRecommendedMovies(mediaID, 1); err == nil {
				for _, m := range rec.Results {
					add(movieResult(m))
				}
			}
		}
	case "tv":
		if similar, err := c.GetSimilarTVShows(mediaID, 1); err == nil {
			for _, t := range similar.Results {
				add(tvResult(t))
			}
		}
		if len(results) < limit {
			if rec, err := c.GetRecommendedTVShows(mediaID, 1); err == nil {
				for _, t := range rec.Results {
					add(tvResult(t))
				}
			}
		}
	}

	if releaseYear > 0 {
		sort.SliceStable(results, func(i, j int) bool {
			return yearDistance(results[i].ReleaseDate, releaseYear) < yearDistance(results[j].ReleaseDate, releaseYear)
		})
	}
	return results, nil
}

func movieResult(m Movie) SearchResult {
	return SearchResult{
		ID: m.ID, Title: m.Title, OriginalTitle: m.OriginalTitle, Overview: m.Overview,
		ReleaseDate: m.ReleaseDate, PosterPath: m.PosterPath, BackdropPath: m.BackdropPath,
		VoteAverage: m.VoteAverage, VoteCount: m.VoteCount, Popularity: m.Popularity,
		MediaType: "movie", Adult: m.Adult, GenreIDs: m.GenreIDs, OriginalLanguage: m.OriginalLanguage,
	}
}

func tvResult(t TV) SearchResult {
	return SearchResult{
		ID: t.ID, Title: t.Name, OriginalTitle: t.OriginalName, Overview: t.Overview,
		ReleaseDate: t.FirstAirDate, PosterPath: t.PosterPath, BackdropPath: t.BackdropPath,
		VoteAverage: t.VoteAverage, VoteCount: t.VoteCount, Popularity: t.Popularity,
		MediaType: "tv", Adult: t.Adult, GenreIDs: t.GenreIDs, OriginalLanguage: t.OriginalLanguage,
	}
}

// YearOf returns the year of a "YYYY-MM-DD" date, or 0.
func YearOf(date string) int {
	if len(date) < 4 {
		return 0
	}
	y, err := strconv.Atoi(date[:4])
	if err != nil {
		return 0
	}
	return y
}

func yearDistance(date string, year int) int {
	d := year - YearOf(date)
	if d < 0 {
		return -d
	}
	return d
}
