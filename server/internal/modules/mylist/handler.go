package mylist

import (
	"net/http"
	"strconv"

	"nextflix/internal/platform/httpserver"

	"github.com/gin-gonic/gin"
)

type handler struct{ s *Service }

// profile returns the active profile (X-User-ID) or answers 400.
func profile(c *gin.Context) (uint, bool) {
	id := httpserver.ProfileID(c)
	if id == 0 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "X-User-ID header is required"})
		return 0, false
	}
	return id, true
}

// target reads the profile and the :id media param.
func target(c *gin.Context) (uint, uint, bool) {
	mediaID, err := strconv.ParseUint(c.Param("id"), 10, 32)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid media ID"})
		return 0, 0, false
	}
	profileID, ok := profile(c)
	if !ok {
		return 0, 0, false
	}
	return profileID, uint(mediaID), true
}

func (h handler) add(c *gin.Context) {
	profileID, mediaID, ok := target(c)
	if !ok {
		return
	}
	var req struct {
		MediaType string `json:"media_type"`
		Notes     string `json:"notes"`
		Priority  int    `json:"priority"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		req.MediaType = "movie"
		req.Priority = 0
	}
	if err := h.s.Add(profileID, mediaID, req.MediaType, req.Notes, req.Priority); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"message": "Item added to My List successfully"})
}

func (h handler) remove(c *gin.Context) {
	profileID, mediaID, ok := target(c)
	if !ok {
		return
	}
	if err := h.s.Remove(profileID, mediaID); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"message": "Item removed from My List successfully"})
}

func (h handler) list(c *gin.Context) {
	profileID, ok := profile(c)
	if !ok {
		return
	}
	items, err := h.s.List(profileID)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, items)
}

func (h handler) check(c *gin.Context) {
	profileID, mediaID, ok := target(c)
	if !ok {
		return
	}
	inList, err := h.s.Contains(profileID, mediaID)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"in_list": inList})
}
