package streaming

import (
	"context"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"math"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
)

// Timeline tests: a subtitle cue at T of the file's timeline (its time from the file's start, as
// VLC / mpv show it) must show while the frame at T is on screen. Generated files carry their frame
// number in the picture and tone bursts at known times; they are played through the real
// seekpoint and play handlers, and every output frame's position is computed the way the players
// do it (muxed: seekpoint answer + presentation time of the fMP4; split: the presentation time).

const tlFrameW, tlFrameH = 320, 48

type tlFixture struct {
	name    string
	file    string
	fps     float64
	vStart  float64   // file time of frame 0 (first video pts - format start_time)
	aStart  float64   // file time of audio sample 0
	kfs     []float64 // keyframes, file time
	acodec  string
	vcopied bool
	cues    []cueRec // the text track's cues, file time (none without one)
}

// tlCueTimes: a cue every 2.5 s from 2.345 s, 1.1 s long (on ms that no frame or packet grid hits).
func tlCueTimes(d float64) []cueRec {
	var out []cueRec
	for i := 0; 2.345+2.5*float64(i)+1.1 < d; i++ {
		s := 2.345 + 2.5*float64(i)
		out = append(out, cueRec{s, s + 1.1})
	}
	return out
}

func tlCueSRT(t *testing.T, dir string, d float64) string {
	t.Helper()
	clock := func(s float64) string {
		ms := int(math.Round(s * 1000))
		return fmt.Sprintf("%02d:%02d:%02d,%03d", ms/3600000, ms/60000%60, ms/1000%60, ms%1000)
	}
	var b strings.Builder
	for i, c := range tlCueTimes(d) {
		fmt.Fprintf(&b, "%d\n%s --> %s\nline %d\n\n", i+1, clock(c.start), clock(c.end), i)
	}
	file := filepath.Join(dir, "cues.srt")
	if err := os.WriteFile(file, []byte(b.String()), 0o644); err != nil {
		t.Fatal(err)
	}
	return file
}

// tlCueSup writes the same cues as a PGS .sup: per cue a display set with a picture (PCS with
// one object, WDS, PDS, ODS of a 100x20 striped picture, END) and one that clears the screen.
func tlCueSup(t *testing.T, dir string, d float64) string {
	t.Helper()
	var b []byte
	seg := func(at float64, typ byte, data []byte) {
		pts := uint32(math.Round(at * 90000))
		b = append(b, 'P', 'G')
		b = binary.BigEndian.AppendUint32(b, pts)
		b = binary.BigEndian.AppendUint32(b, 0)
		b = append(b, typ)
		b = binary.BigEndian.AppendUint16(b, uint16(len(data)))
		b = append(b, data...)
	}
	window := []byte{1, 0, 0, 0, 0, 10, 0, 100, 0, 20} // one window: id 0 at 0,10, 100x20
	var rle []byte
	for range 20 {
		for x := range 100 {
			rle = append(rle, byte(1+x%2))
		}
		rle = append(rle, 0, 0) // end of line
	}
	for i, c := range tlCueTimes(d) {
		n := byte(2 * i)
		// PCS: 320x48, composition n, epoch start, palette 0, object 0 in window 0 at 0,10
		seg(c.start, 0x16, []byte{0x01, 0x40, 0, 0x30, 0x10, 0, n, 0x80, 0, 0, 1, 0, 0, 0, 0, 0, 0, 0, 10})
		seg(c.start, 0x17, window)
		seg(c.start, 0x14, []byte{0, 0, 1, 235, 128, 128, 255, 2, 16, 128, 128, 255})
		ods := []byte{0, 0, 0, 0xc0}
		ods = append(ods, byte((len(rle)+4)>>16), byte((len(rle)+4)>>8), byte(len(rle)+4), 0, 100, 0, 20)
		seg(c.start, 0x15, append(ods, rle...))
		seg(c.start, 0x80, nil)
		seg(c.end, 0x16, []byte{0x01, 0x40, 0, 0x30, 0x10, 0, n + 1, 0, 0, 0, 0})
		seg(c.end, 0x17, window)
		seg(c.end, 0x80, nil)
	}
	file := filepath.Join(dir, "cues.sup")
	if err := os.WriteFile(file, b, 0o644); err != nil {
		t.Fatal(err)
	}
	return file
}

// tlTextCues reads the cues of the file's first text track on the file's timeline, as the
// subtitle timing's own-track reads see them (pts - start_time; mov_text gap samples left out).
func tlTextCues(t *testing.T, file string, start float64) []cueRec {
	t.Helper()
	out, err := exec.Command("ffprobe", "-v", "error", "-select_streams", "s:0", "-show_entries",
		"packet=pts_time,duration_time,size", "-of", "csv=p=0", file).Output()
	if err != nil {
		t.Fatalf("ffprobe %s: %v", file, err)
	}
	var cues []cueRec
	for _, line := range strings.Split(string(out), "\n") {
		parts := strings.Split(strings.TrimSpace(line), ",")
		if len(parts) < 3 {
			continue
		}
		pts, err1 := strconv.ParseFloat(parts[0], 64)
		dur, err2 := strconv.ParseFloat(parts[1], 64)
		size, err3 := strconv.Atoi(parts[2])
		if err1 != nil || err2 != nil || err3 != nil || (strings.HasSuffix(file, ".mp4") && size <= 2) {
			continue
		}
		cues = append(cues, cueRec{pts - start, pts - start + dur})
	}
	return cues
}

// tlFrameNumber draws the frame number as 16 bars of 20 px (bright = 1, lowest bit left).
const tlFrameNumber = `geq=lum='if(mod(floor(N/pow(2,floor(X/20))),2),235,16)':cb=128:cr=128`

// tlBurst: a 1 kHz burst at every whole second of the audio's own time, 40 + 20·(s mod 8) ms long.
const tlBurst = `if(lt(mod(t,1),0.04+0.02*mod(floor(t),8)),0.5*sin(2*PI*1000*t),0)`

func tlNeedFFmpeg(t *testing.T) {
	t.Helper()
	for _, bin := range []string{"ffmpeg", "ffprobe"} {
		if _, err := exec.LookPath(bin); err != nil {
			t.Skip(bin + " not installed")
		}
	}
}

func tlRun(t *testing.T, args ...string) {
	t.Helper()
	if out, err := exec.Command("ffmpeg", append([]string{"-hide_banner", "-v", "error", "-y"}, args...)...).CombinedOutput(); err != nil {
		t.Fatalf("ffmpeg %v: %v %s", args, err, out)
	}
}

// tlMake generates a fixture: d seconds, keyframes forced at kfs (stream time), B-frames.
// vOff / aOff delay the video / audio input; extra are output options; remux re-muxes the result
// with -copyts (a TS capture remuxed to Matroska keeps its start time).
type tlSpec struct {
	name, ext, rate, vcodec, acodec string
	d, vOff, aOff                   float64
	kfs                             string
	extra                           []string
	remux                           string
	subs                            string // codec of a text track (srt, ass, webvtt, mov_text), or pgs
}

func tlMake(t *testing.T, dir string, sp tlSpec) tlFixture {
	t.Helper()
	file := filepath.Join(dir, sp.name+"."+sp.ext)
	ds := strconv.FormatFloat(sp.d, 'f', 3, 64)
	args := []string{}
	if sp.vOff != 0 {
		args = append(args, "-itsoffset", strconv.FormatFloat(sp.vOff, 'f', 3, 64))
	}
	args = append(args, "-f", "lavfi", "-i", "color=c=black:s=320x48:r="+sp.rate+":d="+ds+","+tlFrameNumber)
	if sp.aOff != 0 {
		args = append(args, "-itsoffset", strconv.FormatFloat(sp.aOff, 'f', 3, 64))
	}
	args = append(args, "-f", "lavfi", "-i", "aevalsrc='"+tlBurst+"|"+tlBurst+"':s=48000:d="+ds, "-map", "0:v", "-map", "1:a")
	var subs []string
	pgs := sp.subs == "pgs"
	if sp.subs != "" {
		subs = []string{"-i", tlCueSRT(t, dir, sp.d), "-map", "2:s", "-c:s", sp.subs}
		if pgs {
			// a packet per display set, as mkvmerge writes them (ffmpeg 7.1's muxer merges too)
			subs = []string{"-i", tlCueSup(t, dir, sp.d), "-map", "2:s", "-c:s", "copy", "-bsf:s", "pgs_frame_merge"}
		}
		if sp.remux != "" {
			// The capture's own clock: its streams start at about 10 + 1.4 s
			subs = append([]string{"-itsoffset", "10"}, subs...)
			subs[slices.Index(subs, "2:s")] = "1:s"
		} else {
			args = slices.Insert(args, slices.Index(args, "-map"), subs[:2]...)
			args = append(args, subs[2:]...)
		}
	}
	switch sp.vcodec {
	case "mpeg4":
		args = append(args, "-c:v", "mpeg4", "-q:v", "4", "-bf", "2", "-g", "1000")
	default:
		args = append(args, "-c:v", "libx264", "-preset", "veryfast", "-pix_fmt", "yuv420p",
			"-x264-params", "keyint=1000:min-keyint=1:scenecut=0:bframes=3:b-pyramid=normal")
	}
	args = append(args, "-force_key_frames", sp.kfs)
	switch sp.acodec {
	case "aac":
		args = append(args, "-c:a", "aac", "-b:a", "128k")
	default:
		args = append(args, "-c:a", sp.acodec, "-b:a", "192k")
	}
	args = append(args, sp.extra...)
	if sp.remux != "" {
		tmp := file + ".src." + sp.remux
		tlRun(t, append(args, tmp)...)
		remux := []string{"-copyts", "-i", tmp, "-map", "0", "-c", "copy", file}
		if subs != nil {
			remux = slices.Concat(remux[:3], subs[:4], remux[3:5], subs[4:6], remux[5:7], subs[6:], remux[7:])
		}
		tlRun(t, remux...)
	} else {
		tlRun(t, append(args, file)...)
	}
	fps, _ := tlRate(sp.rate)
	f := tlFixture{name: sp.name, file: file, fps: fps, acodec: sp.acodec, vcopied: sp.vcodec != "mpeg4"}
	f.vStart, f.aStart, f.kfs = tlProbeFixture(t, file)
	if pgs {
		// The generated cues where ffmpeg put the track (each input starts at 0)
		_, _, start := tlStreamStarts(t, file)
		pts := tlPacketPTS(t, file, "s:0", false)
		gen := tlCueTimes(sp.d)
		if len(pts) != 2*len(gen) {
			t.Fatalf("%s: %d PGS packets for %d cues", sp.name, len(pts), len(gen))
		}
		for _, c := range gen {
			d := pts[0] - gen[0].start - start
			f.cues = append(f.cues, cueRec{c.start + d, c.end + d})
		}
	} else if sp.subs != "" {
		_, _, start := tlStreamStarts(t, file)
		if f.cues = tlTextCues(t, file, start); len(f.cues) == 0 {
			t.Fatalf("%s: no cues in the text track", sp.name)
		}
	}
	return f
}

func tlRate(r string) (float64, error) {
	if a, b, ok := strings.Cut(r, "/"); ok {
		n, _ := strconv.ParseFloat(a, 64)
		d, _ := strconv.ParseFloat(b, 64)
		return n / d, nil
	}
	return strconv.ParseFloat(r, 64)
}

// tlProbeFixture reads where the streams start on the file's timeline and the keyframes.
func tlProbeFixture(t *testing.T, file string) (vStart, aStart float64, kfs []float64) {
	t.Helper()
	v, a, start := tlStreamStarts(t, file)
	for _, k := range tlPacketPTS(t, file, "v:0", true) {
		kfs = append(kfs, k-start)
	}
	return v - start, a - start, kfs
}

// tlStreamStarts: the first video and audio pts and the format's start_time.
func tlStreamStarts(t *testing.T, file string) (video, audio, start float64) {
	t.Helper()
	out, err := exec.Command("ffprobe", "-v", "error", "-show_entries", "format=start_time", "-of", "csv=p=0", file).Output()
	if err != nil {
		t.Fatal(err)
	}
	start, _ = strconv.ParseFloat(strings.TrimSpace(strings.Trim(strings.TrimSpace(string(out)), ",")), 64)
	first := func(stream string) float64 {
		pts := tlPacketPTS(t, file, stream, false)
		low := math.Inf(1)
		for _, p := range pts[:min(16, len(pts))] {
			low = min(low, p)
		}
		return low
	}
	return first("v:0"), first("a:0"), start
}

func tlPacketPTS(t *testing.T, file, stream string, keyOnly bool) []float64 {
	t.Helper()
	out, err := exec.Command("ffprobe", "-v", "error", "-select_streams", stream, "-show_entries", "packet=pts_time,flags",
		"-of", "csv=p=0", file).Output()
	if err != nil {
		t.Fatalf("ffprobe %s: %v", file, err)
	}
	var pts []float64
	for _, line := range strings.Split(string(out), "\n") {
		parts := strings.Split(strings.TrimSpace(line), ",")
		if len(parts) < 2 {
			continue
		}
		v, err := strconv.ParseFloat(parts[0], 64)
		if err != nil || (keyOnly && !strings.Contains(parts[1], "K")) {
			continue
		}
		pts = append(pts, v)
	}
	return pts
}

// ---- fragmented MP4 as an MSE player reads it ----

type tlSample struct {
	pts float64 // tfdt + durations + composition offset, seconds
	dur float64
}

type tlTrack struct {
	id        uint32
	kind      string // vide, soun
	timescale uint32
	elst      []int64 // media_time of each edit (-1: empty edit)
	samples   []tlSample
}

// chromeShift is what Chromium's MSE parser (TrackRunIterator) subtracts from the track's
// times: the first edit's media_time, unless that edit is empty (then nothing, the rest of the
// list is ignored as well).
func (tr *tlTrack) chromeShift() float64 {
	if len(tr.elst) == 0 || tr.elst[0] < 0 {
		return 0
	}
	return float64(tr.elst[0]) / float64(tr.timescale)
}

// tlParseFMP4 reads the presentation time of every sample (tfdt based, edit lists not applied).
func tlParseFMP4(b []byte) (map[uint32]*tlTrack, error) {
	tracks := map[uint32]*tlTrack{}
	trex := map[uint32]uint32{} // default sample duration
	var cur *tlTrack
	var walk func(b []byte, path string) error
	walk = func(b []byte, path string) error {
		for len(b) >= 8 {
			size := uint64(binary.BigEndian.Uint32(b))
			typ := string(b[4:8])
			hdr := uint64(8)
			if size == 1 {
				if len(b) < 16 {
					return fmt.Errorf("short box")
				}
				size, hdr = binary.BigEndian.Uint64(b[8:]), 16
			} else if size == 0 {
				size = uint64(len(b))
			}
			if size < hdr || size > uint64(len(b)) {
				return fmt.Errorf("bad box %s", typ)
			}
			body := b[hdr:size]
			switch typ {
			case "moov", "trak", "mdia", "minf", "stbl", "edts", "mvex", "moof":
				if typ == "trak" {
					cur = &tlTrack{}
				}
				if err := walk(body, path+"/"+typ); err != nil {
					return err
				}
				if typ == "trak" {
					tracks[cur.id] = cur
				}
			case "tkhd":
				off := 12
				if body[0] == 1 {
					off = 20
				}
				cur.id = binary.BigEndian.Uint32(body[off:])
			case "mdhd":
				off := 12
				if body[0] == 1 {
					off = 20
				}
				cur.timescale = binary.BigEndian.Uint32(body[off:])
			case "hdlr":
				cur.kind = string(body[8:12])
			case "elst":
				n := int(binary.BigEndian.Uint32(body[4:]))
				for i, off := 0, 8; i < n; i++ {
					if body[0] == 1 {
						cur.elst = append(cur.elst, int64(binary.BigEndian.Uint64(body[off+8:])))
						off += 20
					} else {
						cur.elst = append(cur.elst, int64(int32(binary.BigEndian.Uint32(body[off+4:]))))
						off += 12
					}
				}
			case "trex":
				trex[binary.BigEndian.Uint32(body[4:])] = binary.BigEndian.Uint32(body[12:])
			case "traf":
				if err := tlParseTraf(body, tracks, trex); err != nil {
					return err
				}
			}
			b = b[size:]
		}
		return nil
	}
	return tracks, walk(b, "")
}

func tlParseTraf(b []byte, tracks map[uint32]*tlTrack, trex map[uint32]uint32) error {
	var tr *tlTrack
	var defDur uint32
	var dts uint64
	for len(b) >= 8 {
		size := binary.BigEndian.Uint32(b)
		if size < 8 || int(size) > len(b) {
			return fmt.Errorf("bad traf box")
		}
		typ, body := string(b[4:8]), b[8:size]
		flags := binary.BigEndian.Uint32(body) & 0xffffff
		switch typ {
		case "tfhd":
			id := binary.BigEndian.Uint32(body[4:])
			tr = tracks[id]
			if tr == nil {
				return fmt.Errorf("traf of unknown track %d", id)
			}
			defDur = trex[id]
			off := 8
			if flags&0x1 != 0 {
				off += 8
			}
			if flags&0x2 != 0 {
				off += 4
			}
			if flags&0x8 != 0 {
				defDur = binary.BigEndian.Uint32(body[off:])
			}
		case "tfdt":
			if body[0] == 1 {
				dts = binary.BigEndian.Uint64(body[4:])
			} else {
				dts = uint64(binary.BigEndian.Uint32(body[4:]))
			}
		case "trun":
			n := int(binary.BigEndian.Uint32(body[4:]))
			off := 8
			if flags&0x1 != 0 {
				off += 4
			}
			if flags&0x4 != 0 {
				off += 4
			}
			for i := 0; i < n; i++ {
				dur := defDur
				var cts int64
				if flags&0x100 != 0 {
					dur = binary.BigEndian.Uint32(body[off:])
					off += 4
				}
				if flags&0x200 != 0 {
					off += 4
				}
				if flags&0x400 != 0 {
					off += 4
				}
				if flags&0x800 != 0 {
					v := binary.BigEndian.Uint32(body[off:])
					if body[0] == 1 {
						cts = int64(int32(v))
					} else {
						cts = int64(v)
					}
					off += 4
				}
				tr.samples = append(tr.samples, tlSample{pts: float64(int64(dts)+cts) / float64(tr.timescale),
					dur: float64(dur) / float64(tr.timescale)})
				dts += uint64(dur)
			}
		}
		b = b[size:]
	}
	return nil
}

func tlTrackOf(tracks map[uint32]*tlTrack, kind string) *tlTrack {
	for _, tr := range tracks {
		if tr.kind == kind {
			return tr
		}
	}
	return nil
}

// tlFrameNumbers decodes the video of an MP4 and reads the frame number of every frame
// (presentation order).
func tlFrameNumbers(t *testing.T, file string) []int {
	t.Helper()
	out, err := exec.Command("ffmpeg", "-v", "error", "-i", file, "-map", "0:v:0", "-fps_mode", "passthrough",
		"-f", "rawvideo", "-pix_fmt", "gray", "pipe:1").Output()
	if err != nil {
		t.Fatalf("decode %s: %v", file, err)
	}
	size := tlFrameW * tlFrameH
	var nums []int
	for off := 0; off+size <= len(out); off += size {
		row := out[off+tlFrameH/2*tlFrameW:]
		n := 0
		for bit := 0; bit < 16; bit++ {
			if row[bit*20+10] > 128 {
				n |= 1 << bit
			}
		}
		nums = append(nums, n)
	}
	return nums
}

// tlBursts finds the bursts in mono PCM: onset (s from sample 0) and the second they mark mod 8.
type tlOnset struct {
	at  float64
	mod int
}

func tlBursts(pcm []int16, rate int) []tlOnset {
	win := rate / 1000 // 1 ms
	var out []tlOnset
	on, start, quiet := false, 0, 0
	for i := 0; i+win <= len(pcm); i += win {
		peak := 0
		for _, v := range pcm[i : i+win] {
			peak = max(peak, int(math.Abs(float64(v))))
		}
		loud := peak > 3000
		switch {
		case loud && !on:
			// The exact first sample above the threshold
			j := i
			for j < i+win && math.Abs(float64(pcm[j])) <= 3000 {
				j++
			}
			// A 1 kHz sine reaches the threshold within a quarter period: back to its zero crossing
			start, on, quiet = j, true, 0
		case !loud && on:
			if quiet++; quiet >= 5 {
				dur := float64(i-(quiet-1)*win-start) / float64(rate)
				m := int(math.Round((dur - 0.04) / 0.02))
				// A burst already running at sample 0 (or under an encoder's start ramp) is cut:
				// neither its start nor its length is known
				if start > rate/20 {
					out = append(out, tlOnset{at: float64(start) / float64(rate), mod: ((m % 8) + 8) % 8})
				}
				on = false
			}
		case loud:
			quiet = 0
		}
	}
	return out
}

// tlBurstError: how far each burst is from its true file time, given the position of sample 0.
// near is the expected error (bursts repeat every 8 s by length).
func tlBurstError(f tlFixture, onsets []tlOnset, pos func(float64) float64, near float64) (first, worst float64, n int) {
	for i, o := range onsets {
		p := pos(o.at)
		// File time of burst k is aStart + k; k ≡ mod (8)
		k := math.Round((p - near - f.aStart - float64(o.mod)) / 8)
		e := p - (f.aStart + 8*k + float64(o.mod))
		if i == 0 {
			first = e
		}
		if math.Abs(e) > math.Abs(worst) {
			worst = e
		}
		n++
	}
	return first, worst, n
}

func tlDecodePCM(t *testing.T, file, stream string, rate int) []int16 {
	t.Helper()
	out, err := exec.Command("ffmpeg", "-v", "error", "-i", file, "-map", stream, "-ac", "1", "-ar", strconv.Itoa(rate),
		"-f", "s16le", "pipe:1").Output()
	if err != nil {
		t.Fatalf("decode audio %s: %v", file, err)
	}
	pcm := make([]int16, len(out)/2)
	for i := range pcm {
		pcm[i] = int16(binary.LittleEndian.Uint16(out[2*i:]))
	}
	return pcm
}

// ---- the harness ----

type tlEnv struct {
	r    *gin.Engine
	tap  *testTap
	cues *testCueTap
	dir  string
	kept []tlKept
	mode string
	s    *Service
}

// tlKept is an output kept for the browser check (TL_KEEP=<dir>): what a player would add to its
// times and how to read the frame numbers.
type tlKept struct {
	Name   string  `json:"name"`
	Video  string  `json:"video"`
	Audio  string  `json:"audio,omitempty"`
	Mode   string  `json:"mode"`
	Split  bool    `json:"split"`
	T      float64 `json:"t"`
	Offset float64 `json:"offset"`
	VStart float64 `json:"vstart"`
	FPS    float64 `json:"fps"`
}

func (e *tlEnv) keep(t *testing.T, f tlFixture, start, offset float64, split bool, vFile, aFile string) {
	dir := os.Getenv("TL_KEEP")
	if dir == "" {
		return
	}
	name := fmt.Sprintf("%s-%.3f-%s", f.name, start, strings.NewReplacer(" ", "", ",", "-").Replace(e.mode))
	k := tlKept{Name: name, Video: name + "-v.mp4", Mode: e.mode, Split: split, T: start, Offset: offset, VStart: f.vStart, FPS: f.fps}
	copyTo := func(src, dst string) {
		b, err := os.ReadFile(src)
		if err == nil {
			err = os.WriteFile(filepath.Join(dir, dst), b, 0o644)
		}
		if err != nil {
			t.Fatal(err)
		}
	}
	copyTo(vFile, k.Video)
	if split {
		k.Audio = name + "-a.mp4"
		copyTo(aFile, k.Audio)
	}
	e.kept = append(e.kept, k)
	b, _ := json.MarshalIndent(e.kept, "", " ")
	if err := os.WriteFile(filepath.Join(dir, "manifest.json"), b, 0o644); err != nil {
		t.Fatal(err)
	}
}

func tlServe(t *testing.T, dir string, files map[string]string) *tlEnv {
	t.Helper()
	src := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		file, ok := files[r.URL.Query().Get("link")]
		if !ok {
			http.NotFound(w, r)
			return
		}
		f, err := os.Open(file)
		if err != nil {
			http.NotFound(w, r)
			return
		}
		defer f.Close()
		http.ServeContent(w, r, filepath.Base(file), time.Time{}, f)
	}))
	t.Cleanup(src.Close)
	gin.SetMode(gin.TestMode)
	s := NewService(nil, Config{TorrServerURL: src.URL})
	tap := &testTap{}
	s.SetSpeechTap(tap)
	cues := &testCueTap{want: -1}
	s.SetCueTap(cues)
	r := gin.New()
	h := newHandler(s)
	r.GET("/play", h.play)
	r.GET("/seekpoint", h.seekPoint)
	return &tlEnv{r: r, tap: tap, cues: cues, dir: dir, s: s}
}

func (e *tlEnv) get(t *testing.T, url string) []byte {
	t.Helper()
	w := httptest.NewRecorder()
	e.r.ServeHTTP(w, httptest.NewRequest(http.MethodGet, url, nil))
	if w.Code != http.StatusOK {
		t.Fatalf("%s: %d %s", url, w.Code, w.Body.String())
	}
	return w.Body.Bytes()
}

// tlResult is one play request measured as the player shows it.
type tlResult struct {
	offset     float64 // what the player adds to the stream's time
	startFrame float64 // file time of the first frame
	vFirst     float64 // position error of the first frame
	vWorst     float64 // largest position error of any frame
	aFirst     float64 // position error of the first burst
	aWorst     float64
	bursts     int
	tapFirst   float64 // speech tap: error of the first burst heard
	tapWorst   float64
	taps       int
	cueFirst   float64 // cue tap: error of the first cue (start; end in cueWorst)
	cueWorst   float64
	cues       int
	cueMissed  int     // cues of the file from t on that did not come
	cueHeard   float64 // the last progress reported
	elst       string
	held       float64 // the first frame stays on screen this long (frames before it dropped)
	splitSeek  float64 // split: the seekpoint answer (track=video), the first frame's file time
	note       string
}

// tlPlay plays fixture f from t as a muxed stream (query adds the client caps) and measures it.
// split plays the video and the audio track requests instead.
func (e *tlEnv) tlPlay(t *testing.T, f tlFixture, hash string, start float64, query string, split bool) tlResult {
	t.Helper()
	ts := strconv.FormatFloat(start, 'f', 3, 64)
	base := "hash=" + hash + "&index=1&audio=0&t=" + ts + query
	var res tlResult
	if start > 0 || !split {
		// The players' offset: the seekpoint answer (web: any answer; TV: only exact ones). Split
		// streams need none; there it says where the video starts (TV splices).
		var sp struct {
			T     float64 `json:"t"`
			Exact *bool   `json:"exact"`
		}
		q := base
		if split {
			q += "&track=video"
		}
		if err := json.Unmarshal(e.get(t, "/seekpoint?"+q), &sp); err != nil {
			t.Fatal(err)
		}
		if sp.Exact != nil && !*sp.Exact {
			res.note = "seekpoint not exact"
		}
		if split {
			res.splitSeek = sp.T
		} else {
			res.offset = sp.T
		}
	}
	e.tap.mu.Lock()
	taps := len(e.tap.sinks)
	e.tap.mu.Unlock()
	e.cues.mu.Lock()
	cueTaps := len(e.cues.sinks)
	e.cues.mu.Unlock()

	videoQ, audioQ := base, base
	if split {
		videoQ, audioQ = base+"&track=video", base+"&track=audio"
	}
	vBody := e.get(t, "/play?"+videoQ)
	vFile := filepath.Join(e.dir, "out-v.mp4")
	if err := os.WriteFile(vFile, vBody, 0o644); err != nil {
		t.Fatal(err)
	}
	aFile := vFile
	if split {
		aFile = filepath.Join(e.dir, "out-a.mp4")
		if err := os.WriteFile(aFile, e.get(t, "/play?"+audioQ), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	e.keep(t, f, start, res.offset, split, vFile, aFile)
	if os.Getenv("TL_KEEP") != "" && !split && strings.Contains(query, "acodecs") {
		// Candidate: a muxed stream on the file's timeline, like the split pair
		p, err := e.s.probeFile(context.Background(), hash, 1)
		if err != nil {
			t.Fatal(err)
		}
		args := streamArgs(e.s.fileURL(hash, 1), start, 0, p.videoPlan(false), p.audioPlan(0, map[string]bool{"aac": true}), true, tsTrackBoth)
		args = args[:slices.Index(args, "-avoid_negative_ts")]
		if start > 0 {
			args = append(args, "-output_ts_offset", ts)
		}
		args = append(args, "-use_editlist", "0", "-movflags", "frag_keyframe+empty_moov+default_base_moof+frag_discont+negative_cts_offsets",
			"-frag_duration", "1000000", "-y", filepath.Join(e.dir, "abs.mp4"))
		if out, err := exec.Command("ffmpeg", args...).CombinedOutput(); err != nil {
			t.Fatalf("abs: %v %s", err, out)
		}
		mode := e.mode
		e.mode = "muxed-abs"
		e.keep(t, f, start, 0, false, filepath.Join(e.dir, "abs.mp4"), "")
		e.mode = mode
	}

	// Video: position of every frame against the file time its number says
	vt, err := tlParseFMP4(vBody)
	if err != nil {
		t.Fatal(err)
	}
	vtr := tlTrackOf(vt, "vide")
	nums := tlFrameNumbers(t, vFile)
	if vtr == nil || len(nums) == 0 {
		t.Fatalf("%s t=%s: no video", f.name, ts)
	}
	if len(vtr.elst) > 0 {
		res.elst += fmt.Sprintf(" video %v", vtr.elst)
	}
	pts := make([]float64, len(vtr.samples))
	for i, s := range vtr.samples {
		pts[i] = s.pts - vtr.chromeShift()
	}
	slices.Sort(pts)
	if len(pts) != len(nums) {
		res.note += fmt.Sprintf(" %d samples, %d frames decoded", len(pts), len(nums))
	}
	from := 0
	if len(pts) > 1 && pts[1]-pts[0] > 3/f.fps {
		// A re-encode squashes the frames before t into the first one, shown until t
		res.held = pts[1] - pts[0]
		from = 1
	}
	for i := from; i < min(len(pts), len(nums)); i++ {
		file := f.vStart + float64(nums[i])/f.fps
		e := res.offset + pts[i] - file
		if i == from {
			res.startFrame, res.vFirst = file, e
		}
		if math.Abs(e) > math.Abs(res.vWorst) {
			res.vWorst = e
		}
	}

	// Audio of the stream
	ab := vBody
	if split {
		ab, _ = os.ReadFile(aFile)
	}
	at, err := tlParseFMP4(ab)
	if err != nil {
		t.Fatal(err)
	}
	if atr := tlTrackOf(at, "soun"); atr != nil && len(atr.samples) > 0 {
		if len(atr.elst) > 0 {
			res.elst += fmt.Sprintf(" audio %v/%d", atr.elst, atr.timescale)
		}
		// Decoded PCM position -> the audio frame it belongs to -> that frame's presentation time
		// (a track starting after the other one gets its first sample moved to 0, made longer)
		durs := make([]float64, len(atr.samples))
		for i, s := range atr.samples {
			durs[i] = s.dur
		}
		slices.Sort(durs)
		frameLen := durs[len(durs)/2]
		shift := atr.chromeShift()
		pos := func(at float64) float64 {
			k := min(int(at/frameLen), len(atr.samples)-1)
			return res.offset + atr.samples[k].pts - shift + at - float64(k)*frameLen
		}
		on := tlBursts(tlDecodePCM(t, aFile, "0:a:0", 48000), 48000)
		res.aFirst, res.aWorst, res.bursts = tlBurstError(f, on, pos, res.vFirst)
	}

	// Speech tap of the audio request: sample 0 is t on the file's timeline
	e.tap.mu.Lock()
	var sink *recordSink
	if len(e.tap.sinks) > taps {
		sink = e.tap.sinks[len(e.tap.sinks)-1]
	}
	e.tap.mu.Unlock()
	if sink != nil {
		select {
		case <-sink.closed:
		case <-time.After(10 * time.Second):
			t.Fatalf("%s: tap not closed", f.name)
		}
		res.tapFirst, res.tapWorst, res.taps = tlBurstError(f, tlBursts(sink.pcm(), 8000), func(at float64) float64 { return start + at }, 0)
	}

	// Cue tap of the audio request: the file's own cues on the file's timeline
	e.cues.mu.Lock()
	var cues *recordCues
	if n := len(e.cues.sinks); n > cueTaps {
		if n > cueTaps+1 || math.Abs(e.cues.start[n-1]-start) > 1e-9 {
			t.Fatalf("%s: cue tap opened %d times, start %v", f.name, n-cueTaps, e.cues.start[cueTaps:])
		}
		cues = e.cues.sinks[n-1]
	}
	e.cues.mu.Unlock()
	if (cues != nil) != (len(f.cues) > 0) {
		t.Fatalf("%s: cue tap opened: %v", f.name, cues != nil)
	}
	if cues != nil {
		cues.wait(t)
		res.cues = len(cues.cues)
		for i, c := range cues.cues {
			want := f.cues[0]
			for _, w := range f.cues {
				if math.Abs(w.start-c.start) < math.Abs(want.start-c.start) {
					want = w
				}
			}
			if i == 0 {
				res.cueFirst = c.start - want.start
			}
			for _, d := range []float64{c.start - want.start, c.end - want.end} {
				if math.Abs(d) > math.Abs(res.cueWorst) {
					res.cueWorst = d
				}
			}
		}
		if n := len(cues.heard); n > 0 {
			res.cueHeard = cues.heard[n-1]
		}
		for _, w := range f.cues {
			if w.start >= start && !slices.ContainsFunc(cues.cues, func(c cueRec) bool { return math.Abs(c.start-w.start) < 0.5 }) {
				res.cueMissed++
			}
		}
	}
	return res
}

func (r tlResult) String() string {
	s := fmt.Sprintf("offset %7.3f  first frame %7.3f  video %+6.3f (worst %+6.3f)  audio %+6.3f (worst %+6.3f, %d)  tap %+6.3f (worst %+6.3f, %d)",
		r.offset, r.startFrame, r.vFirst, r.vWorst, r.aFirst, r.aWorst, r.bursts, r.tapFirst, r.tapWorst, r.taps)
	if r.cues > 0 {
		s += fmt.Sprintf("  cues %+6.3f (worst %+6.3f, %d, %d missed, heard to %.2f)", r.cueFirst, r.cueWorst, r.cues, r.cueMissed, r.cueHeard)
	}
	if r.elst != "" {
		s += "  [edit lists:" + r.elst + "]"
	}
	if r.splitSeek > 0 {
		s += fmt.Sprintf("  seekpoint %+.3f", r.splitSeek-r.startFrame)
	}
	if r.held > 0 {
		s += fmt.Sprintf("  [first frame held %.2f s]", r.held)
	}
	return s + r.note
}

// TestPlayTimeline is the end-to-end check of the time axis for every play path.
func TestPlayTimeline(t *testing.T) {
	tlNeedFFmpeg(t)
	if testing.Short() {
		t.Skip("slow")
	}
	dir := t.TempDir()
	kfs := "0,4.2,9.0,21.4,27.0,32.5,38.1,41.0,52.6"
	specs := []tlSpec{
		{name: "mkv", ext: "mkv", rate: "24000/1001", acodec: "aac", d: 60, kfs: kfs, subs: "srt"},
		{name: "mkv-eac3", ext: "mkv", rate: "24000/1001", acodec: "eac3", d: 60, kfs: kfs, subs: "ass"},
		{name: "mkv-adelay", ext: "mkv", rate: "24000/1001", acodec: "aac", d: 60, aOff: 0.5, kfs: kfs, subs: "webvtt"},
		{name: "mkv-vdelay", ext: "mkv", rate: "25", acodec: "ac3", d: 60, vOff: 0.3, kfs: kfs, subs: "srt"},
		{name: "mp4", ext: "mp4", rate: "25", acodec: "aac", d: 60, kfs: kfs, subs: "mov_text"},
		{name: "ts", ext: "ts", rate: "24000/1001", acodec: "ac3", d: 60, kfs: kfs, extra: []string{"-output_ts_offset", "10"}},
		{name: "mkv-from-ts", ext: "mkv", rate: "24000/1001", acodec: "ac3", d: 60, kfs: kfs, extra: []string{"-output_ts_offset", "10"}, remux: "ts", subs: "srt"},
		{name: "mkv-mpeg4", ext: "mkv", rate: "24000/1001", vcodec: "mpeg4", acodec: "aac", d: 60, kfs: kfs, subs: "srt"},
		{name: "mkv-pgs", ext: "mkv", rate: "24000/1001", acodec: "eac3", d: 60, kfs: kfs, subs: "pgs"},
	}
	if only := os.Getenv("TL_ONLY"); only != "" {
		specs = slices.DeleteFunc(specs, func(s tlSpec) bool { return !strings.Contains(","+only+",", ","+s.name+",") })
	}
	files := map[string]string{}
	var fixtures []tlFixture
	for i, sp := range specs {
		f := tlMake(t, dir, sp)
		fixtures = append(fixtures, f)
		files[tlHash(i)] = f.file
		t.Logf("%s: video from %.3f, audio from %.3f, keyframes %v", f.name, f.vStart, f.aStart, f.kfs)
	}
	env := tlServe(t, dir, files)
	modes := []struct {
		name, query string
		split       bool
	}{
		{"muxed, default caps", "", false}, // AAC copied, the rest converted
		{"muxed, audio copied", "&acodecs=aac,ac3,eac3", false},
		{"muxed, audio converted", "&acodecs=opus", false},
		{"split", "&acodecs=aac&split_acodecs=ac3,eac3", true},
	}
	for i, f := range fixtures {
		hash := tlHash(i)
		// Around the keyframes near 27 and 41 s, and between keyframes
		kf := func(k float64) float64 { return f.kfs[tlNearest(f.kfs, k)] }
		starts := []float64{0, 13.37, kf(27.0) + 0.05}
		if os.Getenv("TL_FULL") != "" {
			starts = []float64{0, 13.37, kf(27.0) - 0.05, kf(27.0), kf(27.0) + 0.05, kf(27.0) + 0.2, kf(41.0) + 0.1}
		}
		for _, start := range starts {
			start = math.Round(start*1000) / 1000
			for _, m := range modes {
				env.mode = m.name
				res := env.tlPlay(t, f, hash, start, m.query, m.split)
				t.Logf("%-12s t=%7.3f %-24s %s", f.name, start, m.name, res)
				// MPEG-TS seeks are not exact (logged only, see the docs)
				if f.name == "ts" {
					continue
				}
				// Within a frame (+ 1 ms rounding); audio within 50 ms of the video and the
				// tap within 25 ms of the file (the fixtures' own AAC priming is 21 ms)
				if tol := 1/f.fps + 0.002; math.Abs(res.vFirst) > tol || math.Abs(res.vWorst) > tol {
					t.Errorf("%s t=%.3f %s: video off by %+.3f s (worst %+.3f)", f.name, start, m.name, res.vFirst, res.vWorst)
				}
				if res.bursts > 0 && math.Abs(res.aWorst-res.vFirst) > 0.05 {
					t.Errorf("%s t=%.3f %s: audio %+.3f s vs video %+.3f", f.name, start, m.name, res.aWorst, res.vFirst)
				}
				if res.taps > 0 && math.Abs(res.tapWorst) > 0.025 {
					t.Errorf("%s t=%.3f %s: speech tap off by %+.3f s", f.name, start, m.name, res.tapWorst)
				}
				// Cue tap: the file's own cues within 10 ms, all of them from t on, read to the end
				if len(f.cues) > 0 && (math.Abs(res.cueWorst) > 0.010 || res.cueMissed > 0 ||
					res.cueHeard < f.cues[len(f.cues)-1].start) {
					t.Errorf("%s t=%.3f %s: cue tap off by %+.3f s, %d of %d missed, heard to %.2f", f.name, start,
						m.name, res.cueWorst, res.cueMissed, len(f.cues), res.cueHeard)
				}
			}
		}
	}
}

func tlHash(i int) string { return hx("0123456789abcdef"[i%16]) }

func tlNearest(list []float64, v float64) int {
	best := 0
	for i, x := range list {
		if math.Abs(x-v) < math.Abs(list[best]-v) {
			best = i
		}
	}
	return best
}

// The seek point runs play's input options, so both land on the same keyframe; the muxed
// stream has no edit lists and a re-encode keeps the source's frame times.
func TestStreamInputShared(t *testing.T) {
	copyV, reenc := tsVideoPlan{copy: true}, tsVideoPlan{}
	aac, opus := tsAudioPlan{copy: true}, tsAudioPlan{encoder: "libopus", layout: "stereo", bitrate: "160k"}
	for _, c := range []struct {
		plan  tsVideoPlan
		aplan tsAudioPlan
		small bool
	}{{copyV, aac, true}, {reenc, aac, false}, {copyV, opus, true}, {reenc, opus, true}} {
		in := strings.Join(streamInput("http://ts/f", 42.5, c.plan, c.aplan, c.small), " ")
		for _, track := range []tsTrack{tsTrackBoth, tsTrackVideo, tsTrackAudio} {
			if args := strings.Join(streamArgs("http://ts/f", 42.5, 0, c.plan, c.aplan, c.small, track), " "); !strings.HasPrefix(args, in+" ") {
				t.Fatalf("track %d: %s\ndoes not start with %s", track, args, in)
			}
		}
		if keyed := c.plan.copy || c.aplan.copy; strings.Contains(in, "-noaccurate_seek") != keyed {
			t.Fatalf("%+v %+v: %s", c.plan, c.aplan, in)
		}
	}
	muxed := strings.Join(streamArgs("http://ts/f", 42.5, 0, reenc, tsAudioPlan{copy: true, delay: true}, true, tsTrackBoth), " ")
	for _, want := range []string{"-avoid_negative_ts make_zero -use_editlist 0", "+delay_moov", "-fps_mode vfr -enc_time_base:v 1/90000"} {
		if !strings.Contains(muxed, want) {
			t.Fatalf("muxed: missing %q: %s", want, muxed)
		}
	}
	if args := strings.Join(streamArgs("http://ts/f", 42.5, 0, copyV, aac, true, tsTrackBoth), " "); strings.Contains(args, "fps_mode") {
		t.Fatalf("copied video: %s", args)
	}
}

func TestFrameDuration(t *testing.T) {
	p := &tsProbe{Streams: []tsProbeStream{{CodecType: "audio"}, {CodecType: "video", AvgFrameRate: "24000/1001", RFrameRate: "24000/1001"}}}
	if d := p.frameDuration(); math.Abs(d-1001.0/24000) > 1e-12 {
		t.Fatalf("23.976: %v", d)
	}
	p.Streams[1].AvgFrameRate = "0/0"
	p.Streams[1].RFrameRate = "25/1"
	if d := p.frameDuration(); d != 0.04 {
		t.Fatalf("r_frame_rate fallback: %v", d)
	}
	p.Streams[1].RFrameRate = "90000/1" // a time base, not a rate
	if d := p.frameDuration(); d != 0 {
		t.Fatalf("implausible rate: %v", d)
	}
}
