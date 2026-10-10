package streaming

import (
	"context"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
)

// play streams a torrent file as fragmented MP4, starting at ?t= seconds.
// ?hevc=1 means the browser decodes HEVC, so HEVC sources (4K, 10-bit, HDR) are copied too.
// ?acodecs= lists the audio codecs the browser plays in MP4; such audio tracks are copied.
// ?track=video|audio sends one elementary stream on the file's timeline (split mode, split.go).
func (h handler) play(c *gin.Context) {
	hash, index, ok := fileParams(c)
	if !ok {
		return
	}
	track, ok := playTrack(c)
	if !ok {
		c.JSON(http.StatusBadRequest, gin.H{"error": "track must be video or audio"})
		return
	}
	// A playing torrent is never dropped by the warm-up bookkeeping
	h.s.playBegin(hash)
	defer h.s.playEnd(hash)
	start, _ := strconv.ParseFloat(c.DefaultQuery("t", "0"), 64)
	audio, _ := strconv.Atoi(c.DefaultQuery("audio", "0"))
	if start < 0 {
		start = 0
	}
	if audio < 0 {
		audio = 0
	}

	// Streams the browser can decode only change container; everything else is re-encoded
	var plan tsVideoPlan
	var probe *tsProbe
	smallProbe := false
	if p, err := h.s.probeFile(c.Request.Context(), hash, index); err == nil {
		probe = p
		plan = p.videoPlan(clientHEVC(c))
		smallProbe = p.indexedContainer()
	}
	aplan := probe.audioPlan(audio, playAudioCodecs(c, track))
	args := streamArgs(h.s.fileURL(hash, index), start, audio, plan, aplan, smallProbe, track)

	// A video re-encode takes a transcode slot; when all stay busy the player gets 503
	if track != tsTrackAudio && !plan.copy {
		release, ok := tsTranscodeSlot(c.Request.Context(), tsTranscodeWait)
		if !ok {
			if c.Request.Context().Err() == nil {
				log.Printf("torrent stream (%s:%d): all %d transcode slots busy", tsShortHash(hash), index, cap(tsTranscodes))
				c.Header("Retry-After", "5")
				c.JSON(http.StatusServiceUnavailable, gin.H{"error": "too many streams are being converted right now"})
			}
			return
		}
		defer release()
	}

	// A movie stream outlives the server-wide write timeout
	if err := http.NewResponseController(c.Writer).SetWriteDeadline(time.Time{}); err != nil {
		log.Printf("torrent stream: could not clear write deadline: %v", err)
	}

	if track == tsTrackAudio {
		c.Header("Content-Type", "audio/mp4")
	} else {
		c.Header("Content-Type", "video/mp4")
	}
	c.Header("Cache-Control", "no-store")
	c.Status(http.StatusOK)

	// The request context is cancelled when the viewer closes or seeks, which stops ffmpeg
	cmd := exec.CommandContext(c.Request.Context(), "ffmpeg", args...)
	cmd.Stdout = &tsFirstWrite{w: c.Writer, first: func() { h.s.playFlows(hash) }}
	var stderr strings.Builder
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil && c.Request.Context().Err() == nil {
		log.Printf("torrent stream ffmpeg error (%s:%d): %v %s", hash, index, err, strings.TrimSpace(stderr.String()))
	}
}

// tsTranscodes limits the video re-encodes (libx264) that run at once, server-wide.
// STREAM_MAX_TRANSCODES overrides the default of 3.
var tsTranscodes = make(chan struct{}, tsMaxTranscodes())

// tsTranscodeWait: a stream waits this long for a slot (a seek restarts the stream while the
// old one is still ending).
var tsTranscodeWait = 3 * time.Second

func tsMaxTranscodes() int {
	if n, err := strconv.Atoi(strings.TrimSpace(os.Getenv("STREAM_MAX_TRANSCODES"))); err == nil && n > 0 {
		return n
	}
	return 3
}

// tsTranscodeSlot takes a transcode slot, waiting up to wait; ok is false when none freed up.
func tsTranscodeSlot(ctx context.Context, wait time.Duration) (release func(), ok bool) {
	slots := tsTranscodes
	timer := time.NewTimer(wait)
	defer timer.Stop()
	select {
	case slots <- struct{}{}:
		return func() { <-slots }, true
	case <-timer.C:
	case <-ctx.Done():
	}
	return nil, false
}

// tsFirstWrite calls first once, on the first bytes written to w.
type tsFirstWrite struct {
	w     io.Writer
	first func()
}

func (f *tsFirstWrite) Write(b []byte) (int, error) {
	if f.first != nil && len(b) > 0 {
		f.first()
		f.first = nil
	}
	return f.w.Write(b)
}

func playArgs(src string, start float64, audio int, plan tsVideoPlan, aplan tsAudioPlan, smallProbe bool) []string {
	return streamArgs(src, start, audio, plan, aplan, smallProbe, tsTrackBoth)
}

// streamArgs builds the ffmpeg command of a play stream: muxed (tsTrackBoth) or one elementary
// track of a split pair (see split.go). The input side is the same for every track.
func streamArgs(src string, start float64, audio int, plan tsVideoPlan, aplan tsAudioPlan, smallProbe bool, track tsTrack) []string {
	args := append([]string{"-hide_banner", "-loglevel", "error"}, tsInputGuard()...)
	args = append(args, "-fflags", "+genpts",
		// TorrServer may drop the connection while it waits for pieces
		"-reconnect", "1", "-reconnect_on_network_error", "1", "-reconnect_delay_max", "5")
	if smallProbe {
		// Streams are already known from the probe, so restarts after a seek can open faster
		args = append(args, "-probesize", "2000000", "-analyzeduration", "2000000")
	}
	ss := strconv.FormatFloat(start, 'f', 3, 64)
	if start > 0 {
		if plan.copy || aplan.copy {
			// Copied streams start on the keyframe before ?t= (copied audio is never trimmed).
			// Accurate seeking would start the re-encoded stream at t instead, leaving a hole at
			// the start that browsers handle badly, so both start at the keyframe (see seekPoint).
			args = append(args, "-noaccurate_seek")
		}
		args = append(args, "-ss", ss)
	}
	args = append(args, "-i", src)
	switch track {
	case tsTrackVideo:
		args = append(args, "-map", "0:v:0", "-an", "-sn", "-dn", "-map_chapters", "-1")
	case tsTrackAudio:
		args = append(args, "-map", fmt.Sprintf("0:a:%d", audio), "-vn", "-sn", "-dn", "-map_chapters", "-1")
	default:
		args = append(args, "-map", "0:v:0", "-map", fmt.Sprintf("0:a:%d?", audio), "-sn", "-dn")
	}
	if track != tsTrackAudio {
		switch {
		case plan.copy && plan.hevc:
			// Browsers only accept HEVC in MP4 with the hvc1 tag (parameter sets in the header)
			args = append(args, "-c:v", "copy", "-tag:v", "hvc1")
		case plan.copy:
			args = append(args, "-c:v", "copy")
		default:
			vf := "scale='min(1920,iw)':-2"
			if plan.tonemap != "" {
				// HDR to SDR, otherwise 10-bit PQ / HLG video looks washed out as 8-bit H.264
				vf += ",zscale=tin=" + plan.tonemap + ":min=bt2020nc:pin=bt2020:t=linear:npl=100,format=gbrpf32le," +
					"zscale=p=bt709,tonemap=tonemap=hable:desat=0,zscale=t=bt709:m=bt709:r=tv"
			}
			args = append(args, "-c:v", "libx264", "-preset", "veryfast", "-crf", "22",
				"-pix_fmt", "yuv420p", "-vf", vf)
		}
	}
	if track != tsTrackVideo {
		args = append(args, aplan.args()...)
	}
	movflags := "frag_keyframe+empty_moov+default_base_moof"
	if track != tsTrackVideo && aplan.copy && aplan.delay {
		movflags += "+delay_moov"
	}
	if track == tsTrackBoth {
		// make_zero shifts all streams together so the output starts at 0 without edit lists.
		// 1s fragments so playback starts fast even when source keyframes are far apart.
		return append(args, "-avoid_negative_ts", "make_zero",
			"-movflags", movflags, "-frag_duration", "1000000",
			"-f", "mp4", "pipe:1")
	}
	// Split: the file's own timestamps. output_ts_offset undoes the input offset of -ss; tfdt is only
	// absolute with frag_discont + negative_cts_offsets and no edit list (ffmpeg 5.1 starts at 0
	// otherwise). Audio only: make_non_negative moves encoder priming at t=0 (a few ms) above zero;
	// video keeps its timestamps (negative_cts_offsets keeps B-frame delays off the decode time).
	if start > 0 {
		args = append(args, "-output_ts_offset", ss)
	}
	if track == tsTrackAudio {
		args = append(args, "-avoid_negative_ts", "make_non_negative")
	}
	return append(args, "-use_editlist", "0",
		"-movflags", movflags+"+frag_discont+negative_cts_offsets", "-frag_duration", "1000000",
		"-f", "mp4", "pipe:1")
}
