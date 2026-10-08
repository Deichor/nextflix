package profiles

import (
	"errors"
	"net/http"
	"strconv"

	"github.com/gin-gonic/gin"
)

type handler struct{ s *Service }

func (h handler) load(c *gin.Context) (*Profile, bool) {
	id, err := strconv.ParseUint(c.Param("id"), 10, 64)
	if err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "Profile not found"})
		return nil, false
	}
	p, err := h.s.Get(uint(id))
	if errors.Is(err, ErrNotFound) {
		c.JSON(http.StatusNotFound, gin.H{"error": "Profile not found"})
		return nil, false
	}
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to load profile"})
		return nil, false
	}
	return p, true
}

func (h handler) list(c *gin.Context) {
	out, err := h.s.List()
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to load profiles"})
		return
	}
	c.JSON(http.StatusOK, out)
}

func (h handler) create(c *gin.Context) {
	var in Input
	if err := c.ShouldBindJSON(&in); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid request body"})
		return
	}
	p, err := h.s.Create(in)
	var verr ValidationError
	switch {
	case errors.As(err, &verr), errors.Is(err, ErrLimit):
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
	case err != nil:
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to create profile"})
	default:
		c.JSON(http.StatusCreated, p)
	}
}

func (h handler) update(c *gin.Context) {
	p, ok := h.load(c)
	if !ok {
		return
	}
	var in Input
	if err := c.ShouldBindJSON(&in); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid request body"})
		return
	}
	err := h.s.Update(p, in)
	var verr ValidationError
	switch {
	case errors.As(err, &verr):
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
	case err != nil:
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to update profile"})
	default:
		c.JSON(http.StatusOK, p)
	}
}

func (h handler) delete(c *gin.Context) {
	p, ok := h.load(c)
	if !ok {
		return
	}
	err := h.s.Delete(p.ID)
	switch {
	case errors.Is(err, ErrLastProfile):
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
	case err != nil:
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to delete profile"})
	default:
		c.Status(http.StatusNoContent)
	}
}
