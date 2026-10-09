package segments

import (
	"context"
	"errors"
	"fmt"
	"log"
	"math"
	"sync"
	"time"

	"nextflix/internal/platform/upstream"

	"gorm.io/gorm"
)

// ChapterSource gives the chapter markers and the length of a torrent file (from its probe).
type ChapterSource func(ctx context.Context, hash string, index int) ([]Chapter, float64, error)

// IMDbLookup gives the IMDb id of a TV show.
type IMDbLookup func(tmdbID int) (string, error)

type Config struct {
	Chapters ChapterSource
	IMDb     IMDbLookup
}

const (
	foundTTL   = 30 * 24 * time.Hour
	missingTTL = 3 * 24 * time.Hour // new episodes get their timestamps in the days after airing
	failPause  = 10 * time.Minute
	limitPause = time.Hour
	// A cached lookup serves files whose length is this close (other rips of the same version)
	durationSlack = 5
)

// Service finds the intro and the end credits of an episode: chapter markers of the file first,
// then the community databases (cached per episode and file length in skip_segments).
type Service struct {
	db       *gorm.DB
	http     *upstream.Client
	chapters ChapterSource
	imdb     IMDbLookup

	theIntroDBURL, introDBURL string

	mu       sync.Mutex
	inflight map[string]*lookup
	pausedTo time.Time // no community lookups until then (failures, 429)
}

type lookup struct {
	done chan struct{}
	row  SkipSegment
}

func NewService(db *gorm.DB, cfg Config) *Service {
	return &Service{
		db: db,
		http: upstream.New(upstream.Options{
			Attempts:   2,
			HedgeAfter: 2 * time.Second,
			Budget:     8 * time.Second,
			MaxBody:    256 << 10,
			PoolSize:   4,
			UserAgent:  "Nextflix/1.0",
		}),
		chapters:      cfg.Chapters,
		imdb:          cfg.IMDb,
		theIntroDBURL: theIntroDBURL,
		introDBURL:    introDBURL,
		inflight:      map[string]*lookup{},
	}
}

// Query names the playing file and its episode; Duration is the client's length, used when
// the file cannot be probed.
type Query struct {
	Hash                    string
	Index                   int
	TmdbID, Season, Episode int
	Duration                float64
}

// Get returns the skip segments of a file. Chapters win per segment; the community data fills
// in what they lack. Every segment passes the rules of rules.go.
func (s *Service) Get(ctx context.Context, q Query) Result {
	duration := q.Duration
	var intro, credits *Segment
	if q.Hash != "" && s.chapters != nil {
		cctx, cancel := context.WithTimeout(ctx, 15*time.Second)
		chapters, d, err := s.chapters(cctx, q.Hash, q.Index)
		cancel()
		if err == nil {
			if d > 0 {
				duration = d
			}
			intro, credits = fromChapters(chapters, duration)
		}
	}
	if (intro == nil || credits == nil) && q.TmdbID > 0 && q.Season > 0 && q.Episode > 0 && duration > 0 {
		row := s.community(ctx, q.TmdbID, q.Season, q.Episode, duration)
		if intro == nil && row.IntroEnd > 0 {
			intro = validIntro(row.IntroStart, row.IntroEnd, duration, row.Source)
		}
		if credits == nil && row.CreditsStart > 0 {
			credits = validCredits(row.CreditsStart, row.CreditsEnd, duration, row.Source)
		}
	}
	return Result{Intro: intro, Credits: credits, Duration: duration}
}

// community returns the cached lookup of an episode, asking the databases when it is missing
// or stale. Concurrent callers share one lookup.
func (s *Service) community(ctx context.Context, tmdbID, season, episode int, duration float64) SkipSegment {
	if row, ok := s.cached(tmdbID, season, episode, duration); ok {
		return row
	}
	key := fmt.Sprintf("%d:%d:%d:%d", tmdbID, season, episode, int(math.Round(duration)))
	s.mu.Lock()
	if time.Now().Before(s.pausedTo) {
		s.mu.Unlock()
		return SkipSegment{}
	}
	l, running := s.inflight[key]
	if !running {
		l = &lookup{done: make(chan struct{})}
		s.inflight[key] = l
	}
	s.mu.Unlock()
	if running {
		select {
		case <-l.done:
			return l.row
		case <-ctx.Done():
			return SkipSegment{}
		}
	}

	// The lookup outlives a closed request so its answer is cached for the next one
	lctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	row, err := s.fetch(lctx, tmdbID, season, episode, duration)
	cancel()
	if err != nil {
		pause := failPause
		if errors.Is(err, errRateLimited) {
			pause = limitPause
		}
		log.Printf("skip segments: S%02dE%02d of %d: %v (lookups paused %v)", season, episode, tmdbID, err, pause)
		s.mu.Lock()
		s.pausedTo = time.Now().Add(pause)
		s.mu.Unlock()
	} else if s.db != nil {
		s.save(row)
	}
	s.mu.Lock()
	delete(s.inflight, key)
	s.mu.Unlock()
	l.row = row
	close(l.done)
	return row
}

// fetch asks TheIntroDB, then IntroDB for what it lacks. An error is returned only when
// nothing was found and a database failed (a miss is cached, a failure is not).
func (s *Service) fetch(ctx context.Context, tmdbID, season, episode int, duration float64) (SkipSegment, error) {
	row := SkipSegment{TmdbID: tmdbID, Season: season, Episode: episode, Duration: int(math.Round(duration)), FetchedAt: time.Now()}
	f, err1 := s.theIntroDB(ctx, tmdbID, season, episode, duration)
	if !f.empty() {
		row.Source = "theintrodb"
		row.IntroStart, row.IntroEnd, row.CreditsStart, row.CreditsEnd = f.introStart, f.introEnd, f.creditsStart, f.creditsEnd
	}
	if f.introEnd > 0 && f.creditsStart > 0 {
		return row, nil
	}
	g, err2 := s.introDB(ctx, tmdbID, season, episode)
	if row.IntroEnd <= 0 && g.introEnd > 0 {
		row.IntroStart, row.IntroEnd = g.introStart, g.introEnd
		row.Source = joinSource(row.Source, "introdb")
	}
	if row.CreditsStart <= 0 && g.creditsStart > 0 {
		row.CreditsStart, row.CreditsEnd = g.creditsStart, g.creditsEnd
		row.Source = joinSource(row.Source, "introdb")
	}
	if row.Source == "" {
		if err1 != nil {
			return row, fmt.Errorf("theintrodb: %w", err1)
		}
		if err2 != nil {
			return row, fmt.Errorf("introdb: %w", err2)
		}
	}
	return row, nil
}

func joinSource(a, b string) string {
	if a == "" || a == b {
		return b
	}
	return a + "+" + b
}

func (s *Service) cached(tmdbID, season, episode int, duration float64) (SkipSegment, bool) {
	if s.db == nil {
		return SkipSegment{}, false
	}
	var rows []SkipSegment
	if err := s.db.Where("tmdb_id = ? AND season = ? AND episode = ?", tmdbID, season, episode).Find(&rows).Error; err != nil {
		return SkipSegment{}, false
	}
	best, ok := SkipSegment{}, false
	for _, r := range rows {
		gap := math.Abs(float64(r.Duration) - duration)
		if gap > durationSlack {
			continue
		}
		ttl := foundTTL
		if r.Source == "" {
			ttl = missingTTL
		}
		if time.Since(r.FetchedAt) > ttl {
			continue
		}
		if !ok || gap < math.Abs(float64(best.Duration)-duration) {
			best, ok = r, true
		}
	}
	return best, ok
}

// save stores a lookup, replacing the one for the same file length.
func (s *Service) save(row SkipSegment) {
	err := s.db.Transaction(func(tx *gorm.DB) error {
		if err := tx.Where("tmdb_id = ? AND season = ? AND episode = ? AND duration = ?",
			row.TmdbID, row.Season, row.Episode, row.Duration).Delete(&SkipSegment{}).Error; err != nil {
			return err
		}
		return tx.Create(&row).Error
	})
	if err != nil {
		log.Printf("skip segments: save: %v", err)
	}
}
