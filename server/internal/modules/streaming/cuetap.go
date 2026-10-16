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

// Cue tap: a play stream with a speech tap (muxed or split track=audio) can also copy one
// subtitle track of the file to a third ffmpeg output, so subtitle timing gets the file's own
// line starts for every stretch the viewer plays. The output is framecrc of the copied subtitle
// track (stream 0) and the played audio (stream 1): packet times and sizes only, no text or
// pictures, interleaved in time order, so an audio packet at T means every cue before T came
// already. Times count from the request's -ss (-avoid_negative_ts disabled keeps cues before
// it), so cue = time + t is on the file's timeline, as -copyts -start_at_zero reads and the
// speech tap. Same safeguards as the speech tap: drained by its own goroutine, events dropped
// when the sink is busy.

// CueTrack is a subtitle stream of the played file whose packets give cue times: a text track
// or a PGS (Blu-ray picture) track.
type CueTrack struct {
	Stream  int    // absolute stream index in the file (ffprobe index)
	Lang    string // ISO 639 tag as probed ("eng", "en", "" unknown)
	Title   string
	Codec   string // subrip, ass, webvtt, mov_text, hdmv_pgs_subtitle ...
	Default bool
	Forced  bool
	Frames  int // packets the container counts (Matroska NUMBER_OF_FRAMES tag), 0 unknown
}

// CueTap reads one subtitle track of play streams alongside the speech.
type CueTap interface {
	// OpenCues is called when a play stream with a cue track starts at start seconds of the
	// file; stream is the chosen CueTrack.Stream. ok false: no cues needed.
	OpenCues(hash string, index int, start float64, tracks []CueTrack) (stream int, sink CueSink, ok bool)
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
	tsCueQueue   = 256  // events waiting for the sink; more are dropped
	tsCueHeard   = 1.0  // seconds of progress between Heard calls
	tsCueOpenMax = 15.0 // seconds a picture cue without its end is held open (subtitle timing marks 15 s at most)
)

// tsSubPictureCodecs are the picture subtitle codecs whose packet times give cue times: a PGS
// display set with a picture starts a cue, the next one ends it.
var tsSubPictureCodecs = map[string]bool{"hdmv_pgs_subtitle": true}

// tsCueRule says which packets of a subtitle track are cues.
type tsCueRule struct {
	empty int // packets up to this size are no cue (mov_text gap samples, PGS clearing sets)
	// Picture tracks: packets carry no duration, a cue shows until the next packet; while one
	// shows, packets below update bytes only change it (palette or position). A cue the next
	// packet ends within min seconds is none.
	picture bool
	update  int
	min     float64
}

// tsPGSRule: in Matroska a PGS display set is one packet (tsPictureCues). One that clears the
// screen holds no palette or picture (PCS + WDS + END: 30 bytes, 39 with two windows); a
// palette-only update (fades) stays below ~1350 bytes (256 colours); a picture of a line is
// larger (The Mentalist S03E02 d3g and Night of the Living Dead 1968 BluRay: clears 30 bytes,
// pictures 1724-65013). A "line" shown under 0.25 s is steps of a fade, one read as a clear and
// the next as a picture (real lines show 0.9 s and more: The Mentalist S03E01 / S03E02).
var tsPGSRule = tsCueRule{empty: 64, picture: true, update: 1500, min: 0.25}

// cueTracks lists the subtitle tracks of a probed file whose packets give cue times (text and
// PGS; never VobSub or DVB).
func (p *tsProbe) cueTracks() []CueTrack {
	if p == nil {
		return nil
	}
	var out []CueTrack
	for i := range p.Streams {
		st := &p.Streams[i]
		if st.CodecType != "subtitle" || !tsSubTextCodecs[st.CodecName] && !tsSubPictureCodecs[st.CodecName] {
			continue
		}
		out = append(out, CueTrack{Stream: i, Lang: tsTag(st, "language"), Title: tsTag(st, "title"),
			Codec: st.CodecName, Default: st.Disposition["default"] == 1, Forced: st.Disposition["forced"] == 1,
			Frames: tsFrames(st)})
	}
	return out
}

// tsFrames is a stream's packet count from its Matroska statistics tag NUMBER_OF_FRAMES, in any
// case and also as older mkvmerge versions name it ("NUMBER_OF_FRAMES-eng"); 0 unknown.
func tsFrames(st *tsProbeStream) int {
	if n, err := strconv.Atoi(strings.TrimSpace(tsTag(st, "NUMBER_OF_FRAMES"))); err == nil && n > 0 {
		return n
	}
	for k, v := range st.Tags {
		if name, lang, ok := strings.Cut(k, "-"); ok && lang != "" && strings.EqualFold(name, "NUMBER_OF_FRAMES") {
			if n, err := strconv.Atoi(strings.TrimSpace(v)); err == nil && n > 0 {
				return n
			}
		}
	}
	return 0
}

// cueTap opens the cue tap of a play stream reading audio track n from start, if the file has
// a cue track the tap wants.
func (s *Service) cueTap(p *tsProbe, hash string, index int, start float64, audio int, label string) (tsTap, bool) {
	tracks := p.cueTracks()
	if s.cues == nil || len(tracks) == 0 {
		return tsTap{}, false
	}
	stream, sink, ok := s.cues.OpenCues(hash, index, start, tracks)
	if !ok || sink == nil {
		return tsTap{}, false
	}
	i := slices.IndexFunc(tracks, func(t CueTrack) bool { return t.Stream == stream })
	if i < 0 {
		sink.Close()
		return tsTap{}, false
	}
	var rule tsCueRule
	switch codec := tracks[i].Codec; {
	case codec == "mov_text":
		rule.empty = 2 // a gap sample: just its 16-bit text length
	case tsSubPictureCodecs[codec]:
		rule = tsPGSRule
	}
	// Times count from what ffmpeg seeks to: -ss as play formats it (none at 0)
	ss := 0.0
	if start > 0 {
		ss, _ = strconv.ParseFloat(strconv.FormatFloat(start, 'f', 3, 64), 64)
	}
	return tsTap{args: cueTapArgs(stream, audio), drop: sink.Close,
		pump: func(r io.ReadCloser) { tsPumpCues(r, sink, ss, rule, label) }}, true
}

// cueTapArgs copies subtitle stream (absolute index) and audio track n into framecrc.
func cueTapArgs(stream, n int) []string {
	return []string{"-map", fmt.Sprintf("0:%d", stream), "-map", fmt.Sprintf("0:a:%d?", n), "-c", "copy",
		"-avoid_negative_ts", "disabled", "-flush_packets", "1", "-f", "framecrc"}
}

type tsCueEvent struct {
	heard      bool
	start, end float64
}

// tsPumpCues drains the framecrc output r until EOF and hands cues (offset added) and progress
// to sink from a queue, never waiting for it. Subtitle packets are cues by rule. A picture cue
// is passed once its end comes (progress waits at its start meanwhile). After a dropped cue no
// more progress is reported (that stretch is not complete). It returns at once; done is closed
// after the sink's Close.
func tsPumpCues(r io.ReadCloser, sink CueSink, offset float64, rule tsCueRule, label string) (done <-chan struct{}) {
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
		cue := func(start, end float64) {
			select {
			case queue <- tsCueEvent{start: start, end: end}:
			default:
				lost++
			}
		}
		var pic *tsPictureCues
		if rule.picture {
			pic = &tsPictureCues{rule: rule, open: math.Inf(1), set: math.Inf(1)}
		}
		for {
			line, err := br.ReadString('\n')
			if err != nil {
				// a last line without its newline may be cut off; a picture cue showing at the
				// end showed until there
				if pic != nil {
					pic.endSet(cue)
					if heard > pic.open {
						cue(pic.open, heard)
					}
				}
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
				if pic != nil && at > pic.set {
					pic.endSet(cue) // every packet of the display set came
				}
				heard = max(heard, at+p.dur)
			case pic != nil:
				pic.packet(at, p.size, cue)
				heard = max(heard, at)
			case p.size > rule.empty:
				cue(at, at+p.dur)
				fallthrough
			default:
				heard = max(heard, at)
			}
			progress := heard
			if pic != nil {
				if heard-pic.open > tsCueOpenMax { // its end is lost
					cue(pic.open, pic.open+tsCueOpenMax)
					pic.open = math.Inf(1)
				}
				progress = min(progress, pic.open, pic.set) // not past a cue still to come
			}
			if lost == 0 && progress >= sent+tsCueHeard {
				select {
				case queue <- tsCueEvent{heard: true, start: progress}:
					sent = progress
				default:
				}
			}
		}
	}()
	return finished
}

// tsPictureCues follows the display sets of a picture track (tsPGSRule). Packets of one time
// are one display set (some remuxes keep a packet per segment).
type tsPictureCues struct {
	rule      tsCueRule
	open, set float64 // start of the cue showing, time of the display set being read (+Inf none)
	size      int     // bytes of that display set so far
}

// packet takes a packet of the track; cue gets the cues that ended.
func (c *tsPictureCues) packet(at float64, size int, cue func(start, end float64)) {
	if at != c.set {
		c.endSet(cue)
		c.set = at
	}
	c.size += size
}

// endSet applies the rule to the display set read: it clears the screen, changes the cue
// showing or starts one.
func (c *tsPictureCues) endSet(cue func(start, end float64)) {
	if math.IsInf(c.set, 1) {
		return
	}
	if !math.IsInf(c.open, 1) && (c.size <= c.rule.empty || c.size >= c.rule.update) {
		if c.set-c.open >= c.rule.min {
			cue(c.open, c.set)
		}
		c.open = math.Inf(1)
	}
	if c.size > c.rule.empty && math.IsInf(c.open, 1) {
		c.open = c.set
	}
	c.set, c.size = math.Inf(1), 0
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
