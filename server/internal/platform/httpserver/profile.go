package httpserver

import (
	"strconv"

	"github.com/gin-gonic/gin"
)

// ProfileID returns the active profile from the X-User-ID header, 0 if absent or invalid.
func ProfileID(c *gin.Context) uint {
	id, err := strconv.ParseUint(c.GetHeader("X-User-ID"), 10, 64)
	if err != nil {
		return 0
	}
	return uint(id)
}
