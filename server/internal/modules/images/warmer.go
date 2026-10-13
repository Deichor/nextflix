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
	// TV apps: the hero of a focused card and the backdrop cards of Coming Soon
	heroSizes               = []string{"w780"}
	comingSoonBackdropSizes = []string{"w500", "w780"}
)

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

// homeKeys lists the images of the home page in tm's language in roughly the order they appear on
// screen: billboard, trending, Top 10, cards, coming soon, genre rows.
func (s *Service) homeKeys(tm *tmdb.Client) []key {
	var trending, trendingTV, top10, backdropCards, comingSoon, genres []artwork

	if mv, err := tm.GetUpcomingMovies(); err == nil {
		trending = movieArt(mv.TrendingDaily)
		comingSoon = movieArt(mv.Upcoming)
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
	if s.cfg.Top10 != nil {
		for _, a := range s.cfg.Top10(tm.Lang()) {
			top10 = append(top10, artwork{poster: a.Poster, backdrop: a.Backdrop})
		}
	}
	if pop, err := tm.GetPopularMovies(1); err == nil {
		backdropCards = append(backdropCards, movieArt(pop)...)
	}
	for _, row := range tmdb.HomeGenreRows {
		if list, err := tm.DiscoverMovies(row.Query()); err == nil {
			genres = append(genres, movieArt(list)...)
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

	addCards := func(list []artwork) {
		for _, a := range list {
			add(firstOf(a.backdrop, a.poster), backdropCardSizes)
		}
	}
	addCards(trending)
	addCards(trendingTV)
	// Ranked rows show posters; the TV app's hero shows the backdrop of the focused title
	for _, a := range top10 {
		add(firstOf(a.poster, a.backdrop), posterCardSizes)
		add(a.backdrop, heroSizes)
	}
	addCards(backdropCards)
	// The TV apps' Coming Soon cards use the backdrop (w500) and their hero w780
	for _, a := range comingSoon {
		add(firstOf(a.poster, a.backdrop), posterCardSizes)
		add(a.backdrop, comingSoonBackdropSizes)
	}
	addCards(genres)
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
