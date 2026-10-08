package images

import "github.com/gin-gonic/gin"

// Register mounts the image proxy on the engine root (not under /api), matching the web's /tmdb-img URLs.
func Register(r gin.IRoutes, s *Service) {
	r.GET("/tmdb-img/t/p/:size/*file", s.serve)
	r.HEAD("/tmdb-img/t/p/:size/*file", s.serve)
}
