package catalog

import (
	"github.com/gin-gonic/gin"

	"nextflix/internal/platform/tmdb"
)

func Register(api *gin.RouterGroup, s *Service) {
	api.GET("/upcoming-movies", s.upcomingMovies)
	api.GET("/upcoming-tv-series", s.upcomingTVSeries)
	api.GET("/tmdb-movie/:id", s.details)
	api.GET("/availability", s.availability)

	api.GET("/catalog/discover", s.discover)
	api.GET("/catalog/suggest", s.suggest)

	api.GET("/tmdb/search", s.search)
	api.GET("/tmdb/suggestions", s.suggestions)

	api.GET("/tmdb/movie/latest", s.latestMovie)
	api.GET("/tmdb/movie/now-playing", pagedList(s, (*tmdb.Client).GetNowPlayingMovies, "Failed to get now playing movies"))
	api.GET("/tmdb/movie/upcoming", pagedList(s, (*tmdb.Client).GetUpcomingMoviesList, "Failed to get upcoming movies"))
	api.GET("/tmdb/movie/popular", pagedList(s, (*tmdb.Client).GetPopularMovies, "Failed to get popular movies"))
	api.GET("/tmdb/discover/movie", s.discoverMovies)
	api.GET("/tmdb/movie/:id/images", s.movieImages)

	api.GET("/tmdb/:id/related", s.related)
	api.GET("/tmdb/movie/:id/similar", relatedList(s, (*tmdb.Client).GetSimilarMovies, "Invalid movie ID", "Failed to get similar movies"))
	api.GET("/tmdb/movie/:id/recommendations", relatedList(s, (*tmdb.Client).GetRecommendedMovies, "Invalid movie ID", "Failed to get recommended movies"))
	api.GET("/tmdb/tv/:id/similar", relatedList(s, (*tmdb.Client).GetSimilarTVShows, "Invalid TV show ID", "Failed to get similar TV shows"))
	api.GET("/tmdb/tv/:id/recommendations", relatedList(s, (*tmdb.Client).GetRecommendedTVShows, "Invalid TV show ID", "Failed to get recommended TV shows"))
	api.GET("/tmdb/tv/:id/season/:season", s.season)
}
