package playbacklog

import "github.com/gin-gonic/gin"

func Register(api *gin.RouterGroup, s *Service) {
	api.POST("/playback-log", s.receive)
}
