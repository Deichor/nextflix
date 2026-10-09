package streaming

import "github.com/gin-gonic/gin"

func Register(api *gin.RouterGroup, s *Service) {
	h := newHandler(s)
	api.GET("/torrent-stream/files", h.files)
	api.GET("/torrent-stream/probe", h.probe)
	api.GET("/torrent-stream/play", h.play)
	api.GET("/torrent-stream/seekpoint", h.seekPoint)
	api.POST("/torrent-stream/keepalive", h.keepAlive)
	api.GET("/torrent-stream/resolve", h.resolve)
	api.POST("/torrent-stream/prefetch", h.prefetch)
	api.GET("/torrent-stream/upgrade", h.upgrade)
	api.GET("/torrent-stream/sidegrade", h.sidegrade)
	api.GET("/torrent-stream/subalt", h.subalt)
	api.POST("/torrent-stream/instant", h.instant)
	api.POST("/torrent-stream/release", h.release)
	th := newThumbs(s, thumbCacheDir())
	api.GET("/torrent-stream/thumb", th.get)
	api.POST("/torrent-stream/thumb/prefetch", th.prefetch)

	api.GET("/settings/indexer", h.getSettings)
	api.PUT("/settings/indexer", h.updateSettings)
	api.POST("/settings/indexer/test", h.testSettings)
	api.GET("/settings/debrid", h.getDebridSettings)
	api.PUT("/settings/debrid", h.updateDebridSettings)
	api.POST("/settings/debrid/test", h.testDebridSettings)
}
