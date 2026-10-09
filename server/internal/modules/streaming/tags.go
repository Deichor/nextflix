package streaming

import (
	"fmt"
	"regexp"
	"strings"
)

// Release tags: audio format, HDR format, source and playability hints read from a release
// name, and the same format read from a probed file (which wins where the name is silent or
// cut off by an indexer).

// tsTags is the format of a release. It is part of the resolve answer (`format`).
type tsTags struct {
	Audio     string `json:"audio,omitempty"`    // best audio: truehd, dtsx, dtshd, eac3, dts, ac3, flac, pcm, opus, aac, mp3
	Channels  int    `json:"channels,omitempty"` // of the best audio: 8 = 7.1, 6 = 5.1, 2 = stereo; 0 unknown
	Atmos     bool   `json:"atmos,omitempty"`    // Atmos (or DTS:X) object audio
	DV        bool   `json:"dv,omitempty"`       // Dolby Vision
	DVOnly    bool   `json:"dv_only,omitempty"`  // Dolby Vision without an HDR10 base layer (profile 5), known from a probe
	HDR10     bool   `json:"hdr10,omitempty"`    // HDR10 / HDR / HLG base layer
	HDR10Plus bool   `json:"hdr10plus,omitempty"`
	Source    string `json:"source,omitempty"` // remux, bluray, webdl, webrip, hdtv, hdrip, dvdrip, cinema
	Truncated bool   `json:"truncated,omitempty"`
	Probed    bool   `json:"probed,omitempty"` // audio / HDR corrected from ffprobe
	// Hi10P / 4:4:4 / 4:2:2 H.264: no browser (and few TVs) decodes it, a re-encode is heavy
	Undecodable bool `json:"undecodable,omitempty"`
	// Video codec of the probed file (ffprobe name: h264, hevc, av1, ...); "" before a probe
	Video string `json:"video,omitempty"`
	// Languages of the text subtitles inside (embedded tracks, files next to the video), from
	// the check of the file; nil when unknown
	Subs []string `json:"subs,omitempty"`

	codecs   []string // every audio codec the name lists, best first
	audioCut bool     // the name was cut in its audio tag ("... DD" may be "DDP5 1"): codec and channels unsure
}

// Audio codecs, best first
var tsAudioRank = []string{"truehd", "dtsx", "dtshd", "eac3", "dts", "ac3", "flac", "pcm", "opus", "aac", "mp3"}

var (
	tsAudioPatterns = map[string]*regexp.Regexp{
		"truehd": regexp.MustCompile(`true[ ._-]?hd`),
		"dtsx":   regexp.MustCompile(`(?:^|[^a-z0-9])dts[ ._:-]?x(?:[^a-z0-9]|$)`),
		"dtshd":  regexp.MustCompile(`(?:^|[^a-z0-9])dts[ ._-]?hd`),
		"eac3":   regexp.MustCompile(`(?:^|[^a-z0-9])(?:ddpa?|dd\+|e[ ._-]?ac[ ._-]?3|dd[ ._-]?plus)(?:[^a-z]|$)|dolby[ ._-]?digital[ ._-]?plus`),
		"dts":    regexp.MustCompile(`(?:^|[^a-z0-9])dts(?:[^a-z]|$)`),
		"ac3":    regexp.MustCompile(`(?:^|[^a-z0-9])(?:dd|ac[ ._-]?3)(?:[^a-z+]|$)|dolby[ ._-]?digital(?:[^a-z]|$)`),
		"flac":   regexp.MustCompile(`(?:^|[^a-z0-9])flac`),
		"pcm":    regexp.MustCompile(`(?:^|[^a-z0-9])l?pcm(?:[^a-z]|$)`),
		"opus":   regexp.MustCompile(`(?:^|[^a-z0-9])opus(?:[^a-z]|$)`),
		"aac":    regexp.MustCompile(`(?:^|[^a-z0-9])aac`),
		"mp3":    regexp.MustCompile(`(?:^|[^a-z0-9])mp3(?:[^a-z0-9]|$)`),
	}
	tsAtmosPattern     = regexp.MustCompile(`atmos|(?:^|[^a-z0-9])ddpa(?:[^a-z]|$)`)
	tsChannelPattern   = regexp.MustCompile(`(?:^|[^0-9])([12567])[ .]([01])(?:[^0-9]|$)`)
	tsChannelChPattern = regexp.MustCompile(`(?:^|[^0-9])([1-8])ch(?:[^a-z]|$)`)
	tsHDR10PlusPattern = regexp.MustCompile(`hdr10(?:\+|plus|p(?:[^a-z0-9]|$))`)
	tsHi10Pattern      = regexp.MustCompile(`hi10p?(?:[^a-z0-9]|$)|hi444|hi422|4:4:4|yuv444|(?:^|[^0-9])444(?:[^0-9]|$)`)
	tsTenBitPattern    = regexp.MustCompile(`10[ ._-]?bit`)
	tsGroupPattern     = regexp.MustCompile(`-([A-Za-z0-9]{2,})(?:\[[^\]]*\]|\.[a-z0-9]{2,4})*$`)
	tsNotGroups        = map[string]bool{"dl": true, "rip": true, "hd": true, "ray": true} // "WEB-DL", "DTS-HD" at the end
)

// Words that end a release name only when an indexer cut it off ("... DDP5 1 At", "... WEB-DL DD")
var tsCutWords = []string{"atmos", "truehd", "dts", "ddp", "ddpa", "dd", "eac3", "ac3", "aac", "opus", "flac", "hdr", "hdr10",
	"dv", "dovi", "dolby", "vision", "web", "webdl", "dl", "webrip", "bluray", "remux", "h", "x", "264", "265", "h264", "h265",
	"x264", "x265", "hevc", "avc", "10bit", "ma", "hd", "ddp5", "dd5", "ddpa5", "aac2", "1", "0", "hmax", "amzn", "nf", "dsnp",
	"atvp", "hulu", "2160p", "1080p", "720p"}

// tsParseTags reads the format tags of a release name.
func tsParseTags(title string) tsTags {
	var t tsTags
	lower := strings.ToLower(tsFold(title))
	// A matched codec is cut out before the next is tried: "E-AC3" holds "AC3", "DTS-HD" holds "DTS"
	rest := lower
	for _, name := range tsAudioRank {
		if p := tsAudioPatterns[name]; p.MatchString(rest) {
			t.codecs = append(t.codecs, name)
			rest = p.ReplaceAllString(rest, " ")
		}
	}
	if len(t.codecs) > 0 {
		t.Audio = t.codecs[0]
	}
	t.Atmos = tsAtmosPattern.MatchString(lower) || t.Audio == "dtsx"
	for _, m := range tsChannelPattern.FindAllStringSubmatch(lower, -1) {
		t.Channels = max(t.Channels, tsChannels(m[1], m[2]))
	}
	for _, m := range tsChannelChPattern.FindAllStringSubmatch(lower, -1) {
		t.Channels = max(t.Channels, int(m[1][0]-'0'))
	}

	t.DV = tsDVPattern.MatchString(lower)
	t.HDR10 = tsHDR10Pattern.MatchString(lower)
	t.HDR10Plus = tsHDR10PlusPattern.MatchString(lower)
	t.Source = tsSourceOf(tsNormalize(title))
	h264 := tsH264Pattern.MatchString(lower) && !tsHEVCPattern.MatchString(lower)
	t.Undecodable = (tsHi10Pattern.MatchString(lower) && !tsHEVCPattern.MatchString(lower)) ||
		(h264 && tsTenBitPattern.MatchString(lower))
	t.Truncated = tsLooksTruncated(title)
	if t.Truncated && t.Channels == 0 {
		if tokens := tsNormalize(title); len(tokens) > 0 {
			switch tokens[len(tokens)-1] {
			case "dd", "ddp", "dts", "aac", "ac3", "eac3", "truehd", "ddp5", "dd5", "aac2":
				t.audioCut = true
			}
		}
	}
	return t
}

func tsChannels(main, lfe string) int {
	switch main + "." + lfe {
	case "7.1":
		return 8
	case "6.1":
		return 7
	case "5.1":
		return 6
	case "2.0", "2.1":
		return 2
	case "1.0":
		return 1
	}
	return 0
}

// tsSourceOf names where a release comes from (see tsSourceScore for the tiers).
func tsSourceOf(tokens []string) string {
	has := map[string]bool{}
	for i, t := range tokens {
		has[t] = true
		if i+1 < len(tokens) && len(t) > 1 && len(tokens[i+1]) > 1 {
			has[t+tokens[i+1]] = true // "WEB-DL", "DCP-Rip", "Blu-Ray" are split by tsNormalize
		}
	}
	for t := range has {
		if tsLastResortTokens[t] {
			return "cinema"
		}
	}
	service := false
	for t := range tsStreamServiceTokens {
		service = service || has[t]
	}
	switch {
	case has["remux"] || has["bdremux"]:
		return "remux"
	case has["bluray"] || has["bdrip"] || has["brrip"]:
		return "bluray"
	case has["webrip"]:
		return "webrip"
	case has["webdl"] || has["web"] || service:
		return "webdl"
	case has["hdtv"]:
		return "hdtv"
	case has["hdrip"] || has["dmrip"]:
		return "hdrip"
	case has["dvdrip"]:
		return "dvdrip"
	}
	return ""
}

// tsLooksTruncated: an indexer cut the name off. A name ending in "..." / "…" is, and so is one
// without a release group whose last word is a format tag or the start of one ("... DDP5 1 At",
// "... WEB-DL DD"). Names ending in a bracket or a "-GROUP" are complete.
func tsLooksTruncated(title string) bool {
	t := strings.TrimSpace(title)
	if strings.HasSuffix(t, "...") || strings.HasSuffix(t, "…") {
		return true
	}
	if t == "" || strings.HasSuffix(t, ")") || strings.HasSuffix(t, "]") {
		return false
	}
	if m := tsGroupPattern.FindStringSubmatch(t); m != nil && !tsNotGroups[strings.ToLower(m[1])] {
		return false
	}
	tokens := tsNormalize(t)
	if len(tokens) == 0 {
		return false
	}
	last := tokens[len(tokens)-1]
	for _, w := range tsCutWords {
		if last == w || (len(last) >= 2 && strings.HasPrefix(w, last)) {
			return true
		}
	}
	return false
}

// known counts the format facts a name states (to pick the fullest of several names).
func (t tsTags) known() int {
	n := 0
	for _, b := range []bool{t.Audio != "", t.Channels > 0, t.Atmos, t.DV || t.HDR10 || t.HDR10Plus, t.Source != ""} {
		if b {
			n++
		}
	}
	return n
}

func (t tsTags) String() string {
	var parts []string
	audio := t.Audio
	if audio == "" {
		audio = "audio?"
	}
	if t.Channels > 0 {
		audio += " " + tsChannelLabel(t.Channels)
	}
	if t.Atmos {
		audio += " atmos"
	}
	parts = append(parts, audio)
	switch {
	case t.DVOnly:
		parts = append(parts, "DV-only")
	case t.DV && (t.HDR10 || t.HDR10Plus):
		parts = append(parts, "DV+HDR")
	case t.DV:
		parts = append(parts, "DV")
	case t.HDR10Plus:
		parts = append(parts, "HDR10+")
	case t.HDR10:
		parts = append(parts, "HDR")
	default:
		parts = append(parts, "SDR")
	}
	if t.Source != "" {
		parts = append(parts, t.Source)
	}
	if t.Undecodable {
		parts = append(parts, "hi10p/444")
	}
	if t.Truncated {
		parts = append(parts, "cut")
	}
	if t.Video != "" {
		parts = append(parts, t.Video)
	}
	if t.Probed {
		parts = append(parts, "probed")
	}
	if len(t.Subs) > 0 {
		parts = append(parts, "subs:"+strings.Join(t.Subs, "+"))
	}
	return strings.Join(parts, " ")
}

func tsChannelLabel(ch int) string {
	switch ch {
	case 8:
		return "7.1"
	case 7:
		return "6.1"
	case 6:
		return "5.1"
	case 2:
		return "2.0"
	case 1:
		return "1.0"
	}
	return fmt.Sprintf("%dch", ch)
}

// tsProbeAudioName maps an ffprobe audio stream to the tag names above.
func tsProbeAudioName(st *tsProbeStream) (name string, atmos bool) {
	profile := strings.ToLower(st.Profile)
	title := strings.ToLower(st.Tags["title"])
	atmos = strings.Contains(profile, "atmos") || strings.Contains(title, "atmos")
	name = st.CodecName
	switch {
	case name == "dts" && (strings.Contains(profile, ":x") || strings.Contains(title, "dts:x") || strings.Contains(title, "dts-x")):
		name, atmos = "dtsx", true
	case name == "dts" && strings.Contains(profile, "hd"):
		name = "dtshd"
	case strings.HasPrefix(name, "pcm_"):
		name = "pcm"
	}
	return name, atmos
}

// tsProbedTags reads the format of a probed file: its best audio track (object audio, then
// channels, then codec) and the Dolby Vision / HDR signalling of its video.
func tsProbedTags(p *tsProbe) (tsTags, bool) {
	var t tsTags
	if p == nil {
		return t, false
	}
	rank := func(name string) int {
		for i, n := range tsAudioRank {
			if n == name {
				return len(tsAudioRank) - i
			}
		}
		return 0
	}
	found := false
	for i := range p.Streams {
		st := &p.Streams[i]
		if st.CodecType != "audio" {
			continue
		}
		name, atmos := tsProbeAudioName(st)
		better := !found || (atmos && !t.Atmos) || (atmos == t.Atmos && (st.Channels > t.Channels ||
			(st.Channels == t.Channels && rank(name) > rank(t.Audio))))
		if better {
			t.Audio, t.Channels, t.Atmos, found = name, st.Channels, atmos, true
		}
		t.codecs = append(t.codecs, name)
	}
	v := p.video()
	if v == nil && !found {
		return t, false
	}
	if v != nil {
		profile, compat := v.dolbyVisionInfo()
		t.DV = profile > 0
		t.DVOnly = profile == 5 || (profile == 8 && compat == 0)
		t.HDR10 = v.hdrTransfer() != "" && !t.DVOnly
		t.Undecodable = tsUndecodableVideo(v)
		t.Video = v.CodecName
	}
	t.Probed = true
	return t, true
}

// tsUndecodableVideo: H.264 beyond 8-bit 4:2:0 (Hi10P, Hi422, Hi444PP) or any 4:4:4 / 4:2:2 video.
func tsUndecodableVideo(v *tsProbeStream) bool {
	pix := v.PixFmt
	if strings.Contains(pix, "444") || strings.Contains(pix, "422") {
		return true
	}
	return v.CodecName == "h264" && pix != "" && pix != "yuv420p" && pix != "yuvj420p"
}

// tsMergeProbed corrects name tags with probed ones. The probe fills what the name left out
// and replaces the audio of a cut-off name; Dolby Vision without a base layer, HDR and
// decodability always follow the file.
func tsMergeProbed(name, probed tsTags) tsTags {
	t := name
	if probed.Audio != "" && (name.Truncated || name.Audio == "" || name.Channels == 0) {
		t.Audio, t.Channels, t.codecs = probed.Audio, probed.Channels, probed.codecs
		t.Atmos = probed.Atmos || (name.Atmos && !name.Truncated)
	} else if probed.Atmos {
		t.Atmos = true
	}
	if probed.DV {
		t.DV = true
	}
	t.DVOnly = probed.DVOnly
	if t.DVOnly {
		t.HDR10, t.HDR10Plus = false, false
	} else if probed.HDR10 {
		t.HDR10 = true
	}
	t.Undecodable = probed.Undecodable
	t.Video = probed.Video
	t.Probed = true
	return t
}

// tsResultHash is the info hash of a Jackett result ("" when it has none).
func tsResultHash(r JackettResult) string {
	if h := tsHashFromMagnet(strings.TrimSpace(r.MagnetUri)); h != "" {
		return h
	}
	if tsHashPattern.MatchString(r.InfoHash) {
		return strings.ToLower(r.InfoHash)
	}
	return ""
}

// tsFullerTitles returns, per result, the most informative name of its torrent (see
// tsFuller): the fullest name any copy with the same info hash carries, and for a name that
// is still cut off, the fullest name of another result of the same size (within 2 %) that
// continues it (tsContinues; hashes must not differ). Hashless copies of indexers that cut
// names ("... WEB-DL DD", "... DDP5 1 At") get their tags back this way.
func tsFullerTitles(raw []JackettResult) []string {
	type info struct {
		hash   string
		tokens []string
		tags   tsTags
	}
	infos := make([]info, len(raw))
	best := map[string]int{} // hash -> index of its fullest name
	for i, r := range raw {
		infos[i] = info{hash: tsResultHash(r), tokens: strings.Fields(tsStem(r.Title)), tags: tsParseTags(r.Title)}
		if h := infos[i].hash; h != "" {
			if j, ok := best[h]; !ok || tsFuller(r.Title, infos[i].tags, raw[j].Title, infos[j].tags) {
				best[h] = i
			}
		}
	}
	out := make([]string, len(raw))
	for i, r := range raw {
		k := i
		if j, ok := best[infos[i].hash]; ok {
			k = j
		}
		if cut := infos[k]; cut.tags.Truncated {
			for j, o := range raw {
				if j == k || !tsSameSize(r.Size, o.Size) || (infos[i].hash != "" && infos[j].hash != "" && infos[i].hash != infos[j].hash) {
					continue
				}
				if tsContinues(infos[j].tokens, cut.tokens) && tsFuller(o.Title, infos[j].tags, raw[k].Title, infos[k].tags) {
					k = j
				}
			}
		}
		out[i] = raw[k].Title
	}
	return out
}

// tsContinues: the cut-off name cut is the start of full with words left out (a missing
// episode title) and its last word cut short ("... DDP5 1 At" of "... DDP5 1 Atmos DV ...").
func tsContinues(full, cut []string) bool {
	if len(cut) < 4 || len(full) <= len(cut) {
		return false
	}
	j := 0
	for _, t := range cut[:len(cut)-1] {
		for j < len(full) && full[j] != t {
			j++
		}
		if j == len(full) {
			return false
		}
		j++
	}
	last := cut[len(cut)-1]
	for ; j < len(full); j++ {
		if strings.HasPrefix(full[j], last) {
			return true
		}
	}
	return false
}

func tsFuller(a string, ta tsTags, b string, tb tsTags) bool {
	if ka, kb := ta.known(), tb.known(); ka != kb {
		return ka > kb
	}
	if ta.Truncated != tb.Truncated {
		return !ta.Truncated
	}
	return len(tsStem(a)) > len(tsStem(b))
}
