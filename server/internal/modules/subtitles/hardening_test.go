package subtitles

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"golang.org/x/text/encoding/charmap"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

// smallFileTorrServer answers the stat of a 100 MB file (whole extraction is practical).
func smallFileTorrServer(t *testing.T) *httptest.Server {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, `{"file_stats":[{"id":1,"path":"Show.S01E01.mkv","length":104857600}]}`)
	}))
	t.Cleanup(ts.Close)
	return ts
}

func shortWaits(t *testing.T) {
	savedLive, savedEmb := liveFirstWait, embeddedWait
	liveFirstWait, embeddedWait = 10*time.Millisecond, 50*time.Millisecond
	t.Cleanup(func() { liveFirstWait, embeddedWait = savedLive, savedEmb })
}

func TestSlowExtractionFallsBackToLive(t *testing.T) {
	shortWaits(t)
	svc := NewService(nil, Config{CacheDir: t.TempDir(), TorrServerURL: smallFileTorrServer(t).URL})
	reads := &fakeReads{}
	svc.live.start = reads.start
	release := make(chan struct{})
	full := []byte("WEBVTT\n\n00:00:01.000 --> 00:00:02.000\nTam\n\n")
	var limit atomic.Int64
	svc.extract = func(ctx context.Context, _, _ string, _, _ int) ([]byte, error) {
		if d, ok := ctx.Deadline(); ok {
			limit.Store(int64(time.Until(d)))
		}
		select {
		case <-release:
			return full, nil
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	id := encodeID(SourceEmbedded, "tr", liveHash+"|1|4")
	vtt, partial, err := svc.FileAt(context.Background(), id, 600)
	if err != nil || !partial || reads.count() != 1 || !strings.Contains(string(vtt), PartialNote) {
		t.Fatalf("slow extraction: err=%v partial=%v reads=%d", err, partial, reads.count())
	}
	if l := time.Duration(limit.Load()); l < 5*time.Minute {
		t.Fatalf("extraction runs under a %s limit", l)
	}
	// The extraction goes on and its result wins once cached
	close(release)
	waitUntil(t, func() bool { return svc.hasFile(id) })
	vtt, partial, err = svc.FileAt(context.Background(), id, 600)
	if err != nil || partial || string(vtt) != string(full) {
		t.Fatalf("after extraction: err=%v partial=%v\n%s", err, partial, vtt)
	}
}

func TestFailedExtractionFallsBackToLive(t *testing.T) {
	shortWaits(t)
	svc := NewService(nil, Config{CacheDir: t.TempDir(), TorrServerURL: smallFileTorrServer(t).URL})
	reads := &fakeReads{}
	svc.live.start = reads.start
	svc.extract = func(context.Context, string, string, int, int) ([]byte, error) {
		return nil, errors.New("ffmpeg failed")
	}
	_, partial, err := svc.FileAt(context.Background(), encodeID(SourceEmbedded, "tr", liveHash+"|1|4"), 0)
	if err != nil || !partial || reads.count() != 1 {
		t.Fatalf("err=%v partial=%v reads=%d", err, partial, reads.count())
	}
}

func TestSubSourceDoesNotServeAnotherEpisode(t *testing.T) {
	pack := makeZip(t, map[string]string{"Show.S01E01.srt": sampleSRT, "Show.S01E02.srt": sampleSRT})
	single := makeZip(t, map[string]string{"show.srt": sampleSRT})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/subtitles/1/download":
			w.Write(pack)
		case "/subtitles/2/download":
			w.Write(single)
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()
	s := newSubSource(srv.URL, testClient())
	st := Settings{SubSourceAPIKey: "SS"}
	if b, name, err := s.fetch(context.Background(), "1|1|3", "tr", st); err == nil {
		t.Fatalf("episode 3 served from a pack of 1 and 2: %s (%d bytes)", name, len(b))
	}
	if _, name, err := s.fetch(context.Background(), "1|1|2", "tr", st); err != nil || name != "Show.S01E02.srt" {
		t.Fatalf("episode 2: %q %v", name, err)
	}
	if _, name, err := s.fetch(context.Background(), "2|1|3", "tr", st); err != nil || name != "show.srt" {
		t.Fatalf("single file without an episode name: %q %v", name, err)
	}
}

func TestHugeCueTimes(t *testing.T) {
	if _, ok := parseClock("99:59:59,999"); !ok {
		t.Fatal("99 h must parse")
	}
	for _, s := range []string{"101:00:00,000", "999999999:00:00", "0:9999999:00.00"} {
		if _, ok := parseClock(s); ok {
			t.Errorf("%s parsed", s)
		}
	}
	srt := "1\n00:00:01,000 --> 00:00:02,000\nBir\n\n2\n999:00:00,000 --> 999:00:01,000\nBozuk\n\n" +
		"3\n123456789:00:00,000 --> 123456789:00:01,000\nBozuk\n"
	vtt, err := ToWebVTT([]byte(srt), "x.srt")
	if err != nil || len(vttCues(vtt)) != 1 {
		t.Fatalf("err=%v\n%s", err, vtt)
	}

	// A cue at 99:59:59 is dropped before the timing; the work stays that of the clean subtitle
	file := dialogue(5, episode)
	sub := subtitleFor(file, 1, func(float64) float64 { return 2 })
	windows := truthWindows(file, windowStarts(episode, syncWindowLen, 10), 0.1, 0, 1)
	alloc := func(spans []span) uint64 {
		var a, b runtime.MemStats
		runtime.ReadMemStats(&a)
		if _, ok := align(clampSpans(spans, episode), windows); !ok {
			t.Fatal("no fit")
		}
		runtime.ReadMemStats(&b)
		return b.TotalAlloc - a.TotalAlloc
	}
	clean := alloc(sub)
	junk := alloc(append(append([]span(nil), sub...), span{359999, 360001}))
	if junk > 2*clean {
		t.Fatalf("junk cue: %d MB, clean %d MB", junk>>20, clean>>20)
	}
	if got := clampSpans([]span{{1, 2}, {episode + 30, 1e9}, {1e6, 1e6 + 1}}, episode); len(got) != 2 || got[1].end != episode+60 {
		t.Fatalf("clamp %v", got)
	}
	if got := clampSpans([]span{{1, 2}, {7 * 3600, 7*3600 + 1}}, 0); len(got) != 1 {
		t.Fatalf("clamp without a length %v", got)
	}
}

func TestSettingsDBErrorKeepsStoredKeys(t *testing.T) {
	db, err := gorm.Open(sqlite.Open(filepath.Join(t.TempDir(), "s.db")), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	if err != nil {
		t.Fatal(err)
	}
	svc := NewService(db, Config{CacheDir: t.TempDir()})
	// No table yet: a read error, not a missing row
	if st := svc.Settings(); !st.TurkceAltyazi.Enabled {
		t.Fatalf("defaults expected: %+v", st)
	}
	var in SettingsInput
	if err := json.Unmarshal([]byte(`{"subdl":{"api_key":"NEW"}}`), &in); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.UpdateSettings(in); err == nil {
		t.Fatal("update must fail while the row cannot be read")
	}
	if err := db.AutoMigrate(Models()...); err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&Settings{ID: settingsID, OpenSubtitlesAPIKey: "OS", OpenSubtitlesPassword: "PW", SubSourceAPIKey: "SS", DefaultLangs: "tr"}).Error; err != nil {
		t.Fatal(err)
	}
	st := svc.settings.get()
	if st.OpenSubtitlesAPIKey != "OS" || st.OpenSubtitlesPassword != "PW" || st.SubSourceAPIKey != "SS" {
		t.Fatalf("the defaults were cached: %+v", st)
	}
}

func TestLegacyCharsetsByLanguage(t *testing.T) {
	cases := []struct {
		lang string
		cm   *charmap.Charmap
		text string
	}{
		{"ru", charmap.Windows1251, "Привет, как дела? Всё хорошо, спасибо."},
		{"uk", charmap.Windows1251, "Добрий вечір, як справи?"},
		{"el", charmap.Windows1253, "Καλημέρα, τι κάνεις; Άντε πάμε."},
		{"el", charmap.ISO8859_7, "Καλημέρα, τι κάνεις; Άντε πάμε."},
		{"pl", charmap.Windows1250, "Zażółć gęślą jaźń, proszę. Śmiało!"},
		{"cs", charmap.ISO8859_2, "Příliš žluťoučký kůň úpěl ďábelské ódy."},
		{"ar", charmap.Windows1256, "مرحبا، كيف حالك؟"},
		{"fa", charmap.Windows1256, "سلام، چه خبر؟"},
		{"he", charmap.Windows1255, "שלום, מה שלומך?"},
		{"tr", charmap.Windows1254, "Şişli'de güzel bir gün, ığdır."},
		{"", charmap.ISO8859_9, "Şişli'de güzel bir gün, ığdır."},
		{"de", charmap.Windows1252, "Grüße aus München, schön!"},
		{"pt-BR", charmap.Windows1252, "Não, obrigação é ação."},
	}
	for _, c := range cases {
		raw, err := c.cm.NewEncoder().Bytes([]byte(c.text))
		if err != nil {
			t.Fatalf("%s: %v", c.lang, err)
		}
		if got, cs := decodeTextLang(raw, c.lang); got != c.text {
			t.Errorf("%s (%s): got %q via %s", c.lang, c.cm, got, cs)
		}
	}
	raw, _ := charmap.Windows1251.NewEncoder().Bytes([]byte("1\r\n00:00:01,000 --> 00:00:02,000\r\nПривет!\r\n"))
	if vtt, err := toWebVTTLang(raw, "x.srt", "ru"); err != nil || !strings.Contains(string(vtt), "Привет!") {
		t.Fatalf("%v\n%s", err, vtt)
	}
}

func TestAlternativesSkipOpenSubtitlesDownloads(t *testing.T) {
	svc := NewService(nil, Config{CacheDir: t.TempDir()})
	osNew := encodeID(SourceOpenSubtitles, "tr", "1")
	osCached := encodeID(SourceOpenSubtitles, "tr", "2")
	sdl := encodeID(SourceSubDL, "tr", "x")
	inner := encodeID(SourceTurkceAltyazi, "tr", "y")
	if err := svc.files.write(SourceOpenSubtitles, SourceOpenSubtitles+"|2", ".vtt", []byte("WEBVTT\n\n")); err != nil {
		t.Fatal(err)
	}
	subs := []Sub{
		{ID: osNew, Source: SourceOpenSubtitles, Language: "tr"},
		{ID: osCached, Source: SourceOpenSubtitles, Language: "tr"},
		{ID: inner, Source: SourceTurkceAltyazi, Language: "tr"},
		{ID: sdl, Source: SourceSubDL, Language: "tr"},
	}
	svc.sync.listed(subs, nil, syncHash, 1)
	got := svc.sync.alternatives(syncHash, 1, inner)
	if len(got) != 2 || got[0] != sdl || got[1] != osCached {
		t.Fatalf("alternatives %v", got)
	}
}

func TestOpenSubtitlesLoginFailureIsRemembered(t *testing.T) {
	var logins atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/login" {
			logins.Add(1)
			w.WriteHeader(http.StatusUnauthorized)
			io.WriteString(w, `{"message":"invalid credentials"}`)
			return
		}
		http.NotFound(w, r)
	}))
	defer srv.Close()
	o := newOpenSubtitles(srv.URL, testClient())
	st := Settings{OpenSubtitlesAPIKey: "K", OpenSubtitlesUsername: "u", OpenSubtitlesPassword: "p"}
	for i := 0; i < 3; i++ {
		if tok, _ := o.login(context.Background(), st); tok != "" {
			t.Fatal("token from a failed login")
		}
	}
	if n := logins.Load(); n != 1 {
		t.Fatalf("%d logins", n)
	}
	st.OpenSubtitlesPassword = "new"
	o.login(context.Background(), st)
	if n := logins.Load(); n != 2 {
		t.Fatalf("new password not tried: %d logins", n)
	}
}

func TestFailedTimingIsNotCached(t *testing.T) {
	gin.SetMode(gin.TestMode)
	svc, _ := newSyncTest(t, dialogue(3, episode))
	svc.sync.probe = func(context.Context, string) (mediaProbe, error) { return mediaProbe{}, errors.New("unreadable") }
	inner := cacheProviderSub(t, svc, "1|a|2|7|1", dialogue(3, episode))
	r := gin.New()
	Register(r.Group("/api"), svc)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/api/subtitles/file?id="+wrapSyncID(inner, syncHash, 1), nil))
	if w.Code != 200 || w.Header().Get("X-Subtitle-Sync") != syncStateFailed || w.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("%d sync=%q cache=%q", w.Code, w.Header().Get("X-Subtitle-Sync"), w.Header().Get("Cache-Control"))
	}
	// A plain file answer is still cached by the browser
	w = httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/api/subtitles/file?id="+inner, nil))
	if w.Code != 200 || !strings.Contains(w.Header().Get("Cache-Control"), "max-age") {
		t.Fatalf("%d cache=%q", w.Code, w.Header().Get("Cache-Control"))
	}
}

func TestCacheSweep(t *testing.T) {
	dir := t.TempDir()
	d := diskCache{dir: dir}
	old := time.Now().Add(-40 * 24 * time.Hour)
	write := func(kind, key string, size int, mod time.Time) string {
		if err := d.write(kind, key, ".vtt", make([]byte, size)); err != nil {
			t.Fatal(err)
		}
		p := d.path(kind, key, ".vtt")
		os.Chtimes(p, mod, mod)
		return p
	}
	stale := write("synced", "a", 10, old)
	fresh := write(SourceOpenSubtitles, "b", 10, time.Now())
	used := write("syncref", "c", 10, old)
	tmp := filepath.Join(dir, "synced", ".tmp-1")
	os.WriteFile(tmp, nil, 0o644)
	os.Chtimes(tmp, time.Now().Add(-2*time.Hour), time.Now().Add(-2*time.Hour))
	other := filepath.Join(dir, "other", "keep.db") // not a cache folder
	os.MkdirAll(filepath.Dir(other), 0o755)
	os.WriteFile(other, nil, 0o644)
	os.Chtimes(other, old, old)

	// A read renews an entry kept forever
	if _, ok := d.read("syncref", "c", ".vtt", 0); !ok {
		t.Fatal("read")
	}
	if n := d.sweep(cacheMaxAge, cacheMaxBytes); n != 2 {
		t.Fatalf("removed %d", n)
	}
	exists := func(p string) bool { _, err := os.Stat(p); return err == nil }
	if exists(stale) || exists(tmp) || !exists(fresh) || !exists(used) || !exists(other) {
		t.Fatalf("stale %v tmp %v fresh %v used %v other %v", exists(stale), exists(tmp), exists(fresh), exists(used), exists(other))
	}
	// Over the size cap the least recently used go first
	older := write(SourceSubDL, "d", 100, time.Now().Add(-time.Hour))
	if n := d.sweep(cacheMaxAge, 50); n != 1 || exists(older) || !exists(fresh) {
		t.Fatalf("size cap: removed %d, older %v", n, exists(older))
	}
}

func TestInputLimitsBeforeInput(t *testing.T) {
	args := withInput([]string{"-v", "error", "-ss", "10"}, "http://x/stream")
	i := len(args) - 2
	if args[i] != "-i" || args[i+1] != "http://x/stream" {
		t.Fatalf("args %v", args)
	}
	joined := strings.Join(args[:i], " ")
	if !strings.Contains(joined, "-protocol_whitelist http,https,tcp,tls") || !strings.Contains(joined, "-format_whitelist matroska") {
		t.Fatalf("args %v", args)
	}
}
