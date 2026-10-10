package streaming

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
)

func TestParseKeyframeProbe(t *testing.T) {
	cases := []struct {
		out  string
		want float64
		ok   bool
	}{
		{"30.000000\n0.000000\n", 30, true},
		// start_time 5 s: the packet time is absolute
		{"35.000000\n5.000000\n", 30, true},
		// MPEG-TS style: trailing comma and an empty line
		{"32.566667,\n\n1.483333\n", 31.084, true},
		{"12.5\nN/A\n", 12.5, true},
		// Only the format line: no packet after the seek
		{"0.000000\n", 0, false},
		{"", 0, false},
	}
	for _, c := range cases {
		got, err := parseKeyframeProbe(c.out)
		if (err == nil) != c.ok {
			t.Fatalf("%q: err = %v", c.out, err)
		}
		if c.ok && got != c.want {
			t.Fatalf("%q: got %v, want %v", c.out, got, c.want)
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
