package catalog

import (
	"fmt"
	"sync"
	"time"

	"nextflix/internal/platform/tmdb"
)

const seasonCacheTTL = 6 * time.Hour

type seasonEntry struct {
	season  *tmdb.Season
	expires time.Time
}

type Service struct {
	tm *tmdb.Client

	seasonMu sync.Mutex
	seasons  map[string]seasonEntry

	releaseDatesCache releaseDatesCache

	searchMu sync.Mutex
	searches map[string]searchEntry
}

func NewService(tm *tmdb.Client) *Service {
	return &Service{tm: tm, seasons: map[string]seasonEntry{}, searches: map[string]searchEntry{}}
}

// Season returns a TMDB season with its episodes in lang ("en" or "tr"), cached for 6 hours.
func (s *Service) Season(lang string, tvID, number int) (*tmdb.Season, error) {
	tm := s.tm.WithLang(lang)
	key := fmt.Sprintf("%s:%d:%d", tm.Lang(), tvID, number)
	now := time.Now()

	s.seasonMu.Lock()
	entry, ok := s.seasons[key]
	s.seasonMu.Unlock()
	if ok && now.Before(entry.expires) {
		return entry.season, nil
	}

	season, err := tm.GetSeasonDetails(tvID, number)
	if err != nil {
		return nil, err
	}

	s.seasonMu.Lock()
	for k, e := range s.seasons {
		if now.After(e.expires) {
			delete(s.seasons, k)
		}
	}
	s.seasons[key] = seasonEntry{season: season, expires: now.Add(seasonCacheTTL)}
	s.seasonMu.Unlock()
	return season, nil
}
