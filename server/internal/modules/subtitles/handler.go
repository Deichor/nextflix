package subtitles

import (
	"errors"
	"log"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
)

type handler struct{ s *Service }

func (h handler) list(c *gin.Context) {
	q := Query{
		MediaType: c.DefaultQuery("media_type", "movie"),
		Hash:      strings.ToLower(strings.TrimSpace(c.Query("hash"))),
		Title:     c.Query("title"),
		Langs:     parseLangs(c.Query("langs")),
		UILang:    normLang(c.Query("ui_lang")),
	}
	q.TmdbID, _ = strconv.Atoi(c.Query("tmdb_id"))
	q.Season, _ = strconv.Atoi(c.Query("season"))
	q.Episode, _ = strconv.Atoi(c.Query("episode"))
	q.Index, _ = strconv.Atoi(c.Query("index"))
	q.Year, _ = strconv.Atoi(c.Query("year"))
	q.Refresh = c.Query("refresh") == "1"

	if q.MediaType != "movie" && q.MediaType != "tv" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "media_type must be movie or tv"})
		return
	}
	if q.Hash != "" && !infoHashRe.MatchString(q.Hash) {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid hash"})
		return
	}
	if q.TmdbID <= 0 && q.Hash == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "tmdb_id or hash is required"})
		return
	}
	if q.MediaType == "tv" && q.TmdbID > 0 && (q.Season < 0 || q.Episode <= 0) {
		c.JSON(http.StatusBadRequest, gin.H{"error": "season and episode are required for tv"})
		return
	}
	if len(q.Title) > 300 {
		q.Title = q.Title[:300]
	}
	items := h.s.List(c.Request.Context(), q)
	if items == nil {
		items = []Sub{}
	}
	out := gin.H{"items": items}
	if until := h.s.OpenSubtitlesQuotaUntil(); !until.IsZero() {
		out["opensubtitles_quota_until"] = until.UTC().Format(time.RFC3339)
	}
	c.JSON(http.StatusOK, out)
}

func (h handler) file(c *gin.Context) {
	if c.Query("pre") == "1" {
		// Pre-timing (the next episode): the timing starts, nothing is served or noted as played
		state, err := h.s.PreTime(c.Request.Context(), c.Query("id"))
		if fileError(c, err) {
			return
		}
		c.Header("X-Subtitle-Sync", state)
		c.Status(http.StatusNoContent)
		return
	}
	t, _ := strconv.ParseFloat(c.Query("t"), 64) // the player's position (live embedded tracks, timing logs)
	a, err := h.s.FileFor(c.Request.Context(), c.Query("id"), t)
	vtt, partial := a.VTT, a.Partial
	if fileError(c, err) {
		return
	}
	// Every answer names its timing state; original: not timed (made for the file, or no file named)
	state := a.Sync
	if state == "" {
		state = syncStateOriginal
	}
	c.Header("X-Subtitle-Sync", state)
	if a.Holes != "" {
		c.Header("X-Subtitle-Holes", a.Holes)
	}
	if partial {
		// More cues follow while the file plays; the body also carries PartialNote
		c.Header("Cache-Control", "no-store")
		c.Header("X-Subtitle-Partial", "1")
		c.Data(http.StatusOK, "text/vtt; charset=utf-8", vtt)
		return
	}
	if a.Sync == syncStateFailed || a.Sync == syncStateLive || a.Sync == syncStatePending {
		// The timing is tried again or improves later: no long browser cache
		c.Header("Cache-Control", "no-store")
	} else {
		c.Header("Cache-Control", "private, max-age=86400")
	}
	c.Data(http.StatusOK, "text/vtt; charset=utf-8", vtt)
}

// fileError answers the error of a subtitle file request; false when there is none.
func fileError(c *gin.Context, err error) bool {
	var qe QuotaError
	var ke AIKeyError
	switch {
	case err == nil:
		return false
	case errors.As(err, &qe):
		c.JSON(http.StatusTooManyRequests, gin.H{"error": "OpenSubtitles download quota used up", "quota": true,
			"reset_at": qe.Until.UTC().Format(time.RFC3339)})
	case errors.Is(err, ErrBadID):
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid subtitle id"})
	case errors.Is(err, ErrAIOff):
		c.JSON(http.StatusConflict, gin.H{"error": err.Error(), "ai": true})
	case errors.As(err, &ke):
		c.JSON(http.StatusBadGateway, gin.H{"error": ke.Error(), "ai": true, "error_kind": ke.Kind})
	case errors.Is(err, ErrRateLimited):
		c.JSON(http.StatusTooManyRequests, gin.H{"error": err.Error()})
	default:
		log.Printf("subtitles: file: %v", err)
		c.JSON(http.StatusBadGateway, gin.H{"error": err.Error()})
	}
	return true
}

func (h handler) getSettings(c *gin.Context) {
	c.JSON(http.StatusOK, h.s.Settings())
}

func (h handler) putSettings(c *gin.Context) {
	var in SettingsInput
	if err := c.ShouldBindJSON(&in); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid request body"})
		return
	}
	v, err := h.s.UpdateSettings(in)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to save subtitle settings"})
		return
	}
	c.JSON(http.StatusOK, v)
}

// testAI checks a Claude API key (the body's, else the stored one) with a tiny request.
func (h handler) testAI(c *gin.Context) {
	var in struct {
		APIKey string `json:"api_key"`
	}
	_ = c.ShouldBindJSON(&in)
	if err := h.s.TestAIKey(c.Request.Context(), in.APIKey); err != nil {
		c.JSON(http.StatusOK, gin.H{"ok": false, "error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"ok": true})
}
