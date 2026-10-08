package streaming

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"path/filepath"
	"sort"
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
	h.s.touch(tsHashFromMagnet(magnet))
	// A TorBox source is listed from what is known: TorrServer never loads it (debrid.go)
	var debridStat tsTorrentStat
	if ok, err := h.s.debridStat(magnet, &debridStat); ok && err == nil {
		writeFiles(c, debridStat)
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
	writeFiles(c, stat)
}

// writeFiles answers /files with the video files of a torrent.
func writeFiles(c *gin.Context, stat tsTorrentStat) {
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

// probe returns duration, video info (codec, size, HDR, direct play) and audio tracks of a torrent file
func (h handler) probe(c *gin.Context) {
	hash, index, ok := fileParams(c)
	if !ok {
		return
	}
	h.s.touch(hash)
	// The player reads the header right before it plays: background work of other torrents waits
	h.s.probeBegin(hash)
	p, err := h.s.probeFile(c.Request.Context(), hash, index)
	h.s.probeEnd(hash)
	if err != nil {
		c.JSON(http.StatusBadGateway, gin.H{"error": "Could not read the video (not enough peers?)."})
		return
	}

	codecs, splitCodecs := clientAudioCodecs(c), clientSplitAudioCodecs(c)
	audio := make([]gin.H, 0)
	for _, st := range p.Streams {
		if st.CodecType == "audio" {
			n := len(audio)
			audio = append(audio, gin.H{
				"index":        n,
				"language":     st.Tags["language"],
				"title":        st.Tags["title"],
				"codec":        st.CodecName,
				"channels":     st.Channels,
				"default":      st.Disposition["default"] == 1,
				"direct":       p.audioPlan(n, codecs).copy,      // sent untouched to this client (?acodecs=)
				"split_direct": p.audioPlan(n, splitCodecs).copy, // untouched in a track=audio stream (+ ?split_acodecs=)
			})
		}
	}
	// split: play takes track=video|audio (split.go)
	out := gin.H{"duration": p.duration(), "video_codec": "", "audio_tracks": audio, "split": true}
	// With ?type=&tmdb_id= (and season/episode) the player learns whether this is the whole title
	if rt, mismatch, ok := h.s.probeRuntimeCheck(c, hash, index, p.duration()); ok {
		out["expected_runtime"], out["duration_mismatch"] = rt, mismatch
	}
	if v := p.video(); v != nil {
		// direct: the play stream sends the original video (for this client's ?hevc=)
		out["video_codec"], out["width"], out["height"] = v.CodecName, v.Width, v.Height
		out["hdr"], out["dolby_vision"], out["direct"] = v.hdrTransfer() != "", v.dolbyVision(), p.copiesVideo(clientHEVC(c))
	}
	c.JSON(http.StatusOK, out)
}

func (h handler) getSettings(c *gin.Context) {
	st, err := h.s.Settings()
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to load indexer settings"})
		return
	}
	c.JSON(http.StatusOK, st.View())
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
		c.JSON(http.StatusOK, st.View())
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
