package notifications

type NotificationType string

// Only TMDB-backed types are generated. Older local-media types may still sit in redis and are ignored.
const (
	TypeTMDBUpcoming    NotificationType = "tmdb_upcoming"
	TypeTMDBNowPlaying  NotificationType = "tmdb_now_playing"
	TypeTMDBTrending    NotificationType = "tmdb_trending"
	TypeTMDBUpcomingTV  NotificationType = "tmdb_upcoming_tv"
	TypeTMDBNowAiringTV NotificationType = "tmdb_now_airing_tv"
	TypeTMDBComingSoon  NotificationType = "tmdb_coming_soon"
)

func (t NotificationType) isTMDB() bool {
	switch t {
	case TypeTMDBUpcoming, TypeTMDBNowPlaying, TypeTMDBTrending,
		TypeTMDBUpcomingTV, TypeTMDBNowAiringTV, TypeTMDBComingSoon:
		return true
	}
	return false
}

type Notification struct {
	ID         string           `json:"id"`
	Type       NotificationType `json:"type"`
	Title      string           `json:"title"`
	Message    string           `json:"message"`
	TMDBIDs    []int            `json:"tmdb_ids,omitempty"`
	TMDBTitles []string         `json:"tmdb_titles,omitempty"`
	Timestamp  int64            `json:"timestamp"`
	Read       bool             `json:"read"`

	BackdropURL  string              `json:"backdrop_url,omitempty"`
	PosterURL    string              `json:"poster_url,omitempty"`
	LogoURL      string              `json:"logo_url,omitempty"`
	TrailerKey   string              `json:"trailer_key,omitempty"`
	Rating       float64             `json:"rating,omitempty"`
	ReleaseDate  string              `json:"release_date,omitempty"`
	Runtime      int                 `json:"runtime,omitempty"`
	Genres       []string            `json:"genres,omitempty"`
	Overview     string              `json:"overview,omitempty"`
	Popularity   float64             `json:"popularity,omitempty"`
	Priority     string              `json:"priority,omitempty"`
	Category     string              `json:"category,omitempty"`
	MediaDetails []NotificationMedia `json:"media_details,omitempty"`
	DaysUntil    int                 `json:"days_until,omitempty"`
}

// NotificationMedia is one title inside a multi-title notification.
type NotificationMedia struct {
	ID          int      `json:"id"`
	Title       string   `json:"title"`
	PosterURL   string   `json:"poster_url"`
	BackdropURL string   `json:"backdrop_url"`
	Rating      float64  `json:"rating"`
	Year        int      `json:"year"`
	Runtime     int      `json:"runtime"`
	Genres      []string `json:"genres"`
	Overview    string   `json:"overview"`
	SourceType  string   `json:"source_type"`
	SourceID    string   `json:"source_id"`
}
