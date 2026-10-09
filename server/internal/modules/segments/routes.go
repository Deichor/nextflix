package segments

import "github.com/gin-gonic/gin"

func Register(api *gin.RouterGroup, s *Service) {
	api.GET("/skip-segments", func(c *gin.Context) { get(c, s) })
}
