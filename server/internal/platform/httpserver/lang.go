package httpserver

import (
	"strings"

	"github.com/gin-gonic/gin"
)

// Lang returns the content language of the request from the X-Lang header: "tr" or "en"
// (the default for a missing or unknown value). The answer then depends on the header,
// so Vary tells caches about it.
func Lang(c *gin.Context) string {
	c.Writer.Header().Add("Vary", "X-Lang")
	return parseLang(c.GetHeader("X-Lang"))
}

func parseLang(s string) string {
	s = strings.ToLower(strings.TrimSpace(s))
	if i := strings.IndexAny(s, "-_"); i >= 0 {
		s = s[:i]
	}
	if s == "tr" {
		return "tr"
	}
	return "en"
}
