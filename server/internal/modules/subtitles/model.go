package subtitles

import (
	"encoding/json"
	"strings"
	"time"
)

// Source names used in Sub.Source and in opaque ids.
const (
	SourceTorrent       = "torrent"
	SourceEmbedded      = "embedded"
	SourceOpenSubtitles = "opensubtitles"
	SourceSubDL         = "subdl"
	SourceTurkceAltyazi = "turkcealtyazi"
	SourceSubSource     = "subsource"
	SourceSubf2m        = "subf2m"
	SourceYIFY          = "yifysubtitles"
	SourceAI            = "ai" // a Claude translation of another subtitle (ai.go)
)

// Settings is the single row of subtitle provider settings (id 1). Secrets never leave the server.
type Settings struct {
	ID                    uint   `gorm:"primaryKey"`
	OpenSubtitlesAPIKey   string `gorm:"column:opensubtitles_api_key"`
	OpenSubtitlesUsername string `gorm:"column:opensubtitles_username"`
	OpenSubtitlesPassword string `gorm:"column:opensubtitles_password"`
	SubDLAPIKey           string `gorm:"column:subdl_api_key"`
	SubSourceAPIKey       string `gorm:"column:subsource_api_key"`
	TurkceAltyaziEnabled  bool   `gorm:"column:turkcealtyazi_enabled"`
	Subf2mEnabled         bool   `gorm:"column:subf2m_enabled;default:true"`
	YIFYEnabled           bool   `gorm:"column:yify_enabled;default:true"`
	DefaultLangs          string `gorm:"column:default_langs"`
	AITranslateEnabled    bool   `gorm:"column:ai_translate_enabled"`
	AnthropicAPIKey       string `gorm:"column:anthropic_api_key"`
	AITranslateLangs      string `gorm:"column:ai_translate_langs;default:profile"`
	UpdatedAt             time.Time
}

func (Settings) TableName() string { return "subtitle_settings" }

// Models returns the tables of this module for database.Migrate.
func Models() []any { return []any{&Settings{}, &AIUsage{}} }

func (s Settings) langs() []string {
	if l := parseLangs(s.DefaultLangs); len(l) > 0 {
		return l
	}
	return []string{"tr", "en"}
}

func (s Settings) aiLangs() []string {
	if l := parseAILangs(strings.Split(s.AITranslateLangs, ",")); len(l) > 0 {
		return l
	}
	return []string{aiProfileLang}
}

// SettingsView is what GET /api/settings/subtitles returns.
type SettingsView struct {
	OpenSubtitles struct {
		APIKeySet   bool   `json:"api_key_set"`
		Username    string `json:"username"`
		PasswordSet bool   `json:"password_set"`
	} `json:"opensubtitles"`
	SubDL struct {
		APIKeySet bool `json:"api_key_set"`
	} `json:"subdl"`
	SubSource struct {
		APIKeySet bool `json:"api_key_set"`
	} `json:"subsource"`
	TurkceAltyazi struct {
		Enabled bool `json:"enabled"`
	} `json:"turkcealtyazi"`
	Subf2m struct {
		Enabled bool `json:"enabled"`
	} `json:"subf2m"`
	YIFY struct {
		Enabled bool `json:"enabled"`
	} `json:"yifysubtitles"`
	AI struct {
		Enabled   bool         `json:"enabled"`
		APIKeySet bool         `json:"api_key_set"`
		Langs     []string     `json:"langs"` // "profile" or language codes
		Languages []AILanguage `json:"languages"`
		Model     string       `json:"model"`
		Usage     AIUsageView  `json:"usage"`
	} `json:"ai_translate"`
	DefaultLangs []string `json:"default_langs"`
}

// optString distinguishes a missing field, an explicit null and a value in PUT bodies.
type optString struct {
	Set   bool
	Null  bool
	Value string
}

func (o *optString) UnmarshalJSON(b []byte) error {
	o.Set = true
	if string(b) == "null" {
		o.Null = true
		return nil
	}
	return json.Unmarshal(b, &o.Value)
}

// SettingsInput is the PUT body: empty strings keep the stored value, null clears it.
type SettingsInput struct {
	OpenSubtitles *struct {
		APIKey   optString `json:"api_key"`
		Username optString `json:"username"`
		Password optString `json:"password"`
	} `json:"opensubtitles"`
	SubDL *struct {
		APIKey optString `json:"api_key"`
	} `json:"subdl"`
	SubSource *struct {
		APIKey optString `json:"api_key"`
	} `json:"subsource"`
	TurkceAltyazi *struct {
		Enabled *bool `json:"enabled"`
	} `json:"turkcealtyazi"`
	Subf2m *struct {
		Enabled *bool `json:"enabled"`
	} `json:"subf2m"`
	YIFY *struct {
		Enabled *bool `json:"enabled"`
	} `json:"yifysubtitles"`
	AI *struct {
		Enabled *bool     `json:"enabled"`
		APIKey  optString `json:"api_key"`
		Langs   *[]string `json:"langs"`
	} `json:"ai_translate"`
	DefaultLangs *[]string `json:"default_langs"`
}

// Sub is one subtitle choice returned by GET /api/subtitles.
type Sub struct {
	ID        string  `json:"id"`
	Source    string  `json:"source"`
	Language  string  `json:"language"`
	Label     string  `json:"label"`
	Release   string  `json:"release"`
	HI        bool    `json:"hi"`
	Forced    bool    `json:"forced"`
	Synced    bool    `json:"synced"`
	Score     float64 `json:"score"`
	Author    string  `json:"author,omitempty"`
	FPS       float64 `json:"fps,omitempty"`
	Downloads int     `json:"downloads,omitempty"`
	// An embedded track of a big file: read while it plays, so its file answers grow (live.go)
	Live bool `json:"live,omitempty"`
	// OpenSubtitles' download quota is used up: listed last, its file answers 429 until the reset
	Quota bool `json:"quota,omitempty"`
	// An AI translation: the language it is translated from
	AIFrom string `json:"ai_from,omitempty"`
}

// Query is a subtitle search for one movie or episode.
type Query struct {
	TmdbID    int
	MediaType string // movie | tv
	Season    int
	Episode   int
	Hash      string // TorrServer info hash of the playing torrent
	Index     int    // TorrServer file index of the playing file
	Langs     []string
	Title     string // playing torrent title, a ranking hint only
	Year      int
	// Refresh skips the cached provider answers (late uploads), at most every refreshEvery per title
	Refresh bool
	// FPS is the playing file's probed frame rate (0 = unknown), a ranking hint
	FPS float64
	// UILang is the language of the watching profile (AI translation target "profile")
	UILang string
}

func (q Query) isTV() bool { return q.MediaType == "tv" }

// candidate is a provider result plus the signals the ranking uses.
type candidate struct {
	Sub
	Releases     []string // release names the subtitle is made for (first is shown)
	Rating       float64  // provider rating 0..10, 0 = unknown
	Votes        int
	Trusted      bool
	Machine      bool // machine or AI translated
	Quality      int  // provider quality tier: 1 best .. 0 unknown, higher worse
	MultiCD      bool
	MovieHashHit bool
	Pack         bool // a season pack (its names need not name the episode)
	// AutoSync is the timing result for the playing file: 1 fits once timed, -1 does not fit
	AutoSync int
}

var langLabels = map[string]string{
	"tr": "Türkçe", "en": "English", "de": "Deutsch", "fr": "Français", "es": "Español", "it": "Italiano",
	"pt": "Português", "nl": "Nederlands", "ru": "Русский", "ar": "العربية", "fa": "فارسی", "el": "Ελληνικά",
	"pl": "Polski", "sv": "Svenska", "da": "Dansk", "no": "Norsk", "fi": "Suomi", "ja": "日本語", "ko": "한국어",
	"zh": "中文", "he": "עברית", "hu": "Magyar", "cs": "Čeština", "ro": "Română", "bg": "Български",
	"hr": "Hrvatski", "sr": "Srpski", "uk": "Українська", "az": "Azərbaycan", "id": "Bahasa Indonesia",
}

func langLabel(code string) string {
	if l, ok := langLabels[code]; ok {
		return l
	}
	if l, ok := aiLanguage(code); ok {
		return l.Native
	}
	if code == "" {
		return "Unknown"
	}
	return strings.ToUpper(code)
}

// normLang maps provider language codes ("TR", "pt-BR", "tur", "turkish") to ISO 639-1.
func normLang(code string) string {
	c := strings.ToLower(strings.TrimSpace(code))
	if i := strings.IndexAny(c, "-_"); i > 0 {
		c = c[:i]
	}
	switch c {
	case "tur", "turkish", "türkçe", "turkce":
		return "tr"
	case "eng", "english":
		return "en"
	}
	return c
}

func parseLangs(s string) []string {
	var out []string
	seen := map[string]bool{}
	for _, p := range strings.Split(s, ",") {
		l := normLang(p)
		if len(l) < 2 || len(l) > 3 || seen[l] {
			continue
		}
		seen[l] = true
		out = append(out, l)
	}
	return out
}
