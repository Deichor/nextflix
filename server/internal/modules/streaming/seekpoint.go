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

// tsEncoderDelay: frames between the first decode time and the first frame shown of the libx264
// re-encode (veryfast: B-frames with pyramid, has_b_frames = 2).
const tsEncoderDelay = 2

// seekPoint answers where a stream started at ?t= really begins on the file's timeline. Muxed
// streams: the file time of the stream's time 0, so position = answer + the element's time.
// Streams with copied video or audio start on the keyframe at or before t, and time 0 is the
// first packet's decode time (B-frames: up to two frames before that keyframe is shown). Split
// streams (?track=) carry file times; the answer is where their video starts (the keyframe).
// It takes the same ?hevc=, ?audio=, ?acodecs= (and ?track=, ?split_acodecs=) as play.
func (h handler) seekPoint(c *gin.Context) {
	hash, index, ok := fileParams(c)
	if !ok {
		return
	}
	// Negative times count as 0, as in play
	t, err := strconv.ParseFloat(c.DefaultQuery("t", "0"), 64)
	if err != nil || math.IsNaN(t) || t > tsThumbMaxT {
		c.JSON(http.StatusBadRequest, gin.H{"error": fmt.Sprintf("t must be a time in seconds (0-%d)", tsThumbMaxT)})
		return
	}
	t = max(t, 0)
	h.s.touch(hash)
	h.s.adoptSaved(hash) // a saved TorBox source after a restart plays from TorBox
	// A seek point is asked right before a stream starts: its torrent must stay
	h.s.seekBegin(hash)
	defer h.s.seekEnd(hash)
	track, _ := playTrack(c)
	if t <= 0 && track != tsTrackBoth {
		c.JSON(http.StatusOK, gin.H{"t": 0})
		return
	}
	audio, _ := strconv.Atoi(c.DefaultQuery("audio", "0"))
	audio = max(audio, 0)
	p, err := h.s.probeFile(c.Request.Context(), hash, index)
	if err != nil {
		c.JSON(http.StatusOK, gin.H{"t": t})
		return
	}
	hevc, codecs := clientHEVC(c), seekAudioCodecs(c)
	plan, aplan := p.videoPlan(hevc), p.audioPlan(audio, codecs)
	if !p.startsOnKeyframe(hevc, audio, codecs) {
		// Fully re-encoded streams start at t (accurate seek); time 0 of a muxed one is the
		// encoder's first decode time
		at := t
		if track == tsTrackBoth {
			at -= tsEncoderDelay * p.frameDuration()
		}
		c.JSON(http.StatusOK, gin.H{"t": tsMilli(at)})
		return
	}
	first, err := h.s.firstPackets(c.Request.Context(), hash, index, t, audio, plan, aplan, p.indexedContainer())
	at := first.video.pts
	if track == tsTrackBoth {
		// make_zero: the earliest decode time of the first packets is time 0
		at = first.video.dts
		if !plan.copy {
			// The re-encode starts with the keyframe's picture at t (a later t: the frames
			// before t are squashed into it), shown tsEncoderDelay frames after time 0
			at = -tsEncoderDelay * p.frameDuration()
			if t <= 0 {
				at += first.video.pts
			}
		}
		if first.hasAudio {
			at = min(at, first.audio.dts)
		}
	}
	at = tsMilli(t + at)
	if err != nil || at > t+1 || t-at > 60 {
		c.JSON(http.StatusOK, gin.H{"t": t, "exact": false})
		return
	}
	c.JSON(http.StatusOK, gin.H{"t": at, "exact": true})
}

// tsMilli rounds a seek point up to ms, so a 3-decimal ?t= of a keyframe never lands just before it.
func tsMilli(v float64) float64 { return math.Ceil(v*1000-1e-6) / 1000 }

// tsPacketTime is a packet's time on a play stream's timeline (seconds; file time - t).
type tsPacketTime struct{ pts, dts float64 }

// tsFirstPackets are the first video packet (a keyframe) and audio packet of a play stream.
type tsFirstPackets struct {
	video, audio tsPacketTime
	hasAudio     bool
}

// firstPackets copies the first video and audio packet after the same seek as play
// (streamInput): the video one is the keyframe a stream requested at t starts on. With ffprobe's
// seek a t up to 0.13 s after a keyframe answered that keyframe while play started on the one
// before (positions a whole GOP off: ffmpeg seeks Matroska / MPEG-TS with B-frames to
// t - 0.13 s), and MPEG-TS seeks differed both ways. Also warms TorrServer for that position.
func (s *Service) firstPackets(ctx context.Context, hash string, index int, t float64, audio int, plan tsVideoPlan, aplan tsAudioPlan, smallProbe bool) (tsFirstPackets, error) {
	ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	// Both streams mapped as in play (the demuxer's seek depends on them). -frames:a would drop the
	// video packet (ffmpeg 7.1), so the read stops 0.5 s past t: from the keyframe on, what play reads next.
	args := append(streamInput(s.fileURL(hash, index), t, plan, aplan, smallProbe),
		"-map", "0:v:0", "-map", fmt.Sprintf("0:a:%d?", audio), "-c", "copy", "-sn", "-dn",
		"-frames:v", "1", "-t", "0.5", "-avoid_negative_ts", "disabled", "-f", "framecrc", "pipe:1")
	out, err := exec.CommandContext(ctx, tsFFmpegBin, args...).Output()
	if err != nil {
		return tsFirstPackets{}, fmt.Errorf("ffmpeg failed: %w", err)
	}
	return parseFrameCRC(string(out))
}

// parseFrameCRC reads the first packet of stream 0 (video) and 1 (audio) from ffmpeg's framecrc
// output ("#tb 0: 1/1000" lines, then "0, dts, pts, duration, size, crc").
func parseFrameCRC(out string) (tsFirstPackets, error) {
	var res tsFirstPackets
	tb := map[string][2]float64{}
	seen := map[string]bool{}
	for _, l := range strings.Split(out, "\n") {
		l = strings.TrimSpace(l)
		if rest, ok := strings.CutPrefix(l, "#tb "); ok {
			id, v, _ := strings.Cut(rest, ":")
			a, b, _ := strings.Cut(strings.TrimSpace(v), "/")
			num, _ := strconv.ParseFloat(a, 64)
			den, _ := strconv.ParseFloat(b, 64)
			tb[strings.TrimSpace(id)] = [2]float64{num, den}
			continue
		}
		if l == "" || l[0] == '#' {
			continue
		}
		f := strings.Split(l, ",")
		id := strings.TrimSpace(f[0])
		if len(f) < 3 || seen[id] || tb[id][1] <= 0 || (id != "0" && id != "1") {
			continue
		}
		seen[id] = true
		dts, err1 := strconv.ParseInt(strings.TrimSpace(f[1]), 10, 64)
		pts, err2 := strconv.ParseInt(strings.TrimSpace(f[2]), 10, 64)
		if err1 != nil || err2 != nil {
			return res, fmt.Errorf("bad framecrc line %q", l)
		}
		// dts is never after pts
		p := tsPacketTime{pts: float64(pts) * tb[id][0] / tb[id][1], dts: float64(min(dts, pts)) * tb[id][0] / tb[id][1]}
		if id == "0" {
			res.video = p
		} else {
			res.audio, res.hasAudio = p, true
		}
	}
	if !seen["0"] {
		return res, fmt.Errorf("no video packet after the seek")
	}
	return res, nil
}
