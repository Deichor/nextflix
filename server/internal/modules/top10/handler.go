package top10

import (
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"

	"nextflix/internal/platform/httpserver"
)

type handler struct{ s *Service }

func (h handler) get(c *gin.Context) {
	region, ok := h.s.Region(c.DefaultQuery("region", Global))
	if !ok {
		c.JSON(http.StatusBadRequest, gin.H{"error": "region must be TR or global"})
		return
	}
	mediaType := strings.ToLower(strings.TrimSpace(c.DefaultQuery("type", "movie")))
	if mediaType != "movie" && mediaType != "tv" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "type must be movie or tv"})
		return
	}
	list, ok := h.s.GetIn(region, mediaType, httpserver.Lang(c))
	if !ok {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "Top 10 is not available yet"})
		return
	}
	c.JSON(http.StatusOK, list)
}
