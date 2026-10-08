package mylist

import "gorm.io/gorm"

func countItem(db *gorm.DB, userID, mediaID uint) (int64, error) {
	var n int64
	err := db.Model(&MyListItem{}).Where("user_id = ? AND media_id = ?", userID, mediaID).Count(&n).Error
	return n, err
}

func listItems(db *gorm.DB, userID uint) ([]MyListItem, error) {
	items := make([]MyListItem, 0)
	err := db.Where("user_id = ?", userID).Order("priority DESC, added_at DESC").Find(&items).Error
	return items, err
}

func deleteItem(db *gorm.DB, userID, mediaID uint) error {
	return db.Where("user_id = ? AND media_id = ?", userID, mediaID).Delete(&MyListItem{}).Error
}
