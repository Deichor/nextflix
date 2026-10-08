package streaming

import (
	"fmt"
	"net/http"
	"regexp"
	"strconv"
	"strings"

	"github.com/gin-gonic/gin"
)

// Watch-while-downloading: TorrServer fetches torrent pieces on demand,
// ffmpeg turns the file into browser-friendly fragmented MP4 (H.264 + AAC).

var (
	tsHashPattern = regexp.MustCompile(`^[a-fA-F0-9]{40}$`)
	tsVideoExts   = map[string]bool{".mp4": true, ".mkv": true, ".avi": true, ".webm": true, ".mov": true, ".m4v": true, ".ts": true, ".wmv": true, ".flv": true}
)

type tsFileStat struct {
	ID     int    `json:"id"`
	Path   string `json:"path"`
	Length int64  `json:"length"`
}

type tsTorrentStat struct {
	Hash      string       `json:"hash"`
	Name      string       `json:"name"`
	FileStats []tsFileStat `json:"file_stats"`
}

// fileURL is where ffmpeg / ffprobe read a file: TorrServer, or for a debrid source the
// TorBox proxy (debrid.go).
func (s *Service) fileURL(hash string, index int) string {
	if u, ok := s.debridURL(hash, index); ok {
		return u
	}
	return fmt.Sprintf("%s/stream?link=%s&index=%d&play", s.torrServerURL(), hash, index)
}

// fileParams reads ?hash=&index= and answers 400 when they are invalid.
func fileParams(c *gin.Context) (string, int, bool) {
	hash := strings.ToLower(c.Query("hash"))
	index, err := strconv.Atoi(c.Query("index"))
	if !tsHashPattern.MatchString(hash) || err != nil || index < 0 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "valid hash and index are required"})
		return "", 0, false
	}
	return hash, index, true
}
