package tmdb

import (
	"fmt"
	"net/url"
)

// AlternativeTitle is another name of a movie or TV show in one region.
type AlternativeTitle struct {
	Country string `json:"iso_3166_1"`
	Title   string `json:"title"`
	Type    string `json:"type"`
}

// GetAlternativeTitles returns the alternative titles of a movie ("movie") or TV show
// ("tv"), cached for 24h.
func (c *Client) GetAlternativeTitles(mediaType string, id int) ([]AlternativeTitle, error) {
	if mediaType != "movie" && mediaType != "tv" {
		return nil, fmt.Errorf("unknown media type %q", mediaType)
	}
	return Cached(c.english(), fmt.Sprintf("alt_titles_%s_%d", mediaType, id), func() ([]AlternativeTitle, error) {
		// Movies answer {"titles": [...]}, TV shows {"results": [...]}
		var r struct {
			Titles  []AlternativeTitle `json:"titles"`
			Results []AlternativeTitle `json:"results"`
		}
		if err := c.Get(fmt.Sprintf("/%s/%d/alternative_titles", mediaType, id), url.Values{}, &r); err != nil {
			return nil, err
		}
		return append(r.Titles, r.Results...), nil
	})
}
