package providers

import (
	"net/http"
	"strconv"

	"github.com/gin-gonic/gin"

	"nextflix/internal/platform/httpserver"
)

// TMDB serves at most 500 pages; unknown sort orders fall back to popularity (bounded cache keys).
const maxPage = 500

var tvSorts = map[string]bool{
	"popularity.desc": true, "popularity.asc": true,
	"vote_average.desc": true, "vote_average.asc": true,
	"vote_count.desc": true, "vote_count.asc": true,
	"first_air_date.desc": true, "first_air_date.asc": true,
	"name.desc": true, "name.asc": true,
	"original_name.desc": true, "original_name.asc": true,
}

// GET /api/providers/:id/tv?sort_by=&page=
func (s *Service) tv(c *gin.Context) {
	id, err := strconv.Atoi(c.Param("id"))
	if err != nil || id <= 0 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid provider ID"})
		return
	}
	page, _ := strconv.Atoi(c.DefaultQuery("page", "1"))
	sortBy := c.Query("sort_by")
	if !tvSorts[sortBy] {
		sortBy = "popularity.desc"
	}
	res, err := s.DiscoverTV(httpserver.Lang(c), id, min(max(page, 1), maxPage), sortBy)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, withImages(res))
}
