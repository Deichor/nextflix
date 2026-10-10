package streaming

import (
	"context"
	"fmt"
	"io"
	"log"
	"os"
	"os/exec"
	"slices"
	"strings"
	"sync/atomic"
)

// Speech tap: a play stream with audio (muxed or split track=audio) also writes the dialogue of
// the played audio track as 8 kHz mono PCM to a second ffmpeg output (fd 3). Subtitle timing
// listens to it, so every stretch the viewer plays is a timing reference without reading the
// file again. The tap never slows playback: its own goroutine drains the pipe and drops what the
// sink cannot take in time, and a play ffmpeg that fails with the tap runs again without it.

// SpeechTap receives the dialogue audio of play streams (wired in main.go).
type SpeechTap interface {
	// Open is called when a play stream with audio starts at start seconds of the file. nil
	// means the file needs no speech.
	Open(hash string, index int, start float64) SpeechSink
}

// SpeechSink takes the PCM of one play stream: 8 kHz mono s16le, sample 0 at the stream's
// start. at is the byte offset of pcm in the stream (bytes dropped while the sink was busy
// leave a gap); pcm belongs to the sink. Calls come from one goroutine, Close after the last.
type SpeechSink interface {
	Write(at int64, pcm []byte)
	Close()
}

// SetSpeechTap wires the speech tap (before the server starts).
func (s *Service) SetSpeechTap(t SpeechTap) { s.speech = t }

const (
	tsTapChunk = 16000 // bytes per handover: 1 s of PCM
	tsTapQueue = 32    // chunks waiting for the sink; more are dropped
)

// tsFFmpegBin is the ffmpeg binary of play streams (tests use a fake).
var tsFFmpegBin = "ffmpeg"

// speechTapArgs is the tap output of a play stream for audio track n: the centre channel of
// 5.1 / 7.1, else a downmix, at 8 kHz (the signal the subtitle timing's own window reads use).
// aresample with first_pts=0 trims or pads the start so sample 0 is the stream's start time t
// on the file's timeline, also when the seek starts on an earlier keyframe (-noaccurate_seek).
func speechTapArgs(n int, a *tsProbeStream) []string {
	af := "aresample=8000:async=1:first_pts=0"
	if tsHasCenter(a.ChannelLayout, a.Channels) {
		af = "pan=mono|c0=FC," + af
	}
	return []string{"-map", fmt.Sprintf("0:a:%d", n), "-af", af, "-ac", "1", "-c:a", "pcm_s16le", "-f", "s16le", "pipe:3"}
}

// tsHasCenter reports whether an audio layout has a front centre (dialogue) channel.
func tsHasCenter(layout string, channels int) bool {
	l := strings.ToLower(layout)
	if l == "" {
		return channels >= 6
	}
	for _, p := range []string{"3.0", "3.1", "4.0", "4.1", "5.0", "5.1", "6.0", "6.1", "7.0", "7.1"} {
		if strings.HasPrefix(l, p) {
			return true
		}
	}
	return strings.Contains(l, "fc")
}

// tsRunPlay runs a play ffmpeg writing to out. With a sink, tapArgs add the speech output on
// fd 3; when that run fails before writing anything (and the viewer is still there), it runs
// again without the tap.
func tsRunPlay(ctx context.Context, args, tapArgs []string, sink SpeechSink, out *tsFirstWrite, stderr *strings.Builder, label string) error {
	if sink != nil {
		err := tsRunTapped(ctx, append(slices.Clone(args), tapArgs...), sink, out, stderr, label)
		if err == nil || ctx.Err() != nil || out.wrote {
			return err
		}
		log.Printf("speech tap (%s): ffmpeg failed with the tap, playing without it: %v %s", label, err,
			strings.TrimSpace(stderr.String()))
		stderr.Reset()
	}
	cmd := exec.CommandContext(ctx, tsFFmpegBin, args...)
	cmd.Stdout, cmd.Stderr = out, stderr
	return cmd.Run()
}

func tsRunTapped(ctx context.Context, args []string, sink SpeechSink, out *tsFirstWrite, stderr *strings.Builder, label string) error {
	pr, pw, err := os.Pipe()
	if err != nil {
		sink.Close()
		return err
	}
	tsPumpSpeech(pr, sink, label)
	cmd := exec.CommandContext(ctx, tsFFmpegBin, args...)
	cmd.Stdout, cmd.Stderr = out, stderr
	cmd.ExtraFiles = []*os.File{pw} // fd 3
	err = cmd.Start()
	pw.Close() // the pump sees EOF once ffmpeg is gone
	if err != nil {
		return err
	}
	return cmd.Wait()
}

type tsTapPiece struct {
	at  int64
	pcm []byte
}

// tsPumpSpeech drains r until EOF and hands the PCM to sink from a queue, never waiting for it:
// pieces that do not fit the queue are dropped (the next piece's offset shows the gap). It
// returns at once; done is closed after the sink's Close.
func tsPumpSpeech(r io.ReadCloser, sink SpeechSink, label string) (done <-chan struct{}) {
	queue := make(chan tsTapPiece, tsTapQueue)
	finished := make(chan struct{})
	var dropped atomic.Int64
	go func() {
		defer close(finished)
		defer func() {
			if v := recover(); v != nil {
				log.Printf("speech tap (%s): sink failed: %v", label, v)
			}
		}()
		for p := range queue {
			sink.Write(p.at, p.pcm)
		}
		sink.Close()
	}()
	go func() {
		defer close(queue)
		defer r.Close()
		var at int64
		for {
			buf := make([]byte, tsTapChunk)
			n, err := io.ReadFull(r, buf)
			if n > 0 {
				select {
				case queue <- tsTapPiece{at: at, pcm: buf[:n]}:
				default:
					dropped.Add(int64(n))
				}
				at += int64(n)
			}
			if err != nil {
				if d := dropped.Load(); d > 0 {
					log.Printf("speech tap (%s): %.0f s of %.0f s dropped (sink busy)", label, float64(d)/tsTapChunk,
						float64(at)/tsTapChunk)
				}
				return
			}
		}
	}()
	return finished
}
