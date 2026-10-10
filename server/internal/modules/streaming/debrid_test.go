package streaming

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math/rand"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gin-gonic/gin"

	"nextflix/internal/platform/torbox"
)

const (
	tbHash   = "dd8255ecdc7ca55fb0bbf81323d87062db1f6d1c"
	tbKey    = "tb-s3cret-key"
	tbVideo  = "Movie (2020)/Movie.2020.1080p.mkv"
	tbSample = "Movie (2020)/Sample/sample.mkv"
)

// fakeTorBox is the TorBox API plus its CDN on one test server.
type fakeTorBox struct {
	srv     *httptest.Server
	content []byte

	mu        sync.Mutex
	torrents  map[string]int64 // hash -> id in the account
	failAPI   int              // non-zero: every API call answers this status
	gen       int              // links of older generations are refused (403)
	cutNext   bool             // the next CDN read is cut after 1 MB
	ranges    []string         // Range headers of the CDN reads
	delay     time.Duration    // before every CDN answer (first-byte latency)
	malformed bool             // checkcached answers garbage
	apiDelay  time.Duration    // before every API answer

	checks, creates, lists, dls, cdnReads atomic.Int32
	cdnOpen, cdnPeak                      atomic.Int32 // CDN reads running, most at once
	onlyCached                            atomic.Bool
}

func newFakeTorBox(t *testing.T) *fakeTorBox {
	f := &fakeTorBox{torrents: map[string]int64{}, content: make([]byte, 3<<20)}
	rand.New(rand.NewSource(1)).Read(f.content)
	f.srv = httptest.NewServer(http.HandlerFunc(f.serve))
	t.Cleanup(f.srv.Close)
	return f
}

func (f *fakeTorBox) files() string {
	// TorBox's ids need not follow the list order: the sample comes first here
	return fmt.Sprintf(`[{"id":0,"name":%q,"short_name":"sample.mkv","size":1000},{"id":1,"name":%q,"short_name":"Movie.2020.1080p.mkv","size":%d}]`,
		tbSample, tbVideo, len(f.content))
}

func (f *fakeTorBox) serve(w http.ResponseWriter, r *http.Request) {
	if strings.HasPrefix(r.URL.Path, "/cdn/") {
		f.cdn(w, r)
		return
	}
	f.mu.Lock()
	fail, delay := f.failAPI, f.apiDelay
	f.mu.Unlock()
	time.Sleep(delay)
	if fail != 0 {
		w.WriteHeader(fail)
		fmt.Fprint(w, `{"success":false,"error":"BAD_TOKEN","detail":"bad"}`)
		return
	}
	auth := r.Header.Get("Authorization") == "Bearer "+tbKey || r.URL.Query().Get("token") == tbKey
	if !auth || r.Header.Get("User-Agent") != torbox.UserAgent {
		w.WriteHeader(http.StatusUnauthorized)
		fmt.Fprint(w, `{"success":false,"error":"NO_AUTH"}`)
		return
	}
	switch r.URL.Path {
	case "/user/me":
		fmt.Fprint(w, `{"success":true,"data":{"plan":2,"premium_expires_at":"2026-11-01T00:00:00Z","email":"x@y"}}`)
	case "/torrents/checkcached":
		f.checks.Add(1)
		f.mu.Lock()
		bad := f.malformed
		f.mu.Unlock()
		if bad {
			fmt.Fprint(w, `{"success":true,"data":"?"}`)
			return
		}
		var items []string
		for _, h := range strings.Split(r.URL.Query().Get("hash"), ",") {
			if h == tbHash {
				items = append(items, fmt.Sprintf(`{"name":"Movie (2020)","size":%d,"hash":%q,"files":[{"name":%q,"size":%d},{"name":%q,"size":1000}]}`,
					len(f.content)+1000, h, tbVideo, len(f.content), tbSample))
			}
		}
		fmt.Fprintf(w, `{"success":true,"data":[%s]}`, strings.Join(items, ","))
	case "/torrents/mylist":
		f.lists.Add(1)
		f.mu.Lock()
		defer f.mu.Unlock()
		if id := r.URL.Query().Get("id"); id != "" {
			fmt.Fprintf(w, `{"success":true,"data":{"id":%s,"hash":%q,"download_state":"cached","download_present":true,"files":%s}}`, id, tbHash, f.files())
			return
		}
		var list []string
		for h, id := range f.torrents {
			list = append(list, fmt.Sprintf(`{"id":%d,"hash":%q,"download_state":"cached","files":%s}`, id, strings.ToUpper(h), f.files()))
		}
		fmt.Fprintf(w, `{"success":true,"data":[%s]}`, strings.Join(list, ","))
	case "/torrents/createtorrent":
		f.creates.Add(1)
		_ = r.ParseMultipartForm(1 << 20)
		f.onlyCached.Store(r.FormValue("add_only_if_cached") == "true")
		if !strings.Contains(r.FormValue("magnet"), tbHash) {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		f.mu.Lock()
		f.torrents[tbHash] = 77
		f.mu.Unlock()
		fmt.Fprint(w, `{"success":true,"data":{"torrent_id":77,"hash":"`+tbHash+`"}}`)
	case "/torrents/requestdl":
		f.dls.Add(1)
		if r.URL.Query().Get("torrent_id") != "77" || r.URL.Query().Get("file_id") != "1" {
			w.WriteHeader(http.StatusBadRequest)
			fmt.Fprintf(w, `{"success":false,"detail":"wrong file %s"}`, r.URL.RawQuery)
			return
		}
		f.mu.Lock()
		gen := f.gen
		f.mu.Unlock()
		fmt.Fprintf(w, `{"success":true,"data":"%s/cdn/%d/file.mkv"}`, f.srv.URL, gen)
	default:
		w.WriteHeader(http.StatusNotFound)
	}
}

// cdn serves the content with single ranges; old link generations get 403.
func (f *fakeTorBox) cdn(w http.ResponseWriter, r *http.Request) {
	f.cdnReads.Add(1)
	if n := f.cdnOpen.Add(1); n > f.cdnPeak.Load() {
		f.cdnPeak.Store(n)
	}
	defer f.cdnOpen.Add(-1)
	gen, _ := strconv.Atoi(strings.Split(strings.TrimPrefix(r.URL.Path, "/cdn/"), "/")[0])
	f.mu.Lock()
	expired := gen < f.gen
	cut := f.cutNext
	f.cutNext = false
	f.ranges = append(f.ranges, r.Header.Get("Range"))
	delay := f.delay
	f.mu.Unlock()
	if expired || r.Header.Get("User-Agent") != torbox.UserAgent {
		w.WriteHeader(http.StatusForbidden)
		return
	}
	time.Sleep(delay)
	size := int64(len(f.content))
	start, end := int64(0), size-1
	status := http.StatusOK
	if rg := r.Header.Get("Range"); rg != "" {
		spec := strings.TrimPrefix(rg, "bytes=")
		a, b, _ := strings.Cut(spec, "-")
		start, _ = strconv.ParseInt(a, 10, 64)
		if start >= size {
			w.Header().Set("Content-Range", fmt.Sprintf("bytes */%d", size))
			w.WriteHeader(http.StatusRequestedRangeNotSatisfiable)
			return
		}
		if b != "" {
			end, _ = strconv.ParseInt(b, 10, 64)
		}
		end = min(end, size-1)
		status = http.StatusPartialContent
		w.Header().Set("Content-Range", fmt.Sprintf("bytes %d-%d/%d", start, end, size))
	}
	w.Header().Set("Content-Type", "video/x-matroska")
	w.Header().Set("Content-Length", strconv.FormatInt(end-start+1, 10))
	w.WriteHeader(status)
	body := f.content[start : end+1]
	if cut && len(body) > 1<<20 {
		_, _ = w.Write(body[:1<<20])
		w.(http.Flusher).Flush()
		conn, _, err := w.(http.Hijacker).Hijack()
		if err == nil {
			conn.Close()
		}
		return
	}
	_, _ = w.Write(body)
}

func newDebridService(t *testing.T, f *fakeTorBox) *Service {
	t.Helper()
	s := NewService(memDB(t), Config{TorBoxURL: f.srv.URL, TorrServerURL: "http://127.0.0.1:1"})
	key := tbKey
	in := DebridSettingsInput{}
	in.TorBox = &struct {
		APIKey      optString `json:"api_key"`
		Enabled     *bool     `json:"enabled"`
		Prefer      *bool     `json:"prefer"`
		AddUncached *bool     `json:"add_uncached"`
	}{APIKey: optString{Value: key}}
	if _, err := s.UpdateDebridSettings(in); err != nil {
		t.Fatal(err)
	}
	return s
}

func registerVideo(t *testing.T, s *Service) {
	t.Helper()
	if it := s.debrid.item(context.Background(), tbHash); it == nil {
		t.Fatal("not cached")
	}
	s.debrid.register(tsDebridSource{hash: tbHash, index: 1, path: tbVideo, size: 3 << 20, title: "Movie (2020)"})
}

func TestDebridLinkAddsOnlyCachedAndReusesTorrent(t *testing.T) {
	f := newFakeTorBox(t)
	s := newDebridService(t, f)
	registerVideo(t, s)
	l, err := s.debrid.link(context.Background(), tbHash, 1, false)
	if err != nil {
		t.Fatal(err)
	}
	if l.torrentID != 77 || l.fileID != 1 || f.creates.Load() != 1 || !f.onlyCached.Load() {
		t.Fatalf("link %+v, creates %d, only cached %v", l, f.creates.Load(), f.onlyCached.Load())
	}
	if _, err := s.debrid.link(context.Background(), tbHash, 1, false); err != nil || f.dls.Load() != 1 {
		t.Fatalf("second link: %v, requestdl %d", err, f.dls.Load())
	}
	// Another process: the torrent is found in the account, not added again
	s2 := newDebridService(t, f)
	registerVideo(t, s2)
	if _, err := s2.debrid.link(context.Background(), tbHash, 1, false); err != nil || f.creates.Load() != 1 {
		t.Fatalf("reuse: %v, creates %d", err, f.creates.Load())
	}
}

func TestDebridProxyRefreshesAndResumes(t *testing.T) {
	f := newFakeTorBox(t)
	s := newDebridService(t, f)
	registerVideo(t, s)
	u := s.fileURL(tbHash, 1)
	if !strings.HasPrefix(u, "http://127.0.0.1:") || !strings.Contains(u, "/f/"+tbHash+"/1") {
		t.Fatalf("fileURL %q", u)
	}
	// The link expires (403) and the next read is cut: the reader still gets one whole file
	f.mu.Lock()
	f.gen++
	f.cutNext = true
	f.mu.Unlock()
	resp, err := http.Get(u)
	if err != nil {
		t.Fatal(err)
	}
	got, err := io.ReadAll(resp.Body)
	resp.Body.Close()
	if err != nil || resp.StatusCode != 200 || !bytes.Equal(got, f.content) {
		t.Fatalf("status %d, %d of %d bytes, equal %v, err %v", resp.StatusCode, len(got), len(f.content), bytes.Equal(got, f.content), err)
	}
	if f.dls.Load() != 2 {
		t.Fatalf("requestdl %d, want 2 (refresh after 403)", f.dls.Load())
	}
	// Ranges (ffmpeg seeks, the moviehash reads)
	req, _ := http.NewRequest(http.MethodGet, u, nil)
	req.Header.Set("Range", "bytes=100-199")
	resp, err = http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	got, _ = io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != 206 || resp.Header.Get("Content-Range") != fmt.Sprintf("bytes 100-199/%d", len(f.content)) ||
		!bytes.Equal(got, f.content[100:200]) {
		t.Fatalf("range: %d %q %d bytes", resp.StatusCode, resp.Header.Get("Content-Range"), len(got))
	}
}

func TestDebridFallsBackWhenTorBoxFails(t *testing.T) {
	f := newFakeTorBox(t)
	s := newDebridService(t, f)
	registerVideo(t, s)
	f.mu.Lock()
	f.failAPI = http.StatusForbidden // key revoked / plan expired
	f.mu.Unlock()
	// TorrServer is unreachable too, so the file's TorrServer id is unknown
	u := s.fileURL(tbHash, 1)
	if !strings.HasPrefix(u, "http://127.0.0.1:1/stream?link="+tbHash+"&index=-1&play") {
		t.Fatalf("fileURL %q, want TorrServer", u)
	}
	if s.debrid.usable() {
		t.Fatal("TorBox not paused after an unauthorized answer")
	}
	// While paused nothing is asked
	n := f.checks.Load()
	if got := s.debrid.cachedItems(context.Background(), []string{strings.Repeat("a", 40)}); len(got) != 0 || f.checks.Load() != n {
		t.Fatalf("paused check: %v, calls %d -> %d", got, n, f.checks.Load())
	}
}

func TestDebridMalformedCheckIsNotCached(t *testing.T) {
	f := newFakeTorBox(t)
	s := newDebridService(t, f)
	f.mu.Lock()
	f.malformed = true
	f.mu.Unlock()
	if got := s.debrid.cachedItems(context.Background(), []string{tbHash}); len(got) != 0 {
		t.Fatalf("malformed: %v", got)
	}
	f.mu.Lock()
	f.malformed = false
	f.mu.Unlock()
	// Unknown, not "not cached": the next resolve asks again and finds it
	if got := s.debrid.cachedItems(context.Background(), []string{tbHash}); got[tbHash] == nil || f.checks.Load() != 2 {
		t.Fatalf("second check: %v, calls %d", got, f.checks.Load())
	}
	// Answers are kept: no third call
	other := strings.Repeat("b", 40)
	s.debrid.cachedItems(context.Background(), []string{tbHash})
	s.debrid.cachedItems(context.Background(), []string{other})
	s.debrid.cachedItems(context.Background(), []string{other, tbHash})
	if f.checks.Load() != 3 {
		t.Fatalf("calls %d, want 3", f.checks.Load())
	}
}

func TestDebridBoostWithinResolution(t *testing.T) {
	cand := func(name string, q int, score float64) tsCandidate {
		return tsCandidate{raw: JackettResult{Title: name}, hash: name, quality: q, score: score}
	}
	cands := []tsCandidate{cand("A", 2160, 200), cand("B", 1080, 180), cand("C", 1080, 150), cand("D", 1080, 140), cand("E", 720, 100)}
	cached := map[string]bool{"C": true, "D": true, "E": true}
	tsDebridBoost(cands, func(c tsCandidate) bool { return cached[c.hash] })
	tsSortCandidates(cands, tsResolveQuery{})
	var order []string
	for _, c := range cands {
		order = append(order, fmt.Sprintf("%s%.0f", c.raw.Title, c.score))
	}
	if got := strings.Join(order, " "); got != "A200 C185 B180 D175 E105" {
		t.Fatalf("order %s", got)
	}
}

func TestDebridGateStopsLowerChecks(t *testing.T) {
	cands := []tsCandidate{
		{hash: tbHash, quality: 1080}, {hash: strings.Repeat("b", 40), quality: 1080}, {hash: strings.Repeat("c", 40), quality: 2160},
	}
	g := (&Service{}).newDebridGate(cands, map[string]*torbox.CachedItem{tbHash: {Hash: tbHash}})
	if g.lead != 0 {
		t.Fatalf("lead %d", g.lead)
	}
	g.finish(&tsResolveResult{Quality: "1080p"}, nil)
	if err := g.wait(context.Background(), 1); !errors.Is(err, errDebridPicked) {
		t.Fatalf("same resolution: %v", err)
	}
	if err := g.wait(context.Background(), 2); err != nil {
		t.Fatalf("higher resolution: %v", err)
	}
	// A failed lead lets every check run
	g = (&Service{}).newDebridGate(cands, map[string]*torbox.CachedItem{tbHash: {Hash: tbHash}})
	g.finish(nil, errors.New("probe failed"))
	if err := g.wait(context.Background(), 1); err != nil {
		t.Fatalf("after a failed lead: %v", err)
	}
}

func TestCheckDebridPlaysFromTorBox(t *testing.T) {
	f := newFakeTorBox(t)
	s := newDebridService(t, f)
	s.probeFn = func(ctx context.Context, hash string, index int) (*tsProbe, error) {
		// The probe reads the TorBox file through the proxy
		if u := s.fileURL(hash, index); !strings.Contains(u, "/f/"+tbHash+"/1") {
			return nil, fmt.Errorf("probe url %s", u)
		}
		p := &tsProbe{Streams: []tsProbeStream{{CodecType: "video", CodecName: "h264", Width: 1920, Height: 1080}}}
		p.Format.Duration = "5400"
		return p, nil
	}
	it := s.debrid.item(context.Background(), tbHash)
	q := tsResolveQuery{mediaType: "movie", title: "Movie", year: 2020, runtime: 90}
	c := tsCandidate{raw: JackettResult{Title: "Movie 2020 1080p WEB-DL", Seeders: 3}, hash: tbHash, quality: 1080, score: 100}
	res, err := s.checkDebrid(context.Background(), c, q, it, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if !res.Debrid || res.Source != "torbox" || res.Index != 1 || res.FileName != "Movie.2020.1080p.mkv" || !res.DurationVerified ||
		res.Health == nil || res.Health.Rate <= 0 || !res.good() {
		t.Fatalf("result %+v health %v", res, res.Health)
	}
	b, _ := json.Marshal(res)
	if !strings.Contains(string(b), `"debrid":true`) || !strings.Contains(string(b), `"source":"torbox"`) {
		t.Fatalf("json %s", b)
	}
	// TorrServer's stat of it is answered locally (pings, names never load it there)
	var st tsTorrentStat
	if err := s.torrentStat(context.Background(), tbHash, &st); err != nil || st.Name != c.raw.Title || len(st.FileStats) != 1 {
		t.Fatalf("stat %+v %v", st, err)
	}
	// The cached source plays from TorBox after a restart too
	key := "movie|movie|2020"
	s.storeSource(key, *res)
	s2 := NewService(s.db, Config{TorBoxURL: f.srv.URL, TorrServerURL: "http://127.0.0.1:1"})
	if _, ok := s2.cachedSource(key); !ok {
		t.Fatal("not cached")
	}
	if u := s2.fileURL(tbHash, 1); !strings.Contains(u, "/f/"+tbHash+"/1") {
		t.Fatalf("after restart fileURL %q", u)
	}
}

func TestDebridSettingsKeepTheKeySecret(t *testing.T) {
	f := newFakeTorBox(t)
	s := NewService(memDB(t), Config{TorBoxURL: f.srv.URL})
	gin.SetMode(gin.TestMode)
	r := gin.New()
	Register(r.Group("/api"), s)
	call := func(method, path, body string) (int, string) {
		w := httptest.NewRecorder()
		req := httptest.NewRequest(method, path, strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		r.ServeHTTP(w, req)
		if strings.Contains(w.Body.String(), tbKey) || strings.Contains(w.Body.String(), "wrong-key") {
			t.Fatalf("%s %s leaks the key: %s", method, path, w.Body)
		}
		return w.Code, w.Body.String()
	}
	view := func(body string) DebridSettingsView {
		var v DebridSettingsView
		if err := json.Unmarshal([]byte(body), &v); err != nil {
			t.Fatal(err)
		}
		return v
	}
	if code, body := call(http.MethodGet, "/api/settings/debrid", ""); code != 200 || view(body).TorBox.APIKeySet ||
		!view(body).TorBox.Enabled || !view(body).TorBox.Prefer || view(body).TorBox.AddUncached {
		t.Fatalf("defaults %d %s", code, body)
	}
	if code, _ := call(http.MethodPost, "/api/settings/debrid/test", ""); code != 400 {
		t.Fatalf("test without key: %d", code)
	}
	code, body := call(http.MethodPut, "/api/settings/debrid", `{"torbox":{"api_key":" `+tbKey+` ","prefer":false}}`)
	if v := view(body); code != 200 || !v.TorBox.APIKeySet || v.TorBox.Prefer {
		t.Fatalf("put %d %s", code, body)
	}
	if code, body := call(http.MethodPut, "/api/settings/debrid", `{"torbox":{"api_key":"","enabled":false}}`); code != 200 ||
		!view(body).TorBox.APIKeySet || view(body).TorBox.Enabled {
		t.Fatalf("empty key keeps it: %d %s", code, body)
	}
	if code, body := call(http.MethodPost, "/api/settings/debrid/test", ""); code != 200 ||
		!strings.Contains(body, `"plan_name":"Pro"`) || !strings.Contains(body, `"premium_expires_at":"2026-11-01T00:00:00Z"`) {
		t.Fatalf("test %d %s", code, body)
	}
	if code, body := call(http.MethodPost, "/api/settings/debrid/test", `{"torbox":{"api_key":"wrong-key"}}`); code != 401 {
		t.Fatalf("wrong key: %d %s", code, body)
	}
	if code, body := call(http.MethodPut, "/api/settings/debrid", `{"torbox":{"api_key":null}}`); code != 200 || view(body).TorBox.APIKeySet {
		t.Fatalf("null clears: %d %s", code, body)
	}
	if code, _ := call(http.MethodPut, "/api/settings/debrid", `{"torbox":{"api_key":"has space"}}`); code != 400 {
		t.Fatalf("invalid key: %d", code)
	}
}

// A player re-adding a TorBox source (saved progress, a switch) gets its files without
// TorrServer, which is unreachable here.
func TestDebridFilesSkipTorrServer(t *testing.T) {
	f := newFakeTorBox(t)
	s := newDebridService(t, f)
	registerVideo(t, s)
	gin.SetMode(gin.TestMode)
	r := gin.New()
	Register(r.Group("/api"), s)
	w := httptest.NewRecorder()
	magnet := "magnet:?xt=urn:btih:" + strings.ToUpper(tbHash) + "&dn=Movie"
	r.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/api/torrent-stream/files?magnet="+url.QueryEscape(magnet), nil))
	var got struct {
		Hash  string
		Files []struct {
			ID   int
			Path string
		}
	}
	if w.Code != http.StatusOK || json.Unmarshal(w.Body.Bytes(), &got) != nil {
		t.Fatalf("files: %d %s", w.Code, w.Body)
	}
	if got.Hash != tbHash || len(got.Files) != 1 || got.Files[0].ID != 1 || got.Files[0].Path != tbVideo {
		t.Fatalf("files: %+v", got)
	}
}

// In a pack the index is the file's place in checkcached's list + 1, whatever TorBox's own
// file ids are: another episode of a registered pack maps to its own file.
func TestDebridPackIndexMapsToTheListPosition(t *testing.T) {
	f := newFakeTorBox(t)
	s := newDebridService(t, f)
	registerVideo(t, s)
	src, ok := s.debrid.source(tbHash, 2)
	if !ok || src.path != tbSample || src.size != 1000 {
		t.Fatalf("index 2: %+v %v", src, ok)
	}
	// TorBox's ids run the other way round (sample 0, video 1)
	files := []torbox.File{{ID: 0, Name: tbSample, Size: 1000}, {ID: 1, Name: tbVideo, Size: 3 << 20}}
	if id, ok := tsDebridMatchFile(files, src, s.debrid.cachedNames(tbHash)); !ok || id != 0 {
		t.Fatalf("index 2 -> TorBox file %d %v, want 0", id, ok)
	}
	video, _ := s.debrid.source(tbHash, 1)
	if id, ok := tsDebridMatchFile(files, video, s.debrid.cachedNames(tbHash)); !ok || id != 1 {
		t.Fatalf("index 1 -> TorBox file %d %v, want 1", id, ok)
	}
}

// A problem of one torrent or file (not in the TorBox torrent) does not pause TorBox for
// everything; a transport failure still does.
func TestDebridTorrentProblemDoesNotPause(t *testing.T) {
	f := newFakeTorBox(t)
	s := newDebridService(t, f)
	registerVideo(t, s)
	s.debrid.register(tsDebridSource{hash: tbHash, index: 5, path: "Movie (2020)/Extras/gone.mkv", size: 12345, title: "Movie (2020)"})
	_, err := s.debrid.link(context.Background(), tbHash, 5, false)
	if !errors.Is(err, errDebridTorrent) || !errors.Is(err, errDebridUnavailable) {
		t.Fatalf("missing file: %v", err)
	}
	if !s.debrid.usable() {
		t.Fatal("TorBox paused by one missing file")
	}
	if _, err := s.debrid.link(context.Background(), tbHash, 1, false); err != nil {
		t.Fatalf("other file: %v", err)
	}
	s.debrid.fail(fmt.Errorf("torbox torrents/mylist: %w", errors.New("connection reset by peer")))
	if s.debrid.usable() {
		t.Fatal("a transport failure did not pause TorBox")
	}
}

// A caller whose own ctx runs out gets its ctx error, but the shared request goes on: the
// next caller gets the link, TorBox is not paused and the file is not marked broken.
func TestDebridLinkCallerTimeoutPausesNothing(t *testing.T) {
	f := newFakeTorBox(t)
	s := newDebridService(t, f)
	registerVideo(t, s)
	f.mu.Lock()
	f.apiDelay = 300 * time.Millisecond
	f.mu.Unlock()
	short, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	if _, err := s.debrid.link(short, tbHash, 1, false); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("short caller: %v", err)
	}
	l, err := s.debrid.link(context.Background(), tbHash, 1, false)
	if err != nil || l.fileID != 1 {
		t.Fatalf("second caller: %+v %v", l, err)
	}
	if !s.debrid.usable() || s.debrid.isBroken(tbHash, 1) {
		t.Fatalf("usable %v, broken %v", s.debrid.usable(), s.debrid.isBroken(tbHash, 1))
	}
	if f.creates.Load() != 1 || f.dls.Load() != 1 {
		t.Fatalf("creates %d, requestdl %d: want one shared request", f.creates.Load(), f.dls.Load())
	}
}

// swappedTorrServer lists the torrent the other way round than TorBox: 1 is the sample, 2 the
// video. Streams are served at 4 MB/s.
func swappedTorrServer(t *testing.T, hits *atomic.Int32) *httptest.Server {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		if _, ok := r.URL.Query()["stat"]; ok {
			fmt.Fprintf(w, `{"hash":%q,"name":"Movie (2020)","download_speed":4000000,"active_peers":4,"connected_seeders":3,`+
				`"file_stats":[{"id":1,"path":%q,"length":1000},{"id":2,"path":%q,"length":%d}]}`, tbHash, tbSample, tbVideo, 3<<20)
			return
		}
		w.WriteHeader(http.StatusPartialContent)
		chunk := make([]byte, 200_000)
		for {
			if _, err := w.Write(chunk); err != nil {
				return
			}
			w.(http.Flusher).Flush()
			select {
			case <-time.After(50 * time.Millisecond):
			case <-r.Context().Done():
				return
			}
		}
	}))
	t.Cleanup(srv.Close)
	return srv
}

// TorBox off: a registered debrid source's index (TorBox's list position) is mapped to
// TorrServer's file by path and size, never passed on as is.
func TestDebridOffRemapsTheIndex(t *testing.T) {
	f := newFakeTorBox(t)
	var hits atomic.Int32
	ts := swappedTorrServer(t, &hits)
	s := reuseSetup(t, f, ts.URL, false)
	s.debrid.register(tsDebridSource{hash: tbHash, index: 1, path: tbVideo, size: 3 << 20, title: "Movie (2020)"})
	if u := s.fileURL(tbHash, 1); u != ts.URL+"/stream?link="+tbHash+"&index=2&play" {
		t.Fatalf("fileURL %q, want TorrServer's file 2", u)
	}
	// Unregistered files keep TorrServer's ids
	if u := s.fileURL(tbHash, 2); u != ts.URL+"/stream?link="+tbHash+"&index=2&play" {
		t.Fatalf("fileURL of TorrServer's file 2: %q", u)
	}
}

func TestDebridPruneForgetsIdleState(t *testing.T) {
	d := newDebrid(nil, "")
	other := strings.Repeat("e", 40)
	d.register(tsDebridSource{hash: tbHash, index: 1, path: tbVideo, size: 1})
	d.register(tsDebridSource{hash: other, index: 1, path: tbVideo, size: 1})
	now := time.Now()
	k := tsDebridKey(tbHash, 1)
	d.mu.Lock()
	d.sources[tbHash].used = now.Add(-tsDebridSourceIdle - time.Minute)
	d.links[k] = &tsDebridLink{at: now}
	d.links[tsDebridKey(other, 1)] = &tsDebridLink{at: now}
	d.remaps[k] = 2
	d.warmed[k] = now.Add(-time.Hour)
	d.broken[k] = now.Add(-time.Hour)
	d.added[tbHash] = now.Add(-tsDebridKeep)
	d.adoptTried[tbHash] = now.Add(-tsAdoptRetry)
	d.torrents[tbHash] = tsDebridTorrentRef{id: 1, at: now.Add(-tsDebridKeep)}
	d.torrents[other] = tsDebridTorrentRef{id: 2, at: now.Add(-tsDebridKeep)}
	d.checks[tbHash] = tsDebridCheck{at: now.Add(-tsDebridCacheTTL)}
	d.mu.Unlock()
	d.pruneDebrid(now)
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.sources[tbHash] != nil || d.sources[other] == nil {
		t.Fatalf("sources: idle kept %v, used dropped %v", d.sources[tbHash] != nil, d.sources[other] == nil)
	}
	if len(d.links) != 1 || len(d.remaps) != 0 || len(d.warmed) != 0 || len(d.broken) != 0 || len(d.added) != 0 ||
		len(d.adoptTried) != 0 || len(d.checks) != 0 {
		t.Fatalf("left: links %d remaps %d warmed %d broken %d added %d adopt %d checks %d",
			len(d.links), len(d.remaps), len(d.warmed), len(d.broken), len(d.added), len(d.adoptTried), len(d.checks))
	}
	if _, ok := d.torrents[other]; !ok || len(d.torrents) != 1 {
		t.Fatalf("torrents %v: want only the registered hash", d.torrents)
	}
}

// TorrServer cannot list the torrent: the file id is unknown (-1), never TorBox's list
// position, and TorrServer is not asked again on the next read.
func TestDebridOffUnknownIndexIsNotGuessed(t *testing.T) {
	f := newFakeTorBox(t)
	var hits atomic.Int32
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		w.WriteHeader(http.StatusInternalServerError)
	}))
	t.Cleanup(ts.Close)
	s := reuseSetup(t, f, ts.URL, false)
	s.debrid.register(tsDebridSource{hash: tbHash, index: 1, path: tbVideo, size: 3 << 20, title: "Movie (2020)"})
	want := ts.URL + "/stream?link=" + tbHash + "&index=-1&play"
	for range 2 {
		if u := s.fileURL(tbHash, 1); u != want {
			t.Fatalf("fileURL %q, want %q", u, want)
		}
	}
	if hits.Load() != 1 {
		t.Fatalf("TorrServer asked %d times, want 1", hits.Load())
	}
}

// Nothing fits the caps by name: with "prefer TorBox" the candidates keep the fallback order
// (cheapest first) and leave out the avoided torrents.
func TestDebridOverCapKeepsTheFallbackOrder(t *testing.T) {
	f := newFakeTorBox(t)
	s := newDebridService(t, f)
	avoided, other := strings.Repeat("c", 40), strings.Repeat("b", 40)
	q := tsResolveQuery{mediaType: "movie", title: "Movie", year: 2020, runtime: 90, maxQ: 720, avoid: []string{avoided}}
	rel := func(name, hash string, size int64) JackettResult {
		return JackettResult{Title: name, Seeders: 40, Size: size, MagnetUri: "magnet:?xt=urn:btih:" + hash}
	}
	raw := []JackettResult{
		rel("Movie.2020.2160p.WEB-DL.x264", tbHash, 8*tsGB),
		rel("Movie.2020.1080p.WEB-DL.x264", other, 4*tsGB),
		rel("Movie.2020.1080p.BluRay.x264", avoided, 6*tsGB),
	}
	cands := s.fallbackCandidates(q, raw, nil, nil)
	q.overCap = true
	got, items := s.debridCandidates(context.Background(), q, raw, cands)
	if items[tbHash] == nil {
		t.Fatal("the 2160p one is not cached")
	}
	if len(got) != 2 || got[0].hash != other || got[1].hash != tbHash {
		t.Fatalf("candidates %q", titles(got))
	}
}
