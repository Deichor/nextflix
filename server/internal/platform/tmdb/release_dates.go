package tmdb

import "fmt"

// Release types of /movie/{id}/release_dates.
const (
	ReleasePremiere          = 1
	ReleaseTheatricalLimited = 2
	ReleaseTheatrical        = 3
	ReleaseDigital           = 4
	ReleasePhysical          = 5
	ReleaseTV                = 6
)

// ReleaseDate is one dated release of a movie in one region.
type ReleaseDate struct {
	Country string `json:"iso_3166_1"`
	Type    int    `json:"type"`
	Date    string `json:"date"` // YYYY-MM-DD
}

// GetMovieReleaseDates returns the release dates of a movie in every region. Not cached
// here; callers cache as needed (the dates change while a film is in cinemas).
func (c *Client) GetMovieReleaseDates(movieID int) ([]ReleaseDate, error) {
	var rd releaseDatesResponse
	if err := c.Get(fmt.Sprintf("/movie/%d/release_dates", movieID), nil, &rd); err != nil {
		return nil, err
	}
	out := []ReleaseDate{}
	for _, region := range rd.Results {
		for _, d := range region.ReleaseDates {
			if len(d.ReleaseDate) < 10 {
				continue
			}
			out = append(out, ReleaseDate{Country: region.ISO31661, Type: d.Type, Date: d.ReleaseDate[:10]})
		}
	}
	return out, nil
}
