package subtitles

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"golang.org/x/text/encoding/charmap"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

func fixture(t *testing.T, name string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("testdata", name))
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func TestSubsceneSlug(t *testing.T) {
	cases := map[string]string{
		"The Mentalist":          "the-mentalist",
		"Grey's Anatomy":         "greys-anatomy",
		"Mr. Robot":              "mr-robot",
		"Amélie":                 "amelie",
		"Law & Order: SVU":       "law-order-svu",
		"Çok Güzel Hareketler 2": "cok-guzel-hareketler-2",
	}
	for in, want := range cases {
		if got := subsceneSlug(in); got != want {
			t.Errorf("subsceneSlug(%q) = %q, want %q", in, got, want)
		}
	}
	if got := subsceneSlug("進撃の巨人"); got != "" {
		t.Errorf("non-Latin name gave %q", got)
	}
}

func TestSubf2mSlugs(t *testing.T) {
	tv := subf2mSlugs(Query{MediaType: "tv", Season: 2}, subf2mTitle{Names: []string{"The Mentalist"}})
	if strings.Join(tv, ",") != "the-mentalist-second-season,the-mentalist-season-2" {
		t.Errorf("tv slugs %v", tv)
	}
	mv := subf2mSlugs(Query{MediaType: "movie"}, subf2mTitle{Names: []string{"Inception", "Inception"}, Year: 2010})
	if strings.Join(mv, ",") != "inception-2010,inception" {
		t.Errorf("movie slugs %v", mv)
	}
	many := subf2mSlugs(Query{MediaType: "movie"}, subf2mTitle{Names: []string{"Money Heist", "La casa de papel", "x y"}, Year: 2017})
	if len(many) != subf2mMaxProbes {
		t.Errorf("probes not capped: %v", many)
	}
}

func TestParseSubf2mList(t *testing.T) {
	rows := parseSubf2mList(fixture(t, "subf2m_mentalist_s2_english.html"))
	if len(rows) != 5 {
		t.Fatalf("got %d rows", len(rows))
	}
	r := rows[0]
	if r.Slug != "the-mentalist-second-season" || r.LangPath != "english" || r.ID != "1456917" || len(r.Releases) != 7 ||
		r.Quality != 2 || r.Comment != "Complete Second Season" || r.Author == "" || r.HI {
		t.Errorf("row 0: %+v", r)
	}
	if rows[1].Quality != 4 || rows[2].Releases[0] != "The.Mentalist.S02E10.PROPER.720p.HDTV.x264-CTU" || rows[2].HI {
		t.Errorf("rows 1-2: %+v %+v", rows[1], rows[2])
	}
	if !rows[3].HI || !rows[4].HI {
		t.Error(".HI releases not flagged")
	}
	if !subf2mMatches(fixture(t, "subf2m_inception_turkish.html"), Query{MediaType: "movie"}, subf2mTitle{IMDb: "tt1375666"}) {
		t.Error("IMDb id not matched")
	}
	if subf2mMatches(fixture(t, "subf2m_inception_turkish.html"), Query{MediaType: "movie"}, subf2mTitle{Year: 1999}) {
		t.Error("year mismatch accepted")
	}
}

func TestEpisodeNamed(t *testing.T) {
	cases := []struct {
		name         string
		named, match bool
	}{
		{"The.Mentalist.S02E10.HDTV.XviD-NoTV", true, true},
		{"The.Mentalist.S02E09.HDTV", true, false},
		{"Show.S02E09E10.720p", true, true},
		{"show 2x10 hdtv", true, true},
		{"The.Mentalist.S02.DVDRip.XviD-NODLABS", false, false},
		{"The.Mentalist.PROPER.HDTV.XviD-2HD", false, false},
	}
	for _, c := range cases {
		named, match := episodeNamed(c.name, 2, 10)
		if named != c.named || match != c.match {
			t.Errorf("%q: named %v match %v", c.name, named, match)
		}
	}
}

// subf2mSite fakes subf2m.co with the saved pages; downloads redirect to a CDN path like the site.
func subf2mSite(t *testing.T, requests *int32) *httptest.Server {
	trPage := fixture(t, "subf2m_mentalist_s2_turkish.html")
	enPage := fixture(t, "subf2m_mentalist_s2_english.html")
	movie := fixture(t, "subf2m_inception_turkish.html")
	pack := makeZip(t, map[string]string{
		"The.Mentalist.S02E09.A.Price.Above.Rubies.WEBRip.Amazon.tr-tr.srt": "1\n00:00:01,000 --> 00:00:02,000\nYanlış bölüm\n",
		"The.Mentalist.S02E10.Throwing.Fire.WEBRip.Amazon.tr-tr.srt":        sampleSRT,
	})
	cp1254, _ := charmap.Windows1254.NewEncoder().String(sampleSRT)
	single := makeZip(t, map[string]string{"Inception.2010.BRRip.720p.XviD.AC3-KiNGS.srt": cp1254})
	episode := makeZip(t, map[string]string{"The.Mentalist.S02E10.PROPER.720p.HDTV.x264-CTU.srt": "1\n00:00:01,000 --> 00:00:02,000\nHello there\n"})
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(requests, 1)
		if strings.HasPrefix(r.URL.Path, "/subtitles/inception/turkish/") && strings.HasSuffix(r.URL.Path, "/download") {
			w.Write(single)
			return
		}
		switch r.URL.Path {
		case "/subtitles/the-mentalist-second-season/turkish":
			io.WriteString(w, trPage)
		case "/subtitles/the-mentalist-second-season/english":
			io.WriteString(w, enPage)
		case "/subtitles/inception/turkish":
			io.WriteString(w, movie)
		case "/subtitles/the-mentalist-second-season/turkish/3578444/download":
			if r.Header.Get("Referer") == "" {
				t.Error("download without Referer")
			}
			http.Redirect(w, r, "/cdn/the-mentalist-season-2.zip?md5=x", http.StatusFound)
		case "/cdn/the-mentalist-season-2.zip":
			w.Write(pack)
		case "/subtitles/the-mentalist-second-season/english/331240/download":
			w.Write(episode)
		default:
			http.NotFound(w, r)
		}
	}))
}

func TestSubf2mFlow(t *testing.T) {
	var requests int32
	srv := subf2mSite(t, &requests)
	defer srv.Close()
	f := newSubf2m(srv.URL, diskCache{dir: t.TempDir()})
	f.gate.gap = 0
	ctx := context.Background()

	show := subf2mTitle{IMDb: "tt1196946", Names: []string{"The Mentalist"}, Year: 2008}
	q := Query{TmdbID: 5920, MediaType: "tv", Season: 2, Episode: 10, Langs: []string{"tr", "en"}}
	items, err := f.search(ctx, q, show)
	if err != nil {
		t.Fatal(err)
	}
	var tr, en, packs int
	for _, it := range items {
		switch it.Language {
		case "tr":
			tr++
		case "en":
			en++
		}
		if it.Pack {
			packs++
		}
		if strings.Contains(it.Release, "S02E09") {
			t.Errorf("another episode listed: %q", it.Release)
		}
	}
	if tr != 1 || en != 4 || packs != 3 || !items[0].Pack || items[0].Source != SourceSubf2m {
		t.Fatalf("tr %d en %d packs %d: %+v", tr, en, packs, items)
	}

	// The second search comes from the slug cache and the disk pages
	n := atomic.LoadInt32(&requests)
	if _, err := f.search(ctx, q, show); err != nil || atomic.LoadInt32(&requests) != n {
		t.Fatalf("pages not cached (%v)", err)
	}

	// The Turkish season pack: S02E10 is picked from the zip behind the CDN redirect
	_, lang, ref, err := decodeID(items[0].ID)
	if err != nil {
		t.Fatal(err)
	}
	raw, name, err := f.fetch(ctx, ref, lang)
	if err != nil || !strings.Contains(name, "S02E10") || !strings.Contains(string(raw), "Merhaba") {
		t.Fatalf("pack fetch: %v %q", err, name)
	}
	for _, it := range items {
		if it.Release == "The.Mentalist.S02E10.PROPER.720p.HDTV.x264-CTU" {
			_, lang, ref, _ := decodeID(it.ID)
			if raw, _, err := f.fetch(ctx, ref, lang); err != nil || !strings.Contains(string(raw), "Hello") {
				t.Fatalf("episode fetch: %v", err)
			}
		}
	}

	// Movie: "<name>-<year>" is missing (404), the plain name is the right title
	mq := Query{TmdbID: 27205, MediaType: "movie", Langs: []string{"tr"}}
	movie, err := f.search(ctx, mq, subf2mTitle{IMDb: "tt1375666", Names: []string{"Inception"}, Year: 2010})
	if err != nil || len(movie) != 6 {
		t.Fatalf("movie: %v %d", err, len(movie))
	}
	var kings candidate
	forced := 0
	for _, it := range movie {
		if strings.HasSuffix(it.Release, "-KiNGS") {
			kings = it
		}
		if it.Forced {
			forced++
			if _, _, ref, _ := decodeID(it.ID); !strings.Contains(ref, "|1544196|") {
				t.Errorf("flagged forced: %+v", it)
			}
		}
	}
	if forced != 1 {
		t.Errorf("%d forced subtitles, want 1 (\"forced BluRay altyazı\", not \"Forced altyazı dahildir\")", forced)
	}
	_, lang, ref, _ = decodeID(kings.ID)
	raw, name, err = f.fetch(ctx, ref, lang)
	if err != nil || !strings.HasSuffix(name, ".srt") {
		t.Fatalf("movie fetch: %v %q", err, name)
	}
	vtt, err := toWebVTTLang(raw, name, lang)
	if err != nil || !strings.Contains(string(vtt), "şöyle güzel") {
		t.Fatalf("cp1254 not repaired: %v %q", err, vtt)
	}

	// Another title with the same name: the IMDb id does not match, nothing is listed and the
	// miss is remembered
	other := Query{TmdbID: 1, MediaType: "movie", Langs: []string{"tr"}}
	none, err := f.search(ctx, other, subf2mTitle{IMDb: "tt0000001", Names: []string{"Inception"}})
	if err != nil || len(none) != 0 {
		t.Fatalf("wrong title listed: %v %+v", err, none)
	}
	n = atomic.LoadInt32(&requests)
	if _, err := f.search(ctx, other, subf2mTitle{IMDb: "tt0000001", Names: []string{"Inception"}}); err != nil || atomic.LoadInt32(&requests) != n {
		t.Fatal("miss not remembered")
	}

	for _, bad := range []string{"../x|turkish|1|0|0|0", "inception|turkish|abc|0|0|0", "inception|turkish|1|0|0"} {
		if _, _, err := f.fetch(ctx, bad, "tr"); err == nil {
			t.Errorf("bad ref %q accepted", bad)
		}
	}
}

func TestParseYIFYList(t *testing.T) {
	rows := parseYIFYList(fixture(t, "yify_inception.html"))
	if len(rows) != 5 {
		t.Fatalf("got %d rows", len(rows))
	}
	if rows[0].Lang != "ar" || len(rows[0].Releases) != 4 || rows[0].Rating != 44 || rows[0].Uploader != "ilyass derdabi" {
		t.Errorf("row 0: %+v", rows[0])
	}
	if rows[1].Lang != "zh" || rows[1].Rating != -1 {
		t.Errorf("row 1: %+v", rows[1])
	}
	if rows[3].Lang != "tr" || rows[3].Slug != "inception-2010-turkish-yify-392342" ||
		rows[3].Releases[0] != "Inception.2010.BRRip.720p.XviD.AC3-KiNGS" || rows[3].Uploader != "erlat" || rows[3].HI {
		t.Errorf("row 3: %+v", rows[3])
	}
	if rows[4].Lang != "uk" || !rows[4].HI {
		t.Errorf("row 4: %+v", rows[4])
	}
}

func TestYIFYFlow(t *testing.T) {
	page := fixture(t, "yify_inception.html")
	zipped := makeZip(t, map[string]string{"Inception.2010.BRRip.720p.XviD.AC3-KiNGS.srt": sampleSRT})
	var requests int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&requests, 1)
		switch r.URL.Path {
		case "/movie-imdb/tt1375666":
			io.WriteString(w, page)
		case "/subtitle/inception-2010-turkish-yify-392342.zip":
			w.Write(zipped)
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()
	y := newYIFY(srv.URL, diskCache{dir: t.TempDir()})
	y.gate.gap = 0
	ctx := context.Background()

	q := Query{TmdbID: 27205, MediaType: "movie", Langs: []string{"tr", "en"}}
	items, err := y.search(ctx, q, "tt1375666")
	if err != nil || len(items) != 2 {
		t.Fatalf("search: %v %+v", err, items)
	}
	if items[1].Language != "tr" || items[1].Source != SourceYIFY || items[0].Quality != 2 {
		t.Errorf("items %+v", items)
	}
	_, lang, ref, _ := decodeID(items[1].ID)
	raw, name, err := y.fetch(ctx, ref, lang)
	if err != nil || !strings.Contains(string(raw), "Merhaba") || !strings.HasSuffix(name, ".srt") {
		t.Fatalf("fetch: %v %q", err, name)
	}

	n := atomic.LoadInt32(&requests)
	if items, err := y.search(ctx, Query{MediaType: "tv", Season: 1, Episode: 1, Langs: []string{"tr"}}, "tt1375666"); err != nil || items != nil || atomic.LoadInt32(&requests) != n {
		t.Fatal("TV must not be asked")
	}
	if items, err := y.search(ctx, q, "tt0000001"); err != nil || len(items) != 0 {
		t.Fatalf("unknown movie: %v %+v", err, items)
	}
	if _, _, err := y.fetch(ctx, "../../etc", "tr"); err == nil {
		t.Fatal("bad ref accepted")
	}
}

func TestListWithScrapedProviders(t *testing.T) {
	var requests int32
	site := subf2mSite(t, &requests)
	defer site.Close()
	broken := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer broken.Close()

	svc := NewService(nil, Config{
		CacheDir:  t.TempDir(),
		Subf2mURL: site.URL,
		YIFYURL:   broken.URL,
		IMDb:      func(string, int) (string, error) { return "tt1375666", nil },
		Titles: func(string, int) (TitleInfo, error) {
			return TitleInfo{Names: []string{"Inception"}, Year: 2010}, nil
		},
	})
	svc.sf.gate.gap = 0
	svc.yts.gate.gap = 0
	svc.wait = 3 * time.Second
	if err := svc.settings.save(Settings{Subf2mEnabled: true, YIFYEnabled: true, DefaultLangs: "tr"}); err != nil {
		t.Fatal(err)
	}
	items := svc.List(context.Background(), Query{TmdbID: 27205, MediaType: "movie"})
	if len(items) != 6 || items[0].Source != SourceSubf2m {
		t.Fatalf("a failing provider broke the list: %+v", items)
	}
	vtt, err := svc.File(context.Background(), items[0].ID)
	if err != nil || !strings.HasPrefix(string(vtt), "WEBVTT") {
		t.Fatalf("file: %v", err)
	}

	// Turned off: neither listed nor downloaded
	off := false
	if _, err := svc.UpdateSettings(SettingsInput{Subf2m: &struct {
		Enabled *bool `json:"enabled"`
	}{&off}}); err != nil {
		t.Fatal(err)
	}
	if items := svc.List(context.Background(), Query{TmdbID: 27205, MediaType: "movie"}); len(items) != 0 {
		t.Fatalf("disabled provider listed: %+v", items)
	}
	svc.files = diskCache{dir: t.TempDir()}
	if _, err := svc.File(context.Background(), items[1].ID); err == nil {
		t.Fatal("disabled provider downloaded")
	}
}

func TestScrapedProviderSettingsMigration(t *testing.T) {
	db, err := gorm.Open(sqlite.Open(filepath.Join(t.TempDir(), "s.db")), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	if err != nil {
		t.Fatal(err)
	}
	// A table from before the new columns, with its row
	if err := db.Exec(`CREATE TABLE subtitle_settings (id integer PRIMARY KEY, opensubtitles_api_key text,
		turkcealtyazi_enabled numeric, default_langs text, updated_at datetime)`).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Exec(`INSERT INTO subtitle_settings (id, opensubtitles_api_key, turkcealtyazi_enabled, default_langs) VALUES (1, 'K', 0, 'tr,en')`).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.AutoMigrate(Models()...); err != nil {
		t.Fatal(err)
	}
	svc := NewService(db, Config{CacheDir: t.TempDir()})
	v := svc.Settings()
	if !v.Subf2m.Enabled || !v.YIFY.Enabled || v.TurkceAltyazi.Enabled || !v.OpenSubtitles.APIKeySet {
		t.Fatalf("migrated view %+v", v)
	}

	var in SettingsInput
	if err := json.Unmarshal([]byte(`{"yifysubtitles":{"enabled":false}}`), &in); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.UpdateSettings(in); err != nil {
		t.Fatal(err)
	}
	st, err := newSettingsRepo(db).load() // a fresh read from the table
	if err != nil || st.YIFYEnabled || !st.Subf2mEnabled || st.OpenSubtitlesAPIKey != "K" {
		t.Fatalf("saved %+v (%v)", st, err)
	}
	out, _ := json.Marshal(st.view())
	if !strings.Contains(string(out), `"subf2m":{"enabled":true}`) || !strings.Contains(string(out), `"yifysubtitles":{"enabled":false}`) {
		t.Fatalf("view json %s", out)
	}
}
