package tmdb

import (
	"fmt"
	"net/url"
)

func (c *Client) GetMovieDetails(movieID int) (*MovieDetails, error) {
	return CachedFresh(c, fmt.Sprintf("movie_details_%d", movieID), func() (*MovieDetails, error) {
		var d MovieDetails
		if err := c.Get(fmt.Sprintf("/movie/%d", movieID), c.params("append_to_response", "credits"), &d); err != nil {
			return nil, err
		}
		c.fillMovieDetails(&d)
		return &d, nil
	})
}

// GetMovieDetailsWithExtras returns movie details with credits and videos.
func (c *Client) GetMovieDetailsWithExtras(movieID int) (*MovieDetailsWithExtras, error) {
	return CachedFresh(c, fmt.Sprintf("movie_details_extras_%d", movieID), func() (*MovieDetailsWithExtras, error) {
		var d MovieDetailsWithExtras
		params := c.withVideoLanguages(c.params("append_to_response", "credits,videos"))
		if err := c.Get(fmt.Sprintf("/movie/%d", movieID), params, &d); err != nil {
			return nil, err
		}
		c.fillMovieDetails(&d.MovieDetails)
		return &d, nil
	})
}

// GetMovieImages returns posters, backdrops and logos for a movie. A localized client gets
// images in its language, English and language-less ones; English gets every image.
func (c *Client) GetMovieImages(movieID int) (*Images, error) {
	return Cached(c, fmt.Sprintf("movie_images_%d", movieID), func() (*Images, error) {
		var images Images
		if err := c.Get(fmt.Sprintf("/movie/%d/images", movieID), c.imageParams(), &images); err != nil {
			return nil, err
		}
		return &images, nil
	})
}

func (c *Client) imageParams() url.Values {
	v := url.Values{}
	if l := c.mediaLanguages(); l != "" {
		v.Set("include_image_language", l)
	}
	return v
}

// GetMovieCertification returns the US certification (PG-13, R, ...), falling back to GB/AU/CA, or "".
func (c *Client) GetMovieCertification(movieID int) string {
	if !c.Enabled() || movieID <= 0 {
		return ""
	}
	var rd releaseDatesResponse
	if err := c.Get(fmt.Sprintf("/movie/%d/release_dates", movieID), nil, &rd); err != nil {
		return ""
	}
	for _, country := range []string{"US", "GB", "AU", "CA"} {
		for _, entry := range rd.Results {
			if entry.ISO31661 != country {
				continue
			}
			for _, d := range entry.ReleaseDates {
				if d.Certification != "" {
					return d.Certification
				}
			}
		}
	}
	return ""
}
