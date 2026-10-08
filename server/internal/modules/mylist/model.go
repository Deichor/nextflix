package mylist

import "time"

// MyListItem is one title in a profile's "My List". UserID holds the profile id;
// TMDB titles use media_id "9"+tmdbId.
type MyListItem struct {
	ID        uint      `json:"id" gorm:"primaryKey"`
	UserID    uint      `json:"user_id" gorm:"not null;index"`
	MediaID   uint      `json:"media_id" gorm:"not null;index"`
	MediaType string    `json:"media_type"` // movie, tv, episode
	Notes     string    `json:"notes"`
	Priority  int       `json:"priority" gorm:"default:0"`
	AddedAt   time.Time `json:"added_at" gorm:"autoCreateTime"`
}

func (MyListItem) TableName() string { return "my_list_items" }

func Models() []any {
	return []any{&MyListItem{}}
}
