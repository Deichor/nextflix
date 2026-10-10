package ratings

import (
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/gin-gonic/gin"
)

type handler struct{ s *Service }

// optionalRating tells an absent "rating" apart from an explicit null.
type optionalRating struct {
	Set   bool
	Value *string
}

func (o *optionalRating) UnmarshalJSON(b []byte) error {
	o.Set = true
	if string(b) == "null" {
		o.Value = nil
		return nil
	}
	var v string
	if err := json.Unmarshal(b, &v); err != nil {
		return err
	}
	if v = strings.TrimSpace(v); v == "" {
		o.Value = nil
	} else {
		o.Value = &v
	}
	return nil
}

type putInput struct {
	ProfileID    uint           `json:"profile_id"`
	MediaType    string         `json:"media_type"`
	TmdbID       int            `json:"tmdb_id"`
	Title        string         `json:"title"`
	PosterPath   string         `json:"poster_path"`
	BackdropPath string         `json:"backdrop_path"`
	Rating       optionalRating `json:"rating"`
	Favorite     *bool          `json:"favorite"`
}

// profileFrom reads profile_id from raw, falling back to the X-User-ID header.
func profileFrom(c *gin.Context, raw string) uint {
	raw = strings.TrimSpace(raw)
	if raw == "" || raw == "0" {
		raw = strings.TrimSpace(c.GetHeader("X-User-ID"))
	}
	id, err := strconv.ParseUint(raw, 10, 64)
	if err != nil {
		return 0
	}
	return uint(id)
}

func queryProfile(c *gin.Context) (uint, bool) {
	id := profileFrom(c, c.Query("profile_id"))
	if id == 0 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "profile_id is required"})
		return 0, false
	}
	return id, true
}

func truthy(v string) bool {
	switch strings.ToLower(strings.TrimSpace(v)) {
	case "1", "true", "yes":
		return true
	}
	return false
}

func (h handler) list(c *gin.Context) {
	profileID, ok := queryProfile(c)
	if !ok {
		return
	}
	f := Filter{FavoriteOnly: truthy(c.Query("favorite")), Rating: strings.TrimSpace(c.Query("rating"))}
	if f.Rating != "" && !validRating(f.Rating) {
		c.JSON(http.StatusBadRequest, gin.H{"error": "rating must be dislike, like or love"})
		return
	}
	h.respondList(c, profileID, f)
}

func (h handler) favorites(c *gin.Context) {
	profileID, ok := queryProfile(c)
	if !ok {
		return
	}
	h.respondList(c, profileID, Filter{FavoriteOnly: true})
}

func (h handler) respondList(c *gin.Context, profileID uint, f Filter) {
	rows, err := h.s.List(profileID, f)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to load ratings"})
		return
	}
	c.JSON(http.StatusOK, rows)
}

func (h handler) item(c *gin.Context) {
	profileID, ok := queryProfile(c)
	if !ok {
		return
	}
	mediaType := c.Query("media_type")
	tmdbID, err := strconv.Atoi(c.Query("tmdb_id"))
	if !validMediaType(mediaType) || err != nil || tmdbID <= 0 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "media_type and tmdb_id are required"})
		return
	}
	r, err := h.s.Item(profileID, mediaType, tmdbID)
	if errors.Is(err, ErrNotFound) {
		c.JSON(http.StatusNotFound, gin.H{"error": err.Error()})
		return
	}
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to load rating"})
		return
	}
	c.JSON(http.StatusOK, r)
}

func (h handler) put(c *gin.Context) {
	var in putInput
	if err := json.NewDecoder(c.Request.Body).Decode(&in); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid request body"})
		return
	}
	if in.ProfileID == 0 {
		in.ProfileID = profileFrom(c, "")
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
	if utf8.RuneCountInString(in.Title) > maxTitleLen || len(in.PosterPath) > maxPathLen || len(in.BackdropPath) > maxPathLen {
		c.JSON(http.StatusBadRequest, gin.H{"error": "title or image path too long"})
		return
	}
	if in.Rating.Value != nil && !validRating(*in.Rating.Value) {
		c.JSON(http.StatusBadRequest, gin.H{"error": "rating must be dislike, like, love or null"})
		return
	}

	saved, err := h.s.Put(Update{
		ProfileID:    in.ProfileID,
		MediaType:    in.MediaType,
		TmdbID:       in.TmdbID,
		Title:        in.Title,
		PosterPath:   in.PosterPath,
		BackdropPath: in.BackdropPath,
		RatingSet:    in.Rating.Set,
		Rating:       in.Rating.Value,
		Favorite:     in.Favorite,
	})
	if errors.Is(err, ErrUnknownProfile) {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to save rating"})
		return
	}
	c.JSON(http.StatusOK, saved)
}
