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
	tracks [][]CueTrack
}

func (c *testCueTap) OpenCues(hash string, index int, start float64, tracks []CueTrack) (int, CueSink, bool) {
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
	done := tsPumpCues(pr, sink, 13.37, tsCueRule{empty: 2}, "test")
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

// readSignal closes read once the pump closes its reader (it read everything).
type readSignal struct {
	*os.File
	read chan struct{}
}

func (r readSignal) Close() error {
	close(r.read)
	return r.File.Close()
}

// A stalled sink never holds up ffmpeg; once a cue was dropped no more progress is reported.
func TestPumpCuesNeverBlocks(t *testing.T) {
	pr, pw, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	sink := newRecordCues()
	sink.block = make(chan struct{})
	r := readSignal{pr, make(chan struct{})}
	done := tsPumpCues(r, sink, 0, tsCueRule{}, "test")
	wrote := make(chan struct{})
	go func() {
		pw.WriteString("#tb 0: 1/1000\n#tb 1: 1/1000\n")
		for i := range 5000 {
			fmt.Fprintf(pw, "0, %d, %d, 500, 6, 0x0\n1, %d, %d, 20, 6, 0x0\n", i*1000, i*1000, i*1000+500, i*1000+500)
		}
		pw.Close()
		close(wrote)
	}()
	select {
	case <-wrote:
	case <-time.After(5 * time.Second):
		t.Fatal("the writer was blocked by a stalled sink")
	}
	<-r.read // all of it passed while the sink stalled
	close(sink.block)
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
	tsPumpCues(pr, panicCues{}, 0, tsCueRule{}, "test")
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
			pump: func(r io.ReadCloser) { tsPumpCues(r, cues, 2, tsCueRule{}, "test") }})
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

func TestCueTracks(t *testing.T) {
	p := &tsProbe{Streams: []tsProbeStream{
		{CodecType: "video", CodecName: "h264"},
		{CodecType: "audio", CodecName: "aac"},
		{CodecType: "subtitle", CodecName: "hdmv_pgs_subtitle", Tags: map[string]string{"language": "eng",
			"NUMBER_OF_FRAMES": "1940", "NUMBER_OF_FRAMES-eng": "1940"}},
		{CodecType: "subtitle", CodecName: "subrip", Tags: map[string]string{"LANGUAGE": "eng", "title": "SDH"},
			Disposition: map[string]int{"default": 1}},
		{CodecType: "subtitle", CodecName: "dvd_subtitle"},
		{CodecType: "subtitle", CodecName: "ass", Tags: map[string]string{"language": "tur"}, Disposition: map[string]int{"forced": 1}},
		// older mkvmerge: the statistics tags carry their language
		{CodecType: "subtitle", CodecName: "hdmv_pgs_subtitle", Tags: map[string]string{"language": "eng", "NUMBER_OF_FRAMES-eng": "40"}},
		{CodecType: "subtitle", CodecName: "hdmv_pgs_subtitle", Tags: map[string]string{"number_of_frames": "1744"}},
	}}
	got := p.cueTracks()
	want := []CueTrack{{Stream: 2, Lang: "eng", Codec: "hdmv_pgs_subtitle", Frames: 1940},
		{Stream: 3, Lang: "eng", Title: "SDH", Codec: "subrip", Default: true},
		{Stream: 5, Lang: "tur", Codec: "ass", Forced: true},
		{Stream: 6, Lang: "eng", Codec: "hdmv_pgs_subtitle", Frames: 40},
		{Stream: 7, Codec: "hdmv_pgs_subtitle", Frames: 1744}}
	if fmt.Sprint(got) != fmt.Sprint(want) {
		t.Fatalf("got %+v", got)
	}
	if (*tsProbe)(nil).cueTracks() != nil {
		t.Fatal("nil probe")
	}
	// MPEG-TS: a PGS segment per packet, so only text tracks
	p.Format.FormatName = "mpegts"
	if got := p.cueTracks(); fmt.Sprint(got) != fmt.Sprint([]CueTrack{want[1], want[2]}) {
		t.Fatalf("mpegts: got %+v", got)
	}
}

// pumpFile runs the cue pump over a recorded framecrc output.
func pumpFile(t *testing.T, data string, offset float64, rule tsCueRule) *recordCues {
	t.Helper()
	pr, pw, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	sink := newRecordCues()
	done := tsPumpCues(pr, sink, offset, rule, "test")
	go func() {
		pw.WriteString(data)
		pw.Close()
	}()
	<-done
	return sink
}

// The PGS track of The Mentalist S03E02 (d3g WEBRip) as the cue tap of a play stream from
// 1205 s reads it (times and sizes as recorded, checksums zeroed). The seek landed on the
// keyframe at 1204.203: the screen clear at 1204.246 ends a line that started before it, the
// line at 1204.333 comes at -0.667. Every cue is the extracted track's (pgs_cues.json) to the ms.
func TestPumpCuesPGS(t *testing.T) {
	data, err := os.ReadFile("testdata/cuetap_pgs.framecrc")
	if err != nil {
		t.Fatal(err)
	}
	sink := pumpFile(t, string(data), 1205, tsPGSRule)
	want := []cueRec{{1204.333, 1207.118}, {1207.206, 1208.685}, {1208.772, 1210.774}, {1210.861, 1212.123},
		{1212.211, 1214.125}, {1214.213, 1216.258}, {1216.345, 1217.024}} // the last one shows past the read
	if len(sink.cues) != len(want) {
		t.Fatalf("cues %v", sink.cues)
	}
	for i, c := range sink.cues {
		if math.Abs(c.start-want[i].start) > 1e-6 || math.Abs(c.end-want[i].end) > 1e-6 {
			t.Fatalf("cue %d: %v, want %v", i, c, want[i])
		}
	}
	// Progress waits at the start of a cue that shows: Heard never passes a line still to come
	for _, h := range sink.heard {
		for _, c := range sink.cues {
			if h > c.start+1e-9 && h < c.end-1e-9 {
				t.Fatalf("heard %.3f while the cue %v showed", h, c)
			}
		}
	}
	if last := sink.heard[len(sink.heard)-1]; math.Abs(last-1217.024) > 1e-6 {
		t.Fatalf("heard %v", sink.heard)
	}
}

// Palette updates (fades) and moves change the cue showing; a new picture replaces it; a
// display set kept as a packet per segment counts as one; a fade step read as a clear and the
// next as a picture make no line of 0.1 s; a cue whose end never comes is held for
// tsCueOpenMax at most.
func TestPumpCuesPGSUpdates(t *testing.T) {
	var b strings.Builder
	b.WriteString("#tb 0: 1/1000\n#tb 1: 1/1000\n")
	sub := func(ms int, sizes ...int) {
		for _, size := range sizes {
			fmt.Fprintf(&b, "0, %d, %d, 0, %d, 0x0\n", ms, ms, size)
		}
	}
	sub(1000, 9000) // a line
	sub(1042, 120)  // fade-in steps: palette only
	sub(1083, 1300)
	sub(3000, 12000) // the next line replaces it
	sub(4000, 30)    // cleared
	sub(4500, 39)    // a clear of two windows: nothing shows
	sub(5000, 800)   // a short line (no cue showing)
	sub(6000, 30)
	sub(6500, 22, 13, 85, 4000, 3) // PCS, WDS, PDS, ODS, END as packets
	fmt.Fprintf(&b, "1, %d, %d, 32, 1024, 0x0\n", 6600, 6600)
	sub(6800, 14, 13, 3)
	sub(7000, 5000) // a line fading out: a small step reads as a clear, the next as a picture
	sub(7833, 300)
	sub(7875, 50)
	sub(7917, 120)
	sub(8000, 30)
	sub(9000, 5000) // a line whose clear is lost
	for ms := 7000; ms <= 30000; ms += 32 {
		fmt.Fprintf(&b, "1, %d, %d, 32, 1024, 0x0\n", ms, ms)
	}
	sink := pumpFile(t, b.String(), 100, tsPGSRule)
	want := []cueRec{{101, 103}, {103, 104}, {105, 106}, {106.5, 106.8}, {107, 107.875}, {109, 109 + tsCueOpenMax}}
	if fmt.Sprint(sink.cues) != fmt.Sprint(want) {
		t.Fatalf("cues %v, want %v", sink.cues, want)
	}
	if last := sink.heard[len(sink.heard)-1]; math.Abs(last-130.008) > 1e-6 { // the last audio packet's end
		t.Fatalf("heard %v", sink.heard)
	}
}

// A palette-only set starts no cue unless a clear came before it: not at the start of the
// read, not after a cue held open too long. A cue the read ends within min seconds is none.
func TestPumpCuesPGSNoFalseStarts(t *testing.T) {
	var b strings.Builder
	b.WriteString("#tb 0: 1/1000\n#tb 1: 1/1000\n")
	audio := 0
	audioTo := func(ms int) {
		for ; audio < ms; audio += 32 {
			fmt.Fprintf(&b, "1, %d, %d, 32, 1024, 0x0\n", audio, audio)
		}
	}
	sub := func(ms, size int) {
		audioTo(ms)
		fmt.Fprintf(&b, "0, %d, %d, 0, %d, 0x0\n", ms, ms, size)
	}
	sub(500, 300) // a fade of a line showing since before the read
	sub(1000, 30)
	sub(1500, 900) // a small picture after a clear
	sub(2500, 30)
	sub(3000, 5000) // a line whose clear is lost
	sub(19000, 300) // a fade step after it was held for tsCueOpenMax
	sub(19500, 6000)
	audioTo(19600) // the stream ends 0.1 s into that line
	sink := pumpFile(t, b.String(), 100, tsPGSRule)
	want := []cueRec{{101.5, 102.5}, {103, 103 + tsCueOpenMax}}
	if fmt.Sprint(sink.cues) != fmt.Sprint(want) {
		t.Fatalf("cues %v, want %v", sink.cues, want)
	}
}
