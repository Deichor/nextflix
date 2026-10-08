package recommendations

import "github.com/gin-gonic/gin"

func Register(api *gin.RouterGroup, s *Service) {
	h := handler{s}
	api.GET("/recommendations", h.get)
}
