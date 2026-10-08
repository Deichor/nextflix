package httpserver

import (
	"context"
	"fmt"
	"log"
	"net/http"
	"strings"
	"time"

	"github.com/gin-contrib/cors"
	"github.com/gin-gonic/gin"
)

const requestTimeout = 30 * time.Second

// errorLogger logs only failed requests to keep the log quiet.
func errorLogger() gin.HandlerFunc {
	return gin.LoggerWithFormatter(func(p gin.LogFormatterParams) string {
		if p.StatusCode < 400 {
			return ""
		}
		return fmt.Sprintf("%s - %s %s %d %s\n",
			p.TimeStamp.Format("15:04:05"), p.Method, p.Path, p.StatusCode, p.Latency)
	})
}

// recovery turns panics into 500s; client disconnects (broken pipe) are ignored silently.
func recovery() gin.HandlerFunc {
	return func(c *gin.Context) {
		defer func() {
			err := recover()
			if err == nil {
				return
			}
			if err == http.ErrAbortHandler {
				panic(err)
			}
			msg := fmt.Sprint(err)
			if strings.Contains(msg, "broken pipe") || strings.Contains(msg, "connection reset by peer") {
				c.Abort()
				return
			}
			log.Printf("Panic recovered: %v", err)
			c.AbortWithStatus(http.StatusInternalServerError)
		}()
		c.Next()
	}
}

// requestDeadline bounds request contexts. Torrent streams run for a whole movie
// and manage their own deadlines, so they are skipped.
func requestDeadline() gin.HandlerFunc {
	return func(c *gin.Context) {
		if strings.HasPrefix(c.Request.URL.Path, "/api/torrent-stream/") {
			c.Next()
			return
		}
		ctx, cancel := context.WithTimeout(c.Request.Context(), requestTimeout)
		defer cancel()
		c.Request = c.Request.WithContext(ctx)
		c.Next()
	}
}

func corsMiddleware(allowed []string) gin.HandlerFunc {
	return cors.New(cors.Config{
		AllowOriginFunc: func(origin string) bool { return isAllowedOrigin(origin, allowed) },
		AllowMethods:    []string{"GET", "POST", "PUT", "DELETE", "OPTIONS", "HEAD"},
		AllowHeaders: []string{"Origin", "Content-Type", "Accept", "Authorization", "X-User-ID", "x-user-id",
			"X-Session-ID", "x-session-id", "Range", "Cache-Control", "Pragma", "Expires"},
		ExposeHeaders:    []string{"Content-Length", "Content-Range", "Accept-Ranges", "Content-Type", "X-Cache", "Connection"},
		AllowCredentials: false,
		MaxAge:           12 * time.Hour,
	})
}
