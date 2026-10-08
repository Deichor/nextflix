package streaming

import (
	"maps"

	"github.com/gin-gonic/gin"
)

// Split mode: /play?track=video or ?track=audio delivers one elementary stream, so a client can
// feed video and audio into separate MSE SourceBuffers (an LG TV takes E-AC-3 / Atmos only in an
// audio-only SourceBuffer, never muxed with video).
//
// Both requests of a pair carry the same t, audio, hevc, acodecs and split_acodecs, so both ffmpeg
// runs seek the same way: input -ss t (with -noaccurate_seek when the video or the audio is copied,
// as in the muxed stream), so copied video starts on the keyframe at or before t (what /seekpoint
// reports) and audio at the first audio frame from that seek point on. Unlike the muxed stream
// (which starts at 0 = the seek point), both carry the file's own timestamps (-output_ts_offset t
// undoes the input offset; tfdt is absolute via frag_discont + negative_cts_offsets, no edit list),
// so a client appends both to one MediaSource without offsets and seeks the element to t.

type tsTrack int

const (
	tsTrackBoth tsTrack = iota
	tsTrackVideo
	tsTrackAudio
)

// playTrack reads ?track=; false for an unknown value.
func playTrack(c *gin.Context) (tsTrack, bool) {
	switch c.Query("track") {
	case "":
		return tsTrackBoth, true
	case "video":
		return tsTrackVideo, true
	case "audio":
		return tsTrackAudio, true
	}
	return tsTrackBoth, false
}

// clientSplitAudioCodecs is ?acodecs= plus ?split_acodecs=: codecs the client plays only in an
// audio-only stream. A muxed stream never copies the split-only ones.
func clientSplitAudioCodecs(c *gin.Context) map[string]bool {
	out := maps.Clone(clientAudioCodecs(c))
	maps.Copy(out, tsParseAudioCodecs(c.Query("split_acodecs"), true))
	return out
}

// playAudioCodecs are the codecs a play (or seekpoint) request copies: split requests also copy
// the split-only ones. Both requests of a split pair use them, so they make the same plans.
func playAudioCodecs(c *gin.Context, track tsTrack) map[string]bool {
	if track == tsTrackBoth {
		return clientAudioCodecs(c)
	}
	return clientSplitAudioCodecs(c)
}

// seekAudioCodecs: playAudioCodecs for a seekpoint request (an unknown track counts as muxed).
func seekAudioCodecs(c *gin.Context) map[string]bool {
	track, _ := playTrack(c)
	return playAudioCodecs(c, track)
}
