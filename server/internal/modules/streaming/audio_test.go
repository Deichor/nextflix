package streaming

import (
	"strings"
	"testing"
)

func audioProbe(tracks ...tsProbeStream) *tsProbe {
	p := &tsProbe{Streams: []tsProbeStream{{CodecType: "video", CodecName: "h264", PixFmt: "yuv420p"}}}
	for _, t := range tracks {
		t.CodecType = "audio"
		p.Streams = append(p.Streams, t)
	}
	return p
}

func TestAudioPlan(t *testing.T) {
	edge := tsParseAudioCodecs("aac,opus,flac", true)
	aacOnly := tsParseAudioCodecs("", false)
	p := audioProbe(
		tsProbeStream{CodecName: "eac3", Channels: 6},
		tsProbeStream{CodecName: "aac", Profile: "LC", Channels: 2},
		tsProbeStream{CodecName: "truehd", Channels: 8},
		tsProbeStream{CodecName: "flac", Channels: 6},
		tsProbeStream{CodecName: "aac", Profile: "Main", Channels: 2},
		tsProbeStream{CodecName: "ac3", Channels: 1},
	)
	cases := []struct {
		name   string
		track  int
		client map[string]bool
		want   tsAudioPlan
	}{
		{"E-AC3 5.1 -> Opus 5.1", 0, edge, tsAudioPlan{encoder: "libopus", layout: "5.1", bitrate: "384k"}},
		{"E-AC3 5.1, AAC-only client -> AAC 5.1", 0, aacOnly, tsAudioPlan{encoder: "aac", layout: "5.1", bitrate: "384k"}},
		{"E-AC3 copied for a native client (delay_moov)", 0, tsParseAudioCodecs("eac3,opus", true), tsAudioPlan{copy: true, delay: true}},
		{"AC3 copied for a native client (delay_moov)", 5, tsParseAudioCodecs("ac3,opus", true), tsAudioPlan{copy: true, delay: true}},
		{"AAC-LC copied", 1, edge, tsAudioPlan{copy: true}},
		{"TrueHD 7.1 -> Opus 5.1", 2, edge, tsAudioPlan{encoder: "libopus", layout: "5.1", bitrate: "384k"}},
		{"FLAC copied with -strict", 3, edge, tsAudioPlan{copy: true, strict: true}},
		{"FLAC, no client support -> AAC 5.1", 3, aacOnly, tsAudioPlan{encoder: "aac", layout: "5.1", bitrate: "384k"}},
		{"AAC Main re-encoded, stereo stays stereo", 4, aacOnly, tsAudioPlan{encoder: "aac", layout: "stereo", bitrate: "256k"}},
		{"mono stays mono", 5, edge, tsAudioPlan{encoder: "libopus", layout: "mono", bitrate: "96k"}},
		{"missing track", 9, edge, tsAudioPlan{encoder: "libopus", layout: "5.1|stereo|mono", bitrate: "256k"}},
	}
	for _, tc := range cases {
		if got := p.audioPlan(tc.track, tc.client); got != tc.want {
			t.Errorf("%s: got %+v want %+v", tc.name, got, tc.want)
		}
	}
	if got := (*tsProbe)(nil).audioPlan(0, edge); got.copy || got.encoder != "libopus" {
		t.Errorf("nil probe: %+v", got)
	}

	video := tsVideoPlan{copy: true}
	if args := strings.Join(playArgs("http://ts/f", 0, 0, video, tsAudioPlan{copy: true, delay: true}, false), " "); !strings.Contains(args, "+delay_moov") {
		t.Errorf("copied E-AC3 without delay_moov: %s", args)
	}
	if args := strings.Join(playArgs("http://ts/f", 0, 0, video, tsAudioPlan{copy: true}, false), " "); strings.Contains(args, "delay_moov") {
		t.Errorf("delay_moov for AAC: %s", args)
	}
}

func TestParseAudioCodecs(t *testing.T) {
	got := tsParseAudioCodecs(" AAC, opus ,dts,eac3,xyz", true)
	if len(got) != 4 || !got["aac"] || !got["opus"] || !got["eac3"] || !got["dts"] {
		t.Fatalf("got %v", got)
	}
	if got := tsParseAudioCodecs("", true); len(got) != 0 {
		t.Fatalf("empty list: %v", got)
	}
	if got := tsParseAudioCodecs("", false); len(got) != 1 || !got["aac"] {
		t.Fatalf("old client: %v", got)
	}
}

func TestPlayArgsAudio(t *testing.T) {
	join := func(v tsVideoPlan, a tsAudioPlan) string {
		return strings.Join(playArgs("http://ts/file", 300, 1, v, a, true), " ")
	}
	transcodeV := tsVideoPlan{}
	copyV := tsVideoPlan{copy: true}
	opus := tsAudioPlan{encoder: "libopus", layout: "5.1", bitrate: "384k"}

	// Copied audio: no filters, and the stream starts on the keyframe even with re-encoded video
	cp := join(transcodeV, tsAudioPlan{copy: true})
	if !strings.Contains(cp, "-c:a copy") || strings.Contains(cp, "aresample") || strings.Contains(cp, "-b:a") ||
		!strings.Contains(cp, "-noaccurate_seek") || !strings.Contains(cp, "-map 0:a:1?") {
		t.Fatalf("audio copy: %s", cp)
	}
	if fl := join(copyV, tsAudioPlan{copy: true, strict: true}); !strings.Contains(fl, "-c:a copy -strict -2") {
		t.Fatalf("flac copy: %s", fl)
	}

	// Re-encoded audio keeps 5.1 at 48 kHz, fills gaps, never uses the default stereo downmix
	enc := join(copyV, opus)
	for _, want := range []string{"-c:a libopus -b:a 384k -ar 48000", "aresample=async=1,aformat=channel_layouts=5.1", "-noaccurate_seek"} {
		if !strings.Contains(enc, want) {
			t.Fatalf("opus: missing %q in %s", want, enc)
		}
	}
	if strings.Contains(enc, "-ac ") || strings.Contains(enc, "loudnorm") {
		t.Fatalf("opus: unexpected downmix / normalisation: %s", enc)
	}

	// Fully re-encoded stream seeks accurately
	if all := join(transcodeV, opus); strings.Contains(all, "-noaccurate_seek") {
		t.Fatalf("full transcode: %s", all)
	}
	for _, args := range []string{cp, enc} {
		if !strings.Contains(args, "-avoid_negative_ts make_zero") {
			t.Fatalf("make_zero missing: %s", args)
		}
	}
}

func TestStartsOnKeyframe(t *testing.T) {
	edge := tsParseAudioCodecs("aac,opus,flac", true)
	p := audioProbe(tsProbeStream{CodecName: "eac3", Channels: 6}, tsProbeStream{CodecName: "aac", Channels: 2})
	p.Streams[0].PixFmt = "yuv420p10le" // re-encoded video
	if p.startsOnKeyframe(false, 0, edge) {
		t.Fatal("re-encoded video and audio start exactly at t")
	}
	if !p.startsOnKeyframe(false, 1, edge) {
		t.Fatal("copied audio starts on the keyframe")
	}
	p.Streams[0].PixFmt = "yuv420p"
	if !p.startsOnKeyframe(false, 0, edge) {
		t.Fatal("copied video starts on the keyframe")
	}
}
