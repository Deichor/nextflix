package profiles

import "time"

const (
	MaxProfiles          = 5
	maxProfileNameLen    = 20
	defaultProfileName   = "Home"
	defaultProfileAvatar = "smile"
	defaultProfileColor  = "#E50914"
	defaultLanguage      = "en"
)

// supportedLanguages are the UI languages a profile can pick.
var supportedLanguages = map[string]bool{"en": true, "tr": true}

// Profile is a Netflix-style viewer profile (no passwords).
type Profile struct {
	ID        uint      `json:"id" gorm:"primaryKey"`
	Name      string    `json:"name" gorm:"not null"`
	Avatar    string    `json:"avatar"`
	Color     string    `json:"color"`
	Language  string    `json:"language" gorm:"not null;default:'en'"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"-"`
}

func Models() []any {
	return []any{&Profile{}}
}
