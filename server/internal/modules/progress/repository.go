package progress

import (
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

const keyWhere = "profile_id = ? AND media_type = ? AND tmdb_id = ? AND season = ? AND episode = ?"

// profileExists checks the profiles table directly (modules never import each other).
func profileExists(db *gorm.DB, id uint) (bool, error) {
	var n int64
	err := db.Table("profiles").Where("id = ?", id).Count(&n).Error
	return n > 0, err
}

func upsert(db *gorm.DB, row *StreamProgress) error {
	return db.Clauses(clause.OnConflict{
		Columns: []clause.Column{{Name: "profile_id"}, {Name: "media_type"}, {Name: "tmdb_id"}, {Name: "season"}, {Name: "episode"}},
		DoUpdates: clause.AssignmentColumns([]string{
			"title", "poster_path", "backdrop_path", "episode_title", "magnet", "hash",
			"file_index", "position", "duration", "updated_at",
		}),
	}).Create(row).Error
}

func findByKey(db *gorm.DB, profileID uint, mediaType string, tmdbID, season, episode int) (*StreamProgress, error) {
	var p StreamProgress
	if err := db.Where(keyWhere, profileID, mediaType, tmdbID, season, episode).First(&p).Error; err != nil {
		return nil, err
	}
	return &p, nil
}

func listByProfile(db *gorm.DB, profileID uint, limit int) ([]StreamProgress, error) {
	var rows []StreamProgress
	err := db.Where("profile_id = ?", profileID).Order("updated_at DESC, id DESC").Limit(limit).Find(&rows).Error
	return rows, err
}

func listByTitle(db *gorm.DB, profileID uint, mediaType string, tmdbID, limit int) ([]StreamProgress, error) {
	rows := make([]StreamProgress, 0)
	err := db.Where("profile_id = ? AND media_type = ? AND tmdb_id = ?", profileID, mediaType, tmdbID).
		Order("updated_at DESC, id DESC").Limit(limit).Find(&rows).Error
	return rows, err
}

func deleteTitle(db *gorm.DB, profileID uint, mediaType string, tmdbID int) error {
	return db.Where("profile_id = ? AND media_type = ? AND tmdb_id = ?", profileID, mediaType, tmdbID).
		Delete(&StreamProgress{}).Error
}
