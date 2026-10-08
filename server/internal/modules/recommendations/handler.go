package recommendations

import (
	"net/http"
	"strconv"
	"strings"

	"github.com/gin-gonic/gin"

	"nextflix/internal/platform/httpserver"
)

type handler struct{ s *Service }

func (h handler) get(c *gin.Context) {
	raw := strings.TrimSpace(c.Query("profile_id"))
	if raw == "" {
		raw = strings.TrimSpace(c.GetHeader("X-User-ID"))
	}
	id, err := strconv.ParseUint(raw, 10, 64)
	if err != nil || id == 0 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "profile_id is required"})
		return
	}
	resp, err := h.s.For(uint(id), httpserver.Lang(c))
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to load recommendations"})
		return
	}
	c.JSON(http.StatusOK, resp)
}
