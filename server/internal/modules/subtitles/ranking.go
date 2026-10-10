package subtitles

import (
	"math"
	"regexp"
	"sort"
	"strconv"
	"strings"
)

// releaseInfo is what the ranking reads from a release name like
// "Movie.2024.1080p.AMZN.WEB-DL.DDP5.1.H.264-FLUX".
type releaseInfo struct {
	family  string // bluray, web, tv, dvd, hdrip, cam
	source  string // finer: remux, bluray, bdrip, brrip, webdl, webrip, web, hdtv, dvdrip, ...
	res     string // 2160p, 1080p, 720p, 480p
	group   string
	service string // nf, amzn, dsnp, ...
	edition map[string]bool
	fps     float64
	tokens  map[string]bool
}

var (
	relSplit   = regexp.MustCompile(`[^a-z0-9]+`)
	relFPS     = regexp.MustCompile(`(?i)\b(23[.,]976|23[.,]98|24|25|29[.,]97|30|50|59[.,]94|60)\s?fps\b`)
	relGroup   = regexp.MustCompile(`-\s?([A-Za-z0-9][A-Za-z0-9_]{1,24})\s*(?:\[[^\]]*\])?\s*(?:\.(?:mkv|mp4|avi|srt|ass|ssa|sub|vtt|zip))?$`)
	relBracket = regexp.MustCompile(`[\[(]\s*(YTS[A-Z.]*|YIFY|RARBG|EZTVx?(?:\.to)?|PSA|Tigole|QxR|GalaxyRG|TGx)\s*[\])]`)
	relRes     = regexp.MustCompile(`(?i)\b(2160p|4k|uhd|1080[pi]|720p|576p|480p)\b`)
)

var relServices = map[string]bool{"nf": true, "amzn": true, "dsnp": true, "hmax": true, "max": true, "atvp": true,
	"hulu": true, "pcok": true, "pmtp": true, "stan": true, "cr": true, "itunes": true, "it": true, "blutv": true,
	"exxen": true, "gain": true, "tabii": true, "dsny": true, "sho": true, "hbo": true}

// relServiceAlias: spelled-out service names subtitle uploaders use ("WEBRip.Amazon").
var relServiceAlias = map[string]string{"dsny": "dsnp", "amazon": "amzn", "netflix": "nf", "disney": "dsnp",
	"hbomax": "hmax", "appletv": "atvp", "peacock": "pcok", "paramount": "pmtp"}

var relEditions = map[string]bool{"extended": true, "unrated": true, "directors": true, "imax": true,
	"remastered": true, "theatrical": true, "criterion": true, "uncut": true, "despecialized": true}

// relNotGroup are name parts that follow a dash but are no release group ("WEB-DL", "Blu-ray").
var relNotGroup = map[string]bool{"dl": true, "ray": true, "rip": true, "hd": true, "web": true, "ma": true,
	"x": true, "audio": true, "sub": true, "subs": true}

// relNoise are tokens that say nothing about timing.
var relNoise = map[string]bool{"x264": true, "x265": true, "h264": true, "h265": true, "hevc": true, "avc": true,
	"aac": true, "ac3": true, "dts": true, "ddp5": true, "dd5": true, "atmos": true, "10bit": true, "hdr": true,
	"hdr10": true, "dv": true, "1": true, "0": true, "srt": true, "mkv": true, "mp4": true, "tr": true, "en": true,
	"turkish": true, "english": true, "proper": true, "repack": true, "the": true}

func parseRelease(name string) releaseInfo {
	ri := releaseInfo{edition: map[string]bool{}, tokens: map[string]bool{}}
	if name == "" {
		return ri
	}
	if m := relGroup.FindStringSubmatch(strings.TrimSpace(name)); m != nil {
		if g := strings.ToLower(m[1]); !relNoise[g] && !relNotGroup[g] && !isYear(g) {
			ri.group = g
		}
	}
	if ri.group == "" {
		if m := relBracket.FindStringSubmatch(name); m != nil {
			ri.group = normGroup(m[1])
		}
	}
	ri.group = normGroup(ri.group)
	if m := relFPS.FindStringSubmatch(name); m != nil {
		ri.fps, _ = strconv.ParseFloat(strings.ReplaceAll(m[1], ",", "."), 64)
	}
	if m := relRes.FindStringSubmatch(name); m != nil {
		r := strings.ToLower(m[1])
		switch r {
		case "4k", "uhd":
			r = "2160p"
		case "1080i":
			r = "1080p"
		}
		ri.res = r
	}

	low := strings.ToLower(name)
	toks := relSplit.Split(low, -1)
	for i, t := range toks {
		if t == "" {
			continue
		}
		next := ""
		if i+1 < len(toks) {
			next = toks[i+1]
		}
		switch {
		case t == "remux" || t == "bdremux":
			ri.setSource("bluray", "remux")
		case t == "bluray" || (t == "blu" && next == "ray") || t == "bdmv" || t == "bd25" || t == "bd50":
			ri.setSource("bluray", "bluray")
		case t == "bdrip":
			ri.setSource("bluray", "bdrip")
		case t == "brrip":
			ri.setSource("bluray", "brrip")
		case t == "webdl" || (t == "web" && next == "dl"):
			ri.setSource("web", "webdl")
		case t == "webrip":
			ri.setSource("web", "webrip")
		case t == "web":
			ri.setSource("web", "web")
		case t == "hdtv" || t == "pdtv" || t == "dsr" || t == "tvrip" || t == "hdtvrip" || t == "satrip":
			ri.setSource("tv", "hdtv")
		case t == "dvdrip" || t == "dvd" || t == "dvd5" || t == "dvd9" || t == "dvdr":
			ri.setSource("dvd", "dvdrip")
		case t == "hdrip":
			ri.setSource("hdrip", "hdrip")
		case t == "cam" || t == "hdcam" || t == "ts" || t == "hdts" || t == "telesync" || t == "tc" ||
			t == "dvdscr" || t == "scr" || t == "r5":
			ri.setSource("cam", t)
		}
		if relServices[t] {
			ri.service = t
		}
		if s, ok := relServiceAlias[t]; ok {
			ri.service = s
		}
		if relEditions[t] {
			ri.edition[t] = true
		}
		if len(t) > 1 && !relNoise[t] && !isYear(t) {
			ri.tokens[t] = true
		}
	}
	return ri
}

func (ri *releaseInfo) setSource(family, source string) {
	// Keep the most specific one: "WEB" after "WEB-DL" must not downgrade it.
	if ri.source != "" && source == "web" {
		return
	}
	ri.family, ri.source = family, source
}

func normGroup(g string) string {
	g = strings.ToLower(strings.TrimSpace(g))
	switch {
	case strings.HasPrefix(g, "yts"), g == "yify":
		return "yts"
	case strings.HasPrefix(g, "eztv"):
		return "eztv"
	}
	return g
}

func isYear(t string) bool {
	if len(t) != 4 {
		return false
	}
	n, err := strconv.Atoi(t)
	return err == nil && n >= 1900 && n <= 2100
}

// releaseSimilarity scores how likely a subtitle made for sub is in sync with the video hint (0..1).
func releaseSimilarity(hint, sub releaseInfo) float64 {
	if len(hint.tokens) == 0 && hint.family == "" {
		return 0.3 // nothing known about the playing file
	}
	s := 0.0
	if hint.group != "" && sub.group != "" && hint.group == sub.group {
		s += 0.4
	}
	switch {
	case hint.family != "" && hint.family == sub.family:
		s += 0.22
		if hint.source == sub.source {
			s += 0.05
		}
	case hint.family != "" && sub.family != "":
		s -= 0.1
	}
	if hint.service != "" && hint.service == sub.service {
		s += 0.1
	}
	if hint.res != "" && hint.res == sub.res {
		s += 0.05
	}
	for e := range hint.edition {
		if !sub.edition[e] {
			s -= 0.15
		}
	}
	for e := range sub.edition {
		if !hint.edition[e] {
			s -= 0.15
		}
	}
	if hint.fps > 0 && sub.fps > 0 && math.Abs(hint.fps-sub.fps) > 0.05 {
		s -= 0.3
	}
	s += 0.15 * jaccard(hint.tokens, sub.tokens)
	return clamp01(s)
}

// sameRelease: made for the playing release (its group, or its streaming service and source
// family), so it is timed like one made for the file.
func sameRelease(hint, sub releaseInfo) bool {
	if hint.group != "" && hint.group == sub.group {
		return true
	}
	return hint.service != "" && hint.service == sub.service && hint.family != "" && hint.family == sub.family
}

func jaccard(a, b map[string]bool) float64 {
	if len(a) == 0 || len(b) == 0 {
		return 0
	}
	inter := 0
	for t := range a {
		if b[t] {
			inter++
		}
	}
	return float64(inter) / float64(len(a)+len(b)-inter)
}

func clamp01(v float64) float64 {
	return math.Max(0, math.Min(1, v))
}

var (
	relEpisode  = regexp.MustCompile(`(?i)(?:^|[^a-z0-9])s(\d{1,2})[ ._-]?e(\d{1,3})(?:[-e]+(\d{1,3})(?:[^0-9p]|$))?`)
	relEpisodeX = regexp.MustCompile(`(?:^|[^0-9])(\d{1,2})x(\d{2,3})(?:[^0-9]|$)`)
)

// wrongEpisode reports a TV subtitle whose release names (or torrent path) name episodes, none
// of them the asked one ("S02E06" for S02E07; a season pack names none).
func wrongEpisode(c candidate, q Query) bool {
	if !q.isTV() || q.Episode <= 0 || c.Pack {
		return false
	}
	names := append([]string{c.Release}, c.Releases...)
	named := false
	for _, name := range names {
		for _, m := range relEpisode.FindAllStringSubmatch(name, -1) {
			named = true
			s, _ := strconv.Atoi(m[1])
			a, _ := strconv.Atoi(m[2])
			b := a
			if m[3] != "" {
				if n, _ := strconv.Atoi(m[3]); n > a && n-a <= 5 {
					b = n // a double episode "S01E01E02" / "S01E01-02"
				}
			}
			if s == q.Season && q.Episode >= a && q.Episode <= b {
				return false
			}
		}
		for _, m := range relEpisodeX.FindAllStringSubmatch(name, -1) {
			named = true
			s, _ := strconv.Atoi(m[1])
			e, _ := strconv.Atoi(m[2])
			if s == q.Season && e == q.Episode {
				return false
			}
		}
	}
	return named
}

// qualitySignal turns provider signals (downloads, rating, trusted uploader, site quality tier) into 0..1.
func qualitySignal(c candidate) float64 {
	q := math.Min(1, math.Log10(float64(c.Downloads)+1)/5) * 0.5
	if c.Votes >= 3 && c.Rating > 0 {
		q += c.Rating / 10 * 0.3
	} else {
		q += 0.12
	}
	if c.Trusted {
		q += 0.2
	}
	switch c.Quality {
	case 1:
		q += 0.2
	case 2:
		q += 0.1
	case 4:
		q -= 0.25
	}
	return clamp01(q)
}

// rankCandidates scores and sorts the merged results: requested languages in order, then within a
// language synced files first, then release similarity and provider quality. Forced-only and
// machine-translated subtitles always come last.
func rankCandidates(cands []candidate, q Query) []Sub {
	hint := parseRelease(q.Title)
	if hint.fps == 0 {
		hint.fps = q.FPS
	}
	langOrder := map[string]int{}
	for i, l := range q.Langs {
		langOrder[l] = i
	}
	order := func(l string) int {
		if i, ok := langOrder[l]; ok {
			return i
		}
		if l == "" {
			return 1000
		}
		return 100
	}

	// HI is only penalised in a language that has a regular alternative.
	hasRegular := map[string]bool{}
	for _, c := range cands {
		if !c.HI && !c.Forced && !c.Machine {
			hasRegular[c.Language] = true
		}
	}

	type scored struct {
		sub  Sub
		tier int
	}
	out := make([]scored, 0, len(cands))
	seen := map[string]bool{}
	for _, c := range cands {
		if seen[c.ID] || wrongEpisode(c, q) {
			continue
		}
		seen[c.ID] = true

		sim := 0.0
		same := false
		for _, r := range c.Releases {
			ri := parseRelease(r)
			if ri.fps == 0 {
				ri.fps = c.FPS
			}
			sim = math.Max(sim, releaseSimilarity(hint, ri))
			same = same || sameRelease(hint, ri)
		}
		if len(c.Releases) == 0 {
			sim = releaseSimilarity(hint, releaseInfo{fps: c.FPS})
		}
		qual := qualitySignal(c)

		var score float64
		tier := 1
		switch {
		case c.Synced:
			score = 0.8 + 0.12*sim + 0.08*qual
			tier = 0
		case same && c.AutoSync >= 0:
			// Made for the playing release: ahead of others timed to the file
			score = 0.66 + 0.1*sim + 0.08*qual
			tier = 0
		case c.AutoSync > 0:
			// Timed to the playing file and fitting: after the ones made for it
			score = 0.62 + 0.1*sim + 0.08*qual
			tier = 0
		default:
			score = 0.05 + 0.5*sim + 0.2*qual
			if c.AutoSync < 0 {
				score -= 0.25 // does not fit the file even when timed
			}
		}
		if c.HI && hasRegular[c.Language] {
			score -= 0.15
		}
		if c.MultiCD {
			score -= 0.3
		}
		if c.Forced {
			tier = 2
		}
		if c.Machine {
			tier = 3
			score = math.Min(score, 0.1)
		}
		s := c.Sub
		s.Synced = c.Synced || c.AutoSync > 0
		s.Score = math.Round(clamp01(score)*1000) / 1000
		if s.Label == "" {
			s.Label = langLabel(s.Language)
		}
		out = append(out, scored{sub: s, tier: tier})
	}

	sort.SliceStable(out, func(i, j int) bool {
		a, b := out[i], out[j]
		if oa, ob := order(a.sub.Language), order(b.sub.Language); oa != ob {
			return oa < ob
		}
		if a.sub.Language != b.sub.Language {
			return a.sub.Language < b.sub.Language
		}
		if a.tier != b.tier {
			return a.tier < b.tier
		}
		if a.sub.Score != b.sub.Score {
			return a.sub.Score > b.sub.Score
		}
		return a.sub.Downloads > b.sub.Downloads
	})
	subs := make([]Sub, len(out))
	for i, s := range out {
		subs[i] = s.sub
	}
	return subs
}
