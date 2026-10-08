package streaming

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"strings"
	"time"

	"gorm.io/gorm"
)

const indexerTestTimeout = 30 * time.Second

var errIndexerNotConfigured = errors.New("Jackett URL and API key must be configured")

// SettingsInput holds the optional fields of PUT /api/settings/indexer. For the API key a
// missing field or an empty string keeps the stored key and null clears it.
type SettingsInput struct {
	JackettURL       *string   `json:"jackett_url"`
	JackettAPIKey    optString `json:"jackett_api_key"`
	MinSeeders       *int      `json:"min_seeders"`
	PreferredQuality *string   `json:"preferred_quality"`
	BitsearchEnabled *bool     `json:"bitsearch_enabled"`
}

// optString distinguishes a missing field, an explicit null and a value in PUT bodies.
type optString struct {
	Null  bool
	Value string
}

func (o *optString) UnmarshalJSON(b []byte) error {
	if string(b) == "null" {
		o.Null = true
		return nil
	}
	return json.Unmarshal(b, &o.Value)
}

// TestInput optionally overrides the saved values for POST /api/settings/indexer/test.
type TestInput struct {
	JackettURL    string `json:"jackett_url"`
	JackettAPIKey string `json:"jackett_api_key"`
}

// SettingsError carries a message that is safe to show to the client.
type SettingsError struct{ Msg string }

func (e SettingsError) Error() string { return e.Msg }

// EnsureSettings seeds the indexer settings row from the env defaults and starts the
// background TorrServer tuning.
func (s *Service) EnsureSettings() error {
	s.startTorrServerTuning()
	n, err := countSettings(s.db)
	if err != nil || n > 0 {
		return err
	}
	return s.db.Create(&IndexerSettings{
		JackettURL:       strings.TrimSuffix(strings.TrimSpace(s.cfg.JackettURL), "/"),
		JackettAPIKey:    strings.TrimSpace(s.cfg.JackettAPIKey),
		MinSeeders:       10,
		PreferredQuality: "1080p",
	}).Error
}

// Settings returns the stored indexer settings, seeding them first if needed.
func (s *Service) Settings() (*IndexerSettings, error) {
	st, err := loadSettings(s.db)
	if errors.Is(err, gorm.ErrRecordNotFound) {
		if err := s.EnsureSettings(); err != nil {
			return nil, err
		}
		return loadSettings(s.db)
	}
	return st, err
}

func normalizeJackettURL(raw string) (string, error) {
	v := strings.TrimSuffix(strings.TrimSpace(raw), "/")
	if v == "" {
		return "", nil
	}
	u, err := url.Parse(v)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return "", SettingsError{"Jackett URL must be an http(s) URL"}
	}
	return v, nil
}

func (s *Service) UpdateSettings(in SettingsInput) (*IndexerSettings, error) {
	st, err := s.Settings()
	if err != nil {
		return nil, err
	}
	if in.JackettURL != nil {
		v, err := normalizeJackettURL(*in.JackettURL)
		if err != nil {
			return nil, err
		}
		st.JackettURL = v
	}
	switch k := in.JackettAPIKey; {
	case k.Null:
		st.JackettAPIKey = ""
	case strings.TrimSpace(k.Value) != "":
		v := strings.TrimSpace(k.Value)
		if len(v) > 200 {
			return nil, SettingsError{"Invalid API key"}
		}
		st.JackettAPIKey = v
	}
	if in.BitsearchEnabled != nil {
		st.BitsearchDisabled = !*in.BitsearchEnabled
	}
	if in.MinSeeders != nil {
		if *in.MinSeeders < 0 || *in.MinSeeders > 100000 {
			return nil, SettingsError{"min_seeders must be between 0 and 100000"}
		}
		st.MinSeeders = *in.MinSeeders
	}
	if in.PreferredQuality != nil {
		v := strings.TrimSpace(*in.PreferredQuality)
		if len(v) > 16 {
			return nil, SettingsError{"Invalid preferred quality"}
		}
		st.PreferredQuality = v
	}
	if err := saveSettings(s.db, st); err != nil {
		return nil, err
	}
	// Try the new URL first on the next resolve
	s.jackettBase.Delete("base")
	return st, nil
}

// TestIndexer runs a small search against Jackett. Empty input fields fall back to the
// saved settings, then to the env config. Returns the URL used and the result count.
func (s *Service) TestIndexer(ctx context.Context, in TestInput) (string, int, error) {
	base, err := normalizeJackettURL(in.JackettURL)
	if err != nil {
		return "", 0, err
	}
	key := strings.TrimSpace(in.JackettAPIKey)
	if base == "" || key == "" {
		if st, err := loadSettings(s.db); err == nil {
			if base == "" {
				base = st.JackettURL
			}
			if key == "" {
				key = st.JackettAPIKey
			}
		}
	}
	if base == "" {
		base = strings.TrimSuffix(strings.TrimSpace(s.cfg.JackettURL), "/")
	}
	if key == "" {
		key = strings.TrimSpace(s.cfg.JackettAPIKey)
	}
	if base == "" || key == "" {
		return "", 0, errIndexerNotConfigured
	}

	ctx, cancel := context.WithTimeout(ctx, indexerTestTimeout)
	defer cancel()
	res, err := NewTorrentSearcher(base, key, 0).SearchJackettRaw(ctx, "test", "2000", "5000")
	if err != nil {
		return base, 0, fmt.Errorf("Jackett connection failed: %w", err)
	}
	return base, len(res), nil
}
