package subtitles

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

const sampleSRT = "1\r\n00:00:01,000 --> 00:00:02,500\r\nMerhaba dünya, şöyle güzel bir gün!\r\n\r\n2\r\n00:00:03,000 --> 00:00:04,000\r\nİyi akşamlar.\r\n"

func makeZip(t *testing.T, files map[string]string) []byte {
	t.Helper()
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	for name, body := range files {
		w, err := zw.Create(name)
		if err != nil {
			t.Fatal(err)
		}
		io.WriteString(w, body)
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

// --- OpenSubtitles ---

func TestOpenSubtitlesSearchAndDownload(t *testing.T) {
	var logins, downloads int32
	var srv *httptest.Server
	srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasPrefix(r.URL.Path, "/file/") && (r.Header.Get("Api-Key") != "KEY" || r.Header.Get("User-Agent") != "Nextflix v1") {
			w.WriteHeader(http.StatusForbidden)
			return
		}
		switch r.URL.Path {
		case "/login":
			atomic.AddInt32(&logins, 1)
			var body map[string]string
			json.NewDecoder(r.Body).Decode(&body)
			if body["username"] != "user" || body["password"] != "pass" {
				w.WriteHeader(http.StatusUnauthorized)
				return
			}
			json.NewEncoder(w).Encode(map[string]any{"token": "JWT", "base_url": "api.opensubtitles.com"})
		case "/subtitles":
			// Parameters must be sorted and lower case, as the API demands.
			if r.URL.RawQuery != "languages=en,tr&moviehash=8e245d9679d31e12&tmdb_id=27205&type=movie" {
				t.Errorf("query %q", r.URL.RawQuery)
			}
			if r.Header.Get("Authorization") != "Bearer JWT" {
				t.Errorf("missing token")
			}
			io.WriteString(w, `{"total_count":2,"data":[
			 {"id":"1","type":"subtitle","attributes":{"language":"tr","download_count":1200,"hearing_impaired":false,
			  "fps":23.976,"votes":4,"ratings":8.5,"from_trusted":true,"ai_translated":false,"machine_translated":false,
			  "moviehash_match":true,"release":"Inception.2010.1080p.BluRay.x264-SPARKS","uploader":{"name":"çevirmen","rank":"trusted"},
			  "files":[{"file_id":111,"cd_number":1,"file_name":"Inception.tr.srt"}]}},
			 {"id":"2","type":"subtitle","attributes":{"language":"en","download_count":5,"hearing_impaired":true,
			  "machine_translated":true,"release":"Inception.WEB","files":[{"file_id":222,"file_name":"x.srt"}]}}]}`)
		case "/download":
			atomic.AddInt32(&downloads, 1)
			var body map[string]int
			json.NewDecoder(r.Body).Decode(&body)
			if body["file_id"] != 111 {
				w.WriteHeader(http.StatusNotAcceptable)
				io.WriteString(w, `{"message":"quota","reset_time_utc":"2099-01-01T00:00:00.000Z"}`)
				return
			}
			json.NewEncoder(w).Encode(map[string]any{"link": srv.URL + "/file/111", "file_name": "Inception.tr.srt", "remaining": 99})
		case "/file/111":
			io.WriteString(w, sampleSRT)
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	o := newOpenSubtitles(srv.URL, testClient())
	st := Settings{OpenSubtitlesAPIKey: "KEY", OpenSubtitlesUsername: "user", OpenSubtitlesPassword: "pass"}
	q := Query{TmdbID: 27205, MediaType: "movie", Langs: []string{"tr", "en"}}
	items, err := o.search(context.Background(), q, st, "8e245d9679d31e12")
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 2 {
		t.Fatalf("got %d items", len(items))
	}
	tr := items[0]
	if tr.Language != "tr" || !tr.Synced || !tr.Trusted || tr.Downloads != 1200 || tr.FPS != 23.976 || tr.Author != "çevirmen" {
		t.Errorf("tr item %+v", tr)
	}
	if en := items[1]; !en.Machine || !en.HI {
		t.Errorf("en item %+v", en)
	}

	source, lang, ref, err := decodeID(tr.ID)
	if err != nil || source != SourceOpenSubtitles || lang != "tr" {
		t.Fatalf("id: %v %s %s", err, source, lang)
	}
	raw, name, err := o.fetch(context.Background(), ref, st)
	if err != nil || name != "Inception.tr.srt" || !strings.Contains(string(raw), "Merhaba") {
		t.Fatalf("fetch: %v %s", err, name)
	}
	if logins != 1 {
		t.Errorf("token not cached: %d logins", logins)
	}

	// 406 blocks further downloads until the reset time without asking again.
	_, _, ref2, _ := decodeID(items[1].ID)
	if _, _, err := o.fetch(context.Background(), ref2, st); !errors.Is(err, ErrRateLimited) {
		t.Fatalf("want rate limit, got %v", err)
	}
	before := atomic.LoadInt32(&downloads)
	if _, _, err := o.fetch(context.Background(), ref, st); !errors.Is(err, ErrRateLimited) {
		t.Fatalf("want blocked, got %v", err)
	}
	if atomic.LoadInt32(&downloads) != before {
		t.Error("blocked download still hit the API")
	}
}

func TestOpenSubtitlesRetryAfter(t *testing.T) {
	var calls int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&calls, 1)
		w.Header().Set("Retry-After", "30")
		w.WriteHeader(http.StatusTooManyRequests)
	}))
	defer srv.Close()
	o := newOpenSubtitles(srv.URL, testClient())
	st := Settings{OpenSubtitlesAPIKey: "KEY"}
	q := Query{TmdbID: 1, MediaType: "tv", Season: 1, Episode: 2, Langs: []string{"tr"}}
	if _, err := o.search(context.Background(), q, st, ""); !errors.Is(err, ErrRateLimited) {
		t.Fatalf("got %v", err)
	}
	if _, err := o.search(context.Background(), q, st, ""); !errors.Is(err, ErrRateLimited) {
		t.Fatalf("got %v", err)
	}
	if calls != 1 {
		t.Fatalf("Retry-After ignored: %d calls", calls)
	}
	if d := retryAfter("120", time.Second); d != 2*time.Minute {
		t.Fatal(d)
	}
}

// --- SubDL ---

func TestSubDLSearchAndDownload(t *testing.T) {
	pack := makeZip(t, map[string]string{
		"Show.S02E04.1080p.WEB.tr.srt": "1\n00:00:01,000 --> 00:00:02,000\nYanlış bölüm\n",
		"Show.S02E05.1080p.WEB.tr.srt": sampleSRT,
		"readme.txt":                   "hi",
	})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/api/v1/subtitles":
			q := r.URL.Query()
			if q.Get("api_key") != "SDL" || q.Get("type") != "tv" || q.Get("languages") != "TR,EN" || q.Get("tmdb_id") != "1399" {
				t.Errorf("query %v", q)
			}
			if q.Get("full_season") == "1" {
				io.WriteString(w, `{"status":true,"subtitles":[
				 {"release_name":"Show.S02.1080p.WEB-DL","lang":"turkish","language":"TR","author":"ali","url":"/subtitle/100-200.zip?api_key=SDL",
				  "season":2,"episode":null,"full_season":true,"episode_from":1,"episode_end":10,"hi":false,"unpack_files":[]},
				 {"release_name":"Show.S02.720p","language":"EN","url":"/subtitle/300-400.zip","season":2,"full_season":true,
				  "unpack_files":[{"file_n_id":"f5","name":"Show.S02E05.720p.en.srt","release_name":"Show.S02E05.720p.HDTV-GRP",
				   "season":2,"episode":5,"language":"EN","hi":true,"format":"srt","url":"/subtitle/300/f5?api_key=SDL"}]}]}`)
				return
			}
			io.WriteString(w, `{"status":true,"subtitles":[
			 {"release_name":"Show.S02E05.1080p.WEB-DL-NTb","language":"TR","url":"/subtitle/500-600.zip","season":2,"episode":5,"hi":false,"fps":"23.976"},
			 {"release_name":"Show.S02E06","language":"TR","url":"/subtitle/700-800.zip","season":2,"episode":6},
			 {"release_name":"evil","language":"TR","url":"https://evil.example/x.zip","season":2,"episode":5}]}`)
		case r.URL.Path == "/subtitle/100-200.zip":
			w.Write(pack)
		case r.URL.Path == "/subtitle/300/f5":
			io.WriteString(w, sampleSRT)
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	d := newSubDL(srv.URL+"/api/v1", srv.URL, testClient())
	q := Query{TmdbID: 1399, MediaType: "tv", Season: 2, Episode: 5, Langs: []string{"tr", "en"}}
	items, err := d.search(context.Background(), q, Settings{SubDLAPIKey: "SDL"})
	if err != nil {
		t.Fatal(err)
	}
	byRelease := map[string]candidate{}
	for _, c := range items {
		byRelease[c.Release] = c
	}
	if len(items) != 3 {
		t.Fatalf("got %d items: %+v", len(items), byRelease)
	}
	for _, c := range items {
		if _, _, ref, _ := decodeID(c.ID); strings.Contains(ref, "api_key") || strings.Contains(ref, "?") {
			t.Errorf("file id keeps the URL query: %q", ref)
		}
	}
	if c := byRelease["Show.S02E05.1080p.WEB-DL-NTb"]; c.FPS != 23.976 || c.Language != "tr" {
		t.Errorf("episode item %+v", c)
	}
	unpacked, ok := byRelease["Show.S02E05.720p.HDTV-GRP"]
	if !ok || !unpacked.HI || unpacked.Language != "en" {
		t.Fatalf("unpacked item missing: %+v", byRelease)
	}

	// The season pack is downloaded as zip and the episode is picked from it.
	_, lang, ref, _ := decodeID(byRelease["Show.S02.1080p.WEB-DL"].ID)
	raw, name, err := d.fetch(context.Background(), ref, lang)
	if err != nil || name != "Show.S02E05.1080p.WEB.tr.srt" || !strings.Contains(string(raw), "Merhaba") {
		t.Fatalf("pack: %v %q", err, name)
	}
	_, lang, ref, _ = decodeID(unpacked.ID)
	raw, _, err = d.fetch(context.Background(), ref, lang)
	if err != nil || !strings.Contains(string(raw), "Merhaba") {
		t.Fatalf("raw: %v", err)
	}
	if _, _, err := d.fetch(context.Background(), "https://evil.example/x.zip", "tr"); err == nil {
		t.Fatal("foreign URL accepted")
	}
}

func TestEpisodeOf(t *testing.T) {
	cases := map[string][2]int{
		"Show.S02E05.1080p.srt":         {2, 5},
		"show.2x05.srt":                 {2, 5},
		"Breaking Bad - 5x07 - Say.srt": {5, 7},
		"Dizi.E07.srt":                  {0, 7},
		"Dizi 7. Bölüm.srt":             {0, 7},
		"Dizi Bölüm 12.srt":             {0, 12},
		"Inception.2010.1080p.srt":      {0, 0},
	}
	for name, want := range cases {
		if s, e := episodeOf(name); s != want[0] || e != want[1] {
			t.Errorf("%s: got %d %d", name, s, e)
		}
	}
}

func TestRarReported(t *testing.T) {
	if _, _, err := pickFromArchive([]byte("Rar!\x1a\x07\x00rest"), 0, 0, ""); !errors.Is(err, ErrRarUnsupported) {
		t.Fatal(err)
	}
}

// --- turkcealtyazi.org ---

const taMoviePage = `<html><body>
<div class="nm-block res-alt" id="altyazilar"><h5>Inception Altyazıları</h5><div class="altyazi-list-wrapper">
<div class="ta-container altsondiv"><div class="alisim"><h4>Türkçe Altyazılar</h4></div></div>
<div>
<div class="altsonsez2 row-class2">				<div class="alisim">
	<div class="fl"><span class="kal1"></span>
	<a itemprop="url" class="underline" id="746685" href="/sub/746685/inception1.html" title="Inception - 746685"><strong>Inception</strong></a></div>
	<br /><div class="clear"></div><div class="altsontar"> </div>
</div>
<div class="aldil"><span class="flagtr"></span></div>
<div class="alcd">
						1					</div>
<div class="alcevirmen"><a href="/translator/UMSxbmFyK0JhdHVt"><strong>Pınar Batum</strong></a> <span>&amp;</span> <a href="/translator/bWlzZXJ5"><strong>misery</strong></a><a href="/translator/"><strong></strong></a></div>
<div class="alfps">23.976</div>
<div class="alindirme">6,526</div>
<div class="algonderen"><a href="/member/55801.html"><span style="font-weight:700;">WoraZ</span></a></div>
<div class="clear"></div>
<div class="ta-container"><div class="ripdiv">  <span class="rps r12"></span>   SPARKS / FLAWL3SS				</div><div class="datediv">7 yıl önce</div></div>
</div></div>
<div>
<div class="altsonsez2 row-class1"><div class="alisim"><div class="fl"><span class="kal4"></span>
	<a itemprop="url" class="underline" id="315724" href="/sub/315724/inception1.html" title="Inception - 315724"><strong>Inception</strong></a></div></div>
<div class="aldil"><span class="flagtr"></span></div>
<div class="alcd">2</div>
<div class="alcevirmen"><span class="cps c1"></span></div>
<div class="alfps">25</div><div class="alindirme">7,693</div>
<div class="algonderen"><a href="/member/1.html"><span>celestine</span></a></div>
<div class="ta-container"><div class="ripdiv"><span class="rps r2"></span> ARROW </div></div>
</div></div>
<div class="ta-container altsondiv"><div class="alisim"><h4>İngilizce Altyazılar</h4></div></div>
<div>
<div class="altsonsez2 row-class2"><div class="alisim"><div class="fl"><span class="kal1"></span>
	<a itemprop="url" class="underline" id="612826" href="/sub/612826/inception1.html"><strong>Inception</strong></a></div></div>
<div class="aldil"><span class="flagen"></span></div>
<div class="alcd">1</div><div class="alcevirmen"></div><div class="alfps">23.976</div><div class="alindirme">10</div>
<div class="algonderen">x</div><div class="ta-container"><div class="ripdiv"><span class="rps r8"></span> NTb </div></div>
</div></div>
</div></div></body></html>`

const taTVPage = `<div class="altsonsez1 row-class2 toggle sezon_5"><div class="alisim"><div class="fl"><span class="kal1"></span>
<a itemprop="url" class="underline" id="498919" href="/sub/498919/breaking-badd.html"><strong>Breaking Bad</strong></a></div></div>
<div class="aldil"><span class="flagtr"></span></div>
<div class="alcd">
						S<b>05</b>
						<b>Paket</b>
				</div>
<div class="alcevirmen"><a href="/translator/x"><strong>nazo82</strong></a></div>
<div class="alfps">23.976</div><div class="alindirme">169,964</div><div class="algonderen">Azazel</div>
<div class="ta-container"><div class="ripdiv"><span class="rps r1"></span> HDTV / HDTV.720p </div></div>
</div>
<div class="altsonsez1 row-class1 toggle sezon_5"><div class="alisim"><div class="fl"><span class="kal2"></span>
<a itemprop="url" class="underline" id="500001" href="/sub/500001/breaking-badd.html"><strong>Breaking Bad</strong></a></div></div>
<div class="aldil"><span class="flagtr"></span></div>
<div class="alcd">S<b>05</b> <span class="cred">|</span> E<b>07</b></div>
<div class="alcevirmen">Taskano</div><div class="alfps">23.976</div><div class="alindirme">1.234</div><div class="algonderen">u</div>
<div class="ta-container"><div class="ripdiv"><span class="rps r8"></span> NTb </div></div>
</div>
<div class="altsonsez1 row-class2 toggle sezon_5"><div class="alisim"><div class="fl"><span class="kal1"></span>
<a itemprop="url" class="underline" id="500002" href="/sub/500002/breaking-badd.html"><strong>Breaking Bad</strong></a></div></div>
<div class="aldil"><span class="flagtr"></span></div>
<div class="alcd">S<b>05</b> <span class="cred">|</span> E<b>08</b></div>
<div class="alcevirmen">Taskano</div><div class="alfps">23.976</div><div class="alindirme">99</div><div class="algonderen">u</div>
</div>
<div class="altsonsez1 row-class1 toggle sezon_4 disnone"><div class="alisim"><div class="fl"><span class="kal1"></span>
<a itemprop="url" class="underline" id="400001" href="/sub/400001/breaking-badd.html"><strong>Breaking Bad</strong></a></div></div>
<div class="aldil"><span class="flagtr"></span></div>
<div class="alcd">S<b>04</b> <b>Paket</b></div>
</div>`

const taDetailPage = `<html><div id="sub-details">...</div>
<form style="margin-bottom:0;" method="post" action="/ind">
	<input type="hidden" name="idid" value="746874" />
	<input type="hidden" name="altid" value="ALTID" />
	<input type="hidden" name="sidid" value="0182a47dfc770f0eddd32038fa48d7c5" />
	<button type="submit" class="altIndirButton"></button>
</form></html>`

func TestParseTAList(t *testing.T) {
	rows := parseTAList(taMoviePage)
	if len(rows) != 3 {
		t.Fatalf("got %d rows", len(rows))
	}
	r := rows[0]
	if r.ID != "746685" || r.Href != "/sub/746685/inception1.html" || r.Lang != "tr" || r.Quality != 1 || r.CDs != 1 ||
		r.Translator != "Pınar Batum & misery" || r.FPS != 23.976 || r.Downloads != 6526 || r.Uploader != "WoraZ" ||
		r.Source != "BluRay" || r.Groups != "SPARKS / FLAWL3SS" {
		t.Errorf("row 0: %+v", r)
	}
	if rows[1].CDs != 2 || rows[1].Quality != 4 || rows[1].Source != "DVDRip" {
		t.Errorf("row 1: %+v", rows[1])
	}
	if rows[2].Lang != "en" || rows[2].Source != "WEB-DL" {
		t.Errorf("row 2: %+v", rows[2])
	}

	tv := parseTAList(taTVPage)
	if len(tv) != 4 || !tv[0].Pack || tv[0].Season != 5 || tv[1].Episode != 7 || tv[1].Pack || tv[1].Downloads != 1234 {
		t.Fatalf("tv rows: %+v", tv)
	}
}

func TestTurkceAltyaziFlow(t *testing.T) {
	var requests int32
	moviePack := makeZip(t, map[string]string{"Inception.2010.1080p.BluRay.x264-SPARKS.srt": sampleSRT})
	tvPack := makeZip(t, map[string]string{
		"Breaking.Bad.S05E06.HDTV.srt": "1\n00:00:01,000 --> 00:00:02,000\nYanlış\n",
		"Breaking.Bad.S05E07.HDTV.srt": sampleSRT,
	})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&requests, 1)
		if !strings.Contains(r.Header.Get("User-Agent"), "Nextflix") {
			t.Errorf("user agent %q", r.Header.Get("User-Agent"))
		}
		switch {
		case r.URL.Path == "/tt1375666/":
			http.Redirect(w, r, "/mov/1375666/inception1.html", http.StatusMovedPermanently)
		case r.URL.Path == "/mov/1375666/inception1.html":
			io.WriteString(w, taMoviePage)
		case r.URL.Path == "/tt0903747/":
			io.WriteString(w, taTVPage)
		case strings.HasPrefix(r.URL.Path, "/sub/"):
			id := strings.Split(r.URL.Path, "/")[2]
			io.WriteString(w, strings.Replace(taDetailPage, "ALTID", id, 1))
		case r.URL.Path == "/ind" && r.Method == http.MethodPost:
			r.ParseForm()
			if r.Form.Get("sidid") != "0182a47dfc770f0eddd32038fa48d7c5" || r.Form.Get("idid") != "746874" || r.Header.Get("Referer") == "" {
				t.Errorf("form %v", r.Form)
			}
			w.Header().Set("Content-Type", "application/zip")
			if r.Form.Get("altid") == "498919" {
				w.Write(tvPack)
			} else {
				w.Write(moviePack)
			}
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	dir := t.TempDir()
	ta := newTurkceAltyazi(srv.URL, "", diskCache{dir: dir})
	ta.gap = 50 * time.Millisecond

	q := Query{TmdbID: 27205, MediaType: "movie", Langs: []string{"tr"}}
	items, err := ta.search(context.Background(), q, "tt1375666")
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 2 {
		t.Fatalf("got %d items", len(items))
	}
	first := items[0]
	if first.Release != "BluRay SPARKS / FLAWL3SS" || first.Author != "Pınar Batum & misery" || first.Quality != 1 ||
		len(first.Releases) != 2 || first.Releases[0] != "BluRay-SPARKS" {
		t.Errorf("item %+v", first)
	}
	if !items[1].MultiCD {
		t.Error("2 CD subtitle not flagged")
	}

	// Pages come from the disk cache the second time.
	n := atomic.LoadInt32(&requests)
	if _, err := ta.search(context.Background(), q, "tt1375666"); err != nil || atomic.LoadInt32(&requests) != n {
		t.Fatalf("search page not cached (%v)", err)
	}

	_, lang, ref, _ := decodeID(first.ID)
	raw, name, err := ta.fetch(context.Background(), ref, lang)
	if err != nil || !strings.Contains(string(raw), "Merhaba") || !strings.HasSuffix(name, ".srt") {
		t.Fatalf("fetch: %v %q", err, name)
	}

	// TV: the season pack and the exact episode are listed, other episodes and seasons are not.
	tq := Query{TmdbID: 1396, MediaType: "tv", Season: 5, Episode: 7, Langs: []string{"tr", "en"}}
	tvItems, err := ta.search(context.Background(), tq, "tt0903747")
	if err != nil || len(tvItems) != 2 {
		t.Fatalf("tv: %v %d", err, len(tvItems))
	}
	_, lang, ref, _ = decodeID(tvItems[0].ID)
	raw, name, err = ta.fetch(context.Background(), ref, lang)
	if err != nil || name != "Breaking.Bad.S05E07.HDTV.srt" || !strings.Contains(string(raw), "Merhaba") {
		t.Fatalf("pack fetch: %v %q", err, name)
	}

	if _, _, err := ta.fetch(context.Background(), "1|https://evil.example/|0|0|0", "tr"); err == nil {
		t.Fatal("bad ref accepted")
	}
}

func TestTurkceAltyaziRateLimit(t *testing.T) {
	var times []time.Time
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		times = append(times, time.Now())
		io.WriteString(w, "<html></html>")
	}))
	defer srv.Close()
	ta := newTurkceAltyazi(srv.URL, "", diskCache{})
	ta.gap = 300 * time.Millisecond
	for _, p := range []string{"/a", "/b", "/c"} {
		if _, err := ta.get(context.Background(), p); err != nil {
			t.Fatal(err)
		}
	}
	for i := 1; i < len(times); i++ {
		if d := times[i].Sub(times[i-1]); d < 280*time.Millisecond {
			t.Fatalf("requests %d apart", d)
		}
	}
}

func TestTurkceAltyaziCloudflareFallback(t *testing.T) {
	site := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.Header.Get("Cookie"), "cf_clearance=ok") {
			io.WriteString(w, "direct")
			return
		}
		w.Header().Set("cf-mitigated", "challenge")
		w.WriteHeader(http.StatusForbidden)
		io.WriteString(w, "<title>Just a moment...</title>")
	}))
	defer site.Close()
	fs := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		json.NewDecoder(r.Body).Decode(&body)
		if body["cmd"] != "request.get" {
			t.Errorf("cmd %v", body["cmd"])
		}
		json.NewEncoder(w).Encode(map[string]any{"status": "ok", "solution": map[string]any{
			"status": 200, "response": "solved", "userAgent": "UA",
			"cookies": []map[string]string{{"name": "cf_clearance", "value": "ok"}}}})
	}))
	defer fs.Close()

	ta := newTurkceAltyazi(site.URL, fs.URL, diskCache{})
	ta.gap = 0
	b, err := ta.get(context.Background(), "/x")
	if err != nil || string(b) != "solved" {
		t.Fatalf("%v %q", err, b)
	}
	// Cookies from FlareSolverr are reused for direct requests.
	b, err = ta.get(context.Background(), "/y")
	if err != nil || string(b) != "direct" {
		t.Fatalf("%v %q", err, b)
	}
}
