package recommendations

import "time"

// Item is a recommended movie or TV show. Movies carry release_date, TV shows first_air_date.
type Item struct {
	ID           int     `json:"id"`
	MediaType    string  `json:"media_type"`
	Title        string  `json:"title"`
	Overview     string  `json:"overview"`
	PosterPath   string  `json:"poster_path"`
	BackdropPath string  `json:"backdrop_path"`
	VoteAverage  float64 `json:"vote_average"`
	ReleaseDate  string  `json:"release_date,omitempty"`
	FirstAirDate string  `json:"first_air_date,omitempty"`
	GenreIDs     []int   `json:"genre_ids"`

	Popularity float64 `json:"-"`
	VoteCount  int     `json:"-"`
	Adult      bool    `json:"-"`
}

// Row is one titled row of the recommendations page.
type Row struct {
	ID     string `json:"id"`
	Title  string `json:"title"`
	Reason string `json:"reason"`
	Items  []Item `json:"items"`
}

type Response struct {
	Rows []Row `json:"rows"`
}

// Row reasons.
const (
	ReasonTopPicks       = "top_picks"
	ReasonBecauseLoved   = "because_loved"
	ReasonBecauseLiked   = "because_liked"
	ReasonBecauseFavored = "because_favorite"
	ReasonPopular        = "popular"
)

// Signal is one rated or favorited title of a profile (filled from the ratings module in main.go).
type Signal struct {
	MediaType string
	TmdbID    int
	Title     string
	Rating    string // "", "dislike", "like" or "love"
	Favorite  bool
	UpdatedAt time.Time
}

// RatingsSource returns a profile's rated and favorited titles.
type RatingsSource interface {
	Signals(profileID uint) ([]Signal, error)
}

// RatingsFunc adapts a function to RatingsSource.
type RatingsFunc func(profileID uint) ([]Signal, error)

func (f RatingsFunc) Signals(profileID uint) ([]Signal, error) { return f(profileID) }

// Catalog is the TMDB data the algorithm needs.
type Catalog interface {
	// Related returns TMDB's recommendations and similar titles (page 1 each).
	// lang ("en" or "tr") is the language of titles and overviews.
	Related(lang, mediaType string, id int) (recs, similar []Item, err error)
	Genres(mediaType string, id int) ([]int, error)
	Trending(lang string) ([]Item, error)
}

type titleKey struct {
	mediaType string
	id        int
}

func keyOf(mediaType string, id int) titleKey { return titleKey{mediaType, id} }
