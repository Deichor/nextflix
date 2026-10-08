package images

import (
	"context"
	"log"
	"sync"
	"time"

	"nextflix/internal/platform/tmdb"
)

const (
	warmDelay = 20 * time.Second
	// Few downloads at once: user requests keep most slots, and bursts of new
	// connections are what the ISP's DPI tends to reset.
	warmParallel = 3
	featuredPool = 6
)

// Sizes the home page requests (see web/src/shared/ui/TmdbImage.tsx): blur placeholder,
// srcset candidates and the hover preview.
var (
	backdropCardSizes = []string{"w92", "w300", "w500", "w780"}
	posterCardSizes   = []string{"w92", "w342", "w500"}
	billboardSizes    = []string{"w300", "w780", "w1280", "original"}
)

// Mirrors GENRE_ROWS in web/src/features/home/HomeScreen.tsx (plus the defaults of discoverMovies in feeds.ts).
var homeGenreRows = []map[string]string{
	{"with_genres": "28|12", "vote_count.gte": "300"},
	{"with_genres": "35", "vote_count.gte": "300"},
	{"with_genres": "878|14", "vote_count.gte": "300"},
	{"with_genres": "27", "vote_count.gte": "200"},
	{"sort_by": "vote_average.desc", "vote_count.gte": "5000"},
}

// artwork is one home page title; movieID is 0 for TV shows.
type artwork struct {
	movieID          int
	poster, backdrop string
	overview         string
}

func (s *Service) warmLoop(ctx context.Context) {
	select {
	case <-ctx.Done():
		return
	case <-time.After(warmDelay):
	}
	t := time.NewTicker(s.cfg.WarmEvery)
	defer t.Stop()
	for {
		s.warm(ctx)
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}

// warm pre-downloads the images of the home page rows and billboard candidates.
func (s *Service) warm(ctx context.Context) {
	if !s.tm.Enabled() {
		return
	}
	start := time.Now()
	// Turkish lists can carry Turkish posters and logos
	keys := s.homeKeys(s.tm)
	seenKey := map[key]bool{}
	for _, k := range keys {
		seenKey[k] = true
	}
	for _, k := range s.homeKeys(s.tm.WithLang(tmdb.LangTR)) {
		if !seenKey[k] {
			seenKey[k] = true
			keys = append(keys, k)
		}
	}

	var missing []key
	for _, k := range keys {
		if _, ok := s.store.stat(k); !ok && s.negative(k) == nil {
			missing = append(missing, k)
		}
	}

	var mu sync.Mutex
	fetched, failed := 0, 0
	jobs := make(chan key)
	var wg sync.WaitGroup
	for i := 0; i < warmParallel; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for k := range jobs {
				_, _, err := s.ensure(ctx, k)
				mu.Lock()
				if err != nil {
					failed++
				} else {
					fetched++
				}
				mu.Unlock()
			}
		}()
	}
send:
	for _, k := range missing {
		select {
		case jobs <- k:
		case <-ctx.Done():
			break send
		}
	}
	close(jobs)
	wg.Wait()

	log.Printf("images: warm-up fetched %d new home images (%d failed, %d of %d already cached) in %s",
		fetched, failed, len(keys)-len(missing), len(keys), time.Since(start).Round(time.Second))
}

// homeKeys lists the images of the home page in tm's language in roughly the order they appear on screen.
func (s *Service) homeKeys(tm *tmdb.Client) []key {
	var trending, trendingTV, backdropCards, posterCards []artwork

	if mv, err := tm.GetUpcomingMovies(); err == nil {
		trending = movieArt(mv.TrendingDaily)
		posterCards = movieArt(mv.Upcoming)
	} else {
		log.Printf("images: warm-up movie lists: %v", err)
	}
	if tv, err := tm.GetUpcomingTVSeries(); err == nil {
		trendingTV = tvArt(tv.TrendingDaily)
		backdropCards = append(backdropCards, tvArt(tv.TrendingWeekly)...)
		backdropCards = append(backdropCards, tvArt(tv.OnTheAir)...)
	} else {
		log.Printf("images: warm-up tv lists: %v", err)
	}
	if pop, err := tm.GetPopularMovies(1); err == nil {
		backdropCards = append(backdropCards, movieArt(pop)...)
	}
	for _, params := range homeGenreRows {
		q := map[string]string{"sort_by": "popularity.desc", "include_adult": "false"}
		for k, v := range params {
			q[k] = v
		}
		if list, err := tm.DiscoverMovies(q); err == nil {
			backdropCards = append(backdropCards, movieArt(list)...)
		}
	}

	seen := map[key]bool{}
	var keys []key
	add := func(p string, sizes []string) {
		for _, size := range sizes {
			if k, ok := keyFromTMDBPath(size, p); ok && !seen[k] {
				seen[k] = true
				keys = append(keys, k)
			}
		}
	}

	// Billboard candidates first (same pool as pickFeatured in HomeScreen.tsx), with their logos.
	featured := 0
	for _, a := range append(append([]artwork{}, trending...), trendingTV...) {
		if featured == featuredPool {
			break
		}
		if a.backdrop == "" || len(a.overview) <= 40 {
			continue
		}
		featured++
		add(a.backdrop, billboardSizes)
		if a.movieID != 0 {
			add(movieLogo(tm, a.movieID), []string{"w500"})
		}
	}

	for _, list := range [][]artwork{trending, trendingTV, backdropCards} {
		for _, a := range list {
			add(firstOf(a.backdrop, a.poster), backdropCardSizes)
		}
	}
	for _, a := range posterCards {
		add(firstOf(a.poster, a.backdrop), posterCardSizes)
	}
	return keys
}

// movieLogo picks the same logo as pickLogo in web/src/shared/lib/image.ts: the client's
// language, then English, then a language-less one.
func movieLogo(tm *tmdb.Client, id int) string {
	imgs, err := tm.GetMovieImages(id)
	if err != nil {
		return ""
	}
	for _, lang := range []string{tm.Lang(), tmdb.LangEN, ""} {
		for _, l := range imgs.Logos {
			if l.ISO6391 == lang {
				return l.FilePath
			}
		}
	}
	return ""
}

func movieArt(list []tmdb.Movie) []artwork {
	out := make([]artwork, 0, len(list))
	for _, m := range list {
		out = append(out, artwork{movieID: m.ID, poster: m.PosterPath, backdrop: m.BackdropPath, overview: m.Overview})
	}
	return out
}

func tvArt(list []tmdb.TV) []artwork {
	out := make([]artwork, 0, len(list))
	for _, t := range list {
		out = append(out, artwork{poster: t.PosterPath, backdrop: t.BackdropPath, overview: t.Overview})
	}
	return out
}

func firstOf(a, b string) string {
	if a != "" {
		return a
	}
	return b
}
