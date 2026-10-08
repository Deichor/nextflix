package streaming

import (
	"strings"

	"github.com/gin-gonic/gin"
)

// Audio of the play stream: copied when the browser decodes the codec in MP4 (original
// quality and channels), otherwise re-encoded with up to 5.1 channels.

// tsAudioCodecs are the names the client may send in ?acodecs= (the same as ffprobe's codec
// names). truehd and dts only steer the ranking of a native-audio client (see tsAudioScore).
var tsAudioCodecs = map[string]bool{"aac": true, "opus": true, "flac": true, "mp3": true, "ac3": true, "eac3": true,
	"truehd": true, "dts": true}

// tsMuxableAudio can be copied into fragmented MP4 by our ffmpeg. AC-3 / E-AC-3 need the
// delay_moov flag: the muxer reads their header from the first packets before it writes the moov.
var tsMuxableAudio = map[string]bool{"aac": true, "opus": true, "flac": true, "mp3": true, "ac3": true, "eac3": true}

// How the play stream handles the selected audio track
type tsAudioPlan struct {
	copy    bool
	strict  bool   // FLAC in MP4 is still "experimental" in ffmpeg 5
	delay   bool   // copied AC-3 / E-AC-3: write the moov after the first fragment
	encoder string // libopus or aac
	layout  string // aformat channel layout(s) of the re-encode
	bitrate string
}

// clientAudioCodecs reads ?acodecs=aac,opus,... Clients that do not send it get AAC only,
// which every browser plays in MP4.
func clientAudioCodecs(c *gin.Context) map[string]bool {
	return tsParseAudioCodecs(c.Query("acodecs"), c.Request.URL.Query().Has("acodecs"))
}

func tsParseAudioCodecs(v string, given bool) map[string]bool {
	out := map[string]bool{}
	if !given {
		out["aac"] = true
		return out
	}
	for _, name := range strings.Split(strings.ToLower(v), ",") {
		if name = strings.TrimSpace(name); tsAudioCodecs[name] {
			out[name] = true
		}
	}
	return out
}

// audioStream returns the n-th audio stream (the play stream maps 0:a:n), nil when missing.
func (p *tsProbe) audioStream(n int) *tsProbeStream {
	if p == nil {
		return nil
	}
	for i := range p.Streams {
		if p.Streams[i].CodecType == "audio" {
			if n == 0 {
				return &p.Streams[i]
			}
			n--
		}
	}
	return nil
}

// audioPlan decides between copy and re-encode for audio track n. A nil probe re-encodes.
func (p *tsProbe) audioPlan(n int, client map[string]bool) tsAudioPlan {
	a := p.audioStream(n)
	if a != nil {
		name := a.CodecName
		// Browsers decode AAC-LC and HE-AAC, not the rare Main / LTP profiles
		aacOK := name != "aac" || a.Profile == "" || a.Profile == "LC" || strings.HasPrefix(a.Profile, "HE-AAC")
		if client[name] && tsMuxableAudio[name] && aacOK {
			return tsAudioPlan{copy: true, strict: name == "flac", delay: name == "ac3" || name == "eac3"}
		}
	}

	opus := client["opus"]
	plan := tsAudioPlan{encoder: "aac"}
	if opus {
		plan.encoder = "libopus"
	}
	channels := 0
	if a != nil {
		channels = a.Channels
	}
	// 7.1 / Atmos beds go down to 5.1, everything above stereo becomes 5.1 (never a stereo
	// downmix, which would bury the dialogue), stereo and mono stay as they are
	switch {
	case channels > 2:
		plan.layout = "5.1"
		plan.bitrate = "384k"
	case channels == 2:
		plan.layout = "stereo"
		plan.bitrate = "256k"
		if opus {
			plan.bitrate = "160k"
		}
	case channels == 1:
		plan.layout = "mono"
		plan.bitrate = "128k"
		if opus {
			plan.bitrate = "96k"
		}
	default:
		plan.layout = "5.1|stereo|mono" // unknown: ffmpeg picks the closest layout
		plan.bitrate = "256k"
	}
	return plan
}

// args returns the ffmpeg audio output options of the plan.
func (a tsAudioPlan) args() []string {
	if a.copy {
		if a.strict {
			return []string{"-c:a", "copy", "-strict", "-2"}
		}
		return []string{"-c:a", "copy"}
	}
	// async fills timestamp gaps in the source audio with silence so A/V stay in sync
	return []string{"-c:a", a.encoder, "-b:a", a.bitrate, "-ar", "48000",
		"-af", "aresample=async=1,aformat=channel_layouts=" + a.layout}
}

// startsOnKeyframe reports whether the play stream (for this client and audio track) starts
// on the keyframe at or before ?t= instead of exactly at t. Any copied stream does: copied
// audio is never trimmed to t, so the whole stream starts at the keyframe to stay aligned.
func (p *tsProbe) startsOnKeyframe(hevcOK bool, audio int, client map[string]bool) bool {
	return p.copiesVideo(hevcOK) || p.audioPlan(audio, client).copy
}
