package streaming

import (
	"fmt"
	"log"
	"math"
	"strings"
)

// Format-aware ranking: within a resolution tier, audio, HDR and bitrate decide for what the
// client can really use. These parts are small next to the resolution steps (2160p vs 1080p
// is 45 points), so a format never beats a resolution, and the measured swarm health still
// decides among the checked candidates.

// tsScoreParts is the explainable breakdown of a candidate's score (logged for the top ones).
type tsScoreParts struct {
	Match   float64 // year / episode match
	Res     float64 // resolution for this client
	Source  float64 // remux > bluray > web-dl > webrip > hdtv; cinema leaks far below
	Codec   float64 // video codec fit
	HDR     float64 // HDR fit for this client
	Audio   float64 // audio fit for this client
	Rate    float64 // bitrate (size per minute) against the tier's typical one
	Seeds   float64 // Jackett seeders, saturating above tsEnoughSeeders
	Lang    float64 // audio language hints of the name: dub-only markers, MULTi / Dual Audio (see lang.go)
	Subs    float64 // the profile language's subtitle inside: name hints, then the check (see subs.go)
	Penalty float64 // remux bitrate, Dolby Vision without base layer, undecodable video
	Line    float64 // the file needs more than the home line carries (see tsLinePart)
}

func (p tsScoreParts) total() float64 {
	return p.Match + p.Res + p.Source + p.Codec + p.HDR + p.Audio + p.Lang + p.Subs + p.Rate + p.Seeds + p.Penalty + p.Line
}

func (p tsScoreParts) String() string {
	var b strings.Builder
	for _, f := range []struct {
		name string
		v    float64
	}{{"match", p.Match}, {"res", p.Res}, {"src", p.Source}, {"codec", p.Codec}, {"hdr", p.HDR}, {"audio", p.Audio},
		{"lang", p.Lang}, {"subs", p.Subs}, {"rate", p.Rate}, {"seeds", p.Seeds}, {"penalty", p.Penalty}, {"line", p.Line}} {
		if f.v != 0 {
			fmt.Fprintf(&b, "%s %+.1f ", f.name, f.v)
		}
	}
	return strings.TrimSpace(b.String())
}

const (
	tsEnoughSeeders = 30 // above this, more seeders barely add (the swarm check decides)
	tsDVOnlyPenalty = -90
)

// tsSeedScore: 6·log2(1+s) up to tsEnoughSeeders, then only 2·log2 of the multiple, so a
// big swarm cannot outweigh a better format of a well-seeded one; under 5 seeders is risky.
func tsSeedScore(seeders int) float64 {
	v := 6 * math.Log2(1+float64(min(seeders, tsEnoughSeeders)))
	if seeders > tsEnoughSeeders {
		v += 2 * math.Log2(float64(1+seeders)/float64(1+tsEnoughSeeders))
	}
	if seeders < 5 {
		v -= 20
	}
	return v
}

// tsNativeAudio: the client plays E-AC-3 (and its Atmos) itself (a TV with ?atmos=1&acodecs=eac3).
func (q tsResolveQuery) nativeAudio() bool { return q.atmos && q.acodecs["eac3"] }

// tsAudioPlayable: the client decodes this codec itself (native mode only).
func (q tsResolveQuery) audioPlayable(codec string) bool {
	switch codec {
	case "eac3":
		return q.acodecs["eac3"]
	case "ac3":
		return q.acodecs["ac3"] || q.acodecs["eac3"] // E-AC-3 decoders play AC-3
	case "truehd":
		return q.acodecs["truehd"]
	case "dts", "dtshd", "dtsx":
		return q.acodecs["dts"]
	}
	return true // aac, opus, flac, mp3, pcm: everything plays or gets them
}

// tsAudioScore ranks the audio of a release for the client.
//   - Native (?atmos=1 with eac3): Atmos / DTS:X 14 > 7.1 10 > 5.1 7 > stereo 0, for codecs the
//     client declared; a multichannel track it cannot decode is re-encoded to 5.1 (5).
//   - Otherwise (browsers; the server re-encodes to Opus / AAC 5.1): multichannel 7 > stereo 0,
//     Atmos is just 5.1 / 7.1 there.
//
// Unknown audio scores 2.
func tsAudioScore(t tsTags, q tsResolveQuery) float64 {
	channels := func(codec string) int {
		if t.Channels > 0 || t.audioCut {
			return t.Channels
		}
		switch codec {
		case "truehd", "dtsx", "dtshd", "eac3", "ac3", "dts":
			return 6 // surround codecs without a channel tag are almost always 5.1 or more
		case "aac", "opus", "mp3":
			return 2
		}
		if t.Atmos {
			return 6
		}
		return 0
	}
	if !q.nativeAudio() {
		switch ch := channels(t.Audio); {
		case ch >= 6:
			return 7
		case ch > 0:
			return 0
		}
		return 2
	}
	codecs := t.codecs
	if len(codecs) == 0 {
		codecs = []string{t.Audio}
	}
	best := -1.0
	for _, codec := range codecs {
		ch := channels(codec)
		v := 2.0
		switch {
		case codec != "" && !q.audioPlayable(codec):
			if ch >= 6 {
				v = 5
			} else {
				v = 0
			}
		case t.Atmos && (codec == "eac3" || codec == "truehd" || codec == "dtsx") && t.Audio == codec:
			v = 14
		case ch >= 8:
			v = 10
		case ch >= 6:
			v = 7
		case ch > 0:
			v = 0
		}
		best = max(best, v)
	}
	return best
}

// tsHDRScore ranks the HDR format for an HEVC client (others re-encode to SDR, see tsScore).
// With ?dv=1 Dolby Vision is best (8), then HDR10+ (6) and HDR10 (5). Without it HDR10 and DV
// with an HDR10 base layer score 5 on an HDR display and -5 on an SDR one (tone-mapped); a
// DV tag at the end of a cut-off name may hide that base layer (2). penalty is
// tsDVOnlyPenalty for Dolby Vision without a base layer (profile 5), which shows wrong
// colours anywhere but on a DV display.
func tsHDRScore(t tsTags, q tsResolveQuery, maybeCut bool) (score, penalty float64) {
	dvOnly := t.DVOnly || (t.DV && !t.HDR10 && !t.HDR10Plus && !maybeCut && !t.Probed)
	if q.dv && t.DV {
		return 8, 0
	}
	if dvOnly {
		return 0, tsDVOnlyPenalty
	}
	hdr := t.HDR10 || t.HDR10Plus
	switch {
	case !hdr && t.DV: // DV at the end of a cut-off name
		if !q.hdr {
			return -2, 0
		}
		return 2, 0
	case !hdr:
		return 0, 0
	case !q.hdr:
		return -5, 0 // the browser tone-maps it on an SDR screen
	case t.HDR10Plus && q.dv:
		return 6, 0 // a TV uses the dynamic metadata, browsers do not
	}
	return 5, 0
}

// Typical bitrates (Mbit/s) of a good encode per resolution
var tsRefMbps = map[int]float64{2160: 14, 1080: 5, 720: 2.5, 480: 1.2}

// tsRateScore prefers bigger files per minute within a tier: 6·log2 of the bitrate against
// the tier's typical one, from -12 (a quarter) to +6 (twice); above 3× (remux territory) it
// falls again, a swarm rarely feeds that. Season packs and unknown sizes score 0.
func tsRateScore(size int64, quality, runtimeMin int, mediaType string, kind int) float64 {
	ref := tsRefMbps[quality]
	if size <= 0 || ref == 0 || kind >= 2 {
		return 0
	}
	mbit := float64(tsNeedRate(size, runtimeMin, mediaType)) * 8 / 1e6
	r := mbit / ref
	v := 6 * math.Log2(math.Min(math.Max(r, 0.25), 2))
	if r > 3 {
		v -= 6 * math.Log2(r/3)
	}
	return math.Round(v*10) / 10
}

// tsFormatParts returns the audio, HDR and penalty parts of a release's format for q.
func tsFormatParts(t tsTags, q tsResolveQuery, maybeCutDV bool) (audio, hdr, penalty float64) {
	audio = tsAudioScore(t, q)
	if q.hevc {
		hdr, penalty = tsHDRScore(t, q, maybeCutDV)
	} else {
		if t.DV || t.HDR10 || t.HDR10Plus {
			hdr = -8 // re-encoded and tone-mapped
		}
		if t.DVOnly || (t.DV && !t.HDR10 && !t.HDR10Plus && !maybeCutDV && !t.Probed) {
			penalty = tsDVOnlyPenalty
		}
	}
	if t.Undecodable {
		penalty -= 40 // Hi10P / 4:4:4 H.264: a heavy re-encode on every client
	}
	return audio, hdr, penalty
}

// tsLogRanking logs the score breakdown of the candidates about to be checked.
func tsLogRanking(q tsResolveQuery, cands []tsCandidate) {
	for i, c := range cands {
		log.Printf("resolve %q%s: #%d %.1f [%s] {%s} %q (%d seeders, %.2f GB)", q.title, tsResolveCaps(q), i+1, c.score,
			c.parts, c.tags, c.raw.Title, c.raw.Seeders, float64(c.raw.Size)/float64(tsGB))
	}
}

// applyProbedFormat corrects a checked result's format with its probed file (cached by the
// duration check): a cut-off or silent name gets the real audio, Dolby Vision without a base
// layer is found, and r.rank follows. A file the client cannot show right (DV profile 5
// without ?dv=1, Hi10P / 4:4:4 video) is marked unfit, so tsChoose takes it only when nothing
// else is left. True when the format changed the rank.
func (s *Service) applyProbedFormat(r *tsResolveResult, c tsCandidate, q tsResolveQuery) bool {
	v, ok := s.probeCache.Load(fmt.Sprintf("%s:%d", r.Hash, r.Index))
	if !ok {
		return false
	}
	probed, ok := tsProbedTags(v.(*tsProbe))
	if !ok {
		return false
	}
	tags := tsMergeProbed(c.tags, probed)
	r.Format = &tags
	r.HDR = tags.HDR10 || tags.HDR10Plus || tags.DV
	switch {
	case tags.DVOnly && !q.dv:
		r.unfit = "Dolby Vision without an HDR10 base layer"
	case tags.Undecodable:
		r.unfit = "video the client cannot decode (Hi10P / 4:4:4)"
	}
	audio, hdr, penalty := tsFormatParts(tags, q, c.maybeCutDV)
	nameAudio, nameHDR, namePenalty := tsFormatParts(c.tags, q, c.maybeCutDV)
	r.rankAdj = (audio + hdr + penalty) - (nameAudio + nameHDR + namePenalty)
	r.rank = c.score + r.rankAdj
	return r.rankAdj != 0
}

func tsUnfitNote(unfit string) string {
	if unfit == "" {
		return ""
	}
	return " (unfit: " + unfit + ")"
}
