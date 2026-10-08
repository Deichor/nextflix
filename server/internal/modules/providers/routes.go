package providers

import "github.com/gin-gonic/gin"

func Register(api *gin.RouterGroup, s *Service) {
	api.GET("/providers/:id/tv", s.tv)
}
