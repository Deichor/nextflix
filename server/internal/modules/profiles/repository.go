package profiles

import "gorm.io/gorm"

func listProfiles(db *gorm.DB) ([]Profile, error) {
	out := make([]Profile, 0)
	err := db.Order("id ASC").Find(&out).Error
	return out, err
}

func findProfile(db *gorm.DB, id uint) (*Profile, error) {
	var p Profile
	if err := db.First(&p, id).Error; err != nil {
		return nil, err
	}
	return &p, nil
}

func countProfiles(db *gorm.DB) (int64, error) {
	var n int64
	err := db.Model(&Profile{}).Count(&n).Error
	return n, err
}
