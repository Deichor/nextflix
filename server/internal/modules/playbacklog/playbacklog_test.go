package playbacklog

import (
	"strings"
	"testing"
	"time"
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

func TestRateLimit(t *testing.T) {
	s := NewService("")
	now := time.Unix(0, 0)
	s.now = func() time.Time { return now }
	for i := 0; i < burst; i++ {
		if !s.allow("1.2.3.4") {
			t.Fatalf("batch %d refused", i)
		}
	}
	if s.allow("1.2.3.4") || !s.allow("5.6.7.8") {
		t.Fatal("limit is not per IP")
	}
	now = now.Add(refillEvery)
	if !s.allow("1.2.3.4") {
		t.Fatal("bucket did not refill")
	}
}
