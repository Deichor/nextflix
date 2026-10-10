package streaming

import (
	"context"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
)

// Every ffmpeg / ffprobe input is limited to plain containers over HTTP, set before -i.
func TestFFmpegInputsAreWhitelisted(t *testing.T) {
	check := func(name string, args []string) {
		t.Helper()
		in := slices.Index(args, "-i")
		p := slices.Index(args, "-protocol_whitelist")
		f := slices.Index(args, "-format_whitelist")
		if in < 0 || p < 0 || f < 0 || p > in || f > in {
			t.Fatalf("%s: whitelists missing or after -i: %v", name, args)
		}
		if args[p+1] != "http,tcp" || args[f+1] != tsFormatWhitelist {
			t.Fatalf("%s: whitelists %q / %q", name, args[p+1], args[f+1])
		}
	}
	plan, aplan := tsVideoPlan{copy: true}, tsAudioPlan{copy: true}
	for _, tr := range []tsTrack{tsTrackBoth, tsTrackVideo, tsTrackAudio} {
		check("play", streamArgs("http://ts/f", 30, 0, plan, aplan, true, tr))
	}
	check("thumb", thumbArgs("http://ts/f", thumbKey{hash: thumbHashA, t: 30, w: 320}, "smpte2084", true))
	for _, f := range []string{"hls", "concat", "image2"} {
		if slices.Contains(strings.Split(tsFormatWhitelist, ","), f) {
			t.Fatalf("format %s is allowed", f)
		}
	}
}

func withTranscodes(t *testing.T, n int, wait time.Duration) {
	t.Helper()
	slots, w := tsTranscodes, tsTranscodeWait
	tsTranscodes, tsTranscodeWait = make(chan struct{}, n), wait
	t.Cleanup(func() { tsTranscodes, tsTranscodeWait = slots, w })
}

func TestTranscodeSlots(t *testing.T) {
	withTranscodes(t, 1, 0)
	release, ok := tsTranscodeSlot(context.Background(), 10*time.Millisecond)
	if !ok {
		t.Fatal("first slot refused")
	}
	if _, ok := tsTranscodeSlot(context.Background(), 20*time.Millisecond); ok {
		t.Fatal("second slot granted while full")
	}
	release()
	if r, ok := tsTranscodeSlot(context.Background(), 10*time.Millisecond); !ok {
		t.Fatal("slot not freed")
	} else {
		r()
	}
}

// A re-encode while all transcode slots stay busy answers 503 before any byte is sent.
func TestPlayAnswers503WhenTranscodesAreFull(t *testing.T) {
	withTranscodes(t, 1, 20*time.Millisecond)
	tsTranscodes <- struct{}{} // the only slot is taken
	gin.SetMode(gin.TestMode)
	s := NewService(nil, Config{TorrServerURL: "http://127.0.0.1:1"})
	s.probeFn = func(ctx context.Context, hash string, index int) (*tsProbe, error) {
		// 10-bit HEVC for a client without HEVC: re-encoded
		return &tsProbe{Streams: []tsProbeStream{{CodecType: "video", CodecName: "hevc", PixFmt: "yuv420p10le"}}}, nil
	}
	r := gin.New()
	r.GET("/play", newHandler(s).play)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/play?hash="+hx('a')+"&index=0", nil))
	if w.Code != http.StatusServiceUnavailable || w.Header().Get("Retry-After") == "" {
		t.Fatalf("got %d %s", w.Code, w.Body)
	}
	s.warmMu.Lock()
	n := s.playing[hx('a')]
	s.warmMu.Unlock()
	if n != 0 {
		t.Fatalf("refused stream still counted as playing (%d)", n)
	}
}
