package httpserver

import (
	"os"
	"path/filepath"

	"github.com/gin-gonic/gin"
)

// ServeTVApp serves the built webOS app (tv/webos/dist) under /tv/, so a TV can open it in its
// own browser without Developer Mode. A missing or empty dir disables the route.
func ServeTVApp(r *gin.Engine, dir string) {
	if dir == "" {
		return
	}
	if _, err := os.Stat(filepath.Join(dir, "index.html")); err != nil {
		return
	}
	r.Static("/tv", dir) // gin redirects /tv to /tv/
}
