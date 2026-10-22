package progress

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"gorm.io/gorm"
)

// NextEpisodeFunc gives the episode after season/episode of a show when it is out.
type NextEpisodeFunc func(tvID, season, episode int) (int, int, bool)

// UpcomingFunc gives the episode after season/episode that is not out yet, with its air date.
type UpcomingFunc func(tvID, season, episode int) (int, int, string, bool)

type Service struct {
	db       *gorm.DB
	next     NextEpisodeFunc
	upcoming UpcomingFunc
}

func NewService(db *gorm.DB) *Service {
	return &Service{db: db}
}

// SetNextEpisode lets Continue Watching offer the next episode of a finished one.
func (s *Service) SetNextEpisode(f NextEpisodeFunc) { s.next = f }

// SetUpcoming lets Continue Watching keep a show whose next episode airs soon (with its date).
func (s *Service) SetUpcoming(f UpcomingFunc) { s.upcoming = f }

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
	now := time.Now()
	row.ID = 0
	row.Hash = strings.ToLower(strings.TrimSpace(row.Hash))
	row.CreatedAt, row.UpdatedAt = now, now
	// Check and write in one (immediate) transaction so a concurrent profile delete cannot leave orphans
	err := s.db.Transaction(func(tx *gorm.DB) error {
		ok, err := profileExists(tx, row.ProfileID)
		if err != nil {
			return err
		}
		if !ok {
			return ErrUnknownProfile
		}
		return upsert(tx, &row)
	})
	if err != nil {
		return nil, err
	}
	return findByKey(s.db, row.ProfileID, row.MediaType, row.TmdbID, row.Season, row.Episode)
}

// ContinueWatching returns the newest unfinished entry per title, newest first. A show whose
// newest episode is finished comes with the next unfinished episode instead (its saved
// position, if any). With upcoming, a show whose next episode is not out yet stays with that
// episode and its NextAirDate. The next-episode lookups run in parallel, capped and bounded by a deadline.
func (s *Service) ContinueWatching(ctx context.Context, profileID uint, upcoming bool) ([]StreamProgress, error) {
	rows, err := listByProfile(s.db, profileID, 1000)
	if err != nil {
		return nil, err
	}
	saved := make(map[string]StreamProgress, len(rows))
	for _, r := range rows {
		key := episodeKey(r.MediaType, r.TmdbID, r.Season, r.Episode)
		if _, ok := saved[key]; !ok {
			saved[key] = r
		}
	}
	// The newest entry decides for each title; a finished movie (or last episode) hides it
	seen := make(map[string]bool)
	cands := make([]StreamProgress, 0, continueWatchingLimit)
	var lookups []int // indexes into cands of finished episodes that need the next one
	direct := 0
	for _, r := range rows {
		key := fmt.Sprintf("%s:%d", r.MediaType, r.TmdbID)
		if seen[key] {
			continue
		}
		seen[key] = true
		if r.finished() {
			if r.MediaType != "tv" || s.next == nil || len(lookups) >= maxNextLookups {
				continue
			}
			lookups = append(lookups, len(cands))
		} else {
			direct++
		}
		cands = append(cands, r)
		if direct >= continueWatchingLimit {
			break
		}
	}

	resolved := s.resolveNext(ctx, cands, lookups, saved, upcoming)
	out := make([]StreamProgress, 0, continueWatchingLimit)
	for i, r := range cands {
		if r.finished() {
			n, ok := resolved[i]
			if !ok {
				continue
			}
			r = n
		}
		out = append(out, r)
		if len(out) >= continueWatchingLimit {
			break
		}
	}
	return out, nil
}

// resolveNext finds the next unfinished episode for each finished candidate. Lookups that are
// not done by the deadline (or when ctx ends) drop their title from this answer.
func (s *Service) resolveNext(ctx context.Context, cands []StreamProgress, idx []int, saved map[string]StreamProgress, upcoming bool) map[int]StreamProgress {
	out := make(map[int]StreamProgress, len(idx))
	if len(idx) == 0 {
		return out
	}
	ctx, cancel := context.WithTimeout(ctx, nextLookupTimeout)
	defer cancel()
	type result struct {
		i  int
		p  StreamProgress
		ok bool
	}
	results := make(chan result, len(idx)) // buffered: late workers never block
	sem := make(chan struct{}, nextLookupWorkers)
	for _, i := range idx {
		go func(i int) {
			select {
			case sem <- struct{}{}:
			case <-ctx.Done():
				results <- result{i: i}
				return
			}
			defer func() { <-sem }()
			p, ok := s.stepNext(ctx, cands[i], saved, upcoming)
			results <- result{i, p, ok}
		}(i)
	}
	for range idx {
		select {
		case r := <-results:
			if r.ok {
				out[r.i] = r.p
			}
		case <-ctx.Done():
			return out
		}
	}
	return out
}

// stepNext walks forward from a finished episode past episodes that are finished too (at most
// maxNextSteps), returning the saved row of the first unfinished one or a fresh row at 0. With
// upcoming, an episode that is not out yet ends the walk as a fresh row with its NextAirDate.
func (s *Service) stepNext(ctx context.Context, r StreamProgress, saved map[string]StreamProgress, upcoming bool) (StreamProgress, bool) {
	season, episode := r.Season, r.Episode
	for step := 0; step < maxNextSteps && ctx.Err() == nil; step++ {
		ns, ne, ok := s.next(r.TmdbID, season, episode)
		fresh := StreamProgress{ProfileID: r.ProfileID, MediaType: "tv", TmdbID: r.TmdbID, Season: ns, Episode: ne,
			Title: r.Title, PosterPath: r.PosterPath, BackdropPath: r.BackdropPath, UpdatedAt: r.UpdatedAt}
		if !ok {
			if !upcoming || s.upcoming == nil {
				return StreamProgress{}, false
			}
			if fresh.Season, fresh.Episode, fresh.NextAirDate, ok = s.upcoming(r.TmdbID, season, episode); !ok {
				return StreamProgress{}, false
			}
			return fresh, true
		}
		if ns == season && ne == episode {
			return StreamProgress{}, false
		}
		p, ok := saved[episodeKey("tv", r.TmdbID, ns, ne)]
		if !ok {
			return fresh, true
		}
		if !p.finished() {
			p.UpdatedAt = r.UpdatedAt
			return p, true
		}
		season, episode = ns, ne
	}
	return StreamProgress{}, false
}

func episodeKey(mediaType string, tmdbID, season, episode int) string {
	return fmt.Sprintf("%s:%d:%d:%d", mediaType, tmdbID, season, episode)
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
