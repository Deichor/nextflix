package streaming

import "gorm.io/gorm"

func countSettings(db *gorm.DB) (int64, error) {
	var n int64
	err := db.Model(&IndexerSettings{}).Count(&n).Error
	return n, err
}

// loadSettings returns the first settings row (gorm.ErrRecordNotFound when there is none).
func loadSettings(db *gorm.DB) (*IndexerSettings, error) {
	var st IndexerSettings
	if err := db.First(&st).Error; err != nil {
		return nil, err
	}
	return &st, nil
}

// saveSettings writes only the indexer columns so the other old columns stay as they are.
func saveSettings(db *gorm.DB, st *IndexerSettings) error {
	return db.Model(st).Select("jackett_url", "jackett_api_key", "min_seeders", "preferred_quality", "updated_at").
		Updates(st).Error
}
