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

// ProfileChecker reports whether a profile exists (the profiles module, wired in main.go).
type ProfileChecker func(profileID uint) (bool, error)

type Service struct {
	cache    *cache.Cache
	tm       *tmdb.Client
	repo     *repository    // nil when REDIS_URL is unset: the feed is disabled
	profiles ProfileChecker // nil = every id gets its own read state

	addMu  sync.Mutex // serializes add so concurrent generators cannot store the same title twice
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

// SetProfiles makes ids of profiles that do not exist share one read state. Called once at startup.
func (s *Service) SetProfiles(check ProfileChecker) { s.profiles = check }

// owner is the redis key suffix for a profile's read state, so unknown ids cannot create keys.
func (s *Service) owner(profile uint) (string, error) {
	if profile != 0 && s.profiles != nil {
		ok, err := s.profiles(profile)
		if err != nil {
			return "", err
		}
		if !ok {
			profile = 0
		}
	}
	return stateOwner(profile), nil
}

// Start runs the background generation loop until ctx is cancelled or Close is called.
func (s *Service) Start(ctx context.Context) {
	if !s.enabled() {
		log.Printf("notifications: REDIS_URL not set, feed disabled")
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

	purged := s.repo.purgeNonTMDB(ctx)
	go s.initialBatch(ctx)
	s.loop(ctx, purged)
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
func (s *Service) loop(ctx context.Context, purged bool) {
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
	failing := false

	for n := 0; ; n++ {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
		if n%10 == 0 {
			s.cleanupUsed()
		}
		if !purged {
			purged = s.repo.purgeNonTMDB(ctx)
		}
		// Redis may be down for a while: log when generation starts and stops failing, not every tick
		err := generators[rand.Intn(len(generators))](ctx)
		switch {
		case err != nil && !failing:
			log.Printf("notifications: generation failed (will keep retrying): %v", err)
		case err == nil && failing:
			log.Printf("notifications: generation works again")
		}
		failing = err != nil
		if n%10 == 9 {
			s.repo.trim(ctx)
		}
	}
}

// Counts drives the bell badge: Unseen = newer than the profile's "seen up to" mark and not read.
type Counts struct {
	Unseen int `json:"unseen"`
	Unread int `json:"unread"`
	Total  int `json:"total"`
}

// feed returns all TMDB notifications (newest first) with Read set for the profile, plus its counts.
func (s *Service) feed(ctx context.Context, profile uint) ([]Notification, Counts, error) {
	if !s.enabled() {
		return []Notification{}, Counts{}, nil
	}
	owner, err := s.owner(profile)
	if err != nil {
		return nil, Counts{}, err
	}
	all, err := s.repo.latest(ctx, maxNotifications)
	if err != nil {
		return nil, Counts{}, fmt.Errorf("failed to fetch notifications from Redis: %w", err)
	}
	read, err := s.repo.readIDs(ctx, owner)
	if err != nil {
		return nil, Counts{}, err
	}
	seen, err := s.repo.seenAt(ctx, owner)
	if err != nil {
		return nil, Counts{}, err
	}
	out := make([]Notification, 0, len(all))
	var c Counts
	for _, n := range all {
		if !n.Type.isTMDB() {
			continue
		}
		n.Read = read[n.ID]
		if !n.Read {
			c.Unread++
			if n.Timestamp > seen {
				c.Unseen++
			}
		}
		out = append(out, n)
	}
	c.Total = len(out)
	return out, c, nil
}

// Notifications returns up to limit TMDB notifications, newest first, with the profile's read state
// and TMDB titles in lang ("en" or "tr").
func (s *Service) Notifications(ctx context.Context, profile uint, limit int, lang string) ([]Notification, Counts, error) {
	items, c, err := s.feed(ctx, profile)
	if err != nil {
		return nil, c, err
	}
	if limit > 0 && len(items) > limit {
		items = items[:limit]
	}
	return s.localize(items, lang), c, nil
}

func (s *Service) Count(ctx context.Context, profile uint) (Counts, error) {
	_, c, err := s.feed(ctx, profile)
	return c, err
}

// MarkAsRead marks one notification read; ids that are not in the feed are ignored.
func (s *Service) MarkAsRead(ctx context.Context, profile uint, id string) error {
	if !s.enabled() {
		return nil
	}
	owner, err := s.owner(profile)
	if err != nil {
		return err
	}
	all, err := s.repo.latest(ctx, maxNotifications)
	if err != nil {
		return err
	}
	for _, n := range all {
		if n.ID == id {
			return s.repo.markRead(ctx, owner, id)
		}
	}
	return nil
}

// MarkAllRead marks every notification in the feed read and seen.
func (s *Service) MarkAllRead(ctx context.Context, profile uint) error {
	if !s.enabled() {
		return nil
	}
	owner, err := s.owner(profile)
	if err != nil {
		return err
	}
	all, err := s.repo.latest(ctx, maxNotifications)
	if err != nil {
		return err
	}
	ids := make([]string, 0, len(all))
	var newest int64
	for _, n := range all {
		ids = append(ids, n.ID)
		newest = max(newest, n.Timestamp)
	}
	if err := s.repo.markRead(ctx, owner, ids...); err != nil {
		return err
	}
	return s.repo.setSeen(ctx, owner, newest)
}

// MarkSeen clears the badge up to `until` (a notification timestamp); 0 means the newest notification.
func (s *Service) MarkSeen(ctx context.Context, profile uint, until int64) error {
	if !s.enabled() {
		return nil
	}
	owner, err := s.owner(profile)
	if err != nil {
		return err
	}
	newest, err := s.repo.newest(ctx)
	if err != nil {
		return err
	}
	if until <= 0 || until > newest {
		until = newest
	}
	return s.repo.setSeen(ctx, owner, until)
}

// add stores n unless one of its titles was featured recently or it duplicates a notification from the last hour.
func (s *Service) add(ctx context.Context, n Notification) error {
	s.addMu.Lock()
	defer s.addMu.Unlock()
	if s.recentlyUsed(n.TMDBIDs) {
		return nil
	}
	recent, err := s.repo.since(ctx, time.Now().Unix()-3600)
	if err != nil {
		return fmt.Errorf("read recent notifications: %w", err)
	}
	for _, existing := range recent {
		if isDuplicate(n, existing) {
			return nil
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
