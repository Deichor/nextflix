package ratings

import "github.com/gin-gonic/gin"

func Register(api *gin.RouterGroup, s *Service) {
	h := handler{s}
	api.GET("/ratings", h.list)
	api.GET("/ratings/item", h.item)
	api.PUT("/ratings", h.put)
	api.GET("/favorites", h.favorites)
}
