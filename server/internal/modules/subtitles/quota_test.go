package subtitles

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
)

// quotaFixture: OpenSubtitles (two Turkish files, the first synced) and SubDL (one Turkish file).
// file_id 1 downloads, file_id 2 answers 406 (quota) while quota406 is set.
type quotaFixture struct {
	svc       *Service
	router    *gin.Engine
	downloads atomic.Int32
	searches  atomic.Int32
	quota406  atomic.Bool
	slow      atomic.Bool // downloads wait a moment (concurrent callers overlap)
	reset     string
}

func newQuotaFixture(t *testing.T) *quotaFixture {
	f := &quotaFixture{reset: time.Now().Add(6 * time.Hour).UTC().Format("2006-01-02T15:04:05.000Z")}
	var osSrv *httptest.Server
	osSrv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/subtitles":
			f.searches.Add(1)
			io.WriteString(w, `{"data":[
			 {"id":"1","attributes":{"language":"tr","moviehash_match":true,"download_count":50,"release":"Movie.2020.1080p.WEB",
			  "files":[{"file_id":1,"file_name":"a.srt"}]}},
			 {"id":"2","attributes":{"language":"tr","download_count":10,"release":"Movie.2020.720p",
			  "files":[{"file_id":2,"file_name":"b.srt"}]}}]}`)
		case "/download":
			f.downloads.Add(1)
			if f.slow.Load() {
				time.Sleep(150 * time.Millisecond)
			}
			var body map[string]int
			json.NewDecoder(r.Body).Decode(&body)
			if f.quota406.Load() || body["file_id"] == 2 {
				w.WriteHeader(http.StatusNotAcceptable)
				fmt.Fprintf(w, `{"requests":6,"remaining":-1,"message":"You have downloaded your allowed 5 subtitles for 24h","reset_time_utc":%q}`, f.reset)
				return
			}
			json.NewEncoder(w).Encode(map[string]any{"link": osSrv.URL + "/file/1", "file_name": "a.srt", "remaining": 3})
		case "/file/1":
			io.WriteString(w, sampleSRT)
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(osSrv.Close)
	sdl := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, `{"status":true,"subtitles":[{"release_name":"Movie.2020.WEB","language":"TR","url":"/subtitle/1-2.zip"}]}`)
	}))
	t.Cleanup(sdl.Close)

	f.svc = NewService(nil, Config{CacheDir: t.TempDir(), OpenSubtitlesURL: osSrv.URL, SubDLAPIURL: sdl.URL, SubDLDownloadURL: sdl.URL})
	if err := f.svc.settings.save(Settings{OpenSubtitlesAPIKey: "K", SubDLAPIKey: "S", DefaultLangs: "tr"}); err != nil {
		t.Fatal(err)
	}
	gin.SetMode(gin.TestMode)
	f.router = gin.New()
	Register(f.router.Group("/api"), f.svc)
	return f
}

func (f *quotaFixture) list(t *testing.T, refresh bool) map[string]any {
	t.Helper()
	u := "/api/subtitles?tmdb_id=10&media_type=movie&langs=tr"
	if refresh {
		u += "&refresh=1"
	}
	w := httptest.NewRecorder()
	f.router.ServeHTTP(w, httptest.NewRequest(http.MethodGet, u, nil))
	if w.Code != http.StatusOK {
		t.Fatalf("list: %d %s", w.Code, w.Body)
	}
	var out map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	return out
}

func (f *quotaFixture) file(t *testing.T, id string) (int, map[string]any) {
	t.Helper()
	w := httptest.NewRecorder()
	f.router.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/api/subtitles/file?id="+id, nil))
	var body map[string]any
	_ = json.Unmarshal(w.Body.Bytes(), &body)
	return w.Code, body
}

type quotaItem struct {
	ID     string `json:"id"`
	Source string `json:"source"`
	Quota  bool   `json:"quota"`
}

func quotaItems(t *testing.T, out map[string]any) []quotaItem {
	t.Helper()
	b, _ := json.Marshal(out["items"])
	var items []quotaItem
	if err := json.Unmarshal(b, &items); err != nil {
		t.Fatal(err)
	}
	return items
}

func TestOpenSubtitlesQuotaRanksLastAndAnswersAtOnce(t *testing.T) {
	f := newQuotaFixture(t)
	items := quotaItems(t, f.list(t, false))
	if len(items) != 3 || items[0].Source != SourceOpenSubtitles || items[0].Quota {
		t.Fatalf("before the quota: %+v", items)
	}
	osSynced := items[0].ID
	var osOther string
	for _, it := range items {
		if it.Source == SourceOpenSubtitles && it.ID != osSynced {
			osOther = it.ID
		}
	}

	// The chosen file downloads once; the other one hits the quota (406)
	if code, _ := f.file(t, osSynced); code != http.StatusOK {
		t.Fatalf("first file: %d", code)
	}
	code, body := f.file(t, osOther)
	if code != http.StatusTooManyRequests || body["quota"] != true || body["reset_at"] == nil {
		t.Fatalf("quota answer: %d %v", code, body)
	}
	reset, err := time.Parse(time.RFC3339, body["reset_at"].(string))
	if err != nil || reset.Before(time.Now().Add(5*time.Hour)) {
		t.Fatalf("reset_at %v (%v), want reset_time_utc of the answer", body["reset_at"], err)
	}
	if n := f.downloads.Load(); n != 2 {
		t.Fatalf("%d downloads, want 2", n)
	}

	// During the window: OpenSubtitles last with quota=true, no download is asked for, a cached file still serves
	out := f.list(t, false)
	if out["opensubtitles_quota_until"] == nil {
		t.Fatalf("list misses opensubtitles_quota_until: %v", out)
	}
	items = quotaItems(t, out)
	if len(items) != 3 || items[0].Source != SourceSubDL || items[0].Quota || !items[1].Quota || !items[2].Quota ||
		items[1].ID != osSynced {
		t.Fatalf("during the quota: %+v", items)
	}
	if code, body := f.file(t, osOther); code != http.StatusTooManyRequests || body["quota"] != true {
		t.Fatalf("blocked file: %d %v", code, body)
	}
	if code, _ := f.file(t, osSynced); code != http.StatusOK {
		t.Fatalf("cached file during the quota: %d", code)
	}
	if n := f.downloads.Load(); n != 2 {
		t.Fatalf("quota window still called OpenSubtitles: %d downloads", n)
	}

	// After the reset everything is back to normal
	f.svc.os.mu.Lock()
	f.svc.os.dlBlocked = time.Now().Add(-time.Second)
	f.svc.os.mu.Unlock()
	items = quotaItems(t, f.list(t, false))
	if items[0].Source != SourceOpenSubtitles || items[0].Quota {
		t.Fatalf("after the reset: %+v", items)
	}
	if code, _ := f.file(t, osOther); code != http.StatusTooManyRequests || f.downloads.Load() != 3 {
		t.Fatalf("after the reset the download is asked again: %d, %d downloads", code, f.downloads.Load())
	}
}

func TestOpenSubtitlesLastDownloadBlocksTheNext(t *testing.T) {
	var downloads atomic.Int32
	var srv *httptest.Server
	srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/download":
			downloads.Add(1)
			json.NewEncoder(w).Encode(map[string]any{"link": srv.URL + "/f", "file_name": "a.srt", "remaining": 0})
		case "/f":
			io.WriteString(w, sampleSRT)
		}
	}))
	defer srv.Close()
	o := newOpenSubtitles(srv.URL, testClient())
	st := Settings{OpenSubtitlesAPIKey: "K"}
	if _, _, err := o.fetch(context.Background(), "1|a.srt", st); err != nil {
		t.Fatal(err)
	}
	var qe QuotaError
	if _, _, err := o.fetch(context.Background(), "2|b.srt", st); !errors.As(err, &qe) || !errors.Is(err, ErrRateLimited) {
		t.Fatalf("want a quota error, got %v", err)
	}
	if downloads.Load() != 1 || o.quotaUntil().IsZero() {
		t.Fatalf("remaining 0 did not block: %d downloads", downloads.Load())
	}
}

// Logging in with an account after the bare key's quota ran out lets downloads through again;
// a change elsewhere keeps the block.
func TestNewOpenSubtitlesLoginClearsTheQuota(t *testing.T) {
	f := newQuotaFixture(t)
	f.svc.os.blockDownloads(f.reset)
	if _, err := f.svc.UpdateSettings(SettingsInput{DefaultLangs: &[]string{"tr", "en"}}); err != nil {
		t.Fatal(err)
	}
	if f.svc.OpenSubtitlesQuotaUntil().IsZero() {
		t.Fatal("a languages change cleared the quota")
	}
	var in SettingsInput
	if err := json.Unmarshal([]byte(`{"opensubtitles":{"username":"me","password":"pw"}}`), &in); err != nil {
		t.Fatal(err)
	}
	if _, err := f.svc.UpdateSettings(in); err != nil {
		t.Fatal(err)
	}
	if until := f.svc.OpenSubtitlesQuotaUntil(); !until.IsZero() {
		t.Fatalf("still blocked until %v after a login change", until)
	}
}

func TestOpenSubtitles429QuotaBody(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusTooManyRequests)
		io.WriteString(w, `{"message":"Download quota exceeded","remaining":0}`)
	}))
	defer srv.Close()
	o := newOpenSubtitles(srv.URL, testClient())
	var qe QuotaError
	if _, _, err := o.fetch(context.Background(), "1|a.srt", Settings{OpenSubtitlesAPIKey: "K"}); !errors.As(err, &qe) {
		t.Fatalf("want a quota error, got %v", err)
	}
	if d := time.Until(qe.Until); d < 50*time.Minute || d > 61*time.Minute {
		t.Fatalf("fallback window %s", d)
	}
}

func TestSubtitleFileDownloadsOnce(t *testing.T) {
	f := newQuotaFixture(t)
	items := quotaItems(t, f.list(t, false))
	id := items[0].ID
	f.slow.Store(true)

	// Concurrent requests for one id share a single download
	var wg sync.WaitGroup
	codes := make([]int, 5)
	for i := range codes {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			codes[i], _ = f.file(t, id)
		}(i)
	}
	wg.Wait()
	for _, c := range codes {
		if c != http.StatusOK {
			t.Fatalf("codes %v", codes)
		}
	}
	// Later requests and late-list refreshes are served from the cache
	if code, _ := f.file(t, id); code != http.StatusOK {
		t.Fatal(code)
	}
	searches := f.searches.Load()
	f.list(t, true)
	if f.searches.Load() != searches+1 {
		t.Fatalf("refresh did not ask the provider again")
	}
	if code, _ := f.file(t, id); code != http.StatusOK {
		t.Fatal(code)
	}
	if n := f.downloads.Load(); n != 1 {
		t.Fatalf("%d downloads of one file, want 1", n)
	}
	if strings.Contains(fmt.Sprint(f.list(t, false)), "opensubtitles_quota_until") {
		t.Fatal("quota reported without a quota answer")
	}
}

func TestDemoteQuotaKeepsLanguageGroups(t *testing.T) {
	in := []Sub{
		{ID: "o1", Source: SourceOpenSubtitles, Language: "tr"},
		{ID: "s1", Source: SourceSubDL, Language: "tr"},
		{ID: "o2", Source: SourceOpenSubtitles, Language: "tr"},
		{ID: "t1", Source: SourceTurkceAltyazi, Language: "tr"},
		{ID: "o3", Source: SourceOpenSubtitles, Language: "en"},
		{ID: "e1", Source: SourceEmbedded, Language: "en"},
	}
	got := demoteQuota(in)
	var ids []string
	for _, s := range got {
		ids = append(ids, s.ID)
		if s.Quota != (s.Source == SourceOpenSubtitles) {
			t.Fatalf("quota flag %+v", s)
		}
	}
	if strings.Join(ids, ",") != "s1,t1,o1,o2,e1,o3" {
		t.Fatalf("order %v", ids)
	}
}
