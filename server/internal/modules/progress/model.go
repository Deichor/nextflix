package progress

import "time"

const (
	continueWatchingLimit = 20
	finishedRatio         = 0.92

	// Next-episode lookups (TMDB) per Continue Watching request
	maxNextLookups    = 12
	maxNextSteps      = 5 // finished episodes skipped in a row
	nextLookupWorkers = 4
	nextLookupTimeout = 4 * time.Second

	// Input caps: real values are far shorter
	maxTitleLen  = 500  // runes, title and episode_title
	maxPathLen   = 256  // bytes, poster/backdrop path
	maxMagnetLen = 8192 // bytes, magnet links carry tracker lists
	maxHashLen   = 64
)

// StreamProgress is the resume position of a torrent-streamed movie or episode for one profile.
// Movies use season=0, episode=0.
type StreamProgress struct {
	ID           uint      `json:"id" gorm:"primaryKey"`
	ProfileID    uint      `json:"profile_id" gorm:"not null;uniqueIndex:idx_stream_progress_key,priority:1"`
	MediaType    string    `json:"media_type" gorm:"not null;uniqueIndex:idx_stream_progress_key,priority:2"`
	TmdbID       int       `json:"tmdb_id" gorm:"not null;uniqueIndex:idx_stream_progress_key,priority:3"`
	Season       int       `json:"season" gorm:"not null;default:0;uniqueIndex:idx_stream_progress_key,priority:4"`
	Episode      int       `json:"episode" gorm:"not null;default:0;uniqueIndex:idx_stream_progress_key,priority:5"`
	Title        string    `json:"title"`
	PosterPath   string    `json:"poster_path"`
	BackdropPath string    `json:"backdrop_path"`
	EpisodeTitle string    `json:"episode_title"`
	Magnet       string    `json:"magnet"`
	Hash         string    `json:"hash"`
	FileIndex    int       `json:"file_index"`
	Position     float64   `json:"position"`
	Duration     float64   `json:"duration"`
	CreatedAt    time.Time `json:"-"`
	UpdatedAt    time.Time `json:"updated_at" gorm:"index"`
	// NextAirDate (YYYY-MM-DD) marks a next episode that is not out yet; it is never stored
	NextAirDate string `json:"next_air_date,omitempty" gorm:"-"`
}

func (StreamProgress) TableName() string { return "stream_progress" }

func (p StreamProgress) finished() bool {
	return p.Duration > 0 && p.Position >= p.Duration*finishedRatio
}

func Models() []any {
	return []any{&StreamProgress{}}
}
