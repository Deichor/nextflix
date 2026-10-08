package profiles

import "time"

const (
	MaxProfiles          = 5
	maxProfileNameLen    = 20
	defaultProfileName   = "Home"
	defaultProfileAvatar = "smile"
	defaultProfileColor  = "#E50914"
)

// Profile is a Netflix-style viewer profile (no passwords).
type Profile struct {
	ID        uint      `json:"id" gorm:"primaryKey"`
	Name      string    `json:"name" gorm:"not null"`
	Avatar    string    `json:"avatar"`
	Color     string    `json:"color"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"-"`
}

func Models() []any {
	return []any{&Profile{}}
}
