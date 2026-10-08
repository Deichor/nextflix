package notifications

import (
	"net/http"
	"strconv"

	"github.com/gin-gonic/gin"
)

// GET /api/notifications?limit=50
func (s *Service) list(c *gin.Context) {
	limit, err := strconv.Atoi(c.DefaultQuery("limit", "50"))
	if err != nil || limit <= 0 {
		limit = 50
	}
	items, err := s.Notifications(c.Request.Context(), limit)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to fetch notifications"})
		return
	}
	c.Header("Cache-Control", "public, max-age=30, stale-while-revalidate=60")
	c.Header("ETag", strconv.Itoa(len(items)))
	c.JSON(http.StatusOK, gin.H{"notifications": items, "count": len(items)})
}

// GET /api/notifications/count
func (s *Service) count(c *gin.Context) {
	n, err := s.Count(c.Request.Context())
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to get notification count"})
		return
	}
	c.JSON(http.StatusOK, gin.H{"count": n})
}

// POST /api/notifications/:id/read
func (s *Service) markRead(c *gin.Context) {
	if err := s.MarkAsRead(c.Param("id")); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to mark notification as read"})
		return
	}
	c.JSON(http.StatusOK, gin.H{"success": true, "message": "Notification marked as read"})
}
