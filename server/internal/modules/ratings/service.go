package ratings

import (
	"errors"
	"strings"
	"sync"
	"time"

	"gorm.io/gorm"
)

var (
	ErrNotFound       = errors.New("No rating found")
	ErrUnknownProfile = errors.New("Unknown profile")
)

type Service struct {
	db     *gorm.DB
	titles TitleLookup // optional, fills missing titles/posters of migrated rows

	mu       sync.RWMutex
	onChange []func(profileID uint)
}

func NewService(db *gorm.DB, titles TitleLookup) *Service {
	return &Service{db: db, titles: titles}
}

// OnChange registers fn to be called after a profile's ratings or favorites changed.
func (s *Service) OnChange(fn func(profileID uint)) {
	s.mu.Lock()
	s.onChange = append(s.onChange, fn)
	s.mu.Unlock()
}

func (s *Service) changed(profileID uint) {
	s.mu.RLock()
	hooks := s.onChange
	s.mu.RUnlock()
	for _, fn := range hooks {
		fn(profileID)
	}
}

// DeleteByProfile removes all rows of a profile inside tx (used by profile deletion).
func (s *Service) DeleteByProfile(tx *gorm.DB, id uint) error {
	return tx.Where("profile_id = ?", id).Delete(&TitleRating{}).Error
}

// ProfileDeleted runs the change hooks once the profile deletion committed, so no cache
// is rebuilt from the old rows.
func (s *Service) ProfileDeleted(id uint) { s.changed(id) }

func (s *Service) List(profileID uint, f Filter) ([]TitleRating, error) {
	return listByProfile(s.db, profileID, f)
}

func (s *Service) Item(profileID uint, mediaType string, tmdbID int) (*TitleRating, error) {
	r, err := findByKey(s.db, profileID, mediaType, tmdbID)
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, ErrNotFound
	}
	return r, err
}

// Update is a partial change of one title; nil fields keep their stored value.
type Update struct {
	ProfileID    uint
	MediaType    string
	TmdbID       int
	Title        string // empty keeps the stored value
	PosterPath   string
	BackdropPath string
	RatingSet    bool    // rating was present in the request
	Rating       *string // nil clears the rating (only when RatingSet)
	Favorite     *bool
}

// apply merges u into row and reports whether the row should still exist.
func (u Update) apply(row *TitleRating, now time.Time) bool {
	if t := strings.TrimSpace(u.Title); t != "" {
		row.Title = t
	}
	if u.PosterPath != "" {
		row.PosterPath = u.PosterPath
	}
	if u.BackdropPath != "" {
		row.BackdropPath = u.BackdropPath
	}
	if u.RatingSet {
		if u.Rating == nil {
			row.Rating = nil
		} else {
			v := *u.Rating
			row.Rating = &v
		}
	}
	if u.Favorite != nil {
		if *u.Favorite && !row.Favorite {
			row.FavoritedAt = &now
		}
		if !*u.Favorite {
			row.FavoritedAt = nil
		}
		row.Favorite = *u.Favorite
	}
	row.UpdatedAt = now
	return row.Rating != nil || row.Favorite
}

// Put applies a partial update. A row left with no rating and no favorite is deleted;
// the returned value then shows that cleared state.
func (s *Service) Put(u Update) (*TitleRating, error) {
	ok, err := profileExists(s.db, u.ProfileID)
	if err != nil {
		return nil, err
	}
	if !ok {
		return nil, ErrUnknownProfile
	}
	var out TitleRating
	err = s.db.Transaction(func(tx *gorm.DB) error {
		row, err := findByKey(tx, u.ProfileID, u.MediaType, u.TmdbID)
		exists := err == nil
		if errors.Is(err, gorm.ErrRecordNotFound) {
			row = &TitleRating{ProfileID: u.ProfileID, MediaType: u.MediaType, TmdbID: u.TmdbID}
		} else if err != nil {
			return err
		}
		now := time.Now()
		keep := u.apply(row, now)
		out = *row
		switch {
		case keep && exists:
			return tx.Save(row).Error
		case keep:
			row.CreatedAt = now
			if err := tx.Create(row).Error; err != nil {
				return err
			}
			out = *row
			return nil
		case exists:
			return tx.Delete(&TitleRating{}, row.ID).Error
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	s.changed(u.ProfileID)
	return &out, nil
}
