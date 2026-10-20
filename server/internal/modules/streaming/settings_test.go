package streaming

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/gin-gonic/gin"
)

func TestIndexerSettingsKeepTheKeySecret(t *testing.T) {
	db := memDB(t)
	if err := db.Create(&IndexerSettings{JackettURL: "http://jackett:9117", JackettAPIKey: "s3cret-key", MinSeeders: 10}).Error; err != nil {
		t.Fatal(err)
	}
	s := NewService(db, Config{})
	gin.SetMode(gin.TestMode)
	r := gin.New()
	Register(r.Group("/api"), s)

	call := func(method, body string) IndexerSettingsView {
		t.Helper()
		w := httptest.NewRecorder()
		req := httptest.NewRequest(method, "/api/settings/indexer", strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		r.ServeHTTP(w, req)
		if w.Code != http.StatusOK {
			t.Fatalf("%s %s: %d %s", method, body, w.Code, w.Body)
		}
		if strings.Contains(w.Body.String(), "s3cret") || strings.Contains(w.Body.String(), "new-key") ||
			strings.Contains(w.Body.String(), "jackett_api_key") {
			t.Fatalf("%s answer leaks the key: %s", method, w.Body)
		}
		var v IndexerSettingsView
		if err := json.Unmarshal(w.Body.Bytes(), &v); err != nil {
			t.Fatal(err)
		}
		return v
	}
	key := func() string {
		st, err := loadSettings(db)
		if err != nil {
			t.Fatal(err)
		}
		return st.JackettAPIKey
	}

	if v := call(http.MethodGet, ""); !v.APIKeySet || !v.BitsearchEnabled || v.JackettURL != "http://jackett:9117" {
		t.Fatalf("GET = %+v", v)
	}
	// Missing field and empty string keep the key
	if v := call(http.MethodPut, `{"jackett_url": "http://jackett:9117/"}`); !v.APIKeySet || key() != "s3cret-key" {
		t.Fatalf("missing key: %+v, stored %q", v, key())
	}
	if v := call(http.MethodPut, `{"jackett_api_key": ""}`); !v.APIKeySet || key() != "s3cret-key" {
		t.Fatalf("empty key: %+v, stored %q", v, key())
	}
	if v := call(http.MethodPut, `{"jackett_api_key": " new-key "}`); !v.APIKeySet || key() != "new-key" {
		t.Fatalf("new key: %+v, stored %q", v, key())
	}
	// null clears it
	if v := call(http.MethodPut, `{"jackett_api_key": null}`); v.APIKeySet || key() != "" {
		t.Fatalf("null key: %+v, stored %q", v, key())
	}

	// The Bitsearch toggle
	if v := call(http.MethodPut, `{"bitsearch_enabled": false}`); v.BitsearchEnabled || s.bitsearchEnabled() {
		t.Fatalf("bitsearch off: %+v", v)
	}
	if v := call(http.MethodPut, `{"min_seeders": 5}`); v.BitsearchEnabled || v.MinSeeders != 5 {
		t.Fatalf("other field changed the toggle: %+v", v)
	}
	if v := call(http.MethodPut, `{"bitsearch_enabled": true}`); !v.BitsearchEnabled || !s.bitsearchEnabled() {
		t.Fatalf("bitsearch on: %+v", v)
	}
}

func TestJackettURLMustBeLocal(t *testing.T) {
	for _, u := range []string{"http://jackett:9117", "http://localhost:9117/", "http://127.0.0.1:9117", "https://192.168.1.5",
		"http://10.0.0.2:9117", "http://[::1]:9117", "http://nas.local:9117", "http://host.docker.internal:9117", "http://box.lan"} {
		if _, err := normalizeJackettURL(u); err != nil {
			t.Errorf("%s refused: %v", u, err)
		}
	}
	for _, u := range []string{"http://example.com", "http://8.8.8.8:9117", "http://169.254.169.254", "http://2130706433",
		"http://0x7f000001", "http://user:pw@jackett:9117", "ftp://jackett", "http://.local", "http://metadata.google.internal"} {
		if _, err := normalizeJackettURL(u); err == nil {
			t.Errorf("%s accepted", u)
		}
	}
}

// The indexer test sends a stored key only to the URL it is stored for, follows no redirect
// and answers a generic error.
func TestIndexerTestKeepsTheKeyHome(t *testing.T) {
	var mu sync.Mutex
	var keys []string // apikey of each request Jackett got
	taken := func() []string {
		mu.Lock()
		defer mu.Unlock()
		k := keys
		keys = nil
		return k
	}
	jackett := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		keys = append(keys, r.URL.Query().Get("apikey"))
		mu.Unlock()
		_, _ = w.Write([]byte(`{"Results":[{},{}]}`))
	}))
	defer jackett.Close()
	bounce := httptest.NewServer(http.RedirectHandler(jackett.URL+"/api/v2.0/indexers/all/results", http.StatusFound))
	defer bounce.Close()

	db := memDB(t)
	if err := db.Create(&IndexerSettings{JackettURL: "http://jackett:9117", JackettAPIKey: "s3cret-key", MinSeeders: 10}).Error; err != nil {
		t.Fatal(err)
	}
	s := NewService(db, Config{})
	gin.SetMode(gin.TestMode)
	r := gin.New()
	Register(r.Group("/api"), s)
	test := func(body string) (int, string) {
		w := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodPost, "/api/settings/indexer/test", strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		r.ServeHTTP(w, req)
		return w.Code, w.Body.String()
	}

	// Another URL without a key: the saved key is not sent there
	if code, body := test(`{"jackett_url": "` + jackett.URL + `"}`); code != http.StatusBadRequest {
		t.Fatalf("other URL without key: %d %s", code, body)
	}
	if k := taken(); len(k) != 0 {
		t.Fatalf("the saved key went to another URL: %v", k)
	}
	// With its own key it works
	if code, body := test(`{"jackett_url": "` + jackett.URL + `", "jackett_api_key": "other"}`); code != http.StatusOK {
		t.Fatalf("own key: %d %s", code, body)
	}
	if k := taken(); len(k) != 1 || k[0] != "other" {
		t.Fatalf("own key: sent %v", k)
	}
	// A redirect is not followed and the error names no host
	code, body := test(`{"jackett_url": "` + bounce.URL + `", "jackett_api_key": "k"}`)
	if code != http.StatusServiceUnavailable || strings.Contains(body, "127.0.0.1") || !strings.Contains(body, `"Jackett connection failed"`) {
		t.Fatalf("redirect: %d %s", code, body)
	}
	if k := taken(); len(k) != 0 {
		t.Fatalf("redirect followed: %v", k)
	}
	// A public URL is refused before any request
	if code, body := test(`{"jackett_url": "http://example.com", "jackett_api_key": "k"}`); code != http.StatusBadRequest {
		t.Fatalf("public URL: %d %s", code, body)
	}
	// The saved URL gets the saved key
	if err := db.Model(&IndexerSettings{}).Where("1 = 1").Update("jackett_url", jackett.URL).Error; err != nil {
		t.Fatal(err)
	}
	if code, body := test(`{}`); code != http.StatusOK {
		t.Fatalf("saved URL: %d %s", code, body)
	}
	if k := taken(); len(k) != 1 || k[0] != "s3cret-key" {
		t.Fatalf("saved URL: sent %v", k)
	}
}

func TestPutRefusesAPublicJackettURL(t *testing.T) {
	db := memDB(t)
	if err := db.Create(&IndexerSettings{MinSeeders: 10}).Error; err != nil {
		t.Fatal(err)
	}
	s := NewService(db, Config{})
	evil, local := "http://evil.example.com:9117", "http://jackett:9117/"
	if _, err := s.UpdateSettings(SettingsInput{JackettURL: &evil}); err == nil {
		t.Fatal("public Jackett URL saved")
	}
	if st, err := s.UpdateSettings(SettingsInput{JackettURL: &local}); err != nil || st.JackettURL != "http://jackett:9117" {
		t.Fatalf("default URL: %+v %v", st, err)
	}
}

// Resolve's Jackett requests follow no redirect, so the key cannot be bounced to another host.
func TestJackettSearchFollowsNoRedirect(t *testing.T) {
	var mu sync.Mutex
	hits := 0
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		hits++
		mu.Unlock()
		_, _ = w.Write([]byte(`{"Results":[{}]}`))
	}))
	defer target.Close()
	bounce := httptest.NewServer(http.RedirectHandler(target.URL+"/x", http.StatusFound))
	defer bounce.Close()

	ts := NewTorrentSearcher(bounce.URL, "k", 0)
	ctx := t.Context()
	if _, err := ts.SearchJackettRaw(ctx, "q"); err == nil {
		t.Error("SearchJackettRaw followed the redirect")
	}
	if _, err := ts.SearchIndexer(ctx, "all", "q"); err == nil {
		t.Error("SearchIndexer followed the redirect")
	}
	if _, err := ts.Indexers(ctx); err == nil {
		t.Error("Indexers followed the redirect")
	}
	if _, err := ts.SearchIndexerIMDb(ctx, "all", "tt0133093"); err == nil {
		t.Error("SearchIndexerIMDb followed the redirect")
	}
	mu.Lock()
	defer mu.Unlock()
	if hits != 0 {
		t.Fatalf("redirect target got %d requests", hits)
	}
}

// A new Jackett URL is saved only with its own key: resolve sends the stored key to the saved URL.
func TestPutNewJackettURLNeedsItsKey(t *testing.T) {
	db := memDB(t)
	if err := db.Create(&IndexerSettings{JackettURL: "http://jackett:9117", JackettAPIKey: "s3cret-key", MinSeeders: 10}).Error; err != nil {
		t.Fatal(err)
	}
	s := NewService(db, Config{JackettURL: "http://10.0.0.9:9117", JackettAPIKey: "env-key"})
	stored := func() IndexerSettings {
		t.Helper()
		st, err := loadSettings(db)
		if err != nil {
			t.Fatal(err)
		}
		return *st
	}
	other := "http://192.168.1.50:9117"
	for _, k := range []optString{{}, {Value: " "}, {Null: true}} {
		var serr SettingsError
		if _, err := s.UpdateSettings(SettingsInput{JackettURL: &other, JackettAPIKey: k}); !errors.As(err, &serr) {
			t.Fatalf("new URL with key %+v: %v", k, err)
		}
		if st := stored(); st.JackettURL != "http://jackett:9117" || st.JackettAPIKey != "s3cret-key" {
			t.Fatalf("refused update changed the row: %+v", st)
		}
	}
	// The same URL, the env URL, the compose default and no URL keep the stored key
	for _, u := range []string{"HTTP://Jackett:9117/", "http://10.0.0.9:9117", "http://jackett:9117", ""} {
		if _, err := s.UpdateSettings(SettingsInput{JackettURL: &u}); err != nil {
			t.Fatalf("%q: %v", u, err)
		}
		if st := stored(); st.JackettAPIKey != "s3cret-key" {
			t.Fatalf("%q: key %q", u, st.JackettAPIKey)
		}
	}
	if _, err := s.UpdateSettings(SettingsInput{JackettURL: &other, JackettAPIKey: optString{Value: "own-key"}}); err != nil {
		t.Fatal(err)
	}
	if st := stored(); st.JackettURL != other || st.JackettAPIKey != "own-key" {
		t.Fatalf("new URL with its key: %+v", st)
	}
}
