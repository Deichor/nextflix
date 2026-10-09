package main

import (
	"context"
	"fmt"
	"log"
	"os"
	"os/signal"
	"syscall"
	"time"

	"nextflix/internal/modules/catalog"
	"nextflix/internal/modules/images"
	"nextflix/internal/modules/notifications"
	"nextflix/internal/modules/profiles"
	"nextflix/internal/modules/progress"
	"nextflix/internal/modules/providers"
	"nextflix/internal/modules/ratings"
	"nextflix/internal/modules/recommendations"
	"nextflix/internal/modules/streaming"
	"nextflix/internal/modules/subtitles"
	"nextflix/internal/modules/top10"
	"nextflix/internal/platform/cache"
	"nextflix/internal/platform/config"
	"nextflix/internal/platform/database"
	"nextflix/internal/platform/httpserver"
	"nextflix/internal/platform/tmdb"

	"github.com/joho/godotenv"
)

func main() {
	if err := run(); err != nil {
		log.Fatal(err)
	}
}

func run() error {
	if err := godotenv.Load(); err != nil {
		log.Println("No .env file found, using environment")
	}

	cfg := config.Load()

	db, err := database.Open(cfg.DatabaseURL)
	if err != nil {
		return fmt.Errorf("open database: %w", err)
	}

	var models []any
	models = append(models, profiles.Models()...)
	models = append(models, progress.Models()...)
	models = append(models, ratings.Models()...)
	models = append(models, top10.Models()...)
	models = append(models, streaming.Models()...)
	models = append(models, subtitles.Models()...)
	if err := database.Migrate(db, models...); err != nil {
		return fmt.Errorf("migrate database: %w", err)
	}

	rc, err := cache.New(cfg.RedisURL)
	if err != nil {
		log.Printf("Redis unavailable, running without cache: %v", err)
		rc = nil
	}
	defer rc.Close()

	tm := tmdb.New(cfg.TMDBAPIKey)

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	progressSvc := progress.NewService(db)
	ratingsSvc := ratings.NewService(db, ratings.NewTMDBLookup(tm))
	profilesSvc := profiles.NewService(db, progressSvc, ratingsSvc)
	if err := profilesSvc.EnsureDefault(); err != nil {
		log.Printf("seed default profile: %v", err)
	}
	if res, err := ratingsSvc.MigrateMyList(); err != nil {
		log.Printf("migrate My List to Favorites: %v", err)
	} else if !res.AlreadyDone {
		log.Printf("My List -> Favorites: %d rows read, %d favorites written, %d unmapped, %d orphaned",
			res.Read, res.Migrated, res.Unmapped, res.Orphaned)
	}
	go ratingsSvc.Backfill(ctx)

	recommendationsSvc := recommendations.NewService(ratingSignals(ratingsSvc), recommendations.NewTMDBCatalog(tm))
	ratingsSvc.OnChange(recommendationsSvc.Invalidate)
	recommendationsSvc.SetHistory(watchHistory(progressSvc))

	top10Svc := top10.NewService(db, top10.NewTMDBSearcher(tm), top10.Config{Localizer: top10.NewTMDBLocalizer(tm)})
	top10Svc.Start(ctx)

	catalogSvc := catalog.NewService(tm)

	imagesSvc := images.NewService(images.Config{
		Dir:       envOr("IMAGE_CACHE_DIR", "/app/data/img-cache"),
		WarmEvery: 6 * time.Hour,
	}, tm)
	imagesSvc.Start(ctx)

	providersSvc := providers.NewService(tm)

	notificationsSvc := notifications.NewService(rc, tm)
	go notificationsSvc.Start(ctx)
	defer notificationsSvc.Close()

	streamingSvc := streaming.NewService(db, streaming.Config{
		TorrServerURL:   cfg.TorrServerURL,
		JackettURL:      cfg.JackettURL,
		JackettAPIKey:   cfg.JackettAPIKey,
		TMDB:            tm,
		Availability:    catalogAvailability{catalogSvc},
		BitsearchURL:    envOr("BITSEARCH_URL", "https://bitsearch.to"),
		FlareSolverrURL: envOr("FLARESOLVERR_URL", "http://flaresolverr:8191/v1"),
	})
	if err := streamingSvc.EnsureSettings(); err != nil {
		log.Printf("seed indexer settings: %v", err)
	}
	streamingSvc.StartInstant(ctx, instantFeed(top10Svc, tm))

	subtitlesSvc := subtitles.NewService(db, subtitles.Config{
		TorrServerURL:   cfg.TorrServerURL,
		CacheDir:        envOr("SUBTITLE_CACHE_DIR", "/app/data/subtitles"),
		FlareSolverrURL: envOr("FLARESOLVERR_URL", "http://flaresolverr:8191/v1"),
		IMDb:            imdbLookup(tm),
		Debrid:          debridSource(streamingSvc),
	})
	if err := subtitlesSvc.EnsureSettings(); err != nil {
		log.Printf("seed subtitle settings: %v", err)
	}

	r := httpserver.New(cfg, db)
	httpserver.ServeTVApp(r, os.Getenv("TV_APP_DIR"))
	images.Register(r, imagesSvc)
	api := r.Group("/api")
	profiles.Register(api, profilesSvc)
	progress.Register(api, progressSvc)
	ratings.Register(api, ratingsSvc)
	recommendations.Register(api, recommendationsSvc)
	top10.Register(api, top10Svc)
	catalog.Register(api, catalogSvc)
	providers.Register(api, providersSvc)
	notifications.Register(api, notificationsSvc)
	streaming.Register(api, streamingSvc)
	subtitles.Register(api, subtitlesSvc)

	log.Printf("Nextflix server listening on :%s", cfg.Port)
	if err := httpserver.Run(ctx, r, cfg.Port); err != nil {
		return fmt.Errorf("server stopped: %w", err)
	}
	log.Println("Server stopped")
	return nil
}

// watchHistory feeds the titles a profile has played into the surprise picks.
func watchHistory(svc *progress.Service) recommendations.HistoryFunc {
	return func(profileID uint) ([]recommendations.Watched, error) {
		rows, err := svc.History(profileID)
		if err != nil {
			return nil, err
		}
		out := make([]recommendations.Watched, 0, len(rows))
		for _, r := range rows {
			out = append(out, recommendations.Watched{MediaType: r.MediaType, TmdbID: r.TmdbID, Title: r.Title,
				Finished: r.Finished, UpdatedAt: r.UpdatedAt})
		}
		return out, nil
	}
}

// ratingSignals feeds a profile's ratings and favorites into the recommendations module.
func ratingSignals(svc *ratings.Service) recommendations.RatingsFunc {
	return func(profileID uint) ([]recommendations.Signal, error) {
		rows, err := svc.List(profileID, ratings.Filter{})
		if err != nil {
			return nil, err
		}
		out := make([]recommendations.Signal, 0, len(rows))
		for _, r := range rows {
			s := recommendations.Signal{MediaType: r.MediaType, TmdbID: r.TmdbID, Title: r.Title,
				Favorite: r.Favorite, UpdatedAt: r.UpdatedAt}
			if r.Rating != nil {
				s.Rating = *r.Rating
			}
			out = append(out, s)
		}
		return out, nil
	}
}

// imdbLookup resolves IMDb ids for the subtitles module (turkcealtyazi.org searches by them).
func imdbLookup(tm *tmdb.Client) subtitles.IMDbLookup {
	return func(mediaType string, tmdbID int) (string, error) {
		ids, err := tm.GetExternalIDs(mediaType, tmdbID)
		if err != nil {
			return "", err
		}
		return ids.IMDBID, nil
	}
}

// catalogAvailability gives streaming the release status of a title from the catalog module.
type catalogAvailability struct{ c *catalog.Service }

func (a catalogAvailability) Availability(mediaType string, tmdbID, season, episode int) streaming.TitleAvailability {
	v := a.c.Availability(mediaType, tmdbID, season, episode)
	deref := func(s *string) string {
		if s == nil {
			return ""
		}
		return *s
	}
	return streaming.TitleAvailability{Status: v.Status, ReleaseDate: deref(v.ReleaseDate), DigitalDate: deref(v.DigitalDate)}
}

// instantFeed lists the titles the instant-play hints check before anyone asks: the Top 10
// (Turkey and global) and TMDB's trending movies and shows of the week.
func instantFeed(t *top10.Service, tm *tmdb.Client) streaming.InstantFeed {
	return func() []streaming.InstantItem {
		var out []streaming.InstantItem
		seen := map[string]bool{}
		add := func(typ string, id int, title, date string) {
			k := fmt.Sprintf("%s:%d", typ, id)
			if id <= 0 || seen[k] {
				return
			}
			seen[k] = true
			year := 0
			if len(date) >= 4 {
				fmt.Sscanf(date[:4], "%d", &year)
			}
			out = append(out, streaming.InstantItem{Type: typ, TmdbID: id, Title: title, Year: year})
		}
		for _, region := range []string{"TR", top10.Global} {
			for _, typ := range []string{"movie", "tv"} {
				if l, ok := t.Get(region, typ); ok {
					for _, it := range l.Items {
						add(typ, it.TmdbID, it.Title, it.ReleaseDate+it.FirstAirDate)
					}
				}
			}
		}
		if m, err := tm.GetUpcomingMovies(); err == nil && m != nil {
			for i, it := range m.TrendingWeekly {
				if i < 10 {
					add("movie", it.ID, it.Title, it.ReleaseDate)
				}
			}
		}
		if tv, err := tm.GetUpcomingTVSeries(); err == nil && tv != nil {
			for i, it := range tv.TrendingWeekly {
				if i < 10 {
					add("tv", it.ID, it.Name, it.FirstAirDate)
				}
			}
		}
		return out
	}
}

func envOr(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

// debridSource lets the subtitle reads follow files that play from TorBox.
func debridSource(s *streaming.Service) *subtitles.DebridSource {
	return &subtitles.DebridSource{
		FileURL: s.DebridFileURL,
		Files: func(hash string) ([]subtitles.DebridFile, bool) {
			files, ok := s.DebridFiles(hash)
			out := make([]subtitles.DebridFile, 0, len(files))
			for _, f := range files {
				out = append(out, subtitles.DebridFile{ID: f.ID, Path: f.Path, Length: f.Length})
			}
			return out, ok
		},
	}
}
