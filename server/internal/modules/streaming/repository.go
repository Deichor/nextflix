package streaming

import (
	"time"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

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
	return db.Model(st).Select("jackett_url", "jackett_api_key", "min_seeders", "preferred_quality", "bitsearch_disabled", "updated_at").
		Updates(st).Error
}

// loadSource returns the cached source for key, or nil when there is none.
func loadSource(db *gorm.DB, key string) (*StreamSource, error) {
	var rows []StreamSource
	if err := db.Where("source_key = ?", key).Limit(1).Find(&rows).Error; err != nil || len(rows) == 0 {
		return nil, err
	}
	return &rows[0], nil
}

func saveSource(db *gorm.DB, src *StreamSource) error {
	return db.Clauses(clause.OnConflict{UpdateAll: true}).Create(src).Error
}

func deleteSource(db *gorm.DB, key string) error {
	return db.Where("source_key = ?", key).Delete(&StreamSource{}).Error
}

func pruneSources(db *gorm.DB, before time.Time) error {
	return db.Where("resolved_at < ?", before).Delete(&StreamSource{}).Error
}
