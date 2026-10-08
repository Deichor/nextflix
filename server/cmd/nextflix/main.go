package main

import (
	"context"
	"fmt"
	"log"
	"os/signal"
	"syscall"

	"nextflix/internal/modules/catalog"
	"nextflix/internal/modules/mylist"
	"nextflix/internal/modules/notifications"
	"nextflix/internal/modules/profiles"
	"nextflix/internal/modules/progress"
	"nextflix/internal/modules/providers"
	"nextflix/internal/modules/streaming"
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
	models = append(models, mylist.Models()...)
	models = append(models, streaming.Models()...)
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
	mylistSvc := mylist.NewService(db)
	profilesSvc := profiles.NewService(db, progressSvc, mylistSvc)
	if err := profilesSvc.EnsureDefault(); err != nil {
		log.Printf("seed default profile: %v", err)
	}

	catalogSvc := catalog.NewService(tm)
	providersSvc := providers.NewService(cfg.TMDBAPIKey, rc)

	notificationsSvc := notifications.NewService(rc, tm)
	go notificationsSvc.Start(ctx)
	defer notificationsSvc.Close()

	streamingSvc := streaming.NewService(db, streaming.Config{
		TorrServerURL: cfg.TorrServerURL,
		JackettURL:    cfg.JackettURL,
		JackettAPIKey: cfg.JackettAPIKey,
	})
	if err := streamingSvc.EnsureSettings(); err != nil {
		log.Printf("seed indexer settings: %v", err)
	}

	r := httpserver.New(cfg, db)
	api := r.Group("/api")
	profiles.Register(api, profilesSvc)
	progress.Register(api, progressSvc)
	mylist.Register(api, mylistSvc)
	catalog.Register(api, catalogSvc)
	providers.Register(api, providersSvc)
	notifications.Register(api, notificationsSvc)
	streaming.Register(api, streamingSvc)

	log.Printf("Nextflix server listening on :%s", cfg.Port)
	if err := httpserver.Run(ctx, r, cfg.Port); err != nil {
		return fmt.Errorf("server stopped: %w", err)
	}
	log.Println("Server stopped")
	return nil
}
