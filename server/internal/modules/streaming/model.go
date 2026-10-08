package streaming

import "time"

// IndexerSettings is the Jackett configuration (single row, old table torrent_configs).
// The default tags match the old TorrentConfig so AutoMigrate leaves its columns alone; it only
// adds bitsearch_disabled.
type IndexerSettings struct {
	ID               uint   `gorm:"primaryKey"`
	JackettURL       string `gorm:"default:'http://localhost:9117'"`
	JackettAPIKey    string
	MinSeeders       int    `gorm:"default:10"`
	PreferredQuality string `gorm:"default:'1080p'"`
	// Bitsearch is searched next to Jackett unless this is set (a new column, false = on)
	BitsearchDisabled bool `gorm:"default:false"`
	CreatedAt         time.Time
	UpdatedAt         time.Time
}

func (IndexerSettings) TableName() string { return "torrent_configs" }

// IndexerSettingsView is what GET/PUT /api/settings/indexer answer: the API key is never sent back.
type IndexerSettingsView struct {
	ID               uint      `json:"id"`
	JackettURL       string    `json:"jackett_url"`
	APIKeySet        bool      `json:"api_key_set"`
	MinSeeders       int       `json:"min_seeders"`
	PreferredQuality string    `json:"preferred_quality"`
	BitsearchEnabled bool      `json:"bitsearch_enabled"`
	CreatedAt        time.Time `json:"created_at"`
	UpdatedAt        time.Time `json:"updated_at"`
}

func (st *IndexerSettings) View() IndexerSettingsView {
	return IndexerSettingsView{
		ID:               st.ID,
		JackettURL:       st.JackettURL,
		APIKeySet:        st.JackettAPIKey != "",
		MinSeeders:       st.MinSeeders,
		PreferredQuality: st.PreferredQuality,
		BitsearchEnabled: !st.BitsearchDisabled,
		CreatedAt:        st.CreatedAt,
		UpdatedAt:        st.UpdatedAt,
	}
}

// StreamSource is a resolved torrent for one movie or episode, reused until it expires.
type StreamSource struct {
	SourceKey  string    `gorm:"primaryKey"` // tsResolveKey of the request
	Result     string    // tsResolveResult as JSON
	ResolvedAt time.Time `gorm:"index"`
}

func (StreamSource) TableName() string { return "stream_sources" }

func Models() []any {
	return []any{&IndexerSettings{}, &StreamSource{}}
}
