package notifications

import "github.com/gin-gonic/gin"

func Register(api *gin.RouterGroup, s *Service) {
	api.GET("/notifications", s.list)
	api.GET("/notifications/count", s.count)
	api.POST("/notifications/:id/read", s.markRead)
}
