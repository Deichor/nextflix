package streaming

import (
	"bytes"
	"context"
	"encoding/binary"
	"math"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
)

// recordSink keeps what a tap delivered.
type recordSink struct {
	mu     sync.Mutex
	pieces []tsTapPiece
	closed chan struct{}
	block  chan struct{} // Write waits for it when set
}

func newRecordSink() *recordSink { return &recordSink{closed: make(chan struct{})} }

func (r *recordSink) Write(at int64, pcm []byte) {
	if r.block != nil {
		<-r.block
	}
	r.mu.Lock()
	r.pieces = append(r.pieces, tsTapPiece{at, pcm})
	r.mu.Unlock()
}

func (r *recordSink) Close() { close(r.closed) }

// pcm puts the pieces at their offsets (gaps stay zero).
func (r *recordSink) pcm() []int16 {
	r.mu.Lock()
	defer r.mu.Unlock()
	end := int64(0)
	for _, p := range r.pieces {
		end = max(end, p.at+int64(len(p.pcm)))
	}
	b := make([]byte, end)
	for _, p := range r.pieces {
		copy(b[p.at:], p.pcm)
	}
	out := make([]int16, len(b)/2)
	for i := range out {
		out[i] = int16(binary.LittleEndian.Uint16(b[2*i:]))
	}
	return out
}

// A sink that stops taking data must never hold up the writer (ffmpeg): pieces are dropped and
// the offsets of later ones show the gap.
func TestSpeechPumpNeverBlocks(t *testing.T) {
	pr, pw, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	sink := newRecordSink()
	sink.block = make(chan struct{})
	done := tsPumpSpeech(pr, sink, "test")
	wrote := make(chan struct{})
	go func() {
		chunk := make([]byte, tsTapChunk)
		for range 200 { // 200 s of audio, far more than the queue holds
			if _, err := pw.Write(chunk); err != nil {
				t.Error(err)
				break
			}
		}
		close(wrote)
	}()
	select {
	case <-wrote:
	case <-time.After(5 * time.Second):
		t.Fatal("the writer was blocked by a stalled sink")
	}
	close(sink.block)
	time.Sleep(50 * time.Millisecond) // the sink drains its queue
	if _, err := pw.Write(make([]byte, 2*tsTapChunk)); err != nil {
		t.Fatal(err)
	}
	pw.Close()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("sink not closed")
	}
	<-sink.closed
	n := len(sink.pieces)
	if n == 0 || n > tsTapQueue+8 { // the queue, what was in the pipe's buffer, the last write
		t.Fatalf("%d pieces delivered", n)
	}
	gap := false
	for i := 1; i < n; i++ {
		if sink.pieces[i].at < sink.pieces[i-1].at {
			t.Fatal("pieces out of order")
		}
		gap = gap || sink.pieces[i].at > sink.pieces[i-1].at+tsTapChunk
	}
	if !gap {
		t.Fatalf("no gap marked: last piece at %d", sink.pieces[n-1].at)
	}
}

// A sink that panics stops getting data; the pipe is still drained to the end.
func TestSpeechPumpSurvivesASinkPanic(t *testing.T) {
	pr, pw, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	tsPumpSpeech(pr, panicSink{}, "test")
	wrote := make(chan struct{})
	go func() {
		for range 100 {
			pw.Write(make([]byte, tsTapChunk))
		}
		pw.Close()
		close(wrote)
	}()
	select {
	case <-wrote:
	case <-time.After(5 * time.Second):
		t.Fatal("the writer was blocked after the sink failed")
	}
}

type panicSink struct{}

func (panicSink) Write(int64, []byte) { panic("boom") }
func (panicSink) Close()              {}

func TestSpeechTapArgs(t *testing.T) {
	five := speechTapArgs(1, &tsProbeStream{Channels: 6, ChannelLayout: "5.1(side)"})
	if !slices.Contains(five, "0:a:1") || !strings.HasPrefix(five[slices.Index(five, "-af")+1], "pan=mono|c0=FC,") ||
		five[len(five)-1] != "pipe:3" {
		t.Fatalf("5.1: %v", five)
	}
	stereo := speechTapArgs(0, &tsProbeStream{Channels: 2, ChannelLayout: "stereo"})
	if strings.Contains(strings.Join(stereo, " "), "pan=") || !slices.Contains(stereo, "-ac") {
		t.Fatalf("stereo: %v", stereo)
	}
	if !tsHasCenter("", 6) || tsHasCenter("quad", 4) || !tsHasCenter("7.1", 8) {
		t.Fatal("centre detection")
	}
}

// fakePlayFFmpeg installs a shell script as the play ffmpeg.
func fakePlayFFmpeg(t *testing.T, script string) {
	t.Helper()
	p := filepath.Join(t.TempDir(), "ffmpeg")
	if err := os.WriteFile(p, []byte("#!/bin/sh\n"+script), 0o755); err != nil {
		t.Fatal(err)
	}
	saved := tsFFmpegBin
	tsFFmpegBin = p
	t.Cleanup(func() { tsFFmpegBin = saved })
}

func TestRunPlayWritesTheTap(t *testing.T) {
	fakePlayFFmpeg(t, `printf 'abcd' >&3; printf ok`)
	sink := newRecordSink()
	var out bytes.Buffer
	var stderr strings.Builder
	err := tsRunPlay(context.Background(), []string{"-i", "x", "pipe:1"}, []string{"pipe:3"}, sink,
		&tsFirstWrite{w: &out}, &stderr, "test")
	if err != nil || out.String() != "ok" {
		t.Fatalf("err %v out %q", err, out.String())
	}
	<-sink.closed
	if len(sink.pieces) != 1 || string(sink.pieces[0].pcm) != "abcd" || sink.pieces[0].at != 0 {
		t.Fatalf("pieces %+v", sink.pieces)
	}
}

// ffmpeg failing with the tap before any output plays again without it.
func TestRunPlayFallsBackWithoutTheTap(t *testing.T) {
	fakePlayFFmpeg(t, `for a in "$@"; do [ "$a" = pipe:3 ] && { echo "tap broke" >&2; exit 1; }; done; printf ok`)
	sink := newRecordSink()
	var out bytes.Buffer
	var stderr strings.Builder
	err := tsRunPlay(context.Background(), []string{"-i", "x", "pipe:1"}, []string{"pipe:3"}, sink,
		&tsFirstWrite{w: &out}, &stderr, "test")
	if err != nil || out.String() != "ok" {
		t.Fatalf("err %v out %q stderr %q", err, out.String(), stderr.String())
	}
	select {
	case <-sink.closed:
	case <-time.After(5 * time.Second):
		t.Fatal("sink of the failed run not closed")
	}
}

// After output went out a failure is not retried (the viewer got bytes of that run).
func TestRunPlayNoFallbackAfterOutput(t *testing.T) {
	fakePlayFFmpeg(t, `printf ok; exit 1`)
	var out bytes.Buffer
	var stderr strings.Builder
	if err := tsRunPlay(context.Background(), nil, []string{"pipe:3"}, newRecordSink(), &tsFirstWrite{w: &out}, &stderr, "test"); err == nil || out.String() != "ok" {
		t.Fatalf("err %v out %q", err, out.String())
	}
}

type testTap struct {
	mu    sync.Mutex
	sinks []*recordSink
	start []float64
}

func (t *testTap) Open(hash string, index int, start float64) SpeechSink {
	t.mu.Lock()
	defer t.mu.Unlock()
	s := newRecordSink()
	t.sinks = append(t.sinks, s)
	t.start = append(t.start, start)
	return s
}

// TestSpeechTapFFmpeg plays a generated file through the real play handler with the tap and
// checks that the PCM carries exact file times: tone bursts in the centre channel start every
// 7 s (at 1, 8, 15, ... s) under a steady tone in the front pair, which the tap must leave out.
// Seeks land between keyframes (10 s apart), so copied streams start up to 10 s before t.
// It needs ffmpeg and ffprobe (the server image has them, plain golang images do not).
func TestSpeechTapFFmpeg(t *testing.T) {
	if _, err := exec.LookPath("ffmpeg"); err != nil {
		t.Skip("ffmpeg not installed")
	}
	dir := t.TempDir()
	mkv := filepath.Join(dir, "f.mkv")
	burst := `if(between(mod(t\,7)\,1\,2.5)\,0.5*sin(2*PI*440*t)\,0)`
	bed := `0.3*sin(2*PI*200*t)`
	cmd := exec.Command("ffmpeg", "-v", "error", "-f", "lavfi", "-i", "testsrc=s=320x180:d=120:r=24",
		"-f", "lavfi", "-i", "aevalsrc="+bed+"|"+bed+"|"+burst+"|0|0|0:c=5.1:s=48000:d=120",
		"-c:v", "mpeg4", "-q:v", "6", "-g", "240", "-c:a", "aac", "-b:a", "192k", mkv)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("ffmpeg: %v %s", err, out)
	}
	data, err := os.ReadFile(mkv)
	if err != nil {
		t.Fatal(err)
	}
	src := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.ServeContent(w, r, "f.mkv", time.Time{}, bytes.NewReader(data))
	}))
	defer src.Close()
	gin.SetMode(gin.TestMode)
	s := NewService(nil, Config{TorrServerURL: src.URL})
	tap := &testTap{}
	s.SetSpeechTap(tap)
	r := gin.New()
	r.GET("/play", newHandler(s).play)

	cases := []struct{ name, query string }{
		{"split audio, copied (keyframe seek)", "&track=audio&acodecs=aac"},
		{"split audio, re-encoded", "&track=audio"},
		{"muxed, re-encoded (accurate seek)", ""},
		{"muxed, audio copied (keyframe seek)", "&acodecs=aac"},
	}
	for i, c := range cases {
		start := math.Round((37.3+float64(i)*11.05)*100) / 100
		w := httptest.NewRecorder()
		r.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/play?hash="+hx('a')+"&index=1&t="+
			strconv.FormatFloat(start, 'f', 2, 64)+c.query, nil))
		if w.Code != http.StatusOK || w.Body.Len() == 0 {
			t.Fatalf("%s: %d, %d bytes", c.name, w.Code, w.Body.Len())
		}
		tap.mu.Lock()
		if len(tap.sinks) != i+1 || math.Abs(tap.start[i]-start) > 1e-9 {
			t.Fatalf("%s: tap opened %d times, start %v", c.name, len(tap.sinks), tap.start)
		}
		sink := tap.sinks[i]
		tap.mu.Unlock()
		select {
		case <-sink.closed:
		case <-time.After(10 * time.Second):
			t.Fatalf("%s: sink not closed", c.name)
		}
		pcm := sink.pcm()
		if got := float64(len(pcm)) / 8000; math.Abs(got-(120-start)) > 0.5 {
			t.Fatalf("%s: %.2f s of PCM, want %.2f", c.name, got, 120-start)
		}
		// 10 ms frames: loud (burst) or silent (the front pair's tone must not be in it)
		var onsets []float64
		quiet, loud := 0.0, 0
		prevLoud := true
		for f := 0; f+80 <= len(pcm); f += 80 {
			e := 0.0
			for _, v := range pcm[f : f+80] {
				e += float64(v) * float64(v)
			}
			rms := math.Sqrt(e / 80)
			isLoud := rms > 2000
			if isLoud && !prevLoud {
				onsets = append(onsets, start+float64(f)/8000)
			}
			if !isLoud {
				quiet = math.Max(quiet, rms)
			} else {
				loud++
			}
			prevLoud = isLoud
		}
		if quiet > 300 || loud == 0 {
			t.Fatalf("%s: quiet frames up to rms %.0f (not the centre channel?), %d loud", c.name, quiet, loud)
		}
		if len(onsets) < 6 {
			t.Fatalf("%s: onsets %v", c.name, onsets)
		}
		for _, o := range onsets {
			want := 1 + 7*math.Round((o-1)/7)
			if math.Abs(o-want) > 0.025 {
				t.Fatalf("%s (t=%.2f): burst heard at %.3f s of the file, played at %.0f s", c.name, start, o, want)
			}
		}
		t.Logf("%s (t=%.2f): %d bursts on time, first at %.3f s", c.name, start, len(onsets), onsets[0])
	}
}
