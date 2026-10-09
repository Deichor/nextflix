package catalog

import (
	"encoding/json"
	"log"
	"net/http"
	"strconv"

	"github.com/gin-gonic/gin"

	"nextflix/internal/platform/httpserver"
	"nextflix/internal/platform/tmdb"
)

func pageParam(c *gin.Context) int {
	if p, err := strconv.Atoi(c.Query("page")); err == nil && p > 0 {
		return p
	}
	return 1
}

// client returns the TMDB client in the request language (X-Lang).
func (s *Service) client(c *gin.Context) *tmdb.Client { return s.tm.WithLang(httpserver.Lang(c)) }

func fail(c *gin.Context, status int, msg string, err error) {
	log.Printf("catalog: %s: %v", msg, err)
	c.JSON(status, gin.H{"error": msg, "details": err.Error()})
}

// GET /api/tmdb/search?q=&type=multi|movie|tv&page= – multi (default) is ranked by RankedSearch
// (one page, did_you_mean on typos); movie and tv pass TMDB's answer through.
func (s *Service) search(c *gin.Context) {
	query := c.Query("q")
	if query == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Search query is required"})
		return
	}
	page := pageParam(c)

	switch c.DefaultQuery("type", "multi") {
	case "movie":
		res, err := s.client(c).SearchMoviesOnly(query, page)
		if err != nil {
			fail(c, http.StatusInternalServerError, "Failed to search movies", err)
			return
		}
		c.JSON(http.StatusOK, res)
	case "tv":
		res, err := s.client(c).SearchTVOnly(query, page)
		if err != nil {
			fail(c, http.StatusInternalServerError, "Failed to search TV shows", err)
			return
		}
		c.JSON(http.StatusOK, res)
	default:
		// Ranked: every result is on page 1
		if page > 1 {
			c.JSON(http.StatusOK, DiscoverPage{Page: page, TotalPages: 1, Results: []tmdb.SearchResult{}})
			return
		}
		res, err := s.RankedSearch(s.client(c), query, Filter{Type: "all"}, httpserver.ProfileID(c))
		if err != nil {
			fail(c, http.StatusInternalServerError, "Failed to search TMDB", err)
			return
		}
		c.JSON(http.StatusOK, res)
	}
}

// GET /api/tmdb/suggestions?q= – the 5 best ranked search results for the navbar dropdown.
func (s *Service) suggestions(c *gin.Context) {
	query := c.Query("q")
	if query == "" {
		c.JSON(http.StatusOK, gin.H{"results": []any{}, "total_results": 0})
		return
	}
	res, err := s.RankedSearch(s.client(c), query, Filter{Type: "all"}, httpserver.ProfileID(c))
	if err != nil {
		fail(c, http.StatusInternalServerError, "Failed to get suggestions", err)
		return
	}
	results := res.Results
	if len(results) > 5 {
		results = results[:5]
	}
	c.JSON(http.StatusOK, gin.H{"results": results, "total_results": len(results), "did_you_mean": res.DidYouMean})
}

// detailsMap converts details to a JSON map and adds genre_names when genres are present.
func detailsMap(v any, genres []tmdb.Genre) map[string]any {
	m := map[string]any{}
	if b, err := json.Marshal(v); err == nil {
		json.Unmarshal(b, &m)
	}
	if genres != nil {
		names := make([]string, 0, len(genres))
		for _, g := range genres {
			names = append(names, g.Name)
		}
		m["genre_names"] = names
	}
	return m
}

// GET /api/tmdb-movie/:id?type=movie|tv – without type, movie is tried first, then TV.
func (s *Service) details(c *gin.Context) {
	id, err := strconv.Atoi(c.Param("id"))
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid movie/TV ID"})
		return
	}
	mediaType := c.Query("type")
	tm := s.client(c)

	movie := func() error {
		d, err := tm.GetMovieDetailsWithExtras(id)
		if err == nil {
			c.JSON(http.StatusOK, gin.H{"media_type": "movie", "data": detailsMap(d, d.Genres)})
		}
		return err
	}
	tv := func() error {
		d, err := tm.GetTVDetails(id)
		if err == nil {
			c.JSON(http.StatusOK, gin.H{"media_type": "tv", "data": detailsMap(d, d.Genres)})
		}
		return err
	}

	switch mediaType {
	case "movie":
		if err := movie(); err != nil {
			fail(c, http.StatusNotFound, "Movie not found", err)
		}
	case "tv":
		if err := tv(); err != nil {
			fail(c, http.StatusNotFound, "TV series not found", err)
		}
	default:
		movieErr := movie()
		if movieErr == nil {
			return
		}
		tvErr := tv()
		if tvErr == nil {
			return
		}
		c.JSON(http.StatusNotFound, gin.H{
			"error":       "Movie or TV series not found",
			"movie_error": movieErr.Error(),
			"tv_error":    tvErr.Error(),
		})
	}
}

// GET /api/upcoming-movies
func (s *Service) upcomingMovies(c *gin.Context) {
	res, err := s.client(c).GetUpcomingMovies()
	if err != nil {
		fail(c, http.StatusInternalServerError, "Failed to get upcoming movies", err)
		return
	}
	c.JSON(http.StatusOK, res)
}

// GET /api/upcoming-tv-series
func (s *Service) upcomingTVSeries(c *gin.Context) {
	res, err := s.client(c).GetUpcomingTVSeries()
	if err != nil {
		fail(c, http.StatusInternalServerError, "Failed to get TV series", err)
		return
	}
	c.JSON(http.StatusOK, res)
}

// GET /api/tmdb/movie/latest
func (s *Service) latestMovie(c *gin.Context) {
	res, err := s.client(c).GetLatestMovie()
	if err != nil {
		fail(c, http.StatusInternalServerError, "Failed to get latest movie", err)
		return
	}
	c.JSON(http.StatusOK, res)
}

// pagedList serves {results, page, total_results} for a paged TMDB movie list.
func pagedList[T any](s *Service, fetch func(tm *tmdb.Client, page int) ([]T, error), errMsg string) gin.HandlerFunc {
	return func(c *gin.Context) {
		page := pageParam(c)
		movies, err := fetch(s.client(c), page)
		if err != nil {
			fail(c, http.StatusInternalServerError, errMsg, err)
			return
		}
		c.JSON(http.StatusOK, gin.H{"results": movies, "page": page, "total_results": len(movies)})
	}
}

// GET /api/tmdb/discover/movie – every query param is forwarded to TMDB.
func (s *Service) discoverMovies(c *gin.Context) {
	params := map[string]string{}
	for k, v := range c.Request.URL.Query() {
		if len(v) > 0 {
			params[k] = v[0]
		}
	}
	movies, err := s.client(c).DiscoverMovies(params)
	if err != nil {
		fail(c, http.StatusInternalServerError, "Failed to discover movies", err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"results": movies, "total_results": len(movies), "filters": params})
}

// GET /api/tmdb/movie/:id/images
func (s *Service) movieImages(c *gin.Context) {
	id, err := strconv.Atoi(c.Param("id"))
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid movie ID"})
		return
	}
	images, err := s.client(c).GetMovieImages(id)
	if err != nil {
		fail(c, http.StatusInternalServerError, "Failed to get movie images", err)
		return
	}
	c.JSON(http.StatusOK, images)
}

// GET /api/tmdb/:id/related?type=movie|tv&limit=&year=
func (s *Service) related(c *gin.Context) {
	id, err := strconv.Atoi(c.Param("id"))
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid media ID"})
		return
	}
	mediaType := c.Query("type")
	if mediaType == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Media type is required (movie or tv)"})
		return
	}
	if mediaType != "movie" && mediaType != "tv" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Media type must be 'movie' or 'tv'"})
		return
	}
	limit := 20
	if l, err := strconv.Atoi(c.Query("limit")); err == nil && l > 0 && l <= 50 {
		limit = l
	}
	year := 0
	if y, err := strconv.Atoi(c.Query("year")); err == nil && y > 1800 && y <= 2100 {
		year = y
	}

	results, err := s.client(c).GetRelatedMedia(id, mediaType, limit, year)
	if err != nil {
		fail(c, http.StatusInternalServerError, "Failed to get related media", err)
		return
	}
	c.JSON(http.StatusOK, gin.H{
		"results":       results,
		"total_results": len(results),
		"media_type":    mediaType,
		"media_id":      id,
	})
}

// relatedList serves a TMDB similar/recommendations page as-is.
func relatedList[T any](s *Service, fetch func(tm *tmdb.Client, id, page int) (T, error), invalidMsg, errMsg string) gin.HandlerFunc {
	return func(c *gin.Context) {
		id, err := strconv.Atoi(c.Param("id"))
		if err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": invalidMsg})
			return
		}
		res, err := fetch(s.client(c), id, pageParam(c))
		if err != nil {
			fail(c, http.StatusInternalServerError, errMsg, err)
			return
		}
		c.JSON(http.StatusOK, res)
	}
}

// GET /api/tmdb/tv/:id/season/:season – TMDB season JSON (episodes with name, overview, still_path, runtime).
func (s *Service) season(c *gin.Context) {
	tvID, err := strconv.Atoi(c.Param("id"))
	if err != nil || tvID <= 0 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid TV show ID"})
		return
	}
	number, err := strconv.Atoi(c.Param("season"))
	if err != nil || number < 0 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid season number"})
		return
	}
	season, err := s.Season(httpserver.Lang(c), tvID, number)
	if err != nil {
		log.Printf("catalog: TMDB season %d of TV %d failed: %v", number, tvID, err)
		c.JSON(http.StatusBadGateway, gin.H{"error": "Failed to get season details"})
		return
	}
	c.JSON(http.StatusOK, season)
}
