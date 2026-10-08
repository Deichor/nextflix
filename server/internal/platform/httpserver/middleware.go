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

var (
	corsMethods = []string{"GET", "POST", "PUT", "DELETE", "OPTIONS", "HEAD"}
	corsHeaders = []string{"Origin", "Content-Type", "Accept", "Authorization", "X-User-ID", "x-user-id",
		"X-Session-ID", "x-session-id", "X-Lang", "Range", "Cache-Control", "Pragma", "Expires"}
	corsExpose = []string{"Content-Length", "Content-Range", "Accept-Ranges", "Content-Type", "X-Cache", "Connection"}
)

func corsMiddleware(allowed []string) gin.HandlerFunc {
	lan := cors.New(cors.Config{
		AllowOriginFunc:  func(origin string) bool { return isAllowedOrigin(origin, allowed) },
		AllowMethods:     corsMethods,
		AllowHeaders:     corsHeaders,
		ExposeHeaders:    corsExpose,
		AllowCredentials: false,
		MaxAge:           12 * time.Hour,
	})
	return func(c *gin.Context) {
		if c.GetHeader("Origin") == "null" && isWebOSApp(c.Request) {
			allowWebOSApp(c)
			return
		}
		lan(c)
	}
}

// isWebOSApp: the packaged LG TV app runs from file:// and sends "Origin: null". Only the TV's
// own browser engine (Web0S user agent) gets that origin, so a sandboxed iframe on some website
// opened in a normal browser still cannot read this API.
func isWebOSApp(r *http.Request) bool {
	ua := r.UserAgent()
	return strings.Contains(ua, "Web0S") || strings.Contains(ua, "webOS")
}

func allowWebOSApp(c *gin.Context) {
	h := c.Writer.Header()
	h.Set("Access-Control-Allow-Origin", "null")
	h.Add("Vary", "Origin")
	if c.Request.Method == http.MethodOptions {
		h.Set("Access-Control-Allow-Methods", strings.Join(corsMethods, ","))
		h.Set("Access-Control-Allow-Headers", strings.Join(corsHeaders, ","))
		h.Set("Access-Control-Max-Age", "43200")
		c.AbortWithStatus(http.StatusNoContent)
		return
	}
	h.Set("Access-Control-Expose-Headers", strings.Join(corsExpose, ","))
	c.Next()
}
