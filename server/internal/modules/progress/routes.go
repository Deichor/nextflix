package progress

import "github.com/gin-gonic/gin"

func Register(api *gin.RouterGroup, s *Service) {
	h := handler{s}
	api.GET("/stream-progress", h.continueWatching)
	api.PUT("/stream-progress", h.save)
	api.DELETE("/stream-progress", h.delete)
	api.GET("/stream-progress/item", h.item)
	api.GET("/stream-progress/title", h.title)
}
