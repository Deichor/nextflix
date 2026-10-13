package httpserver

import (
	"strings"

	"github.com/gin-gonic/gin"
)

// Lang returns the content language of the request: "tr" or "en" (the default for a missing or
// unknown value). It reads the X-Lang header, then ?x_lang= (header-less GETs skip the CORS
// preflight). The answer then depends on the header, so Vary tells caches about it.
func Lang(c *gin.Context) string {
	c.Writer.Header().Add("Vary", "X-Lang")
	v := c.GetHeader("X-Lang")
	if strings.TrimSpace(v) == "" {
		v = c.Query("x_lang")
	}
	return parseLang(v)
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
