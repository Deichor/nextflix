package progress

import (
	"errors"
	"fmt"
	"strings"
	"time"

	"gorm.io/gorm"
)

type Service struct {
	db *gorm.DB
}

func NewService(db *gorm.DB) *Service {
	return &Service{db: db}
}

var (
	ErrNotFound       = errors.New("No progress found")
	ErrUnknownProfile = errors.New("Unknown profile")
)

// DeleteByProfile removes all rows of a profile inside tx (used by profile deletion).
func (s *Service) DeleteByProfile(tx *gorm.DB, id uint) error {
	return tx.Where("profile_id = ?", id).Delete(&StreamProgress{}).Error
}

// Save upserts a resume position keyed by (profile, media_type, tmdb_id, season, episode).
// The handler validates the input first.
func (s *Service) Save(row StreamProgress) (*StreamProgress, error) {
	ok, err := profileExists(s.db, row.ProfileID)
	if err != nil {
		return nil, err
	}
	if !ok {
		return nil, ErrUnknownProfile
	}
	now := time.Now()
	row.ID = 0
	row.Hash = strings.ToLower(strings.TrimSpace(row.Hash))
	row.CreatedAt, row.UpdatedAt = now, now
	if err := upsert(s.db, &row); err != nil {
		return nil, err
	}
	return findByKey(s.db, row.ProfileID, row.MediaType, row.TmdbID, row.Season, row.Episode)
}

// ContinueWatching returns the newest unfinished entry per title, newest first.
func (s *Service) ContinueWatching(profileID uint) ([]StreamProgress, error) {
	rows, err := listByProfile(s.db, profileID, 1000)
	if err != nil {
		return nil, err
	}
	// The newest entry decides for each title; a finished one hides the title
	seen := make(map[string]bool)
	out := make([]StreamProgress, 0, continueWatchingLimit)
	for _, r := range rows {
		key := fmt.Sprintf("%s:%d", r.MediaType, r.TmdbID)
		if seen[key] {
			continue
		}
		seen[key] = true
		if r.finished() {
			continue
		}
		out = append(out, r)
		if len(out) >= continueWatchingLimit {
			break
		}
	}
	return out, nil
}

func (s *Service) Item(profileID uint, mediaType string, tmdbID, season, episode int) (*StreamProgress, error) {
	if mediaType == "movie" {
		season, episode = 0, 0
	}
	p, err := findByKey(s.db, profileID, mediaType, tmdbID, season, episode)
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, ErrNotFound
	}
	return p, err
}

// Title returns every entry of one title (all episodes, finished ones included), newest first.
func (s *Service) Title(profileID uint, mediaType string, tmdbID int) ([]StreamProgress, error) {
	return listByTitle(s.db, profileID, mediaType, tmdbID, 500)
}

// DeleteTitle removes all saved positions of a title (every episode for TV).
func (s *Service) DeleteTitle(profileID uint, mediaType string, tmdbID int) error {
	return deleteTitle(s.db, profileID, mediaType, tmdbID)
}
