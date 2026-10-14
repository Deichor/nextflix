package streaming

import (
	"bufio"
	"fmt"
	"io"
	"log"
	"math"
	"slices"
	"strconv"
	"strings"
)

// Cue tap: a play stream with a speech tap (muxed or split track=audio) can also copy one text
// subtitle track of the file to a third ffmpeg output, so subtitle timing gets the file's own
// line starts for every stretch the viewer plays. The output is framecrc of the copied text
// track (stream 0) and the played audio (stream 1): packet times only, no text, interleaved in
// time order, so an audio packet at T means every cue before T came already. Times count from
// the request's -ss (-avoid_negative_ts disabled keeps cues before it), so cue = time + t is on
// the file's timeline, as -copyts -start_at_zero reads and the speech tap. Same safeguards as
// the speech tap: drained by its own goroutine, events dropped when the sink is busy.

// TextTrack is a text subtitle stream of the played file.
type TextTrack struct {
	Stream  int    // absolute stream index in the file (ffprobe index)
	Lang    string // ISO 639 tag as probed ("eng", "en", "" unknown)
	Title   string
	Codec   string // subrip, ass, webvtt, mov_text ...
	Default bool
	Forced  bool
}

// CueTap reads one text subtitle track of play streams alongside the speech.
type CueTap interface {
	// OpenCues is called when a play stream with a text track starts at start seconds of the
	// file; stream is the chosen TextTrack.Stream. ok false: no cues needed.
	OpenCues(hash string, index int, start float64, tracks []TextTrack) (stream int, sink CueSink, ok bool)
}

// CueSink takes cue times in seconds on the file's timeline (the speech tap's timeline). from
// is where the stream's reading started; Heard(t) reports that the stream has read the file up
// to t (cues before t are complete). Calls come from one goroutine; Close after the last.
type CueSink interface {
	Cue(start, end float64)
	Heard(t float64)
	Close()
}

// SetCueTap wires the cue tap (before the server starts).
func (s *Service) SetCueTap(t CueTap) { s.cues = t }

const (
	tsCueQueue = 256 // events waiting for the sink; more are dropped
	tsCueHeard = 1.0 // seconds of progress between Heard calls
)

// textTracks lists the text subtitle tracks of a probed file (never bitmap ones).
func (p *tsProbe) textTracks() []TextTrack {
	if p == nil {
		return nil
	}
	var out []TextTrack
	for i := range p.Streams {
		st := &p.Streams[i]
		if st.CodecType != "subtitle" || !tsSubTextCodecs[st.CodecName] {
			continue
		}
		out = append(out, TextTrack{Stream: i, Lang: tsTag(st, "language"), Title: tsTag(st, "title"),
			Codec: st.CodecName, Default: st.Disposition["default"] == 1, Forced: st.Disposition["forced"] == 1})
	}
	return out
}

// cueTap opens the cue tap of a play stream reading audio track n from start, if the file has
// a text track the tap wants.
func (s *Service) cueTap(p *tsProbe, hash string, index int, start float64, audio int, label string) (tsTap, bool) {
	tracks := p.textTracks()
	if s.cues == nil || len(tracks) == 0 {
		return tsTap{}, false
	}
	stream, sink, ok := s.cues.OpenCues(hash, index, start, tracks)
	if !ok || sink == nil {
		return tsTap{}, false
	}
	i := slices.IndexFunc(tracks, func(t TextTrack) bool { return t.Stream == stream })
	if i < 0 {
		sink.Close()
		return tsTap{}, false
	}
	empty := 0
	if tracks[i].Codec == "mov_text" {
		empty = 2 // a gap sample: just its 16-bit text length
	}
	// Times count from what ffmpeg seeks to: -ss as play formats it (none at 0)
	ss := 0.0
	if start > 0 {
		ss, _ = strconv.ParseFloat(strconv.FormatFloat(start, 'f', 3, 64), 64)
	}
	return tsTap{args: cueTapArgs(stream, audio), drop: sink.Close,
		pump: func(r io.ReadCloser) { tsPumpCues(r, sink, ss, empty, label) }}, true
}

// cueTapArgs copies text stream (absolute index) and audio track n into framecrc.
func cueTapArgs(stream, n int) []string {
	return []string{"-map", fmt.Sprintf("0:%d", stream), "-map", fmt.Sprintf("0:a:%d?", n), "-c", "copy",
		"-avoid_negative_ts", "disabled", "-flush_packets", "1", "-f", "framecrc"}
}

type tsCueEvent struct {
	heard      bool
	start, end float64
}

// tsPumpCues drains the framecrc output r until EOF and hands cues (offset added) and progress
// to sink from a queue, never waiting for it. Text packets up to empty bytes clear the screen
// and are no cues. After a dropped cue no more progress is reported (that stretch is not
// complete). It returns at once; done is closed after the sink's Close.
func tsPumpCues(r io.ReadCloser, sink CueSink, offset float64, empty int, label string) (done <-chan struct{}) {
	queue := make(chan tsCueEvent, tsCueQueue)
	finished := make(chan struct{})
	go func() {
		defer close(finished)
		defer func() {
			if v := recover(); v != nil {
				log.Printf("cue tap (%s): sink failed: %v", label, v)
			}
		}()
		for e := range queue {
			if e.heard {
				sink.Heard(e.start)
			} else {
				sink.Cue(e.start, e.end)
			}
		}
		sink.Close()
	}()
	go func() {
		defer close(queue)
		defer r.Close()
		br := bufio.NewReader(r)
		tb := map[string][2]float64{}
		heard, sent := -1.0, -1.0
		lost := 0
		for {
			line, err := br.ReadString('\n')
			if err != nil {
				// a last line without its newline may be cut off
				if heard > sent && lost == 0 {
					select {
					case queue <- tsCueEvent{heard: true, start: heard}:
					default:
					}
				}
				if lost > 0 {
					log.Printf("cue tap (%s): %d cues dropped (sink busy)", label, lost)
				}
				return
			}
			p, ok := tsParseCueLine(strings.TrimSpace(line), tb)
			if !ok {
				continue
			}
			at := p.pts + offset
			switch {
			case p.id != "0":
				heard = max(heard, at+p.dur)
			case p.size > empty:
				select {
				case queue <- tsCueEvent{start: at, end: at + p.dur}:
				default:
					lost++
				}
				fallthrough
			default:
				heard = max(heard, at)
			}
			if lost == 0 && heard >= sent+tsCueHeard {
				select {
				case queue <- tsCueEvent{heard: true, start: heard}:
					sent = heard
				default:
				}
			}
		}
	}()
	return finished
}

type tsCuePacket struct {
	id       string // output stream: "0" text, "1" audio
	pts, dur float64
	size     int
}

// tsParseCueLine reads a framecrc line: "#tb 0: 1/1000" sets a time base (kept in tb), a packet
// line "0, dts, pts, duration, size, crc" gives a packet (times in seconds).
func tsParseCueLine(l string, tb map[string][2]float64) (tsCuePacket, bool) {
	if rest, found := strings.CutPrefix(l, "#tb "); found {
		k, v, _ := strings.Cut(rest, ":")
		a, b, _ := strings.Cut(strings.TrimSpace(v), "/")
		num, err1 := strconv.ParseFloat(a, 64)
		den, err2 := strconv.ParseFloat(b, 64)
		if err1 == nil && err2 == nil && num > 0 && den > 0 {
			tb[strings.TrimSpace(k)] = [2]float64{num, den}
		}
		return tsCuePacket{}, false
	}
	if l == "" || l[0] == '#' {
		return tsCuePacket{}, false
	}
	f := strings.Split(l, ",")
	if len(f) != 6 {
		return tsCuePacket{}, false
	}
	id := strings.TrimSpace(f[0])
	unit, known := tb[id]
	p, err1 := strconv.ParseInt(strings.TrimSpace(f[2]), 10, 64)
	d, err2 := strconv.ParseInt(strings.TrimSpace(f[3]), 10, 64)
	size, err3 := strconv.Atoi(strings.TrimSpace(f[4]))
	if !known || err1 != nil || err2 != nil || err3 != nil || p == math.MinInt64 { // MinInt64: no pts
		return tsCuePacket{}, false
	}
	return tsCuePacket{id: id, pts: float64(p) * unit[0] / unit[1], dur: float64(max(d, 0)) * unit[0] / unit[1], size: size}, true
}
