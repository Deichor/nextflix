package tmdb

import (
	"net/url"
	"strconv"
)

// MoviePage is one page of /discover/movie.
type MoviePage struct {
	Page         int     `json:"page"`
	Results      []Movie `json:"results"`
	TotalPages   int     `json:"total_pages"`
	TotalResults int     `json:"total_results"`
}

// TVPage is one page of /discover/tv.
type TVPage struct {
	Page         int  `json:"page"`
	Results      []TV `json:"results"`
	TotalPages   int  `json:"total_pages"`
	TotalResults int  `json:"total_results"`
}

// discoverQuery copies params and adds the client's language and a page (1 by default).
func (c *Client) discoverQuery(params url.Values) url.Values {
	q := url.Values{}
	for k, v := range params {
		q[k] = append([]string(nil), v...)
	}
	q.Set("language", c.Language())
	if p, err := strconv.Atoi(q.Get("page")); err != nil || p < 1 {
		q.Set("page", "1")
	}
	return q
}

// DiscoverMoviePage returns a page of /discover/movie with its totals, cached 24 h per query.
// Untranslated text of a localized page is filled from the English page.
func (c *Client) DiscoverMoviePage(params url.Values) (*MoviePage, error) {
	q := c.discoverQuery(params)
	return Cached(c, "discover_movie_page_"+q.Encode(), func() (*MoviePage, error) {
		var res MoviePage
		if err := c.Get("/discover/movie", q, &res); err != nil {
			return nil, err
		}
		fillList(c, res.Results, movieText, func() ([]Movie, error) {
			en, err := c.english().DiscoverMoviePage(params)
			if err != nil {
				return nil, err
			}
			return en.Results, nil
		})
		return &res, nil
	})
}

// DiscoverTVPage returns a page of /discover/tv with its totals, cached 24 h per query.
func (c *Client) DiscoverTVPage(params url.Values) (*TVPage, error) {
	q := c.discoverQuery(params)
	return Cached(c, "discover_tv_page_"+q.Encode(), func() (*TVPage, error) {
		var res TVPage
		if err := c.Get("/discover/tv", q, &res); err != nil {
			return nil, err
		}
		fillList(c, res.Results, tvText, func() ([]TV, error) {
			en, err := c.english().DiscoverTVPage(params)
			if err != nil {
				return nil, err
			}
			return en.Results, nil
		})
		return &res, nil
	})
}

// MovieResult converts a movie to the normalized search shape.
func MovieResult(m Movie) SearchResult { return movieResult(m) }

// TVResult converts a TV show to the normalized search shape (name -> title, first_air_date -> release_date).
func TVResult(t TV) SearchResult { return tvResult(t) }
