package streaming

import (
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
	"strconv"
	"strings"
	"time"
)

type tsProbeStream struct {
	CodecType     string            `json:"codec_type"`
	CodecName     string            `json:"codec_name"`
	Profile       string            `json:"profile"`
	PixFmt        string            `json:"pix_fmt"`
	Width         int               `json:"width"`
	Height        int               `json:"height"`
	ColorTransfer string            `json:"color_transfer"`
	AvgFrameRate  string            `json:"avg_frame_rate"`
	RFrameRate    string            `json:"r_frame_rate"`
	Channels      int               `json:"channels"`
	ChannelLayout string            `json:"channel_layout"`
	Tags          map[string]string `json:"tags"`
	Disposition   map[string]int    `json:"disposition"` // default, forced, ... (0 or 1)
	SideData      []struct {
		Type      string `json:"side_data_type"`
		DVProfile int    `json:"dv_profile"`
		BLCompat  int    `json:"dv_bl_signal_compatibility_id"` // 0 none (profile 5), 1 HDR10, 2 SDR, 4 HLG, 6 Blu-ray HDR10
	} `json:"side_data_list"`
}

type tsProbe struct {
	Streams []tsProbeStream `json:"streams"`
	Format  struct {
		FormatName string `json:"format_name"`
		Duration   string `json:"duration"`
	} `json:"format"`
	// Container chapters (Matroska / MP4), read from the header with the rest
	Chapters []struct {
		StartTime string            `json:"start_time"`
		EndTime   string            `json:"end_time"`
		Tags      map[string]string `json:"tags"`
	} `json:"chapters"`
}

// How the play stream handles the video of a file
type tsVideoPlan struct {
	copy    bool   // stream copy, the browser decodes the original
	hevc    bool   // copied HEVC, tagged hvc1 so browsers accept it in MP4
	tonemap string // HDR transfer of a re-encoded source; mapped to SDR so colours stay right
}

// Torrent files are untrusted: ffmpeg / ffprobe may only open them as plain video containers
// over HTTP (TorrServer, the loopback TorBox proxy). Playlists (hls, concat, ...) and other
// protocols (file, https, ...) are refused, so a crafted file cannot make ffmpeg fetch other URLs.
const (
	tsProtocolWhitelist = "http,tcp"
	tsFormatWhitelist   = "matroska,webm,mov,mp4,m4a,avi,mpegts,mpeg,flv,asf,ogg,rm"
)

// tsInputGuard returns the input options that go before ffmpeg's -i (or ffprobe's input).
func tsInputGuard() []string {
	return []string{"-protocol_whitelist", tsProtocolWhitelist, "-format_whitelist", tsFormatWhitelist}
}

// probeFile runs ffprobe on a torrent file through TorrServer (cached per hash:index).
func (s *Service) probeFile(ctx context.Context, hash string, index int) (*tsProbe, error) {
	return s.probeFileTimeout(ctx, hash, index, 90*time.Second)
}

// tsProbeCall is a running ffprobe of one file; later callers wait for it instead of
// starting a second one (the player's probe while resolve's check still reads the header).
type tsProbeCall struct {
	done chan struct{}
	p    *tsProbe
	err  error
}

// probeFileTimeout is probeFile with its own time limit; a probe that runs out is not cached.
// A probe of the same file that is already running is joined; when it fails, the caller
// runs its own with the time it has left. Background work waits in the quiet gate before it
// registers its probe, so a player never joins a probe that is put off.
func (s *Service) probeFileTimeout(ctx context.Context, hash string, index int, timeout time.Duration) (*tsProbe, error) {
	key := fmt.Sprintf("%s:%d", hash, index)
	deadline := time.Now().Add(timeout)
	quiet := false
	for tries := 0; ; tries++ {
		if p, ok := s.probeCache.Load(key); ok {
			return p.(*tsProbe), nil
		}
		s.probeMu.Lock()
		call, running := s.probing[key]
		if !running && !quiet {
			s.probeMu.Unlock()
			// A background probe of another torrent waits while a stream starts (quiet.go)
			if !s.waitQuiet(ctx, hash, "probe") {
				return nil, ctx.Err()
			}
			quiet, deadline = true, time.Now().Add(timeout)
			tries--
			continue
		}
		if !running {
			call = &tsProbeCall{done: make(chan struct{})}
			s.probing[key] = call
		}
		s.probeMu.Unlock()
		if !running {
			left := timeout // a first try has the whole limit, counted after the quiet wait
			if tries > 0 {
				left = time.Until(deadline)
			}
			call.p, call.err = s.runProbe(ctx, hash, index, left)
			if call.err == nil {
				s.probeCache.Store(key, call.p)
			}
			s.probeMu.Lock()
			if call.err == nil {
				if s.probeAt == nil {
					s.probeAt = map[string]time.Time{}
				}
				s.probeAt[key] = time.Now()
			}
			delete(s.probing, key)
			s.probeMu.Unlock()
			close(call.done)
			return call.p, call.err
		}
		wait := time.NewTimer(time.Until(deadline))
		select {
		case <-call.done:
			wait.Stop()
			if call.err == nil {
				return call.p, nil
			}
			if ctx.Err() != nil || time.Until(deadline) < time.Second {
				return nil, call.err
			}
		case <-wait.C:
			return nil, fmt.Errorf("ffprobe failed: %w", context.DeadlineExceeded)
		case <-ctx.Done():
			wait.Stop()
			return nil, ctx.Err()
		}
	}
}

// runProbe runs one ffprobe (tests replace it through probeFn).
func (s *Service) runProbe(ctx context.Context, hash string, index int, timeout time.Duration) (*tsProbe, error) {
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	if s.probeFn != nil {
		return s.probeFn(ctx, hash, index)
	}
	args := append([]string{"-v", "error"}, tsInputGuard()...)
	args = append(args, "-print_format", "json", "-show_format", "-show_streams", "-show_chapters", s.fileURL(hash, index))
	out, err := exec.CommandContext(ctx, "ffprobe", args...).Output()
	if err != nil {
		return nil, fmt.Errorf("ffprobe failed: %w", err)
	}
	var p tsProbe
	if err := json.Unmarshal(out, &p); err != nil {
		return nil, err
	}
	return &p, nil
}

func (p *tsProbe) video() *tsProbeStream {
	for i := range p.Streams {
		if p.Streams[i].CodecType == "video" && p.Streams[i].CodecName != "mjpeg" && p.Streams[i].CodecName != "png" {
			return &p.Streams[i]
		}
	}
	return nil
}

// videoPlan decides between copy and re-encode. Browsers play 8-bit H.264 everywhere and
// HEVC Main / Main10 when the client said it can (hevcOK).
func (p *tsProbe) videoPlan(hevcOK bool) tsVideoPlan {
	v := p.video()
	if v == nil {
		return tsVideoPlan{}
	}
	switch {
	case v.CodecName == "h264" && (v.PixFmt == "yuv420p" || v.PixFmt == "yuvj420p"):
		return tsVideoPlan{copy: true}
	case hevcOK && v.CodecName == "hevc" && (v.PixFmt == "yuv420p" || v.PixFmt == "yuv420p10le"):
		return tsVideoPlan{copy: true, hevc: true}
	}
	return tsVideoPlan{tonemap: v.hdrTransfer()}
}

// copiesVideo reports whether the play stream copies the video (it then starts on a keyframe).
func (p *tsProbe) copiesVideo(hevcOK bool) bool { return p.videoPlan(hevcOK).copy }

// frameDuration is one frame of the video in seconds (avg_frame_rate, else r_frame_rate), 0 when unknown.
func (p *tsProbe) frameDuration() float64 {
	v := p.video()
	if v == nil {
		return 0
	}
	for _, r := range []string{v.AvgFrameRate, v.RFrameRate} {
		a, b, ok := strings.Cut(r, "/")
		n, err1 := strconv.ParseFloat(a, 64)
		d, err2 := strconv.ParseFloat(b, 64)
		if ok && err1 == nil && err2 == nil && n > 0 && d > 0 && n/d >= 5 && n/d <= 240 {
			return d / n
		}
	}
	return 0
}

// hdrTransfer returns the PQ / HLG transfer of an HDR stream, "" for SDR.
func (v *tsProbeStream) hdrTransfer() string {
	if v.ColorTransfer == "smpte2084" || v.ColorTransfer == "arib-std-b67" {
		return v.ColorTransfer
	}
	return ""
}

// dolbyVision returns the Dolby Vision profile of the stream, 0 when there is none.
func (v *tsProbeStream) dolbyVision() int {
	p, _ := v.dolbyVisionInfo()
	return p
}

// dolbyVisionInfo returns the Dolby Vision profile and base layer compatibility id (0, 0 without DV).
func (v *tsProbeStream) dolbyVisionInfo() (profile, compat int) {
	for _, sd := range v.SideData {
		if strings.Contains(strings.ToLower(sd.Type), "dovi") {
			return sd.DVProfile, sd.BLCompat
		}
	}
	return 0, 0
}

// indexedContainer is true for formats whose stream info is in the header,
// so ffmpeg can start with a small probe. MPEG-TS can reveal streams late.
func (p *tsProbe) indexedContainer() bool {
	f := p.Format.FormatName
	return strings.Contains(f, "mp4") || strings.Contains(f, "matroska") || strings.Contains(f, "avi")
}

// duration is the file length in seconds: the container duration, else the longest Matroska
// DURATION tag of a stream. 0 when unknown.
func (p *tsProbe) duration() float64 {
	if d, err := strconv.ParseFloat(p.Format.Duration, 64); err == nil && d > 0 {
		return d
	}
	best := 0.0
	for _, st := range p.Streams {
		for k, v := range st.Tags {
			if strings.HasPrefix(strings.ToUpper(k), "DURATION") {
				if d := tsParseClock(v); d > best {
					best = d
				}
			}
		}
	}
	return best
}

// tsParseClock reads "01:26:03.123000000" as seconds (0 when invalid).
func tsParseClock(v string) float64 {
	parts := strings.Split(strings.TrimSpace(v), ":")
	if len(parts) != 3 {
		return 0
	}
	h, err1 := strconv.Atoi(parts[0])
	m, err2 := strconv.Atoi(parts[1])
	sec, err3 := strconv.ParseFloat(parts[2], 64)
	if err1 != nil || err2 != nil || err3 != nil || h < 0 || m < 0 || sec < 0 {
		return 0
	}
	return float64(h*3600+m*60) + sec
}
