package httpserver

import (
	"context"
	"errors"
	"log"
	"net/http"
	"time"

	"nextflix/internal/platform/config"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

const shutdownGrace = 10 * time.Second

// New builds the gin engine with logging, recovery, Host / cross-site / body size guards,
// request deadlines, CORS, gzip for JSON answers and /api/health.
func New(cfg config.Config, db *gorm.DB) *gin.Engine {
	gin.SetMode(gin.ReleaseMode)
	r := gin.New()
	r.Use(errorLogger(), recovery(),
		hostGuard(cfg.AllowedOrigins, cfg.AllowedHosts),
		fetchSiteGuard(cfg.AllowedOrigins, cfg.TVAppKey),
		bodyLimit(maxBodyBytes),
		requestDeadline(),
		corsMiddleware(cfg.AllowedOrigins, cfg.TVAppKey),
		gzipJSON())
	r.GET("/api/health", healthHandler(db))
	return r
}

// Run serves until ctx is cancelled, then shuts down gracefully.
// Connections still open after the grace period (e.g. running streams) are closed.
func Run(ctx context.Context, e *gin.Engine, port string) error {
	srv := &http.Server{
		Addr:           "0.0.0.0:" + port,
		Handler:        e,
		ReadTimeout:    30 * time.Second,
		WriteTimeout:   5 * time.Minute, // streaming handlers lift this per request
		IdleTimeout:    120 * time.Second,
		MaxHeaderBytes: 1 << 20,
	}

	errCh := make(chan error, 1)
	go func() { errCh <- srv.ListenAndServe() }()

	select {
	case err := <-errCh:
		return err
	case <-ctx.Done():
	}

	log.Println("Shutting down server...")
	shutdownCtx, cancel := context.WithTimeout(context.Background(), shutdownGrace)
	defer cancel()
	if err := srv.Shutdown(shutdownCtx); err != nil {
		srv.Close()
		if !errors.Is(err, context.DeadlineExceeded) {
			return err
		}
	}
	if err := <-errCh; err != nil && !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	return nil
}
