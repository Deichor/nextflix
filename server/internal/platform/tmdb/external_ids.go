package tmdb

import (
	"fmt"
	"net/url"
)

// ExternalIDs holds the ids other sites use for a movie or TV show.
type ExternalIDs struct {
	IMDBID   string `json:"imdb_id"`
	TVDBID   int    `json:"tvdb_id"`
	WikiData string `json:"wikidata_id"`
}

// GetExternalIDs returns the external ids of a movie ("movie") or TV show ("tv"), cached for 24h.
func (c *Client) GetExternalIDs(mediaType string, id int) (*ExternalIDs, error) {
	if mediaType != "movie" && mediaType != "tv" {
		return nil, fmt.Errorf("unknown media type %q", mediaType)
	}
	return Cached(c.english(), fmt.Sprintf("external_ids_%s_%d", mediaType, id), func() (*ExternalIDs, error) {
		var e ExternalIDs
		if err := c.Get(fmt.Sprintf("/%s/%d/external_ids", mediaType, id), url.Values{}, &e); err != nil {
			return nil, err
		}
		return &e, nil
	})
}
