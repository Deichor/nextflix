package streaming

import "github.com/gin-gonic/gin"

func Register(api *gin.RouterGroup, s *Service) {
	h := newHandler(s)
	api.GET("/torrent-stream/files", h.files)
	api.GET("/torrent-stream/probe", h.probe)
	api.GET("/torrent-stream/play", h.play)
	api.GET("/torrent-stream/resolve", h.resolve)

	api.GET("/settings/indexer", h.getSettings)
	api.PUT("/settings/indexer", h.updateSettings)
	api.POST("/settings/indexer/test", h.testSettings)
}
