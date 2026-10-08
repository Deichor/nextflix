package mylist

import (
	"errors"

	"gorm.io/gorm"
)

type Service struct {
	db *gorm.DB
}

func NewService(db *gorm.DB) *Service {
	return &Service{db: db}
}

// ErrExists is answered with 500 like before; the frontend treats it as "already in the list".
var ErrExists = errors.New("item already exists in my list")

// DeleteByProfile removes all rows of a profile inside tx (used by profile deletion).
func (s *Service) DeleteByProfile(tx *gorm.DB, id uint) error {
	return tx.Where("user_id = ?", id).Delete(&MyListItem{}).Error
}

func (s *Service) Add(profileID, mediaID uint, mediaType, notes string, priority int) error {
	n, err := countItem(s.db, profileID, mediaID)
	if err != nil {
		return err
	}
	if n > 0 {
		return ErrExists
	}
	return s.db.Create(&MyListItem{
		UserID:    profileID,
		MediaID:   mediaID,
		MediaType: mediaType,
		Notes:     notes,
		Priority:  priority,
	}).Error
}

func (s *Service) Remove(profileID, mediaID uint) error {
	return deleteItem(s.db, profileID, mediaID)
}

func (s *Service) List(profileID uint) ([]MyListItem, error) {
	return listItems(s.db, profileID)
}

func (s *Service) Contains(profileID, mediaID uint) (bool, error) {
	n, err := countItem(s.db, profileID, mediaID)
	return n > 0, err
}
