package streaming

import (
	"context"
	"encoding/json"
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

// ?t= that is no usable time is refused before ffmpeg or the play bookkeeping start.
func TestPlayRejectsBadTimes(t *testing.T) {
	gin.SetMode(gin.TestMode)
	s := NewService(nil, Config{TorrServerURL: "http://127.0.0.1:1"})
	s.probeFn = func(ctx context.Context, hash string, index int) (*tsProbe, error) {
		t.Fatal("probed a refused request")
		return nil, nil
	}
	r := gin.New()
	r.GET("/play", newHandler(s).play)
	for _, v := range []string{"NaN", "Inf", "1e300"} {
		w := httptest.NewRecorder()
		r.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/play?hash="+hx('a')+"&index=0&t="+v, nil))
		if w.Code != http.StatusBadRequest {
			t.Fatalf("t=%s: %d %s", v, w.Code, w.Body)
		}
	}
}

// savedTorBoxService is a fresh process (nothing registered yet) whose source cache names
// tbHash:1 as a TorBox source.
func savedTorBoxService(t *testing.T) *Service {
	t.Helper()
	f := newFakeTorBox(t)
	s := newDebridService(t, f)
	res := tsResolveResult{Magnet: tsBuildMagnet(tbHash, "Movie (2020)", nil), Hash: tbHash, Index: 1, FileName: "Movie.2020.1080p.mkv",
		TorrentTitle: "Movie (2020)", Size: 3 << 20, Source: tsSourceTorBox, Debrid: true}
	data, _ := json.Marshal(res)
	if err := saveSource(s.db, &StreamSource{SourceKey: "v10|movie|movie|2020|0|0|hevc", Result: string(data), ResolvedAt: time.Now()}); err != nil {
		t.Fatal(err)
	}
	return NewService(s.db, Config{TorBoxURL: f.srv.URL, TorrServerURL: "http://127.0.0.1:1"})
}

// After a restart a play of a saved TorBox source (no probe call before it) reads the file
// from TorBox, not TorrServer with TorBox's list position as the file id.
func TestPlayAdoptsSavedTorBoxSource(t *testing.T) {
	withTranscodes(t, 1, 20*time.Millisecond)
	tsTranscodes <- struct{}{} // re-encode refused: the play ends after the probe
	s := savedTorBoxService(t)
	var probed string
	s.probeFn = func(ctx context.Context, hash string, index int) (*tsProbe, error) {
		probed = s.fileURL(hash, index)
		return &tsProbe{Streams: []tsProbeStream{{CodecType: "video", CodecName: "hevc", PixFmt: "yuv420p10le"}}}, nil
	}
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.GET("/play", newHandler(s).play)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/play?hash="+tbHash+"&index=1", nil))
	if !strings.Contains(probed, "/f/"+tbHash+"/1") {
		t.Fatalf("play read %q, want the TorBox proxy", probed)
	}
}
