package streaming

import (
	"context"
	"fmt"
	"math"
	"net/http"
	"os/exec"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
)

// seekPoint answers where a stream started at ?t= really begins. A stream with copied video or
// audio starts on the keyframe at or before t, so the player uses that as its offset to show
// the right time. It takes the same ?hevc=, ?audio=, ?acodecs= (and ?track=, ?split_acodecs=) as play.
func (h handler) seekPoint(c *gin.Context) {
	hash, index, ok := fileParams(c)
	if !ok {
		return
	}
	h.s.touch(hash)
	// A seek point is asked right before a stream starts: its torrent must stay
	h.s.seekBegin(hash)
	defer h.s.seekEnd(hash)
	t, _ := strconv.ParseFloat(c.DefaultQuery("t", "0"), 64)
	if t <= 0 {
		c.JSON(http.StatusOK, gin.H{"t": 0})
		return
	}
	audio, _ := strconv.Atoi(c.DefaultQuery("audio", "0"))
	p, err := h.s.probeFile(c.Request.Context(), hash, index)
	if err != nil || !p.startsOnKeyframe(clientHEVC(c), max(audio, 0), seekAudioCodecs(c)) {
		// Fully re-encoded streams start exactly at t
		c.JSON(http.StatusOK, gin.H{"t": t})
		return
	}
	kf, err := h.s.keyframeBefore(c.Request.Context(), hash, index, t)
	if err != nil || kf > t+1 || t-kf > 60 {
		c.JSON(http.StatusOK, gin.H{"t": t, "exact": false})
		return
	}
	c.JSON(http.StatusOK, gin.H{"t": kf, "exact": true})
}

// keyframeBefore returns the first video packet ffmpeg reads after seeking to t, which is
// the same keyframe the play stream starts from. Also warms TorrServer for that position.
// Times are relative to the file's start_time like ffmpeg's -ss and the play stream ("+t" seeks
// from start_time; the packet time is absolute, so start_time is subtracted).
func (s *Service) keyframeBefore(ctx context.Context, hash string, index int, t float64) (float64, error) {
	ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, "ffprobe", "-v", "error", "-select_streams", "v:0",
		"-read_intervals", "+"+strconv.FormatFloat(t, 'f', 3, 64)+"%+#1",
		"-show_entries", "packet=pts_time:format=start_time", "-of", "csv=p=0", s.fileURL(hash, index)).Output()
	if err != nil {
		return 0, fmt.Errorf("ffprobe failed: %w", err)
	}
	return parseKeyframeProbe(string(out))
}

// parseKeyframeProbe reads keyframeBefore's ffprobe output: the packet's pts_time first, the
// format's start_time last ("N/A" counts as 0).
func parseKeyframeProbe(out string) (float64, error) {
	var lines []string
	for _, l := range strings.Split(out, "\n") {
		if l = strings.TrimSpace(strings.TrimSuffix(strings.TrimSpace(l), ",")); l != "" {
			lines = append(lines, l)
		}
	}
	if len(lines) < 2 {
		return 0, fmt.Errorf("no video packet after the seek")
	}
	kf, err := strconv.ParseFloat(lines[0], 64)
	if err != nil {
		return 0, err
	}
	if start, err := strconv.ParseFloat(lines[len(lines)-1], 64); err == nil {
		kf -= start
	}
	// Round up so a 3-decimal ?t= never lands just before the keyframe (and on the previous one)
	return math.Ceil(kf*1000) / 1000, nil
}
