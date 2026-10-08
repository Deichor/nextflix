package streaming

import (
	"fmt"
	"log"
	"regexp"
	"sort"
	"strings"
	"time"
)

// Audio language of releases. Russian trackers often release a Russian dub alone (or first,
// untagged in the name), so a release whose audio has neither the title's original language
// nor the profile's must not win over one with the original audio. Release names give a mild
// hint before the check; the probe of a checked file decides (it is then unfit, like DV
// profile 5 without dv: picked only when nothing else is left).

const (
	tsLangDubPart   = -8 // a dub-only marker in the name
	tsLangMultiPart = 4  // MULTi / Dual Audio / ENG: the original is likely there
	tsOrigLangWait  = 2 * time.Second
)

// tsISO6392 maps ffprobe's ISO 639-2 tags (B and T forms) to TMDB's ISO 639-1.
var tsISO6392 = map[string]string{
	"eng": "en", "tur": "tr", "rus": "ru", "ukr": "uk", "spa": "es", "fre": "fr", "fra": "fr", "ger": "de",
	"deu": "de", "ita": "it", "por": "pt", "jpn": "ja", "kor": "ko", "chi": "zh", "zho": "zh", "cmn": "zh",
	"yue": "zh", "hin": "hi", "ara": "ar", "pol": "pl", "dut": "nl", "nld": "nl", "swe": "sv", "nor": "no",
	"nob": "no", "dan": "da", "fin": "fi", "cze": "cs", "ces": "cs", "hun": "hu", "rum": "ro", "ron": "ro",
	"gre": "el", "ell": "el", "heb": "he", "tha": "th", "vie": "vi", "ind": "id", "per": "fa", "fas": "fa",
	"bul": "bg", "hrv": "hr", "srp": "sr", "tam": "ta", "tel": "te", "kaz": "kk", "aze": "az", "geo": "ka",
	"kat": "ka", "ice": "is", "isl": "is", "est": "et", "lav": "lv", "lit": "lt", "slv": "sl", "slo": "sk",
	"slk": "sk", "cat": "ca", "may": "ms", "msa": "ms", "fil": "tl", "tgl": "tl",
}

// tsLangCode normalizes a language tag ("eng", "en-US", "und") to ISO 639-1; "" when unknown.
func tsLangCode(tag string) string {
	c := strings.ToLower(strings.TrimSpace(tag))
	if i := strings.IndexAny(c, "-_"); i > 0 {
		c = c[:i]
	}
	switch c {
	case "", "und", "mul", "zxx", "mis", "unk", "unknown":
		return ""
	}
	if len(c) == 3 {
		if m, ok := tsISO6392[c]; ok {
			return m
		}
	}
	return c
}

// tsLangNames are words a track title may use for a language ("English", "Türkçe").
var tsLangNames = map[string][]string{
	"en": {"english", "eng"}, "tr": {"turkish", "türkçe", "turkce"}, "ru": {"russian", "русский"},
	"ja": {"japanese"}, "ko": {"korean"}, "fr": {"french", "français"}, "de": {"german", "deutsch"},
	"es": {"spanish", "español"}, "it": {"italian", "italiano"}, "hi": {"hindi"}, "zh": {"chinese", "mandarin"},
}

// Release-name tokens (after tsNormalize) that name a dub or the audio language
var (
	tsDubTokens = map[string]bool{"mvo": true, "dvo": true, "avo": true, "dub": true, "dubbed": true}
	// Cyrillic tracker words: "Дубляж", "дублированный", "многоголосый" (voice-over)
	tsDubCyrillic  = regexp.MustCompile(`(?i)дубл|многоголос|двухголос`)
	tsLangTokens   = map[string]string{"rus": "ru", "russian": "ru", "ukr": "uk", "ukrainian": "uk", "ita": "it", "italian": "it", "latino": "es", "castellano": "es", "spanish": "es", "french": "fr", "truefrench": "fr", "vff": "fr", "vfq": "fr", "vfi": "fr", "german": "de", "hindi": "hi"}
	tsMultiTokens  = map[string]bool{"multi": true, "multilang": true, "multilanguage": true, "dual": true}
	tsOrigTokens   = map[string]bool{"original": true, "orig": true}
	tsEnglishToken = map[string]bool{"eng": true, "english": true}
)

// tsLangScore is the name's language part: a dub or a foreign-language tag (one that is not
// the original language) without a hint of more tracks is mildly negative, MULTi / Dual Audio /
// ENG (for an English or unknown original) / "Original" mildly positive. rest are the name's
// normalized tokens after the title.
func tsLangScore(title string, rest []string, origLang string) float64 {
	dub, multi := tsDubCyrillic.MatchString(title), false
	for i, t := range rest {
		switch {
		case t == "multi" && i+1 < len(rest) && tsSubWords[rest[i+1]]:
			// MULTi-SUB names subtitles, not audio (see subs.go)
		case tsMultiTokens[t] || tsOrigTokens[t]:
			multi = true
		case tsEnglishToken[t]:
			// For another original ENG may be a dub or a second track: no hint
			multi = multi || origLang == "" || origLang == "en"
		case tsDubTokens[t]:
			dub = true
		default:
			if l, ok := tsLangTokens[t]; ok && l != origLang {
				dub = true
			}
		}
	}
	switch {
	case multi:
		return tsLangMultiPart
	case dub:
		return tsLangDubPart
	}
	return 0
}

// tsWrongAudio explains why a probed file's audio does not fit ("" = fine or unknown): every
// audio track carries a language tag and none is the original language or the profile's. An
// untagged track (often the original), or one whose title says "original" or names the
// original language, keeps the file. Without an original language nothing is judged.
func tsWrongAudio(p *tsProbe, origLang, profileLang string) string {
	if p == nil || origLang == "" {
		return ""
	}
	seen := map[string]bool{}
	for i := range p.Streams {
		st := &p.Streams[i]
		if st.CodecType != "audio" {
			continue
		}
		lang := tsLangCode(tsTag(st, "language"))
		if lang == "" || lang == origLang || lang == profileLang || tsTitleSaysOriginal(tsTag(st, "title"), origLang) {
			return ""
		}
		seen[lang] = true
	}
	if len(seen) == 0 {
		return ""
	}
	langs := make([]string, 0, len(seen))
	for l := range seen {
		langs = append(langs, l)
	}
	sort.Strings(langs)
	return fmt.Sprintf("audio only in %s, original %s", strings.Join(langs, "+"), origLang)
}

func tsTitleSaysOriginal(title, origLang string) bool {
	t := strings.ToLower(title)
	if t == "" {
		return false
	}
	if strings.Contains(t, "orig") || strings.Contains(t, "оригинал") {
		return true
	}
	for _, name := range tsLangNames[origLang] {
		if strings.Contains(t, name) {
			return true
		}
	}
	return false
}

// tsTag reads a stream tag case-insensitively (Matroska files may carry "LANGUAGE").
func tsTag(st *tsProbeStream, key string) string {
	if v, ok := st.Tags[key]; ok {
		return v
	}
	for k, v := range st.Tags {
		if strings.EqualFold(k, key) {
			return v
		}
	}
	return ""
}

// originalLanguage is q's original language: from the names lookup, else from TMDB (cached
// in memory by the shared client), waiting at most tsOrigLangWait.
func (s *Service) originalLanguage(q tsResolveQuery) string {
	if q.origLang != "" || q.tmdbID <= 0 || s.cfg.TMDB == nil {
		return q.origLang
	}
	ch := make(chan string, 1)
	go func() {
		lang := ""
		if q.mediaType == "movie" {
			if d, err := s.cfg.TMDB.GetMovieDetails(q.tmdbID); err == nil && d != nil {
				lang = d.OriginalLanguage
			}
		} else if d, err := s.cfg.TMDB.GetTVDetails(q.tmdbID); err == nil && d != nil {
			lang = d.OriginalLanguage
		}
		ch <- strings.ToLower(lang)
	}()
	select {
	case lang := <-ch:
		return lang
	case <-time.After(tsOrigLangWait):
		return ""
	}
}

// applyProbedLanguage marks a checked result unfit when its probed audio has neither the
// original language nor the profile's (see tsWrongAudio). True when it did.
func (s *Service) applyProbedLanguage(r *tsResolveResult, q tsResolveQuery) bool {
	if r.unfit != "" {
		return false
	}
	v, ok := s.probeCache.Load(fmt.Sprintf("%s:%d", r.Hash, r.Index))
	if !ok {
		return false
	}
	why := tsWrongAudio(v.(*tsProbe), s.originalLanguage(q), q.lang)
	if why == "" {
		return false
	}
	r.unfit = why
	log.Printf("resolve %q: %q has the wrong audio (%s), taken only when nothing else is left", q.title, r.TorrentTitle, why)
	return true
}
