package providers

import (
	"net/http"
	"strconv"

	"github.com/gin-gonic/gin"

	"nextflix/internal/platform/httpserver"
)

// GET /api/providers/:id/tv?sort_by=&page=
func (s *Service) tv(c *gin.Context) {
	id, err := strconv.Atoi(c.Param("id"))
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid provider ID"})
		return
	}
	page, _ := strconv.Atoi(c.DefaultQuery("page", "1"))
	res, err := s.DiscoverTV(httpserver.Lang(c), id, max(page, 1), c.DefaultQuery("sort_by", "popularity.desc"))
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, withImages(res))
}
