package playbacklog

import (
	"bytes"
	"fmt"
	"log"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
)

func TestFormatSanitizes(t *testing.T) {
	b := batch{SID: "abc123", Seq: 2, Client: "tv", Ctx: &ctx{Type: "tv", TmdbID: 1399, Season: 1, Episode: 2,
		Title: "Line\nbreak", Src: "abcdef12:3", Mode: "muxed"}, Events: []event{
		{T: 0, K: "wait", P: 12.345, X: map[string]any{"d": 420.0, "end": "playing x\n", "Bad": 1.0, "seeking": false}},
		{T: 0, K: "Bad kind"},
	}}
	lines := format(b)
	if len(lines) != 2 {
		t.Fatalf("lines = %d: %q", len(lines), lines)
	}
	for _, l := range lines {
		if strings.ContainsAny(l, "\n\r") {
			t.Fatalf("line break in %q", l)
		}
	}
	if !strings.Contains(lines[0], "S01E02") || !strings.Contains(lines[0], "invalid=1") {
		t.Fatalf("header %q", lines[0])
	}
	if !strings.Contains(lines[1], "pos=12.35 wait d=420 end=playing_x_ seeking=false") || strings.Contains(lines[1], "Bad") {
		t.Fatalf("event %q", lines[1])
	}
}

func TestRateLimitPerSession(t *testing.T) {
	s := NewService("")
	now := time.Unix(0, 0)
	s.now = func() time.Time { return now }
	for i := 0; i < burst; i++ {
		if !s.allow("1.2.3.4", "aaaa") {
			t.Fatalf("batch %d refused", i)
		}
	}
	if s.allow("1.2.3.4", "aaaa") {
		t.Fatal("session over its burst was allowed")
	}
	// Another player behind the same proxy IP has its own bucket
	if !s.allow("1.2.3.4", "bbbb") || !s.allow("5.6.7.8", "aaaa") {
		t.Fatal("limit is not per session")
	}
	now = now.Add(refillEvery)
	if !s.allow("1.2.3.4", "aaaa") {
		t.Fatal("bucket did not refill")
	}
}

func TestBucketsStayBounded(t *testing.T) {
	s := NewService("")
	now := time.Unix(0, 0)
	s.now = func() time.Time { return now }
	for i := 0; i < maxBuckets+50; i++ {
		s.allow("1.2.3.4", fmt.Sprintf("s%d", i))
	}
	if n := len(s.sessions.m); n > maxBuckets {
		t.Fatalf("%d buckets", n)
	}
	// Idle buckets are pruned, so new sessions get in again later
	now = now.Add(idleAfter + time.Second)
	if !s.allow("1.2.3.4", "fresh") || len(s.sessions.m) > maxBuckets/2 {
		t.Fatalf("idle buckets not pruned: %d", len(s.sessions.m))
	}
}

func TestIPLimitIsGenerous(t *testing.T) {
	s := NewService("")
	now := time.Unix(0, 0)
	s.now = func() time.Time { return now }
	for i := 0; i < ipBurst; i++ {
		if !s.allowIP("10.0.0.2") {
			t.Fatalf("request %d refused", i)
		}
	}
	if s.allowIP("10.0.0.2") {
		t.Fatal("ip over its burst was allowed")
	}
}

func TestStdoutOnlyHeaderWithFiles(t *testing.T) {
	gin.SetMode(gin.TestMode)
	var out bytes.Buffer
	log.SetOutput(&out)
	defer log.SetOutput(os.Stderr)

	dir := t.TempDir()
	s := NewService(dir)
	body := `{"sid":"abcd1234","seq":1,"client":"web","events":[{"t":1,"k":"wait","p":1},{"t":2,"k":"play","p":2}]}`
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest("POST", "/api/playback-log", strings.NewReader(body))
	s.receive(c)
	if c.Writer.Status() != 204 {
		t.Fatalf("status %d", c.Writer.Status())
	}
	if n := strings.Count(out.String(), "playback: "); n != 1 {
		t.Fatalf("%d stdout lines, want 1:\n%s", n, out.String())
	}
	files, _ := filepath.Glob(filepath.Join(dir, "*.log"))
	if len(files) != 1 {
		t.Fatalf("files %v", files)
	}
	data, _ := os.ReadFile(files[0])
	if strings.Count(string(data), "\n") != 3 {
		t.Fatalf("file:\n%s", data)
	}
}
