package recommendations

import (
	"errors"
	"log"
	"net/http"
	"strconv"
	"strings"

	"github.com/gin-gonic/gin"

	"nextflix/internal/platform/httpserver"
)

type handler struct{ s *Service }

// profileOf reads profile_id, falling back to X-User-ID (0 when missing or invalid).
func profileOf(c *gin.Context) uint {
	raw := strings.TrimSpace(c.Query("profile_id"))
	if raw == "" {
		raw = strings.TrimSpace(c.GetHeader("X-User-ID"))
	}
	id, err := strconv.ParseUint(raw, 10, 64)
	if err != nil {
		return 0
	}
	return uint(id)
}

func (h handler) get(c *gin.Context) {
	id := profileOf(c)
	if id == 0 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "profile_id is required"})
		return
	}
	resp, err := h.s.For(id, httpserver.Lang(c))
	if errors.Is(err, ErrUnknownProfile) {
		c.JSON(http.StatusNotFound, gin.H{"error": "Unknown profile"})
		return
	}
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to load recommendations"})
		return
	}
	c.JSON(http.StatusOK, resp)
}

// GET /api/recommendations/surprise?profile_id=&exclude=movie-1,tv-2
func (h handler) surprise(c *gin.Context) {
	id := profileOf(c)
	if id == 0 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "profile_id is required"})
		return
	}
	resp, err := h.s.Surprise(id, httpserver.Lang(c), ParseExclude(c.Query("exclude")))
	switch {
	case errors.Is(err, ErrUnknownProfile):
		c.JSON(http.StatusNotFound, gin.H{"error": "Unknown profile"})
	case errors.Is(err, ErrNoSurprise):
		c.JSON(http.StatusNotFound, gin.H{"error": "Nothing to suggest right now"})
	case err != nil:
		log.Printf("recommendations: surprise for profile %d: %v", id, err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to pick a title"})
	default:
		c.Header("Cache-Control", "no-store")
		c.JSON(http.StatusOK, resp)
	}
}
