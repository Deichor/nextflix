package streaming

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"math"
	"os"
	"strings"
	"sync"
	"testing"
	"time"
)

type cueRec struct{ start, end float64 }

// recordCues keeps what a cue tap delivered.
type recordCues struct {
	mu     sync.Mutex
	cues   []cueRec
	heard  []float64
	closed chan struct{}
	block  chan struct{} // calls wait for it when set
}

func newRecordCues() *recordCues { return &recordCues{closed: make(chan struct{})} }

func (r *recordCues) Cue(start, end float64) {
	if r.block != nil {
		<-r.block
	}
	r.mu.Lock()
	r.cues = append(r.cues, cueRec{start, end})
	r.mu.Unlock()
}

func (r *recordCues) Heard(t float64) {
	r.mu.Lock()
	r.heard = append(r.heard, t)
	r.mu.Unlock()
}

func (r *recordCues) Close() { close(r.closed) }

func (r *recordCues) wait(t *testing.T) {
	t.Helper()
	select {
	case <-r.closed:
	case <-time.After(10 * time.Second):
		t.Fatal("cue sink not closed")
	}
}

// testCueTap opens a sink for the first track of the stream index it is given (-1: the first).
type testCueTap struct {
	mu     sync.Mutex
	want   int
	sinks  []*recordCues
	start  []float64
	tracks [][]TextTrack
}

func (c *testCueTap) OpenCues(hash string, index int, start float64, tracks []TextTrack) (int, CueSink, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	stream := tracks[0].Stream
	if c.want >= 0 {
		stream = c.want
	}
	s := newRecordCues()
	c.sinks = append(c.sinks, s)
	c.start = append(c.start, start)
	c.tracks = append(c.tracks, tracks)
	return stream, s, true
}

const cueFrameCRC = `#software: Lavf61.7.103
#tb 0: 1/1000
#media_type 0: subtitle
#tb 1: 1/48000
#media_type 1: audio
1,    -211584,    -211584,     1024,      212, 0xc49c6668
0,      -2113,      -2113,     1100,        6, 0x07f001fc
1,       4656,       4656,     1024,      203, 0x389263d2
0,        387,        387,     1100,        6, 0x07f101fd
0,       1487,       1487,     1400,        2, 0x00000000
1,      96000,      96000,     1024,      203, 0x389263d2
0,       2887,       2887,        0,        6, 0x07f201fe
1,     192000,     192000,     1024,      203, 0x389263d2
0,       5387,       53`

func TestPumpCuesTimes(t *testing.T) {
	pr, pw, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	sink := newRecordCues()
	done := tsPumpCues(pr, sink, 13.37, 2, "test")
	if _, err := pw.WriteString(cueFrameCRC); err != nil {
		t.Fatal(err)
	}
	pw.Close()
	<-done
	want := []cueRec{{11.257, 12.357}, {13.757, 14.857}, {16.257, 16.257}} // the cut last line is left out
	if len(sink.cues) != len(want) {
		t.Fatalf("cues %v", sink.cues)
	}
	for i, c := range sink.cues {
		if math.Abs(c.start-want[i].start) > 1e-9 || math.Abs(c.end-want[i].end) > 1e-9 {
			t.Fatalf("cue %d: %v, want %v", i, c, want[i])
		}
	}
	// Progress: whole seconds apart, then the last packet at EOF (audio 4 s + 1024 samples)
	last := sink.heard[len(sink.heard)-1]
	if len(sink.heard) < 3 || math.Abs(last-(13.37+4+1024.0/48000)) > 1e-9 {
		t.Fatalf("heard %v", sink.heard)
	}
	for i := 1; i < len(sink.heard); i++ {
		if sink.heard[i] < sink.heard[i-1]+tsCueHeard && i < len(sink.heard)-1 {
			t.Fatalf("heard %v", sink.heard)
		}
	}
}

// A stalled sink never holds up ffmpeg; once a cue was dropped no more progress is reported.
func TestPumpCuesNeverBlocks(t *testing.T) {
	pr, pw, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	sink := newRecordCues()
	sink.block = make(chan struct{})
	done := tsPumpCues(pr, sink, 0, 0, "test")
	wrote := make(chan struct{})
	go func() {
		pw.WriteString("#tb 0: 1/1000\n#tb 1: 1/1000\n")
		for i := range 5000 {
			fmt.Fprintf(pw, "0, %d, %d, 500, 6, 0x0\n1, %d, %d, 20, 6, 0x0\n", i*1000, i*1000, i*1000+500, i*1000+500)
		}
		close(wrote)
	}()
	select {
	case <-wrote:
	case <-time.After(5 * time.Second):
		t.Fatal("the writer was blocked by a stalled sink")
	}
	close(sink.block)
	pw.Close()
	<-done
	if len(sink.cues) == 0 || len(sink.cues) > tsCueQueue+1 {
		t.Fatalf("%d cues delivered", len(sink.cues))
	}
	if n := len(sink.heard); n > 0 && sink.heard[n-1] > float64(len(sink.cues)+1) {
		t.Fatalf("heard up to %.0f s with %d cues", sink.heard[n-1], len(sink.cues))
	}
}

type panicCues struct{}

func (panicCues) Cue(float64, float64) { panic("boom") }
func (panicCues) Heard(float64)        {}
func (panicCues) Close()               {}

func TestPumpCuesSurvivesASinkPanic(t *testing.T) {
	pr, pw, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	tsPumpCues(pr, panicCues{}, 0, 0, "test")
	wrote := make(chan struct{})
	go func() {
		pw.WriteString("#tb 0: 1/1000\n")
		for i := range 20000 {
			fmt.Fprintf(pw, "0, %d, %d, 500, 6, 0x0\n", i, i)
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

// Both taps get their own pipe (fd 3 and 4); a run that fails with them plays without them.
func TestRunPlayTwoTaps(t *testing.T) {
	fakePlayFFmpeg(t, `for a in "$@"; do [ "$a" = pipe:4 ] && [ -n "$FAIL" ] && exit 1; done
printf 'abcd' >&3; printf '#tb 0: 1/1000\n0, 1000, 1000, 500, 1, 0x0\n' >&4; printf ok`)
	for _, fail := range []bool{false, true} {
		if fail {
			t.Setenv("FAIL", "1")
		}
		speech, cues := newRecordSink(), newRecordCues()
		taps := append(speechTaps(speech), tsTap{args: []string{"-f", "framecrc"}, drop: cues.Close,
			pump: func(r io.ReadCloser) { tsPumpCues(r, cues, 2, 0, "test") }})
		var out bytes.Buffer
		var stderr strings.Builder
		if err := tsRunPlay(context.Background(), []string{"-i", "x", "pipe:1"}, taps, &tsFirstWrite{w: &out}, &stderr, "test"); err != nil || out.String() != "ok" {
			t.Fatalf("fail=%v: err %v out %q", fail, err, out.String())
		}
		<-speech.closed
		cues.wait(t)
		if fail {
			if len(cues.cues) != 0 || len(speech.pieces) != 0 {
				t.Fatalf("failed run delivered %v %v", cues.cues, speech.pieces)
			}
			continue
		}
		if len(speech.pieces) != 1 || len(cues.cues) != 1 || cues.cues[0] != (cueRec{3, 3.5}) {
			t.Fatalf("speech %v cues %v", speech.pieces, cues.cues)
		}
	}
}

func TestTextTracks(t *testing.T) {
	p := &tsProbe{Streams: []tsProbeStream{
		{CodecType: "video", CodecName: "h264"},
		{CodecType: "audio", CodecName: "aac"},
		{CodecType: "subtitle", CodecName: "hdmv_pgs_subtitle", Tags: map[string]string{"language": "eng"}},
		{CodecType: "subtitle", CodecName: "subrip", Tags: map[string]string{"LANGUAGE": "eng", "title": "SDH"},
			Disposition: map[string]int{"default": 1}},
		{CodecType: "subtitle", CodecName: "dvd_subtitle"},
		{CodecType: "subtitle", CodecName: "ass", Tags: map[string]string{"language": "tur"}, Disposition: map[string]int{"forced": 1}},
	}}
	got := p.textTracks()
	want := []TextTrack{{Stream: 3, Lang: "eng", Title: "SDH", Codec: "subrip", Default: true},
		{Stream: 5, Lang: "tur", Codec: "ass", Forced: true}}
	if fmt.Sprint(got) != fmt.Sprint(want) {
		t.Fatalf("got %+v", got)
	}
	if (*tsProbe)(nil).textTracks() != nil {
		t.Fatal("nil probe")
	}
}
