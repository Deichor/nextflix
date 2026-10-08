package profiles

import "github.com/gin-gonic/gin"

func Register(api *gin.RouterGroup, s *Service) {
	h := handler{s}
	api.GET("/profiles", h.list)
	api.POST("/profiles", h.create)
	api.PUT("/profiles/:id", h.update)
	api.DELETE("/profiles/:id", h.delete)
}
