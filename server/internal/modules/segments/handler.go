package segments

import (
	"net/http"
	"regexp"
	"strconv"
	"strings"

	"github.com/gin-gonic/gin"
)

var hashPattern = regexp.MustCompile(`^[a-f0-9]{40}$`)

// get answers the intro and end credits of the playing episode:
// ?hash=&index= (the file) and ?tmdb_id=&season=&episode= (the show), optional ?duration=.
func get(c *gin.Context, s *Service) {
	num := func(k string) int {
		n, _ := strconv.Atoi(c.Query(k))
		return n
	}
	q := Query{TmdbID: num("tmdb_id"), Season: num("season"), Episode: num("episode"), Index: num("index")}
	if h := strings.ToLower(c.Query("hash")); hashPattern.MatchString(h) && q.Index >= 0 {
		q.Hash = h
	}
	if d, err := strconv.ParseFloat(c.Query("duration"), 64); err == nil && d > 0 && d < 24*3600 {
		q.Duration = d
	}
	if q.Hash == "" && (q.TmdbID <= 0 || q.Season <= 0 || q.Episode <= 0) {
		c.JSON(http.StatusBadRequest, gin.H{"error": "a file (hash, index) or an episode (tmdb_id, season, episode) is required"})
		return
	}
	c.JSON(http.StatusOK, s.Get(c.Request.Context(), q))
}
