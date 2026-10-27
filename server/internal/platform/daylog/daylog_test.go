package daylog

import (
	"bytes"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestBudgetPerKeyAndDay(t *testing.T) {
	var out bytes.Buffer
	log.SetOutput(&out)
	defer log.SetOutput(os.Stderr)

	dir := t.TempDir()
	w := New(dir, "playback", Options{Keep: 14, DayCap: 3000, KeyCap: 1000})
	now := time.Date(2026, 10, 10, 12, 0, 0, 0, time.UTC)
	line := []string{strings.Repeat("x", 79)} // 100 bytes with the stamp
	for range 30 {
		w.Write(now, "10.0.0.9", line)
	}
	size := func() int64 {
		st, err := os.Stat(filepath.Join(dir, "2026-10-10.log"))
		if err != nil {
			t.Fatal(err)
		}
		return st.Size()
	}
	if n := size(); n != 1000 {
		t.Fatalf("one client wrote %d bytes, want its 1000", n)
	}
	w.Write(now, "10.0.0.7", line)
	if n := size(); n != 1100 {
		t.Fatalf("another client was dropped (%d bytes)", n)
	}
	for i := range 40 {
		w.Write(now, fmt.Sprintf("10.0.1.%d", i), line)
	}
	if n := size(); n > 3000+100 {
		t.Fatalf("day file grew to %d bytes", n)
	}
	if c := strings.Count(out.String(), "playback: 2026-10-10.log is over"); c != 1 {
		t.Fatalf("day cap logged %d times:\n%s", c, out.String())
	}
	if c := strings.Count(out.String(), "10.0.0.9 wrote"); c != 1 {
		t.Fatalf("client cap logged %d times:\n%s", c, out.String())
	}
	// A new day starts with fresh budgets
	w.Write(now.AddDate(0, 0, 1), "10.0.0.9", line)
	if _, err := os.Stat(filepath.Join(dir, "2026-10-11.log")); err != nil {
		t.Fatal(err)
	}
}

func TestDaysAreUTCAndOldOnesPruned(t *testing.T) {
	dir := t.TempDir()
	old := filepath.Join(dir, "2026-09-01.log")
	if err := os.WriteFile(old, []byte("x\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	w := New(dir, "timing log", Options{Keep: 14, DayCap: 1 << 20})
	// 01:30 in Istanbul is still the day before in UTC
	at := time.Date(2026, 10, 11, 1, 30, 0, 0, time.FixedZone("TRT", 3*3600))
	w.Write(at, "", []string{"a"})
	b, err := os.ReadFile(filepath.Join(dir, "2026-10-10.log"))
	if err != nil || string(b) != "2026-10-10 22:30:00 a\n" {
		t.Fatalf("%q %v", b, err)
	}
	if _, err := os.Stat(old); !os.IsNotExist(err) {
		t.Fatal("a file older than 14 days was kept")
	}
	if New("", "x", Options{}) != nil {
		t.Fatal("a writer without a dir")
	}
	var none *Writer
	none.Write(at, "", []string{"a"}) // no dir: nothing, no panic
}

func TestTeeKeepsPickedLines(t *testing.T) {
	dir := t.TempDir()
	w := New(dir, "timing log", Options{Keep: 14, DayCap: 1 << 20})
	tee := NewTee(w, func(l string) bool { return strings.HasPrefix(l, "subtitles: ") })
	logger := log.New(tee, "", log.LstdFlags)
	logger.Printf("subtitles: timing of a on b:1: synced")
	logger.Printf("streaming: something else")
	logger.Printf("subtitles: two\nlines")
	var got string
	for deadline := time.Now().Add(5 * time.Second); time.Now().Before(deadline); time.Sleep(10 * time.Millisecond) {
		files, _ := filepath.Glob(filepath.Join(dir, "*.log"))
		if len(files) == 1 {
			b, _ := os.ReadFile(files[0])
			if got = string(b); strings.Count(got, "\n") == 2 {
				break
			}
		}
	}
	lines := strings.Split(strings.TrimSuffix(got, "\n"), "\n")
	if len(lines) != 2 || !strings.HasSuffix(lines[0], " subtitles: timing of a on b:1: synced") ||
		!strings.HasSuffix(lines[1], " subtitles: two lines") || len(lines[0]) != len("2026-10-10 22:30:00 subtitles: timing of a on b:1: synced") {
		t.Fatalf("file:\n%s", got)
	}
	if n, err := tee.Write([]byte("not a stamp subtitles: x\n")); n != 25 || err != nil {
		t.Fatal(n, err)
	}
}

func TestStripStamp(t *testing.T) {
	for in, want := range map[string]string{
		"2026/10/22 00:30:35 subtitles: x": "subtitles: x",
		"subtitles: x":                     "subtitles: x",
		"2026-10-22 00:30:35 subtitles: x": "2026-10-22 00:30:35 subtitles: x",
		"short":                            "short",
	} {
		if got := stripStamp(in); got != want {
			t.Errorf("%q: %q", in, got)
		}
	}
}
