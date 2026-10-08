package top10

import "time"

// Item is one entry of a Top 10 list. Movies carry release_date, TV shows first_air_date.
type Item struct {
	Rank         int     `json:"rank"`
	TmdbID       int     `json:"tmdb_id"`
	MediaType    string  `json:"media_type"`
	Title        string  `json:"title"`
	PosterPath   string  `json:"poster_path"`
	BackdropPath string  `json:"backdrop_path"`
	Overview     string  `json:"overview"`
	VoteAverage  float64 `json:"vote_average"`
	ReleaseDate  string  `json:"release_date,omitempty"`
	FirstAirDate string  `json:"first_air_date,omitempty"`
}

// List is the response of GET /api/top10.
type List struct {
	Region string `json:"region"`
	Week   string `json:"week"`
	Type   string `json:"type"`
	Items  []Item `json:"items"`
}

// storedList keeps the last good list of a region and type across restarts.
type storedList struct {
	Region    string    `gorm:"primaryKey"`
	MediaType string    `gorm:"primaryKey"`
	Week      string    `gorm:"not null"`
	Items     string    `gorm:"type:text;not null"` // JSON []Item
	FetchedAt time.Time `gorm:"not null"`
}

func (storedList) TableName() string { return "top10_lists" }

// mappedTitle caches the TMDB match of a Netflix title; TmdbID 0 means "no match found".
type mappedTitle struct {
	ID           uint   `gorm:"primaryKey"`
	MediaType    string `gorm:"not null;uniqueIndex:idx_top10_titles_key,priority:1"`
	Query        string `gorm:"not null;uniqueIndex:idx_top10_titles_key,priority:2"` // normalized Netflix title
	NetflixTitle string
	TmdbID       int `gorm:"not null;default:0"`
	Title        string
	Overview     string
	PosterPath   string
	BackdropPath string
	VoteAverage  float64
	Date         string
	UpdatedAt    time.Time
}

func (mappedTitle) TableName() string { return "top10_titles" }

func Models() []any {
	return []any{&storedList{}, &mappedTitle{}}
}

func (m mappedTitle) item(rank int) Item {
	it := Item{Rank: rank, TmdbID: m.TmdbID, MediaType: m.MediaType, Title: m.Title, PosterPath: m.PosterPath,
		BackdropPath: m.BackdropPath, Overview: m.Overview, VoteAverage: m.VoteAverage}
	if m.MediaType == "tv" {
		it.FirstAirDate = m.Date
	} else {
		it.ReleaseDate = m.Date
	}
	return it
}
