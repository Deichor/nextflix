package subtitles

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
)

func TestSettingsMerge(t *testing.T) {
	s := defaultSettings()
	s.OpenSubtitlesAPIKey = "old"
	s.SubDLAPIKey = "sdl"
	var in SettingsInput
	body := `{"opensubtitles":{"api_key":"","username":"me","password":"pw"},"subdl":{"api_key":null},
	          "turkcealtyazi":{"enabled":false},"default_langs":["en","TR","xx-long-bad",""]}`
	if err := json.Unmarshal([]byte(body), &in); err != nil {
		t.Fatal(err)
	}
	in.apply(&s)
	if s.OpenSubtitlesAPIKey != "old" || s.OpenSubtitlesUsername != "me" || s.OpenSubtitlesPassword != "pw" ||
		s.SubDLAPIKey != "" || s.TurkceAltyaziEnabled || s.DefaultLangs != "en,tr,xx" {
		t.Fatalf("merged %+v", s)
	}
}

func TestSettingsEndpointsHideSecrets(t *testing.T) {
	gin.SetMode(gin.TestMode)
	svc := NewService(nil, Config{CacheDir: t.TempDir()})
	r := gin.New()
	Register(r.Group("/api"), svc)

	put := httptest.NewRequest(http.MethodPut, "/api/settings/subtitles",
		strings.NewReader(`{"opensubtitles":{"api_key":"SECRETKEY","username":"deich","password":"SECRETPW"},"subdl":{"api_key":"SECRETSDL"}}`))
	put.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, put)
	if w.Code != 200 {
		t.Fatalf("put: %d %s", w.Code, w.Body)
	}

	w = httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/api/settings/subtitles", nil))
	out := w.Body.String()
	if strings.Contains(out, "SECRET") {
		t.Fatalf("secret leaked: %s", out)
	}
	var v SettingsView
	json.Unmarshal(w.Body.Bytes(), &v)
	if !v.OpenSubtitles.APIKeySet || !v.OpenSubtitles.PasswordSet || v.OpenSubtitles.Username != "deich" ||
		!v.SubDL.APIKeySet || !v.TurkceAltyazi.Enabled || strings.Join(v.DefaultLangs, ",") != "tr,en" {
		t.Fatalf("view %s", out)
	}
}

func TestListMergesAndServesCachedFiles(t *testing.T) {
	var osDownloads, taPosts int32
	var osSrv *httptest.Server
	osSrv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/subtitles":
			io.WriteString(w, `{"data":[{"id":"1","attributes":{"language":"en","download_count":10,
			 "release":"Inception.2010.1080p.BluRay.x264-SPARKS","files":[{"file_id":5,"file_name":"a.srt"}]}}]}`)
		case "/download":
			atomic.AddInt32(&osDownloads, 1)
			json.NewEncoder(w).Encode(map[string]string{"link": osSrv.URL + "/f", "file_name": "a.srt"})
		case "/f":
			io.WriteString(w, sampleSRT)
		}
	}))
	defer osSrv.Close()

	taSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/tt1375666/":
			io.WriteString(w, taMoviePage)
		case strings.HasPrefix(r.URL.Path, "/sub/"):
			io.WriteString(w, strings.Replace(taDetailPage, "ALTID", strings.Split(r.URL.Path, "/")[2], 1))
		case r.URL.Path == "/ind":
			atomic.AddInt32(&taPosts, 1)
			w.Write(makeZip(t, map[string]string{"x.srt": sampleSRT}))
		}
	}))
	defer taSrv.Close()

	// A provider that never answers in time must not hold the response.
	slow := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(2 * time.Second)
		io.WriteString(w, `{"status":true,"subtitles":[]}`)
	}))
	defer slow.Close()

	svc := NewService(nil, Config{
		CacheDir:         t.TempDir(),
		OpenSubtitlesURL: osSrv.URL,
		SubDLAPIURL:      slow.URL,
		TurkceAltyaziURL: taSrv.URL,
		IMDb:             func(string, int) (string, error) { return "tt1375666", nil },
	})
	svc.ta.gap = 0
	svc.wait = 700 * time.Millisecond
	if err := svc.settings.save(Settings{OpenSubtitlesAPIKey: "K", SubDLAPIKey: "S", TurkceAltyaziEnabled: true, DefaultLangs: "tr,en"}); err != nil {
		t.Fatal(err)
	}

	start := time.Now()
	items := svc.List(context.Background(), Query{TmdbID: 27205, MediaType: "movie", Title: "Inception.2010.1080p.BluRay.x264-SPARKS"})
	if d := time.Since(start); d > 1500*time.Millisecond {
		t.Fatalf("List waited %s for a slow provider", d)
	}
	if len(items) != 4 {
		t.Fatalf("got %d items: %+v", len(items), items)
	}
	if items[0].Source != SourceTurkceAltyazi || items[0].Language != "tr" || items[len(items)-1].Language != "en" {
		t.Fatalf("order: %+v", items)
	}

	var osItem Sub
	for _, it := range items {
		if it.Source == SourceOpenSubtitles {
			osItem = it
		}
	}
	if items[2].ID != osItem.ID {
		t.Fatalf("BluRay-SPARKS English subtitle must lead English: %+v", items)
	}
	for _, it := range []Sub{items[0], osItem} {
		for i := 0; i < 2; i++ {
			vtt, err := svc.File(context.Background(), it.ID)
			if err != nil || !strings.HasPrefix(string(vtt), "WEBVTT") || !strings.Contains(string(vtt), "Merhaba dünya") {
				t.Fatalf("%s: %v %q", it.Source, err, vtt)
			}
		}
	}
	if osDownloads != 1 || taPosts != 1 {
		t.Fatalf("cache missed: %d OpenSubtitles downloads, %d turkcealtyazi downloads", osDownloads, taPosts)
	}
	if _, err := svc.File(context.Background(), "not-an-id"); err != ErrBadID {
		t.Fatalf("bad id: %v", err)
	}
}
