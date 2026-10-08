package catalog

import "github.com/gin-gonic/gin"

func Register(api *gin.RouterGroup, s *Service) {
	api.GET("/upcoming-movies", s.upcomingMovies)
	api.GET("/upcoming-tv-series", s.upcomingTVSeries)
	api.GET("/tmdb-movie/:id", s.details)

	api.GET("/tmdb/search", s.search)
	api.GET("/tmdb/suggestions", s.suggestions)

	api.GET("/tmdb/movie/latest", s.latestMovie)
	api.GET("/tmdb/movie/now-playing", pagedList(s.tm.GetNowPlayingMovies, "Failed to get now playing movies"))
	api.GET("/tmdb/movie/upcoming", pagedList(s.tm.GetUpcomingMoviesList, "Failed to get upcoming movies"))
	api.GET("/tmdb/movie/popular", pagedList(s.tm.GetPopularMovies, "Failed to get popular movies"))
	api.GET("/tmdb/discover/movie", s.discoverMovies)
	api.GET("/tmdb/movie/:id/images", s.movieImages)

	api.GET("/tmdb/:id/related", s.related)
	api.GET("/tmdb/movie/:id/similar", relatedList(s.tm.GetSimilarMovies, "Invalid movie ID", "Failed to get similar movies"))
	api.GET("/tmdb/movie/:id/recommendations", relatedList(s.tm.GetRecommendedMovies, "Invalid movie ID", "Failed to get recommended movies"))
	api.GET("/tmdb/tv/:id/similar", relatedList(s.tm.GetSimilarTVShows, "Invalid TV show ID", "Failed to get similar TV shows"))
	api.GET("/tmdb/tv/:id/recommendations", relatedList(s.tm.GetRecommendedTVShows, "Invalid TV show ID", "Failed to get recommended TV shows"))
	api.GET("/tmdb/tv/:id/season/:season", s.season)
}
