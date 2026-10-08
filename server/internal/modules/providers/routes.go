package providers

import "github.com/gin-gonic/gin"

func Register(api *gin.RouterGroup, s *Service) {
	api.GET("/providers", s.list)
	api.GET("/providers/showcase", s.showcase)
	api.GET("/providers/:id", s.get)
	api.GET("/providers/:id/content", s.content)
	api.GET("/providers/:id/movies", s.discoverBy("movie"))
	api.GET("/providers/:id/tv", s.discoverBy("tv"))
	api.GET("/providers/:id/trending", s.typed(s.Trending))
	api.GET("/providers/:id/popular", s.typed(s.Popular))
	api.GET("/providers/:id/top-rated", s.typed(s.TopRated))
	api.GET("/providers/:id/new", s.typed(s.NewReleases))
	api.GET("/content/:type/:id/providers", s.contentProviders)
}
