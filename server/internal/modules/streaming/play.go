package streaming

import (
	"fmt"
	"log"
	"net/http"
	"os/exec"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
)

// play streams a torrent file as fragmented MP4, starting at ?t= seconds
func (h handler) play(c *gin.Context) {
	hash, index, ok := fileParams(c)
	if !ok {
		return
	}
	start, _ := strconv.ParseFloat(c.DefaultQuery("t", "0"), 64)
	audio, _ := strconv.Atoi(c.DefaultQuery("audio", "0"))
	if start < 0 {
		start = 0
	}
	if audio < 0 {
		audio = 0
	}

	// Browsers play 8-bit H.264 directly, so only the container changes; everything else is re-encoded
	copyVideo := false
	if p, err := h.s.probeFile(c.Request.Context(), hash, index); err == nil {
		for _, st := range p.Streams {
			if st.CodecType == "video" {
				copyVideo = st.CodecName == "h264" && (st.PixFmt == "yuv420p" || st.PixFmt == "yuvj420p")
				break
			}
		}
	}

	args := []string{"-hide_banner", "-loglevel", "error"}
	if start > 0 {
		args = append(args, "-ss", strconv.FormatFloat(start, 'f', 3, 64))
	}
	args = append(args, "-i", h.s.fileURL(hash, index),
		"-map", "0:v:0", "-map", fmt.Sprintf("0:a:%d?", audio), "-sn", "-dn")
	if copyVideo {
		args = append(args, "-c:v", "copy")
	} else {
		args = append(args, "-c:v", "libx264", "-preset", "veryfast", "-crf", "22",
			"-pix_fmt", "yuv420p", "-vf", "scale='min(1920,iw)':-2")
	}
	// 1s fragments so playback starts fast even when source keyframes are far apart
	args = append(args, "-c:a", "aac", "-b:a", "192k", "-ac", "2",
		"-movflags", "frag_keyframe+empty_moov+default_base_moof", "-frag_duration", "1000000",
		"-f", "mp4", "pipe:1")

	// A movie stream outlives the server-wide write timeout
	if err := http.NewResponseController(c.Writer).SetWriteDeadline(time.Time{}); err != nil {
		log.Printf("torrent stream: could not clear write deadline: %v", err)
	}

	c.Header("Content-Type", "video/mp4")
	c.Header("Cache-Control", "no-store")
	c.Status(http.StatusOK)

	// The request context is cancelled when the viewer closes or seeks, which stops ffmpeg
	cmd := exec.CommandContext(c.Request.Context(), "ffmpeg", args...)
	cmd.Stdout = c.Writer
	var stderr strings.Builder
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil && c.Request.Context().Err() == nil {
		log.Printf("torrent stream ffmpeg error (%s:%d): %v %s", hash, index, err, strings.TrimSpace(stderr.String()))
	}
}
