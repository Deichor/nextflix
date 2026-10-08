package tmdb

import (
	"fmt"
	"net/url"
)

// GetTVDetails returns TV details with credits and videos.
func (c *Client) GetTVDetails(tvID int) (*TVDetails, error) {
	return Cached(c, fmt.Sprintf("tv_details_%d", tvID), func() (*TVDetails, error) {
		var d TVDetails
		if err := c.Get(fmt.Sprintf("/tv/%d", tvID), url.Values{"append_to_response": {"credits,videos"}}, &d); err != nil {
			return nil, err
		}
		return &d, nil
	})
}

// GetTVImages returns posters, backdrops and logos for a TV show.
func (c *Client) GetTVImages(tvID int) (*Images, error) {
	return Cached(c, fmt.Sprintf("tv_images_%d", tvID), func() (*Images, error) {
		var images Images
		if err := c.Get(fmt.Sprintf("/tv/%d/images", tvID), nil, &images); err != nil {
			return nil, err
		}
		return &images, nil
	})
}

// GetSeasonDetails returns a season with all its episodes. Not cached here; callers cache as needed.
func (c *Client) GetSeasonDetails(tvID, seasonNumber int) (*Season, error) {
	var season Season
	if err := c.Get(fmt.Sprintf("/tv/%d/season/%d", tvID, seasonNumber), lang(), &season); err != nil {
		return nil, err
	}
	return &season, nil
}

func (c *Client) GetEpisodeDetails(tvID, seasonNumber, episodeNumber int) (*Episode, error) {
	var episode Episode
	path := fmt.Sprintf("/tv/%d/season/%d/episode/%d", tvID, seasonNumber, episodeNumber)
	if err := c.Get(path, lang(), &episode); err != nil {
		return nil, err
	}
	return &episode, nil
}

// GetTVCertification returns the US content rating (TV-MA, TV-14, ...), falling back to GB/AU/CA, or "".
func (c *Client) GetTVCertification(tvID int) string {
	if !c.Enabled() || tvID <= 0 {
		return ""
	}
	var cr contentRatingsResponse
	if err := c.Get(fmt.Sprintf("/tv/%d/content_ratings", tvID), nil, &cr); err != nil {
		return ""
	}
	for _, country := range []string{"US", "GB", "AU", "CA"} {
		for _, entry := range cr.Results {
			if entry.ISO31661 == country && entry.Rating != "" {
				return entry.Rating
			}
		}
	}
	return ""
}
