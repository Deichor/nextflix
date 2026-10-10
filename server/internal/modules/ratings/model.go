package ratings

import "time"

// Rating values; a row without a rating is kept only while it is a favorite.
const (
	Dislike = "dislike"
	Like    = "like"
	Love    = "love"
)

// TitleRating is one profile's rating and/or favorite flag for a TMDB title.
type TitleRating struct {
	ID           uint       `json:"-" gorm:"primaryKey"`
	ProfileID    uint       `json:"profile_id" gorm:"not null;uniqueIndex:idx_title_ratings_key,priority:1"`
	MediaType    string     `json:"media_type" gorm:"not null;uniqueIndex:idx_title_ratings_key,priority:2"`
	TmdbID       int        `json:"tmdb_id" gorm:"not null;uniqueIndex:idx_title_ratings_key,priority:3"`
	Title        string     `json:"title"`
	PosterPath   string     `json:"poster_path"`
	BackdropPath string     `json:"backdrop_path"`
	Rating       *string    `json:"rating" gorm:"type:text"`
	Favorite     bool       `json:"favorite" gorm:"not null;default:false;index"`
	FavoritedAt  *time.Time `json:"-"` // orders the Favorites list by the time a title was added
	BackfilledAt *time.Time `json:"-"` // last TMDB lookup for missing title data
	CreatedAt    time.Time  `json:"-"`
	UpdatedAt    time.Time  `json:"updated_at" gorm:"index"`
}

func (TitleRating) TableName() string { return "title_ratings" }

// dataMigration marks one-time data migrations (e.g. My List -> Favorites) as done.
type dataMigration struct {
	Name   string    `gorm:"primaryKey"`
	DoneAt time.Time `gorm:"not null"`
}

func (dataMigration) TableName() string { return "rating_migrations" }

func Models() []any {
	return []any{&TitleRating{}, &dataMigration{}}
}

// Input caps: real TMDB values are far shorter.
const (
	maxTitleLen = 500 // runes
	maxPathLen  = 256 // bytes
)

func validMediaType(t string) bool { return t == "movie" || t == "tv" }

func validRating(r string) bool { return r == Dislike || r == Like || r == Love }
