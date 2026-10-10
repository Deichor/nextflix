package subtitles

import "github.com/gin-gonic/gin"

func Register(api *gin.RouterGroup, s *Service) {
	h := handler{s}
	api.GET("/subtitles", h.list)
	api.GET("/subtitles/file", h.file)
	api.GET("/settings/subtitles", h.getSettings)
	api.PUT("/settings/subtitles", h.putSettings)
	api.POST("/settings/subtitles/ai-test", h.testAI)
}
