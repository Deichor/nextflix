package streaming

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
)

type handler struct {
	s          *Service
	statClient *http.Client
}

func newHandler(s *Service) handler {
	return handler{s: s, statClient: &http.Client{Timeout: 90 * time.Second}}
}

// files adds a magnet to TorrServer and lists its video files
func (h handler) files(c *gin.Context) {
	magnet := strings.TrimSpace(c.Query("magnet"))
	if !strings.HasPrefix(magnet, "magnet:?") {
		c.JSON(http.StatusBadRequest, gin.H{"error": "a magnet link is required"})
		return
	}

	statURL := h.s.torrServerURL() + "/stream?" + url.Values{"link": {magnet}, "stat": {""}}.Encode()
	req, _ := http.NewRequestWithContext(c.Request.Context(), http.MethodGet, statURL, nil)
	resp, err := h.statClient.Do(req)
	if err != nil {
		c.JSON(http.StatusBadGateway, gin.H{"error": "TorrServer is unreachable: " + err.Error()})
		return
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		c.JSON(http.StatusBadGateway, gin.H{"error": "Could not get torrent info (not enough peers?). Try another source."})
		return
	}

	var stat tsTorrentStat
	if err := json.NewDecoder(resp.Body).Decode(&stat); err != nil {
		c.JSON(http.StatusBadGateway, gin.H{"error": "invalid TorrServer response"})
		return
	}

	files := make([]tsFileStat, 0)
	for _, f := range stat.FileStats {
		if tsVideoExts[strings.ToLower(filepath.Ext(f.Path))] {
			files = append(files, f)
		}
	}
	sort.Slice(files, func(i, j int) bool { return files[i].Path < files[j].Path })

	out := make([]gin.H, 0, len(files))
	for _, f := range files {
		out = append(out, gin.H{"id": f.ID, "path": f.Path, "name": filepath.Base(f.Path), "size": f.Length})
	}
	c.JSON(http.StatusOK, gin.H{"hash": strings.ToLower(stat.Hash), "name": stat.Name, "files": out})
}

// probe returns duration, video codec and audio tracks of a torrent file
func (h handler) probe(c *gin.Context) {
	hash, index, ok := fileParams(c)
	if !ok {
		return
	}
	p, err := h.s.probeFile(c.Request.Context(), hash, index)
	if err != nil {
		c.JSON(http.StatusBadGateway, gin.H{"error": "Could not read the video (not enough peers?)."})
		return
	}

	duration, _ := strconv.ParseFloat(p.Format.Duration, 64)
	videoCodec := ""
	audio := make([]gin.H, 0)
	for _, st := range p.Streams {
		switch st.CodecType {
		case "video":
			if videoCodec == "" {
				videoCodec = st.CodecName
			}
		case "audio":
			audio = append(audio, gin.H{
				"index":    len(audio),
				"language": st.Tags["language"],
				"title":    st.Tags["title"],
				"codec":    st.CodecName,
				"channels": st.Channels,
			})
		}
	}
	c.JSON(http.StatusOK, gin.H{"duration": duration, "video_codec": videoCodec, "audio_tracks": audio})
}

func (h handler) getSettings(c *gin.Context) {
	st, err := h.s.Settings()
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to load indexer settings"})
		return
	}
	c.JSON(http.StatusOK, st)
}

func (h handler) updateSettings(c *gin.Context) {
	var in SettingsInput
	if err := c.ShouldBindJSON(&in); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid request body"})
		return
	}
	st, err := h.s.UpdateSettings(in)
	var serr SettingsError
	switch {
	case errors.As(err, &serr):
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
	case err != nil:
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to update indexer settings"})
	default:
		c.JSON(http.StatusOK, st)
	}
}

func (h handler) testSettings(c *gin.Context) {
	var in TestInput
	_ = c.ShouldBindJSON(&in) // the body is optional
	base, n, err := h.s.TestIndexer(c.Request.Context(), in)
	var serr SettingsError
	switch {
	case errors.As(err, &serr), errors.Is(err, errIndexerNotConfigured):
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error(), "success": false})
	case err != nil:
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": err.Error(), "success": false})
	default:
		c.JSON(http.StatusOK, gin.H{
			"success":      true,
			"message":      "Jackett connection successful",
			"jackett_url":  base,
			"test_results": n,
		})
	}
}
