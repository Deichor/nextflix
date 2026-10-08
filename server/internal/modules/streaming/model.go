package streaming

import "time"

// IndexerSettings is the Jackett configuration (single row, old table torrent_configs).
// The default tags match the old TorrentConfig so AutoMigrate leaves the table alone.
type IndexerSettings struct {
	ID               uint      `json:"id" gorm:"primaryKey"`
	JackettURL       string    `json:"jackett_url" gorm:"default:'http://localhost:9117'"`
	JackettAPIKey    string    `json:"jackett_api_key"`
	MinSeeders       int       `json:"min_seeders" gorm:"default:10"`
	PreferredQuality string    `json:"preferred_quality" gorm:"default:'1080p'"`
	CreatedAt        time.Time `json:"created_at"`
	UpdatedAt        time.Time `json:"updated_at"`
}

func (IndexerSettings) TableName() string { return "torrent_configs" }

func Models() []any {
	return []any{&IndexerSettings{}}
}
