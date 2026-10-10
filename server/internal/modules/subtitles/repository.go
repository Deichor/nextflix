package subtitles

import (
	"errors"
	"log"
	"strings"
	"sync"

	"gorm.io/gorm"
)

const settingsID = 1

// settingsRepo stores the single settings row and keeps a copy in memory.
type settingsRepo struct {
	db     *gorm.DB
	mu     sync.RWMutex
	cached *Settings
}

func newSettingsRepo(db *gorm.DB) *settingsRepo { return &settingsRepo{db: db} }

func defaultSettings() Settings {
	return Settings{ID: settingsID, TurkceAltyaziEnabled: true, Subf2mEnabled: true, YIFYEnabled: true, DefaultLangs: "tr,en",
		AITranslateLangs: aiProfileLang}
}

// ensure seeds the row on first start.
func (r *settingsRepo) ensure() error {
	if r.db == nil {
		return nil
	}
	var s Settings
	err := r.db.First(&s, settingsID).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		d := defaultSettings()
		return r.db.Create(&d).Error
	}
	return err
}

// get returns the settings; on a database error the defaults (not cached, keys empty).
func (r *settingsRepo) get() Settings {
	s, err := r.load()
	if err != nil {
		log.Printf("subtitles: settings: %v", err)
		return defaultSettings()
	}
	return s
}

// load reads the settings row. Only a missing row means the defaults; other errors are
// returned and not cached, so a later save cannot overwrite the stored keys with defaults.
func (r *settingsRepo) load() (Settings, error) {
	r.mu.RLock()
	if r.cached != nil {
		s := *r.cached
		r.mu.RUnlock()
		return s, nil
	}
	r.mu.RUnlock()

	s := defaultSettings()
	if r.db != nil {
		var row Settings
		err := r.db.First(&row, settingsID).Error
		switch {
		case err == nil:
			s = row
		case !errors.Is(err, gorm.ErrRecordNotFound):
			return Settings{}, err
		}
	}
	r.mu.Lock()
	r.cached = &s
	r.mu.Unlock()
	return s, nil
}

func (r *settingsRepo) save(s Settings) error {
	s.ID = settingsID
	if r.db != nil {
		if err := r.db.Save(&s).Error; err != nil {
			return err
		}
	}
	r.mu.Lock()
	r.cached = &s
	r.mu.Unlock()
	return nil
}

// apply merges a PUT body into s: missing or empty keeps the value, null clears it.
func (in SettingsInput) apply(s *Settings) {
	set := func(dst *string, o optString) {
		switch {
		case o.Null:
			*dst = ""
		case o.Set && strings.TrimSpace(o.Value) != "":
			*dst = strings.TrimSpace(o.Value)
		}
	}
	if o := in.OpenSubtitles; o != nil {
		set(&s.OpenSubtitlesAPIKey, o.APIKey)
		set(&s.OpenSubtitlesUsername, o.Username)
		set(&s.OpenSubtitlesPassword, o.Password)
	}
	if o := in.SubDL; o != nil {
		set(&s.SubDLAPIKey, o.APIKey)
	}
	if o := in.SubSource; o != nil {
		set(&s.SubSourceAPIKey, o.APIKey)
	}
	if o := in.TurkceAltyazi; o != nil && o.Enabled != nil {
		s.TurkceAltyaziEnabled = *o.Enabled
	}
	if o := in.Subf2m; o != nil && o.Enabled != nil {
		s.Subf2mEnabled = *o.Enabled
	}
	if o := in.YIFY; o != nil && o.Enabled != nil {
		s.YIFYEnabled = *o.Enabled
	}
	if o := in.AI; o != nil {
		if o.Enabled != nil {
			s.AITranslateEnabled = *o.Enabled
		}
		set(&s.AnthropicAPIKey, o.APIKey)
		if o.Langs != nil {
			if l := parseAILangs(*o.Langs); len(l) > 0 {
				s.AITranslateLangs = strings.Join(l, ",")
			}
		}
	}
	if in.DefaultLangs != nil {
		if l := parseLangs(strings.Join(*in.DefaultLangs, ",")); len(l) > 0 {
			s.DefaultLangs = strings.Join(l, ",")
		}
	}
}

func (s Settings) view() SettingsView {
	var v SettingsView
	v.OpenSubtitles.APIKeySet = s.OpenSubtitlesAPIKey != ""
	v.OpenSubtitles.Username = s.OpenSubtitlesUsername
	v.OpenSubtitles.PasswordSet = s.OpenSubtitlesPassword != ""
	v.SubDL.APIKeySet = s.SubDLAPIKey != ""
	v.SubSource.APIKeySet = s.SubSourceAPIKey != ""
	v.TurkceAltyazi.Enabled = s.TurkceAltyaziEnabled
	v.Subf2m.Enabled = s.Subf2mEnabled
	v.YIFY.Enabled = s.YIFYEnabled
	v.AI.Enabled = s.AITranslateEnabled
	v.AI.APIKeySet = s.AnthropicAPIKey != ""
	v.AI.Langs = s.aiLangs()
	v.AI.Languages = aiLanguages
	v.AI.Model = aiModel
	v.DefaultLangs = s.langs()
	return v
}
