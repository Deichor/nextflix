package httpserver

import (
	"context"
	"net/http"
	"time"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

type healthResponse struct {
	Status    string            `json:"status"`
	Timestamp time.Time         `json:"timestamp"`
	Version   string            `json:"version"`
	Services  map[string]string `json:"services"`
	Uptime    string            `json:"uptime"`
}

var startTime = time.Now()

// healthHandler reports "healthy", or "degraded" with 503 when the database is unreachable.
// services.internet says whether the server reaches the internet (players show it on errors).
func healthHandler(db *gorm.DB, online func(context.Context) bool) gin.HandlerFunc {
	return func(c *gin.Context) {
		dbStatus := "not_configured"
		if db != nil {
			dbStatus = "healthy"
			if sqlDB, err := db.DB(); err != nil || sqlDB.PingContext(c.Request.Context()) != nil {
				dbStatus = "unhealthy"
			}
		}

		internet := "online"
		if online != nil && !online(c.Request.Context()) {
			internet = "offline"
		}

		status, code := "healthy", http.StatusOK
		if dbStatus == "unhealthy" {
			status, code = "degraded", http.StatusServiceUnavailable
		}

		c.JSON(code, healthResponse{
			Status:    status,
			Timestamp: time.Now(),
			Version:   "3.0.0",
			Services:  map[string]string{"database": dbStatus, "internet": internet},
			Uptime:    time.Since(startTime).String(),
		})
	}
}
