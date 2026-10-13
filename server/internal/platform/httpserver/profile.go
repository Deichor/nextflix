package httpserver

import (
	"strconv"

	"github.com/gin-gonic/gin"
)

// ProfileID returns the active profile from the X-User-ID header, then ?profile_id=
// (header-less GETs skip the CORS preflight); 0 if both are absent or invalid.
func ProfileID(c *gin.Context) uint {
	if id, ok := parseProfileID(c.GetHeader("X-User-ID")); ok {
		return id
	}
	id, _ := parseProfileID(c.Query("profile_id"))
	return id
}

func parseProfileID(s string) (uint, bool) {
	id, err := strconv.ParseUint(s, 10, 64)
	if err != nil {
		return 0, false
	}
	return uint(id), true
}
