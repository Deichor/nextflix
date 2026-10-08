package mylist

import "github.com/gin-gonic/gin"

func Register(api *gin.RouterGroup, s *Service) {
	h := handler{s}
	api.GET("/mylist", h.list)
	api.GET("/mylist/check/:id", h.check)
	api.POST("/mylist/:id", h.add)
	api.DELETE("/mylist/:id", h.remove)
}
