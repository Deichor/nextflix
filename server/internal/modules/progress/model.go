package progress

import "time"

const (
	continueWatchingLimit = 20
	finishedRatio         = 0.92
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
}

func (StreamProgress) TableName() string { return "stream_progress" }

func (p StreamProgress) finished() bool {
	return p.Duration > 0 && p.Position >= p.Duration*finishedRatio
}

func Models() []any {
	return []any{&StreamProgress{}}
}
