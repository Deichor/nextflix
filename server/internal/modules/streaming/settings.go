package streaming

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log"
	"net"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"time"

	"gorm.io/gorm"
)

const indexerTestTimeout = 30 * time.Second

var (
	errIndexerNotConfigured = errors.New("Jackett URL and API key must be configured")
	errIndexerUnreachable   = errors.New("Jackett connection failed")
)

// tsDefaultJackettURL is the compose service (deploy/docker-compose.yml).
const tsDefaultJackettURL = "http://jackett:9117"

// jackettClient never follows redirects, so a request (and its API key) cannot be bounced
// to another host. Jackett answers are read up to jackettMaxBody.
var jackettClient = &http.Client{
	CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
}

const jackettMaxBody = 32 << 20

// tsServiceName: a single-label host (a compose service or LAN machine) that starts with a
// letter, so no resolver reads it as a numeric IP ("2130706433", "0x7f000001").
var tsServiceName = regexp.MustCompile(`^[a-z]([a-z0-9-]*[a-z0-9])?$`)

// jackettHostAllowed: Jackett runs next to Nextflix, so its URL may only name a loopback or
// private (RFC 1918 / ULA) address, a compose service or LAN name, or a *.local / *.lan host.
func jackettHostAllowed(host string) bool {
	h := strings.ToLower(strings.TrimSuffix(host, "."))
	if ip := net.ParseIP(h); ip != nil {
		return ip.IsLoopback() || ip.IsPrivate()
	}
	if tsServiceName.MatchString(h) || h == "host.docker.internal" {
		return true
	}
	for _, suf := range []string{".local", ".lan", ".home.arpa", ".localhost"} {
		if strings.HasSuffix(h, suf) && len(h) > len(suf) {
			return true
		}
	}
	return false
}

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
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" || u.User != nil {
		return "", SettingsError{"Jackett URL must be an http(s) URL"}
	}
	if !jackettHostAllowed(u.Hostname()) {
		return "", SettingsError{"Jackett URL must point to a local host (e.g. http://jackett:9117, localhost, a private IP or *.local)"}
	}
	return v, nil
}

// sameJackettURL compares two Jackett base URLs (scheme and host case-insensitive).
func sameJackettURL(a, b string) bool {
	norm := func(v string) string {
		v = strings.TrimSuffix(strings.TrimSpace(v), "/")
		if u, err := url.Parse(v); err == nil && u.Host != "" {
			u.Scheme, u.Host = strings.ToLower(u.Scheme), strings.ToLower(u.Host)
			return u.String()
		}
		return v
	}
	return a != "" && norm(a) == norm(b)
}

// jackettKeyHome reports whether base is a URL the stored keys already go to: the saved
// URL, the env URL or the compose default.
func (s *Service) jackettKeyHome(base, savedURL string) bool {
	envURL := strings.TrimSuffix(strings.TrimSpace(s.cfg.JackettURL), "/")
	return sameJackettURL(base, savedURL) || sameJackettURL(base, envURL) || sameJackettURL(base, tsDefaultJackettURL)
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
		// Resolve sends the stored (or env) key to the saved URL, so a new URL needs its own key
		if v != "" && !s.jackettKeyHome(v, st.JackettURL) && strings.TrimSpace(in.JackettAPIKey.Value) == "" {
			return nil, SettingsError{"Enter the API key for the new Jackett URL"}
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

// TestIndexer runs a small search against Jackett. An empty URL falls back to the saved
// settings, then to the env config. An empty key falls back to a stored key (saved, then env)
// only when the URL is one resolve sends that key to: a stored key never goes to a new URL.
// Returns the URL used and the result count.
func (s *Service) TestIndexer(ctx context.Context, in TestInput) (string, int, error) {
	base, err := normalizeJackettURL(in.JackettURL)
	if err != nil {
		return "", 0, err
	}
	var saved IndexerSettings
	if s.db != nil {
		if st, err := loadSettings(s.db); err == nil {
			saved = *st
		}
	}
	envURL := strings.TrimSuffix(strings.TrimSpace(s.cfg.JackettURL), "/")
	if base == "" {
		base = saved.JackettURL
	}
	if base == "" {
		base = envURL
	}
	key := strings.TrimSpace(in.JackettAPIKey)
	if key == "" && s.jackettKeyHome(base, saved.JackettURL) {
		key = saved.JackettAPIKey
		if key == "" {
			key = strings.TrimSpace(s.cfg.JackettAPIKey)
		}
	}
	if base == "" || key == "" {
		return "", 0, errIndexerNotConfigured
	}
	// A saved or env URL from before the host check is checked too
	if _, err := normalizeJackettURL(base); err != nil {
		return "", 0, err
	}

	ctx, cancel := context.WithTimeout(ctx, indexerTestTimeout)
	defer cancel()
	n, err := jackettTestSearch(ctx, base, key)
	if err != nil {
		log.Printf("settings: Jackett test failed: %v", err)
		return base, 0, errIndexerUnreachable
	}
	return base, n, nil
}

// jackettTestSearch runs Jackett's aggregate search for "test" (no redirects) and counts the
// results. Errors never carry the request URL (it holds the key).
func jackettTestSearch(ctx context.Context, base, key string) (int, error) {
	params := url.Values{"apikey": {key}, "Query": {"test"}, "Category[]": {"2000", "5000"}}
	u := strings.TrimSuffix(base, "/") + "/api/v2.0/indexers/all/results?" + params.Encode()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return 0, errors.New("invalid Jackett URL")
	}
	resp, err := jackettClient.Do(req)
	if err != nil {
		var ue *url.Error
		if errors.As(err, &ue) {
			err = ue.Err
		}
		return 0, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return 0, errors.New("jackett returned status " + resp.Status)
	}
	var jr JackettResponse
	if err := json.NewDecoder(io.LimitReader(resp.Body, jackettMaxBody)).Decode(&jr); err != nil {
		return 0, errors.New("jackett answer is not JSON")
	}
	return len(jr.Results), nil
}
