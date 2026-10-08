package streaming

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
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
