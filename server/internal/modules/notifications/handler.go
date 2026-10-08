package notifications

import (
	"net/http"
	"strconv"

	"github.com/gin-gonic/gin"

	"nextflix/internal/platform/httpserver"
)

const maxIDLength = 128

// Read state is per profile, so responses must never be cached by the browser.
func noStore(c *gin.Context) { c.Header("Cache-Control", "no-store") }

// respondCounts answers a mutation with the profile's fresh counts so the badge syncs without another request.
func (s *Service) respondCounts(c *gin.Context) {
	counts, err := s.Count(c.Request.Context(), httpserver.ProfileID(c))
	if err != nil {
		c.JSON(http.StatusOK, gin.H{"success": true})
		return
	}
	c.JSON(http.StatusOK, gin.H{"success": true, "count": counts.Unseen, "unseen": counts.Unseen, "unread": counts.Unread, "total": counts.Total})
}

// GET /api/notifications?limit=50
func (s *Service) list(c *gin.Context) {
	noStore(c)
	limit, err := strconv.Atoi(c.DefaultQuery("limit", "50"))
	if err != nil || limit <= 0 {
		limit = 50
	}
	limit = min(limit, maxNotifications)
	items, counts, err := s.Notifications(c.Request.Context(), httpserver.ProfileID(c), limit, httpserver.Lang(c))
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to fetch notifications"})
		return
	}
	c.JSON(http.StatusOK, gin.H{
		"notifications": items,
		"count":         len(items),
		"unseen":        counts.Unseen,
		"unread":        counts.Unread,
		"total":         counts.Total,
	})
}

// GET /api/notifications/count -> count is the unseen (badge) count
func (s *Service) count(c *gin.Context) {
	noStore(c)
	counts, err := s.Count(c.Request.Context(), httpserver.ProfileID(c))
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to get notification count"})
		return
	}
	c.JSON(http.StatusOK, gin.H{"count": counts.Unseen, "unseen": counts.Unseen, "unread": counts.Unread, "total": counts.Total})
}

// POST /api/notifications/:id/read
func (s *Service) markRead(c *gin.Context) {
	noStore(c)
	id := c.Param("id")
	if id == "" || len(id) > maxIDLength {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid notification id"})
		return
	}
	if err := s.MarkAsRead(c.Request.Context(), httpserver.ProfileID(c), id); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to mark notification as read"})
		return
	}
	s.respondCounts(c)
}

// POST /api/notifications/read-all
func (s *Service) markAllRead(c *gin.Context) {
	noStore(c)
	if err := s.MarkAllRead(c.Request.Context(), httpserver.ProfileID(c)); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to mark notifications as read"})
		return
	}
	s.respondCounts(c)
}

// POST /api/notifications/seen  body (optional): {"until": <unix timestamp of the newest shown notification>}
func (s *Service) markSeen(c *gin.Context) {
	noStore(c)
	var body struct {
		Until int64 `json:"until"`
	}
	_ = c.ShouldBindJSON(&body) // an empty body means "everything"
	if err := s.MarkSeen(c.Request.Context(), httpserver.ProfileID(c), body.Until); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to mark notifications as seen"})
		return
	}
	s.respondCounts(c)
}
