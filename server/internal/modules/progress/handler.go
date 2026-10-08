package progress

import (
	"errors"
	"net/http"
	"strconv"
	"strings"

	"github.com/gin-gonic/gin"
)

type handler struct{ s *Service }

type saveInput struct {
	ProfileID    uint    `json:"profile_id"`
	MediaType    string  `json:"media_type"`
	TmdbID       int     `json:"tmdb_id"`
	Title        string  `json:"title"`
	PosterPath   string  `json:"poster_path"`
	BackdropPath string  `json:"backdrop_path"`
	Season       int     `json:"season"`
	Episode      int     `json:"episode"`
	EpisodeTitle string  `json:"episode_title"`
	Magnet       string  `json:"magnet"`
	Hash         string  `json:"hash"`
	FileIndex    int     `json:"file_index"`
	Position     float64 `json:"position"`
	Duration     float64 `json:"duration"`
}

func validMediaType(t string) bool { return t == "movie" || t == "tv" }

// headerProfileID parses X-User-ID; 0 when absent or invalid.
func headerProfileID(c *gin.Context) uint {
	id, err := strconv.ParseUint(strings.TrimSpace(c.GetHeader("X-User-ID")), 10, 64)
	if err != nil {
		return 0
	}
	return uint(id)
}

// queryProfileID reads profile_id from the query, falling back to the X-User-ID header.
func queryProfileID(c *gin.Context) (uint, bool) {
	raw := strings.TrimSpace(c.Query("profile_id"))
	if raw == "" {
		raw = strings.TrimSpace(c.GetHeader("X-User-ID"))
	}
	id, err := strconv.ParseUint(raw, 10, 64)
	if err != nil || id == 0 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "profile_id is required"})
		return 0, false
	}
	return uint(id), true
}

// titleQuery reads profile_id, media_type and tmdb_id, answering 400 when one is missing.
func titleQuery(c *gin.Context) (uint, string, int, bool) {
	profileID, ok := queryProfileID(c)
	if !ok {
		return 0, "", 0, false
	}
	mediaType := c.Query("media_type")
	tmdbID, err := strconv.Atoi(c.Query("tmdb_id"))
	if !validMediaType(mediaType) || err != nil || tmdbID <= 0 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "media_type and tmdb_id are required"})
		return 0, "", 0, false
	}
	return profileID, mediaType, tmdbID, true
}

func (h handler) save(c *gin.Context) {
	var in saveInput
	if err := c.ShouldBindJSON(&in); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid request body"})
		return
	}
	if in.ProfileID == 0 {
		in.ProfileID = headerProfileID(c)
	}
	if in.ProfileID == 0 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "profile_id is required"})
		return
	}
	if !validMediaType(in.MediaType) {
		c.JSON(http.StatusBadRequest, gin.H{"error": "media_type must be movie or tv"})
		return
	}
	if in.TmdbID <= 0 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "tmdb_id is required"})
		return
	}
	if in.MediaType == "movie" {
		in.Season, in.Episode = 0, 0
	} else if in.Season < 0 || in.Episode < 0 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid season or episode"})
		return
	}
	if in.Position < 0 || in.Duration < 0 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid position or duration"})
		return
	}

	saved, err := h.s.Save(StreamProgress{
		ProfileID:    in.ProfileID,
		MediaType:    in.MediaType,
		TmdbID:       in.TmdbID,
		Season:       in.Season,
		Episode:      in.Episode,
		Title:        in.Title,
		PosterPath:   in.PosterPath,
		BackdropPath: in.BackdropPath,
		EpisodeTitle: in.EpisodeTitle,
		Magnet:       in.Magnet,
		Hash:         in.Hash,
		FileIndex:    in.FileIndex,
		Position:     in.Position,
		Duration:     in.Duration,
	})
	if errors.Is(err, ErrUnknownProfile) {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to save progress"})
		return
	}
	c.JSON(http.StatusOK, saved)
}

func (h handler) continueWatching(c *gin.Context) {
	profileID, ok := queryProfileID(c)
	if !ok {
		return
	}
	out, err := h.s.ContinueWatching(profileID)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to load progress"})
		return
	}
	c.JSON(http.StatusOK, out)
}

func (h handler) item(c *gin.Context) {
	profileID, mediaType, tmdbID, ok := titleQuery(c)
	if !ok {
		return
	}
	season, _ := strconv.Atoi(c.DefaultQuery("season", "0"))
	episode, _ := strconv.Atoi(c.DefaultQuery("episode", "0"))
	p, err := h.s.Item(profileID, mediaType, tmdbID, season, episode)
	if errors.Is(err, ErrNotFound) {
		c.JSON(http.StatusNotFound, gin.H{"error": err.Error()})
		return
	}
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to load progress"})
		return
	}
	c.JSON(http.StatusOK, p)
}

func (h handler) title(c *gin.Context) {
	profileID, mediaType, tmdbID, ok := titleQuery(c)
	if !ok {
		return
	}
	rows, err := h.s.Title(profileID, mediaType, tmdbID)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to load progress"})
		return
	}
	c.JSON(http.StatusOK, rows)
}

func (h handler) delete(c *gin.Context) {
	profileID, mediaType, tmdbID, ok := titleQuery(c)
	if !ok {
		return
	}
	if err := h.s.DeleteTitle(profileID, mediaType, tmdbID); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to delete progress"})
		return
	}
	c.Status(http.StatusNoContent)
}
