package top10

import "github.com/gin-gonic/gin"

func Register(api *gin.RouterGroup, s *Service) {
	h := handler{s}
	api.GET("/top10", h.get)
}
