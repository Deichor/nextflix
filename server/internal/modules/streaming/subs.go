package streaming

import (
	"fmt"
	"path"
	"sort"
	"strings"
)

// Subtitle languages a release carries. A new release often has the profile's subtitle only
// inside some torrents (platform subtitles muxed into WEB-DL MKVs, "MULTi-SUB") long before
// subtitle sites have it. The release name gives a hint before the check; the probe of a
// checked file (its text subtitle tracks) and the torrent's subtitle files decide. These parts
// are small next to a resolution step and the measured swarm health decides first.

const (
	tsSubNamePart   = 6 // the name names the profile language's subtitle ("TR Sub", "Turkish", "TUR")
	tsSubMultiPart  = 4 // MULTi-SUB / Multi Subs: platform subtitles muxed in, often the profile's too
	tsSubProbedPart = 8 // a text subtitle track (or file) of the profile language is inside
)

// Text subtitle codecs (ffprobe names); image ones (PGS, VobSub, DVB) cannot become WebVTT.
var tsSubTextCodecs = map[string]bool{"subrip": true, "srt": true, "ass": true, "ssa": true, "mov_text": true,
	"webvtt": true, "text": true}

var tsSubFileExts = map[string]bool{".srt": true, ".ass": true, ".ssa": true, ".vtt": true}

var (
	// Release-name words for subtitles (after tsNormalize: "Türkçe Altyazı" is "turkce altyazi")
	tsSubWords      = map[string]bool{"sub": true, "subs": true, "subbed": true, "subtitle": true, "subtitles": true, "altyazi": true, "altyazili": true}
	tsSubMultiWords = map[string]bool{"multisub": true, "multisubs": true, "msub": true, "msubs": true, "multisubbed": true}
	// Words that name the profile language's subtitle on their own (the user's tracker habits)
	tsSubStrong = map[string]map[string]bool{
		"tr": {"tur": true, "turkish": true, "turkce": true, "trsub": true, "trsubs": true, "trsubbed": true},
	}
	// Tokens of subtitle file names that are no language
	tsSubFlagTokens = map[string]bool{"sdh": true, "hi": true, "cc": true, "full": true, "default": true, "sub": true,
		"subs": true, "subtitle": true, "subtitles": true, "track": true, "altyazi": true, "hearing": true, "impaired": true}
	// Two-letter codes a subtitle file name may end in ("Show.S01E01.tr.srt")
	tsSubCodes2 = map[string]bool{"tr": true, "en": true, "de": true, "fr": true, "es": true, "it": true, "pt": true,
		"ru": true, "ar": true, "nl": true, "pl": true, "sv": true, "da": true, "no": true, "nb": true, "fi": true, "el": true,
		"he": true, "hu": true, "cs": true, "ro": true, "bg": true, "hr": true, "sr": true, "uk": true, "ja": true, "ko": true,
		"zh": true, "fa": true, "id": true, "vi": true, "th": true, "ms": true}
	tsSubLangByName = func() map[string]string {
		m := map[string]string{}
		for code, names := range tsLangNames {
			for _, n := range names {
				for _, t := range tsNormalize(n) {
					if len(t) > 3 {
						m[t] = code
					}
				}
			}
		}
		return m
	}()
)

// tsSubsMatter: a subtitle in lang is worth ranking for. Not for audio in that language (the
// original), and an English profile only for a known non-English original.
func tsSubsMatter(lang, origLang string) bool {
	switch {
	case lang == "" || lang == origLang:
		return false
	case lang == "en":
		return origLang != ""
	}
	return true
}

// tsSubTokenLang is the language a release-name or file-name token names ("" = none): a
// language name, an ISO 639-2 code or a two-letter code.
func tsSubTokenLang(t string) string {
	if l, ok := tsSubLangByName[t]; ok {
		return l
	}
	if len(t) == 3 {
		if l, ok := tsISO6392[t]; ok {
			return l
		}
	}
	if len(t) == 2 && tsSubCodes2[t] {
		if t == "nb" {
			return "no"
		}
		return t
	}
	return ""
}

// tsSubNameScore is the subtitle part of a release name (rest: its normalized tokens after the
// title) for the profile language lang: tsSubNamePart when it names that language's subtitle
// ("TR Sub", "Türkçe Altyazı", "Subs: ENG TUR"; "TUR" / "Turkish" alone for Turkish),
// tsSubMultiPart for MULTi-SUB / Multi Subs, else 0.
func tsSubNameScore(rest []string, lang, origLang string) float64 {
	if !tsSubsMatter(lang, origLang) {
		return 0
	}
	subWord, named, multi := false, false, false
	for i, t := range rest {
		switch {
		case tsSubStrong[lang][t]:
			return tsSubNamePart
		case tsSubWords[t]:
			subWord = true
		case tsSubMultiWords[t]:
			multi = true
		case t == "multi" && i+1 < len(rest) && tsSubWords[rest[i+1]]:
			multi = true
		}
		named = named || tsSubTokenLang(t) == lang
	}
	switch {
	case named && subWord:
		return tsSubNamePart
	case multi:
		return tsSubMultiPart
	}
	return 0
}

// tsProbedSubLangs lists the languages of a probed file's text subtitle tracks, sorted.
// Forced-only tracks (foreign parts) and tracks of unknown language are left out.
func tsProbedSubLangs(p *tsProbe) []string {
	if p == nil {
		return nil
	}
	var langs []string
	for i := range p.Streams {
		st := &p.Streams[i]
		if st.CodecType != "subtitle" || !tsSubTextCodecs[st.CodecName] {
			continue
		}
		title := strings.ToLower(tsTag(st, "title"))
		if st.Disposition["forced"] == 1 || strings.Contains(title, "forced") {
			continue
		}
		lang := tsLangCode(tsTag(st, "language"))
		if lang == "" {
			for _, t := range tsNormalize(title) {
				if lang = tsSubTokenLang(t); lang != "" {
					break
				}
			}
		}
		langs = tsAddLang(langs, lang)
	}
	return langs
}

// tsSubFileLang guesses the language of a subtitle file from its name ("Show.S01E01.tr.srt",
// "24_Turkish.srt", "tur.sdh.srt"), else its folder ("Subs/Turkish/1.srt"). Forced files
// (foreign parts only) and unknown ones give "".
func tsSubFileLang(p string) string {
	p = strings.ReplaceAll(p, "\\", "/")
	base := strings.TrimSuffix(path.Base(p), path.Ext(p))
	tokens := tsNormalize(base)
	for _, t := range tokens {
		if t == "forced" || t == "foreign" {
			return ""
		}
	}
	trailing := func(tokens []string) string {
		for i := len(tokens) - 1; i >= 0; i-- {
			t := tokens[i]
			if tsSubFlagTokens[t] || tsDigits(t) {
				continue
			}
			return tsSubTokenLang(t)
		}
		return ""
	}
	if l := trailing(tokens); l != "" {
		return l
	}
	for _, t := range tokens {
		if l, ok := tsSubLangByName[t]; ok {
			return l
		}
	}
	if dir := path.Base(path.Dir(p)); dir != "." && dir != "/" {
		return trailing(tsNormalize(dir))
	}
	return ""
}

func tsDigits(s string) bool {
	for _, r := range s {
		if r < '0' || r > '9' {
			return false
		}
	}
	return s != ""
}

// tsTorrentSubLangs lists the languages of the subtitle files that belong to video in a
// torrent's file list: all of them when it holds one video, else those whose path names the
// video's file name ("Subs/<video name>/2_Turkish.srt", "<video name>.tr.srt").
func tsTorrentSubLangs(files []tsFileStat, video tsFileStat) []string {
	videos := 0
	for _, f := range files {
		if tsVideoExts[strings.ToLower(path.Ext(f.Path))] {
			videos++
		}
	}
	stem := strings.ToLower(strings.TrimSuffix(path.Base(strings.ReplaceAll(video.Path, "\\", "/")), path.Ext(video.Path)))
	var langs []string
	for _, f := range files {
		if !tsSubFileExts[strings.ToLower(path.Ext(f.Path))] || f.Length > 5<<20 {
			continue
		}
		if videos > 1 && (stem == "" || !strings.Contains(strings.ToLower(f.Path), stem)) {
			continue
		}
		langs = tsAddLang(langs, tsSubFileLang(f.Path))
	}
	return langs
}

// tsAddLang adds lang to the sorted set langs ("" is ignored).
func tsAddLang(langs []string, lang string) []string {
	if lang == "" || tsHasLang(langs, lang) {
		return langs
	}
	langs = append(langs, lang)
	sort.Strings(langs)
	return langs
}

func tsHasLang(langs []string, lang string) bool {
	for _, l := range langs {
		if l == lang {
			return true
		}
	}
	return false
}

// applyProbedSubs reads the subtitle languages of a checked file (the text tracks of its probe
// and the subtitle files next to it in the torrent) into r.Format.Subs. For the profile
// language the subtitle part of the rank follows: tsSubProbedPart when it is inside, 0 when the
// probe shows it is not (the name's hint goes); without a probe the name's hint stays. True when
// the rank moved.
func (s *Service) applyProbedSubs(r *tsResolveResult, c tsCandidate, q tsResolveQuery, files []tsFileStat, file tsFileStat) bool {
	langs := tsTorrentSubLangs(files, file)
	v, probed := s.probeCache.Load(fmt.Sprintf("%s:%d", r.Hash, r.Index))
	if probed {
		for _, l := range tsProbedSubLangs(v.(*tsProbe)) {
			langs = tsAddLang(langs, l)
		}
	}
	f := tsTags{}
	if r.Format != nil {
		f = *r.Format
	}
	f.Subs = langs
	r.Format = &f
	if !tsSubsMatter(q.lang, q.origLang) {
		return false
	}
	part := c.parts.Subs
	switch {
	case tsHasLang(langs, q.lang):
		part = tsSubProbedPart
	case probed:
		part = 0
	}
	adj := part - c.parts.Subs
	r.rankAdj += adj
	r.rank = c.score + r.rankAdj
	return adj != 0
}
