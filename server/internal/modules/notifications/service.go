package notifications

import (
	"context"
	"fmt"
	"log"
	"math/rand"
	"sync"
	"time"

	"nextflix/internal/platform/cache"
	"nextflix/internal/platform/tmdb"
)

const (
	generateEvery = 20 * time.Second
	reuseCooldown = 2 * time.Hour // a title is not featured again within this window
	usedMaxAge    = 6 * time.Hour
)

type Service struct {
	cache *cache.Cache
	tm    *tmdb.Client
	repo  *repository // nil when redis is unavailable: the feed is disabled

	usedMu sync.Mutex
	used   map[int]time.Time // TMDB id -> last time it was featured

	stop     chan struct{}
	stopOnce sync.Once
}

func NewService(c *cache.Cache, tm *tmdb.Client) *Service {
	s := &Service{cache: c, tm: tm, used: map[int]time.Time{}, stop: make(chan struct{})}
	if rdb := c.Redis(); rdb != nil {
		s.repo = &repository{rdb: rdb}
	}
	return s
}

func (s *Service) enabled() bool { return s.repo != nil }

// Start runs the background generation loop until ctx is cancelled or Close is called.
func (s *Service) Start(ctx context.Context) {
	if !s.enabled() {
		log.Printf("notifications: redis unavailable, feed disabled")
		return
	}
	if !s.tm.Enabled() {
		log.Printf("notifications: TMDB_API_KEY not set, no notifications will be generated")
		return
	}

	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	go func() {
		select {
		case <-s.stop:
			cancel()
		case <-ctx.Done():
		}
	}()

	s.repo.purgeNonTMDB(ctx)
	go s.initialBatch(ctx)
	s.loop(ctx)
}

func (s *Service) Close() {
	s.stopOnce.Do(func() { close(s.stop) })
}

// initialBatch fills an empty-ish feed right after startup.
func (s *Service) initialBatch(ctx context.Context) {
	select {
	case <-ctx.Done():
		return
	case <-time.After(2 * time.Second):
	}
	for _, gen := range []func(context.Context) error{
		s.comingSoon,
		s.trendingMovies,
		s.upcomingMovies,
		s.nowPlaying,
		s.upcomingTV,
		s.nowAiringTV,
	} {
		if ctx.Err() != nil {
			return
		}
		if err := gen(ctx); err != nil {
			log.Printf("notifications: initial batch: %v", err)
		}
	}
}

// loop creates one notification from a random TMDB generator every 20 seconds.
func (s *Service) loop(ctx context.Context) {
	generators := []func(context.Context) error{
		s.comingSoon,
		s.trendingMovies,
		s.singleMovie,
		s.nowPlaying,
		s.upcomingTV,
		s.nowAiringTV,
		s.singleTV,
		s.upcomingMovies,
	}
	ticker := time.NewTicker(generateEvery)
	defer ticker.Stop()

	for n := 0; ; n++ {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
		if n%10 == 0 {
			s.cleanupUsed()
		}
		if err := generators[rand.Intn(len(generators))](ctx); err != nil {
			log.Printf("notifications: generation failed: %v", err)
		}
		if n%10 == 9 {
			s.repo.trim(ctx)
		}
	}
}

// Notifications returns up to limit TMDB notifications, newest first.
func (s *Service) Notifications(ctx context.Context, limit int) ([]Notification, error) {
	if !s.enabled() {
		return []Notification{}, nil
	}
	if limit <= 0 || limit > maxNotifications {
		limit = maxNotifications
	}
	all, err := s.repo.latest(ctx, maxNotifications)
	if err != nil {
		return nil, fmt.Errorf("failed to fetch notifications from Redis: %w", err)
	}
	out := make([]Notification, 0, limit)
	for _, n := range all {
		if len(out) == limit {
			break
		}
		if n.Type.isTMDB() {
			out = append(out, n)
		}
	}
	return out, nil
}

func (s *Service) Count(ctx context.Context) (int64, error) {
	if !s.enabled() {
		return 0, nil
	}
	return s.repo.count(ctx)
}

// MarkAsRead is accepted for API compatibility; read state is not stored.
func (s *Service) MarkAsRead(id string) error {
	return nil
}

// add stores n unless one of its titles was featured recently or it duplicates a notification from the last hour.
func (s *Service) add(ctx context.Context, n Notification) error {
	if s.recentlyUsed(n.TMDBIDs) {
		return nil
	}
	recent, err := s.repo.since(ctx, time.Now().Unix()-3600)
	if err == nil {
		for _, existing := range recent {
			if isDuplicate(n, existing) {
				return nil
			}
		}
	}
	if err := s.repo.add(ctx, n); err != nil {
		return err
	}
	s.markUsed(n.TMDBIDs)
	return nil
}

func isDuplicate(n, existing Notification) bool {
	if n.Type != existing.Type {
		return false
	}
	for _, a := range n.TMDBIDs {
		for _, b := range existing.TMDBIDs {
			if a == b {
				return true
			}
		}
	}
	return false
}

func (s *Service) recentlyUsed(ids []int) bool {
	s.usedMu.Lock()
	defer s.usedMu.Unlock()
	for _, id := range ids {
		if t, ok := s.used[id]; ok && time.Since(t) < reuseCooldown {
			return true
		}
	}
	return false
}

// recentIDs returns the TMDB ids featured within the cooldown window.
func (s *Service) recentIDs() map[int]bool {
	s.usedMu.Lock()
	defer s.usedMu.Unlock()
	out := map[int]bool{}
	for id, t := range s.used {
		if time.Since(t) < reuseCooldown {
			out[id] = true
		}
	}
	return out
}

func (s *Service) markUsed(ids []int) {
	s.usedMu.Lock()
	defer s.usedMu.Unlock()
	now := time.Now()
	for _, id := range ids {
		s.used[id] = now
	}
}

func (s *Service) cleanupUsed() {
	s.usedMu.Lock()
	defer s.usedMu.Unlock()
	for id, t := range s.used {
		if time.Since(t) > usedMaxAge {
			delete(s.used, id)
		}
	}
}
