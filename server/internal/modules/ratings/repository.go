package ratings

import "gorm.io/gorm"

const keyWhere = "profile_id = ? AND media_type = ? AND tmdb_id = ?"

// profileExists checks the profiles table directly (modules never import each other).
func profileExists(db *gorm.DB, id uint) (bool, error) {
	var n int64
	err := db.Table("profiles").Where("id = ?", id).Count(&n).Error
	return n > 0, err
}

func findByKey(db *gorm.DB, profileID uint, mediaType string, tmdbID int) (*TitleRating, error) {
	var r TitleRating
	if err := db.Where(keyWhere, profileID, mediaType, tmdbID).First(&r).Error; err != nil {
		return nil, err
	}
	return &r, nil
}

// Filter narrows a profile's list; zero values mean "any".
type Filter struct {
	FavoriteOnly bool
	Rating       string
}

func listByProfile(db *gorm.DB, profileID uint, f Filter) ([]TitleRating, error) {
	rows := make([]TitleRating, 0)
	q := db.Where("profile_id = ?", profileID)
	if f.Rating != "" {
		q = q.Where("rating = ?", f.Rating)
	}
	if f.FavoriteOnly {
		q = q.Where("favorite = ?", true).Order("favorited_at DESC")
	}
	err := q.Order("updated_at DESC, id DESC").Find(&rows).Error
	return rows, err
}
