package streaming

import (
	"context"
	"fmt"
	"io"
	"log"
	"math"
	"net/http"
	"os"
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
	start, _ := strconv.ParseFloat(c.DefaultQuery("t", "0"), 64)
	if math.IsNaN(start) || start > tsThumbMaxT {
		c.JSON(http.StatusBadRequest, gin.H{"error": fmt.Sprintf("t must be a time in seconds (0-%d)", tsThumbMaxT)})
		return
	}
	h.s.adoptSaved(hash) // a saved TorBox source after a restart plays from TorBox
	// A playing torrent is never dropped by the warm-up bookkeeping
	h.s.playBegin(hash)
	defer h.s.playEnd(hash)
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
	if c.Query("pace") == "1" {
		args = pacedInput(args)
	}

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

	// The dialogue of the played audio also goes to the subtitle timing (speechtap.go)
	var sink SpeechSink
	var tapArgs []string
	if track != tsTrackVideo && h.s.speech != nil {
		if a := probe.audioStream(audio); a != nil {
			if sink = h.s.speech.Open(hash, index, start); sink != nil {
				tapArgs = speechTapArgs(audio, a)
			}
		}
	}

	// The request context is cancelled when the viewer closes or seeks, which stops ffmpeg
	out := &tsFirstWrite{w: c.Writer, first: func() { h.s.playFlows(hash) }}
	var stderr strings.Builder
	label := fmt.Sprintf("%s:%d", tsShortHash(hash), index)
	if err := tsRunPlay(c.Request.Context(), args, tapArgs, sink, out, &stderr, label); err != nil && c.Request.Context().Err() == nil {
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
	wrote bool
}

func (f *tsFirstWrite) Write(b []byte) (int, error) {
	if len(b) > 0 && !f.wrote {
		f.wrote = true
		if f.first != nil {
			f.first()
		}
	}
	return f.w.Write(b)
}

// streamInput is the input side of a play stream up to -i. The seek point answer runs the same
// options, so both land on the same keyframe: without AVFMT_SEEK_TO_PTS (Matroska, MPEG-TS) and
// with B-frames ffmpeg seeks to t - 0.13 s, and ffprobe's seek differs from ffmpeg's.
func streamInput(src string, start float64, plan tsVideoPlan, aplan tsAudioPlan, smallProbe bool) []string {
	args := append([]string{"-hide_banner", "-loglevel", "error"}, tsInputGuard()...)
	args = append(args, "-fflags", "+genpts",
		// TorrServer may drop the connection while it waits for pieces
		"-reconnect", "1", "-reconnect_on_network_error", "1", "-reconnect_delay_max", "5")
	if smallProbe {
		// Streams are already known from the probe, so restarts after a seek can open faster
		args = append(args, "-probesize", "2000000", "-analyzeduration", "2000000")
	}
	if start > 0 {
		if plan.copy || aplan.copy {
			// Copied streams start on the keyframe before ?t= (copied audio is never trimmed).
			// Accurate seeking would start the re-encoded stream at t instead, leaving a hole at
			// the start that browsers handle badly, so both start at the keyframe (see seekPoint).
			args = append(args, "-noaccurate_seek")
		}
		args = append(args, "-ss", strconv.FormatFloat(start, 'f', 3, 64))
	}
	return append(args, "-i", src)
}

// pacedInput reads the input at playback speed after a 30 s burst (and faster while behind, after a
// slow read). For players that fetch the stream themselves (Safari's MediaSource feed): WebKit keeps
// downloading whatever the server sends, so an unpaced stream piles up in the phone's memory.
func pacedInput(args []string) []string {
	for i, a := range args {
		if a == "-i" {
			out := append([]string{}, args[:i]...)
			out = append(out, "-readrate", "1", "-readrate_initial_burst", "30")
			return append(out, args[i:]...)
		}
	}
	return args
}

func playArgs(src string, start float64, audio int, plan tsVideoPlan, aplan tsAudioPlan, smallProbe bool) []string {
	return streamArgs(src, start, audio, plan, aplan, smallProbe, tsTrackBoth)
}

// streamArgs builds the ffmpeg command of a play stream: muxed (tsTrackBoth) or one elementary
// track of a split pair (see split.go). The input side is the same for every track.
func streamArgs(src string, start float64, audio int, plan tsVideoPlan, aplan tsAudioPlan, smallProbe bool, track tsTrack) []string {
	args := streamInput(src, start, plan, aplan, smallProbe)
	ss := strconv.FormatFloat(start, 'f', 3, 64)
	switch track {
	case tsTrackVideo:
		args = append(args, "-map", "0:v:0", "-an", "-sn", "-dn", "-map_chapters", "-1")
	case tsTrackAudio:
		args = append(args, "-map", fmt.Sprintf("0:a:%d", audio), "-vn", "-sn", "-dn", "-map_chapters", "-1")
	default:
		// No chapter text track: MediaSource players (Safari) may reject it; chapters come from the probe
		args = append(args, "-map", "0:v:0", "-map", fmt.Sprintf("0:a:%d?", audio), "-sn", "-dn", "-map_chapters", "-1")
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
			// Frames keep the source's times: vfr and a 90 kHz encoder time base (the default
			// constant rate and 1/fps time base moved them onto a frame grid from t, up to 1.5
			// frames late)
			args = append(args, "-c:v", "libx264", "-preset", "veryfast", "-crf", "22",
				"-pix_fmt", "yuv420p", "-vf", vf, "-fps_mode", "vfr", "-enc_time_base:v", "1/90000")
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
		// make_zero shifts all streams together so the output starts at 0: time 0 is the first
		// video packet's decode time (B-frames: before the keyframe is shown, see seekPoint). No
		// edit lists: Chromium ignores an empty first edit (a track's start delay), Firefox does
		// not; without them the muxer moves a later track's first sample to 0 in both.
		// 1s fragments so playback starts fast even when source keyframes are far apart.
		return append(args, "-avoid_negative_ts", "make_zero", "-use_editlist", "0",
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
