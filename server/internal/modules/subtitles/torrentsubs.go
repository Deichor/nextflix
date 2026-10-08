package subtitles

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"path"
	"regexp"
	"strings"
	"time"
	"unicode"
)

const (
	maxSubtitleBytes = 5 << 20
	torrSubTimeout   = 30 * time.Second
)

var (
	torrHashPattern = regexp.MustCompile(`^[a-fA-F0-9]{40}$`)
	subtitleExts    = map[string]string{".srt": "srt", ".ass": "ass", ".ssa": "ass", ".vtt": "vtt", ".sub": "sub"}
	torrHTTP        = &http.Client{Timeout: torrSubTimeout}
)

// ErrTooLarge is returned for subtitle files above the size cap.
var ErrTooLarge = errors.New("subtitle file too large")

type TorrentSub struct {
	FileID   int // TorrServer file id
	Path     string
	Language string // ISO 639-1 or ""
	Format   string // "srt", "ass", "vtt", "sub"
	Forced   bool
	HI       bool // hearing impaired / SDH
}

type torrFile struct {
	ID     int    `json:"id"`
	Path   string `json:"path"`
	Length int64  `json:"length"`
}

type torrStat struct {
	FileStats []torrFile `json:"file_stats"`
}

// ListTorrentSubs lists subtitle files shipped inside the torrent (TorrServer /stream?link=<hash>&stat file_stats).
func ListTorrentSubs(ctx context.Context, torrServerURL, hash string) ([]TorrentSub, error) {
	files, err := torrentFiles(ctx, torrServerURL, hash)
	if err != nil {
		return nil, err
	}
	// A .sub next to a same-named .idx is VobSub (images), not MicroDVD text.
	idx := map[string]bool{}
	for _, f := range files {
		if strings.EqualFold(path.Ext(f.Path), ".idx") {
			idx[strings.ToLower(strings.TrimSuffix(f.Path, path.Ext(f.Path)))] = true
		}
	}
	var subs []TorrentSub
	for _, f := range files {
		ext := strings.ToLower(path.Ext(f.Path))
		format, ok := subtitleExts[ext]
		if !ok || f.Length > maxSubtitleBytes {
			continue
		}
		if format == "sub" && idx[strings.ToLower(strings.TrimSuffix(f.Path, path.Ext(f.Path)))] {
			continue
		}
		forced, hi := subFlags(f.Path)
		subs = append(subs, TorrentSub{
			FileID:   f.ID,
			Path:     f.Path,
			Language: GuessLanguage(f.Path),
			Format:   format,
			Forced:   forced,
			HI:       hi,
		})
	}
	return subs, nil
}

func torrentFiles(ctx context.Context, torrServerURL, hash string) ([]torrFile, error) {
	if !torrHashPattern.MatchString(hash) {
		return nil, fmt.Errorf("invalid torrent hash %q", hash)
	}
	statURL := strings.TrimRight(torrServerURL, "/") + "/stream?" + url.Values{"link": {strings.ToLower(hash)}, "stat": {""}}.Encode()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, statURL, nil)
	if err != nil {
		return nil, err
	}
	resp, err := torrHTTP.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("torrserver stat returned %d", resp.StatusCode)
	}
	var st torrStat
	if err := json.NewDecoder(io.LimitReader(resp.Body, 8<<20)).Decode(&st); err != nil {
		return nil, fmt.Errorf("torrserver stat: %w", err)
	}
	return st.FileStats, nil
}

// FetchTorrentSub downloads one subtitle file of the torrent through TorrServer (capped at 5 MB).
func FetchTorrentSub(ctx context.Context, torrServerURL, hash string, fileID int) ([]byte, error) {
	if !torrHashPattern.MatchString(hash) || fileID < 0 {
		return nil, fmt.Errorf("invalid torrent file %q/%d", hash, fileID)
	}
	u := torrFileURL(torrServerURL, hash, fileID)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil, err
	}
	resp, err := torrHTTP.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusPartialContent {
		return nil, fmt.Errorf("torrserver returned %d", resp.StatusCode)
	}
	if resp.ContentLength > maxSubtitleBytes {
		return nil, ErrTooLarge
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, maxSubtitleBytes+1))
	if err != nil {
		return nil, err
	}
	if len(data) > maxSubtitleBytes {
		return nil, ErrTooLarge
	}
	return data, nil
}

// Language guessing

var languageNames = map[string]string{
	"turkish": "tr", "türkçe": "tr", "turkce": "tr",
	"english": "en", "ingilizce": "en", "german": "de", "deutsch": "de", "almanca": "de",
	"french": "fr", "français": "fr", "francais": "fr", "fransizca": "fr",
	"spanish": "es", "español": "es", "espanol": "es", "castellano": "es", "latino": "es",
	"italian": "it", "italiano": "it", "portuguese": "pt", "português": "pt", "portugues": "pt", "brazilian": "pt",
	"russian": "ru", "arabic": "ar", "dutch": "nl", "nederlands": "nl", "polish": "pl", "polski": "pl",
	"swedish": "sv", "svenska": "sv", "danish": "da", "dansk": "da", "norwegian": "no", "norsk": "no",
	"finnish": "fi", "suomi": "fi", "greek": "el", "hebrew": "he", "hungarian": "hu", "magyar": "hu",
	"czech": "cs", "romanian": "ro", "bulgarian": "bg", "croatian": "hr", "serbian": "sr", "ukrainian": "uk",
	"japanese": "ja", "korean": "ko", "chinese": "zh", "mandarin": "zh", "cantonese": "zh", "persian": "fa",
	"farsi": "fa", "indonesian": "id", "vietnamese": "vi", "thai": "th", "hindi": "hi", "malay": "ms",
}

// ISO 639-2 (B and T) codes. Exported through iso639To1 for ffprobe language tags as well.
var languageCodes3 = map[string]string{
	"tur": "tr", "eng": "en", "ger": "de", "deu": "de", "fre": "fr", "fra": "fr", "spa": "es", "ita": "it",
	"por": "pt", "pob": "pt", "rus": "ru", "ara": "ar", "dut": "nl", "nld": "nl", "pol": "pl", "swe": "sv",
	"dan": "da", "nor": "no", "nob": "no", "fin": "fi", "gre": "el", "ell": "el", "heb": "he", "hun": "hu",
	"cze": "cs", "ces": "cs", "rum": "ro", "ron": "ro", "bul": "bg", "hrv": "hr", "scr": "hr", "srp": "sr",
	"scc": "sr", "ukr": "uk", "jpn": "ja", "kor": "ko", "chi": "zh", "zho": "zh", "per": "fa", "fas": "fa",
	"ind": "id", "vie": "vi", "tha": "th", "hin": "hi", "may": "ms", "msa": "ms",
}

// Two-letter codes are only trusted next to the extension ("Movie.tr.srt"); "hi" means hearing impaired there.
var languageCodes2 = map[string]bool{
	"tr": true, "en": true, "de": true, "fr": true, "es": true, "it": true, "pt": true, "ru": true, "ar": true,
	"nl": true, "pl": true, "sv": true, "da": true, "no": true, "nb": true, "fi": true, "el": true, "he": true,
	"hu": true, "cs": true, "ro": true, "bg": true, "hr": true, "sr": true, "uk": true, "ja": true, "ko": true,
	"zh": true, "fa": true, "id": true, "vi": true, "th": true, "ms": true,
}

// Three-letter codes trusted anywhere in a name; the rest ("dan", "vie", "ron", "por"...) are also common words
// or names and only count next to the extension.
var anywhereCodes3 = map[string]bool{
	"tur": true, "eng": true, "ger": true, "deu": true, "fre": true, "fra": true, "ita": true, "rus": true,
	"jpn": true, "kor": true, "nld": true, "swe": true, "ukr": true, "cze": true, "ces": true, "heb": true,
	"hrv": true, "srp": true, "bul": true, "zho": true, "fas": true, "nob": true, "pob": true, "ell": true,
}

// Tokens that may sit between the language and the extension.
var flagTokens = map[string]bool{
	"forced": true, "foreign": true, "sdh": true, "hi": true, "cc": true, "full": true, "default": true,
	"sub": true, "subs": true, "subtitle": true, "subtitles": true, "altyazi": true,
	"srt": true, "ass": true, "ssa": true, "vtt": true, "hearing": true, "impaired": true, "track": true,
}

// GuessLanguage guesses an ISO 639-1 language from a file name like "Movie.2024.tr.srt", "Turkish.srt",
// "tur.forced.srt" or "2_English.srt" (parent folders count too); "" when unknown.
func GuessLanguage(fileName string) string {
	p := strings.ReplaceAll(fileName, "\\", "/")
	base := path.Base(p)
	if ext := path.Ext(base); subtitleExts[strings.ToLower(ext)] != "" || strings.EqualFold(ext, ".idx") {
		base = strings.TrimSuffix(base, ext)
	}
	if lang := guessFromTokens(nameTokens(base)); lang != "" {
		return lang
	}
	// Folders like "Subs/Turkish/1.srt" or "Subs/tur/..."
	dirs := strings.Split(path.Dir(p), "/")
	for i := len(dirs) - 1; i >= 0 && i >= len(dirs)-2; i-- {
		if lang := guessFromTokens(nameTokens(dirs[i])); lang != "" {
			return lang
		}
	}
	return ""
}

func guessFromTokens(tokens []string) string {
	// The token right before the extension, flags skipped.
	for i := len(tokens) - 1; i >= 0; i-- {
		t := tokens[i]
		if flagTokens[t] || isDigits(t) {
			continue
		}
		if lang := codeOrName(t, true); lang != "" {
			return lang
		}
		// Region suffix: "pt-BR", "zh-TW"
		if len(t) == 2 && i > 0 && languageCodes2[tokens[i-1]] {
			return normalize2(tokens[i-1])
		}
		break
	}
	// Anywhere else only unambiguous names and codes.
	for i := len(tokens) - 1; i >= 0; i-- {
		if lang := codeOrName(tokens[i], false); lang != "" {
			return lang
		}
	}
	return ""
}

func codeOrName(t string, trailing bool) string {
	if lang, ok := languageNames[t]; ok {
		return lang
	}
	if lang, ok := languageCodes3[t]; ok && (trailing || anywhereCodes3[t]) {
		return lang
	}
	if trailing && languageCodes2[t] {
		return normalize2(t)
	}
	return ""
}

func normalize2(code string) string {
	if code == "nb" {
		return "no"
	}
	return code
}

func nameTokens(s string) []string {
	// Dotted and dotless i fold to "i" so "İNGİLİZCE", "Altyazı" and "ENGLISH" all match.
	s = strings.ToLower(strings.NewReplacer("İ", "i", "ı", "i").Replace(s))
	return strings.FieldsFunc(s, func(r rune) bool { return !unicode.IsLetter(r) && !unicode.IsDigit(r) })
}

func isDigits(s string) bool {
	for _, r := range s {
		if r < '0' || r > '9' {
			return false
		}
	}
	return s != ""
}

// subFlags reads forced and hearing-impaired markers from a file name.
func subFlags(fileName string) (forced, hi bool) {
	tokens := nameTokens(path.Base(strings.ReplaceAll(fileName, "\\", "/")))
	for i, t := range tokens {
		switch t {
		case "forced", "foreign":
			forced = true
		case "sdh", "hi", "cc":
			hi = true
		case "hearing":
			hi = hi || (i+1 < len(tokens) && tokens[i+1] == "impaired")
		}
	}
	return forced, hi
}

// iso639To1 maps an ffprobe language tag ("tur", "eng", "en") to ISO 639-1; "" when unknown.
func iso639To1(tag string) string {
	tag = strings.ToLower(strings.TrimSpace(tag))
	if lang, ok := languageCodes3[tag]; ok {
		return lang
	}
	if languageCodes2[tag] {
		return normalize2(tag)
	}
	return ""
}
