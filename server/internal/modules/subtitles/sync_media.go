package subtitles

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"math"
	"net"
	"net/http"
	"os/exec"
	"strconv"
	"strings"
	"sync/atomic"
	"time"
)

// The timing reference is read from a few windows of the playing file: ffmpeg seeks with
// -ss (only that stretch of the file is read, through TorrServer or the TorBox proxy) and
// takes the cues of the file's own text subtitle track there or, without one, decodes the
// dialogue channel as 8 kHz mono PCM for speech detection.

const syncSampleRate = 8000

// mediaProbe is what the timing needs to know about the playing file.
type mediaProbe struct {
	Duration float64 `json:"duration"`
	FPS      float64 `json:"fps"`
	Audio    int     `json:"audio"`    // stream index of the audio read for speech, -1 none
	Center   bool    `json:"center"`   // that audio has a front centre (dialogue) channel
	TextSub  int     `json:"text_sub"` // stream index of a text subtitle track, -1 none
	Bitrate  float64 `json:"bitrate"`  // bytes per second of the file (0 = unknown)
}

type mediaProbeOutput struct {
	Format struct {
		Duration string `json:"duration"`
		BitRate  string `json:"bit_rate"`
		Size     string `json:"size"`
	} `json:"format"`
	Streams []struct {
		Index         int               `json:"index"`
		CodecName     string            `json:"codec_name"`
		CodecType     string            `json:"codec_type"`
		Channels      int               `json:"channels"`
		ChannelLayout string            `json:"channel_layout"`
		AvgFrameRate  string            `json:"avg_frame_rate"`
		RFrameRate    string            `json:"r_frame_rate"`
		Tags          map[string]string `json:"tags"`
		Disposition   map[string]int    `json:"disposition"`
	} `json:"streams"`
}

func probeMedia(ctx context.Context, url string) (mediaProbe, error) {
	ctx, cancel := context.WithTimeout(ctx, embeddedProbeTimeout)
	defer cancel()
	args := withInput([]string{"-v", "error", "-print_format", "json", "-show_format", "-show_streams"}, url)
	out, err := exec.CommandContext(ctx, "ffprobe", args...).Output()
	if err != nil {
		return mediaProbe{}, fmt.Errorf("ffprobe failed: %w", err)
	}
	return parseMediaProbe(out)
}

func parseMediaProbe(out []byte) (mediaProbe, error) {
	var p mediaProbeOutput
	if err := json.Unmarshal(out, &p); err != nil {
		return mediaProbe{}, err
	}
	m := mediaProbe{Audio: -1, TextSub: -1}
	m.Duration, _ = strconv.ParseFloat(p.Format.Duration, 64)
	if br, _ := strconv.ParseFloat(p.Format.BitRate, 64); br > 0 {
		m.Bitrate = br / 8
	} else if size, _ := strconv.ParseFloat(p.Format.Size, 64); size > 0 && m.Duration > 0 {
		m.Bitrate = size / m.Duration
	}
	subScore := -1
	for _, s := range p.Streams {
		switch s.CodecType {
		case "video":
			if s.Disposition["attached_pic"] == 1 || m.FPS > 0 {
				continue
			}
			m.FPS = frameRate(s.AvgFrameRate)
			if m.FPS <= 0 || m.FPS > 120 {
				m.FPS = frameRate(s.RFrameRate)
			}
		case "audio":
			// The default track, else the first
			if m.Audio < 0 || s.Disposition["default"] == 1 && !isDefaultStream(p, m.Audio) {
				m.Audio = s.Index
				m.Center = hasCenter(s.ChannelLayout, s.Channels)
			}
		case "subtitle":
			score := ownTrackScore(s.CodecName, s.Tags["language"], s.Tags["title"], s.Disposition["forced"] == 1,
				s.Disposition["hearing_impaired"] == 1)
			if score > subScore {
				subScore, m.TextSub = score, s.Index
			}
		}
	}
	return m, nil
}

// ownTrackScore rates a subtitle track of the file as the timing reference (higher is better, -1
// not usable).
func ownTrackScore(codec, lang, title string, forced, hi bool) int {
	if !textSubCodecs[codec] {
		return -1
	}
	f, h := subFlags(title)
	if forced || f {
		return -1 // a few lines only
	}
	if hi || h {
		return 1 // sound descriptions blur the speech timing a little
	}
	if iso639To1(lang) == "en" {
		return 3 // a line per cue (some languages merge lines)
	}
	return 2
}

// isDefaultStream reports whether the stream has the default disposition.
func isDefaultStream(p mediaProbeOutput, index int) bool {
	for _, s := range p.Streams {
		if s.Index == index {
			return s.Disposition["default"] == 1
		}
	}
	return false
}

func hasCenter(layout string, channels int) bool {
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

// frameRate reads ffprobe's "24000/1001".
func frameRate(s string) float64 {
	a, b, ok := strings.Cut(s, "/")
	n, err1 := strconv.ParseFloat(a, 64)
	if !ok {
		if err1 != nil {
			return 0
		}
		return n
	}
	d, err2 := strconv.ParseFloat(b, 64)
	if err1 != nil || err2 != nil || d == 0 {
		return 0
	}
	return n / d
}

// syncWindowReader reads one reference window (tests replace it).
type syncWindowReader func(ctx context.Context, url string, p mediaProbe, start, length float64) (refWindow, error)

// ffmpegSyncWindow reads [start, start+length) of the file: the cues of its text subtitle track
// when it has one with enough lines there (exact timing), else speech from the audio. The reads
// go through a byte cap (a subtitle read in a stretch without lines would read on to the next
// cue, possibly the end of the file).
func ffmpegSyncWindow(ctx context.Context, url string, p mediaProbe, start, length float64) (refWindow, error) {
	capped, used, stop, err := cappedURL(url, windowByteCap(p, length))
	if err != nil {
		return refWindow{}, err
	}
	defer stop()
	w, err := readSyncWindow(ctx, capped, p, start, length)
	w.Read = used()
	return w, err
}

// ffmpegSubsWindow reads only the cues of the file's own text track in [start, start+length)
// (the early windows of a file above the byte budget; no audio fallback).
func ffmpegSubsWindow(ctx context.Context, url string, p mediaProbe, start, length float64) (refWindow, error) {
	capped, used, stop, err := cappedURL(url, windowByteCap(p, length))
	if err != nil {
		return refWindow{}, err
	}
	defer stop()
	w, ok, err := readSubsWindow(ctx, capped, p, start, length)
	w.Read = used()
	if !ok && err == nil {
		err = fmt.Errorf("window at %.0fs: too few lines of its own track", start)
	}
	return w, err
}

// readSubsWindow reads the cues of the text track in the window; ok when there are enough.
func readSubsWindow(ctx context.Context, url string, p mediaProbe, start, length float64) (refWindow, bool, error) {
	if p.TextSub < 0 {
		return refWindow{}, false, nil
	}
	// -copyts: after -ss a subtitle's times count from the keyframe before start, not from start
	args := withInput([]string{"-nostdin", "-v", "error",
		"-reconnect", "1", "-reconnect_on_network_error", "1", "-reconnect_delay_max", "5",
		"-ss", strconv.FormatFloat(start, 'f', 3, 64), "-copyts", "-start_at_zero"}, url)
	args = append(args, "-map", fmt.Sprintf("0:%d", p.TextSub), "-c:s", "webvtt",
		"-to", strconv.FormatFloat(start+length, 'f', 3, 64), "-f", "webvtt", "pipe:1")
	out, err := runFFmpeg(ctx, args, 4<<20)
	if ctx.Err() != nil {
		return refWindow{}, false, err
	}
	w, ok := cueWindow(out, start, int(length*syncRate))
	return w, ok, nil // also when the cap ended the read: the window's lines are in
}

func readSyncWindow(ctx context.Context, url string, p mediaProbe, start, length float64) (refWindow, error) {
	frames := int(length * syncRate)
	if w, ok, err := readSubsWindow(ctx, url, p, start, length); ctx.Err() != nil {
		return refWindow{}, err
	} else if ok {
		return w, nil
	}
	if p.Audio < 0 {
		return refWindow{}, fmt.Errorf("window at %.0fs: no lines and no audio", start)
	}
	// Pads a late first sample and gaps so sample 0 is start, like the speech tap
	resample := "aresample=async=1:first_pts=0"
	af := []string{"-ac", "1", "-af", resample}
	if p.Center {
		af = []string{"-af", "pan=mono|c0=FC," + resample}
	}
	args := withInput([]string{"-nostdin", "-v", "error",
		"-reconnect", "1", "-reconnect_on_network_error", "1", "-reconnect_delay_max", "5",
		"-ss", strconv.FormatFloat(start, 'f', 3, 64)}, url)
	args = append(args, "-map", fmt.Sprintf("0:%d", p.Audio))
	args = append(args, af...)
	args = append(args, "-ar", strconv.Itoa(syncSampleRate), "-t", strconv.FormatFloat(length, 'f', 3, 64),
		"-f", "s16le", "pipe:1")
	raw, err := runFFmpeg(ctx, args, int(length*syncSampleRate*2)+1<<16)
	if err != nil && len(raw) == 0 {
		return refWindow{}, err
	}
	pcm := make([]int16, len(raw)/2)
	for i := range pcm {
		pcm[i] = int16(binary.LittleEndian.Uint16(raw[2*i:]))
	}
	speech := speechFrames(pcm, syncSampleRate)
	if len(speech) > frames {
		speech = speech[:frames]
	}
	if len(speech) < frames/2 {
		return refWindow{}, fmt.Errorf("window at %.0fs: only %d frames read", start, len(speech))
	}
	return refWindow{Start: start, Kind: "audio", Speech: speech}, nil
}

// runFFmpeg returns ffmpeg's output (at most max bytes) and, when it failed, also the error.
func runFFmpeg(ctx context.Context, args []string, max int) ([]byte, error) {
	cmd := exec.CommandContext(ctx, "ffmpeg", args...)
	var stdout limitedBuffer
	stdout.max = max
	var stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	if err := cmd.Run(); err != nil {
		return stdout.Bytes(), fmt.Errorf("ffmpeg: %w: %s", err, truncateString(strings.TrimSpace(stderr.String()), 300))
	}
	return stdout.Bytes(), nil
}

// cueWindow turns a window's cues (times of the file) into a reference window; ok
// is false when there are too few cues to rely on.
func cueWindow(vtt []byte, start float64, frames int) (refWindow, bool) {
	var cues []liveCue
	parseVTTStream(bytes.NewReader(vtt), func(c liveCue) bool {
		cues = append(cues, c)
		return true
	})
	if len(cues) < 4 {
		return refWindow{}, false
	}
	speech := make([]byte, frames)
	for _, c := range cues {
		markCue(speech, c.start-start, c.end-start)
	}
	return refWindow{Start: start, Kind: "subs", Speech: speech}, true
}

// fullCueWindows cuts a whole subtitle of the file (synced to it) into reference windows.
func fullCueWindows(cues []span, length float64) []refWindow {
	end := 0.0
	for _, c := range cues {
		end = math.Max(end, c.end)
	}
	var out []refWindow
	for start := 0.0; start < end; start += length {
		w := refWindow{Start: start, Kind: "subs", Speech: make([]byte, int(length*syncRate))}
		for _, c := range cues {
			markCue(w.Speech, c.start-start, c.end-start)
		}
		out = append(out, w)
	}
	return out
}

// markCue marks a cue (seconds from the window start) in a reference window: 1 while it shows,
// 2 where it starts. A cue cut at the window start (started before it) has no start here.
func markCue(speech []byte, start, end float64) {
	a := int(math.Round(start * syncRate))
	b := min(int(math.Round(end*syncRate)), len(speech))
	for i := max(a, 0); i < b; i++ {
		if speech[i] == 0 {
			speech[i] = 1
		}
	}
	if a > 0 && a < len(speech) {
		speech[a] = 2
	}
}

// windowByteCap is what one window read may pull: its stretch of the file with some room for
// the header and index reads (160 MB when the bitrate is unknown).
func windowByteCap(p mediaProbe, length float64) int64 {
	if p.Bitrate <= 0 {
		return 160 << 20
	}
	return int64(p.Bitrate*length*1.25) + 16<<20
}

// cappedURL serves url to ffmpeg through a local proxy that stops after max bytes in total
// (Range requests are passed on); used reports the bytes passed, stop ends the proxy.
func cappedURL(url string, max int64) (capped string, used func() int64, stop func(), err error) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return "", nil, nil, err
	}
	var n atomic.Int64
	srv := &http.Server{ReadHeaderTimeout: 10 * time.Second, Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if n.Load() >= max {
			http.Error(w, "byte cap reached", http.StatusServiceUnavailable)
			return
		}
		req, err := http.NewRequestWithContext(r.Context(), r.Method, url, nil)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadGateway)
			return
		}
		if rg := r.Header.Get("Range"); rg != "" {
			req.Header.Set("Range", rg)
		}
		resp, err := http.DefaultTransport.RoundTrip(req)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadGateway)
			return
		}
		defer resp.Body.Close()
		for _, h := range []string{"Content-Type", "Content-Length", "Content-Range", "Accept-Ranges"} {
			if v := resp.Header.Get(h); v != "" {
				w.Header().Set(h, v)
			}
		}
		w.WriteHeader(resp.StatusCode)
		buf := make([]byte, 64<<10)
		for {
			k, rerr := resp.Body.Read(buf)
			if k > 0 {
				if n.Add(int64(k)) > max {
					return // cut off: ffmpeg ends with what it has
				}
				if _, werr := w.Write(buf[:k]); werr != nil {
					return
				}
			}
			if rerr != nil {
				return
			}
		}
	})}
	go func() { _ = srv.Serve(ln) }()
	return "http://" + ln.Addr().String() + "/file", n.Load, func() { _ = srv.Close() }, nil
}
