package streaming

import (
	"context"
	"fmt"
	"math"
	"net/http"
	"net/http/httptest"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
)

func TestStreamArgsSplit(t *testing.T) {
	hevc := tsVideoPlan{copy: true, hevc: true}
	eac3 := tsAudioPlan{copy: true, delay: true}
	video := strings.Join(streamArgs("http://ts/f", 300, 1, hevc, eac3, true, tsTrackVideo), " ")
	audio := strings.Join(streamArgs("http://ts/f", 300, 1, hevc, eac3, true, tsTrackAudio), " ")
	muxed := strings.Join(streamArgs("http://ts/f", 300, 1, hevc, eac3, true, tsTrackBoth), " ")

	// Both tracks seek the same way, so they start at the same keyframe
	in := "-noaccurate_seek -ss 300.000 -i http://ts/f"
	for name, args := range map[string]string{"video": video, "audio": audio} {
		if !strings.Contains(args, in) {
			t.Errorf("%s: input side differs: %s", name, args)
		}
		for _, want := range []string{"-output_ts_offset 300.000", "-use_editlist 0", "+frag_discont+negative_cts_offsets"} {
			if !strings.Contains(args, want) {
				t.Errorf("%s: missing %q: %s", name, want, args)
			}
		}
		if strings.Contains(args, "make_zero") {
			t.Errorf("%s: split streams keep the file's timestamps: %s", name, args)
		}
	}
	if !strings.Contains(video, "-map 0:v:0 -an") || strings.Contains(video, "0:a") || strings.Contains(video, "-c:a") ||
		strings.Contains(video, "delay_moov") || !strings.Contains(video, "-c:v copy -tag:v hvc1") {
		t.Errorf("video track: %s", video)
	}
	if !strings.Contains(audio, "-map 0:a:1 -vn") || strings.Contains(audio, "-c:v") || !strings.Contains(audio, "-c:a copy") ||
		!strings.Contains(audio, "+delay_moov") || !strings.Contains(audio, "make_non_negative") || strings.Contains(video, "avoid_negative_ts") {
		t.Errorf("audio track: %s", audio)
	}
	if want := strings.Join(playArgs("http://ts/f", 300, 1, hevc, eac3, true), " "); muxed != want ||
		!strings.Contains(muxed, "make_zero") || strings.Contains(muxed, "output_ts_offset") {
		t.Errorf("muxed stream changed: %s", muxed)
	}
	if args := strings.Join(streamArgs("http://ts/f", 0, 0, hevc, eac3, false, tsTrackVideo), " "); strings.Contains(args, "-ss") ||
		strings.Contains(args, "output_ts_offset") {
		t.Errorf("start at 0: %s", args)
	}
	// Re-encoded audio of a split pair is encoded like the muxed one
	opus := tsAudioPlan{encoder: "libopus", layout: "5.1", bitrate: "384k"}
	if args := strings.Join(streamArgs("http://ts/f", 60, 0, hevc, opus, false, tsTrackAudio), " "); !strings.Contains(args, "-c:a libopus") ||
		!strings.Contains(args, "-noaccurate_seek") {
		t.Errorf("re-encoded audio track: %s", args)
	}
}

func TestPlayAudioCodecsSplit(t *testing.T) {
	gin.SetMode(gin.TestMode)
	ctx := func(q string) *gin.Context {
		c, _ := gin.CreateTestContext(httptest.NewRecorder())
		c.Request = httptest.NewRequest(http.MethodGet, "/play?"+q, nil)
		return c
	}
	c := ctx("acodecs=aac,opus&split_acodecs=ac3,eac3&track=audio")
	if got := playAudioCodecs(c, tsTrackBoth); got["eac3"] || got["ac3"] || !got["opus"] {
		t.Errorf("muxed copies split-only codecs: %v", got)
	}
	for _, tr := range []tsTrack{tsTrackVideo, tsTrackAudio} {
		if got := playAudioCodecs(c, tr); !got["eac3"] || !got["ac3"] || !got["aac"] {
			t.Errorf("split track %d: %v", tr, got)
		}
	}
	if got := seekAudioCodecs(c); !got["eac3"] {
		t.Errorf("seekpoint of a split pair: %v", got)
	}
	if got := seekAudioCodecs(ctx("acodecs=aac&split_acodecs=eac3")); got["eac3"] {
		t.Errorf("seekpoint of a muxed stream: %v", got)
	}
	if _, ok := playTrack(ctx("track=subs")); ok {
		t.Error("unknown track accepted")
	}
	// The probe's E-AC-3 track: converted muxed, copied in an audio-only stream
	p := audioProbe(tsProbeStream{CodecName: "eac3", Channels: 6})
	if p.audioPlan(0, playAudioCodecs(c, tsTrackBoth)).copy || !p.audioPlan(0, playAudioCodecs(c, tsTrackAudio)).copy {
		t.Error("split plans")
	}
}

// TestSplitSyntheticAlignment runs both split outputs on a generated HEVC 10-bit + E-AC-3 5.1 file
// and checks that they share the file's timeline and start together. Needs ffmpeg with libx265.
func TestSplitSyntheticAlignment(t *testing.T) {
	if _, err := exec.LookPath("ffmpeg"); err != nil {
		t.Skip("ffmpeg not installed")
	}
	dir := t.TempDir()
	src := filepath.Join(dir, "src.mkv")
	gen := exec.Command("ffmpeg", "-hide_banner", "-v", "error", "-f", "lavfi", "-i", "testsrc2=size=640x360:rate=24",
		"-f", "lavfi", "-i", "sine=frequency=440:sample_rate=48000", "-t", "20",
		"-filter_complex", "[1:a]pan=5.1|c0=c0|c1=c0|c2=c0|c3=c0|c4=c0|c5=c0[a]", "-map", "0:v", "-map", "[a]",
		"-c:v", "libx265", "-preset", "ultrafast", "-pix_fmt", "yuv420p10le", "-x265-params", "log-level=error:keyint=48:bframes=3",
		"-c:a", "eac3", "-b:a", "640k", src)
	if out, err := gen.CombinedOutput(); err != nil {
		t.Skipf("cannot generate the sample (libx265?): %v %s", err, out)
	}
	// Served over HTTP like TorrServer (the reconnect options need an HTTP input)
	srv := httptest.NewServer(http.FileServer(http.Dir(dir)))
	defer srv.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	plan, aplan := tsVideoPlan{copy: true, hevc: true}, tsAudioPlan{copy: true, delay: true}
	run := func(start float64, track tsTrack) string {
		out := filepath.Join(dir, fmt.Sprintf("out%d.mp4", track))
		args := streamArgs(srv.URL+"/src.mkv", start, 0, plan, aplan, true, track)
		args = append(args[:len(args)-1], "-y", out) // file instead of pipe:1
		if b, err := exec.CommandContext(ctx, "ffmpeg", args...).CombinedOutput(); err != nil {
			t.Fatalf("t=%v track %d: %v %s", start, track, err, b)
		}
		return out
	}
	const frame = 1.0 / 24
	// File time is pts - the format's start_time (E-AC-3 priming starts this file at -5 ms)
	_, _, srcStart := tlStreamStarts(t, src)
	fileTimes := func(stream string) []float64 {
		pts := allPTS(t, ctx, src, stream)
		for i := range pts {
			pts[i] -= srcStart
		}
		return pts
	}
	srcVideo, srcAudio := fileTimes("v:0"), fileTimes("a:0")
	for _, start := range []float64{0, 7.3, 13.9} {
		v := firstPTS(t, ctx, run(start, tsTrackVideo), "v:0")
		a := firstPTS(t, ctx, run(start, tsTrackAudio), "a:0")
		t.Logf("t=%v: video starts %.3f, audio %.3f", start, v, a)
		if start == 0 {
			// Known: from the file's start the muxer moves the video by its B-frame delay (its
			// first decode time is before 0), so the keyframe at 0.005 starts at 0.088
			if !hasPTS(srcAudio, a) || v < srcVideo[0] || v > srcVideo[0]+2*frame+0.002 {
				t.Errorf("t=0: video %.3f / audio %.3f", v, a)
			}
			continue
		}
		// Both keep the source's own timestamps (the A/V relation of the file)
		if !hasPTS(srcVideo, v) || !hasPTS(srcAudio, a) {
			t.Errorf("t=%v: %.3f / %.3f are not file times", start, v, a)
		}
		// Copied video starts on the keyframe at or before t (2 s GOP), on the file's timeline
		if v > start+frame || v < start-2.1 {
			t.Errorf("t=%v: video starts at %.3f", start, v)
		}
		if math.Abs(v-a) > frame {
			t.Errorf("t=%v: video %.3f and audio %.3f start more than a frame apart", start, v, a)
		}
	}
}

// allPTS lists the presentation times of every packet of one stream (tfdt based for fMP4).
func allPTS(t *testing.T, ctx context.Context, file, stream string) []float64 {
	t.Helper()
	out, err := exec.CommandContext(ctx, "ffprobe", "-v", "error", "-use_tfdt", "1", "-select_streams", stream,
		"-show_entries", "packet=pts_time", "-of", "csv=p=0", file).Output()
	if err != nil {
		t.Fatalf("ffprobe %s: %v", file, err)
	}
	var pts []float64
	for _, line := range strings.Fields(string(out)) {
		if v, err := strconv.ParseFloat(strings.TrimSuffix(line, ","), 64); err == nil {
			pts = append(pts, v)
		}
	}
	return pts
}

// firstPTS is the lowest presentation time among the first packets of one stream.
func firstPTS(t *testing.T, ctx context.Context, file, stream string) float64 {
	t.Helper()
	pts := allPTS(t, ctx, file, stream)
	if len(pts) == 0 {
		t.Fatalf("no %s packets in %s", stream, file)
	}
	low := math.Inf(1)
	for _, v := range pts[:min(8, len(pts))] {
		low = min(low, v)
	}
	return low
}

func hasPTS(list []float64, v float64) bool {
	for _, x := range list {
		if math.Abs(x-v) < 0.002 {
			return true
		}
	}
	return false
}
