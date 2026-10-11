package streaming

import (
	"context"
	"errors"
	"math"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
)

func TestParseFrameCRC(t *testing.T) {
	head := "#software: Lavf61.7.100\n#tb 0: 1/1000\n#media_type 0: video\n#codec_id 0: h264\n#dimensions 0: 320x48\n#sar 0: 1/1\n"
	audio := "#tb 1: 1/48000\n#media_type 1: audio\n"
	cases := []struct {
		out          string
		video, audio tsPacketTime
		hasAudio, ok bool
	}{
		// Matroska: a keyframe 4.34 s before t (times are file time - t), shown two frames after
		// its decode time; the audio starts 20 ms after it
		{head + audio + "0,      -4423,      -4340,       42,     2214, 0x5a2f4e1d, F=0x1\n1,    -207360,    -207360,     1024,      371, 0x1\n" +
			"1,    -206336,    -206336,     1024,      371, 0x1\n", tsPacketTime{-4.34, -4.423}, tsPacketTime{-4.32, -4.32}, true, true},
		{"#tb 0: 1/90000\n0,     -11259,      -3750,     3754,     9031, 0x1\n", tsPacketTime{-3750.0 / 90000, -11259.0 / 90000}, tsPacketTime{}, false, true},
		{head + "0,          0,         83,       42,     2214, 0x5a2f4e1d\n", tsPacketTime{0.083, 0}, tsPacketTime{}, false, true},
		// No video packet after the seek
		{head + audio + "1,    -207360,    -207360,     1024,      371, 0x1\n", tsPacketTime{}, tsPacketTime{}, false, false},
		{"", tsPacketTime{}, tsPacketTime{}, false, false},
	}
	near := func(a, b tsPacketTime) bool { return math.Abs(a.pts-b.pts) < 1e-9 && math.Abs(a.dts-b.dts) < 1e-9 }
	for _, c := range cases {
		got, err := parseFrameCRC(c.out)
		if (err == nil) != c.ok {
			t.Fatalf("%q: err = %v", c.out, err)
		}
		if c.ok && (!near(got.video, c.video) || got.hasAudio != c.hasAudio || !near(got.audio, c.audio)) {
			t.Fatalf("%q: got %+v, want %+v / %+v", c.out, got, c.video, c.audio)
		}
	}
}

func seekPointGet(s *Service, query string) *httptest.ResponseRecorder {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.GET("/seekpoint", newHandler(s).seekPoint)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/seekpoint?"+query, nil))
	return w
}

// ?t= that is no usable time is refused instead of failing to encode the answer.
func TestSeekPointRejectsBadTimes(t *testing.T) {
	s := NewService(nil, Config{TorrServerURL: "http://127.0.0.1:1"})
	s.probeFn = func(ctx context.Context, hash string, index int) (*tsProbe, error) {
		return nil, errors.New("no probe")
	}
	base := "hash=" + hx('a') + "&index=0&t="
	for _, v := range []string{"NaN", "Inf", "1e300", "x"} {
		if w := seekPointGet(s, base+v); w.Code != http.StatusBadRequest {
			t.Fatalf("t=%s: %d %s", v, w.Code, w.Body)
		}
	}
	for v, want := range map[string]string{"-5": `{"t":0}`, "30": `{"t":30}`} {
		if w := seekPointGet(s, base+v); w.Code != http.StatusOK || w.Body.String() != want {
			t.Fatalf("t=%s: %d %s", v, w.Code, w.Body)
		}
	}
}

// After a restart a seek point of a saved TorBox source reads the file from TorBox.
func TestSeekPointAdoptsSavedTorBoxSource(t *testing.T) {
	s := savedTorBoxService(t)
	var probed string
	s.probeFn = func(ctx context.Context, hash string, index int) (*tsProbe, error) {
		probed = s.fileURL(hash, index)
		return nil, errors.New("no probe")
	}
	if w := seekPointGet(s, "hash="+tbHash+"&index=1&t=30"); w.Code != http.StatusOK {
		t.Fatalf("seek point %d %s", w.Code, w.Body)
	}
	if !strings.Contains(probed, "/f/"+tbHash+"/1") {
		t.Fatalf("seek point read %q, want the TorBox proxy", probed)
	}
}
