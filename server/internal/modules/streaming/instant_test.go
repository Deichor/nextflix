package streaming

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
)

// instantTorBox is a checkcached-only TorBox that records every call.
type instantTorBox struct {
	srv    *httptest.Server
	cached map[string]bool
	fail   atomic.Bool

	mu    sync.Mutex
	calls [][]string
}

func newInstantTorBox(t *testing.T, cached ...string) *instantTorBox {
	f := &instantTorBox{cached: map[string]bool{}}
	for _, h := range cached {
		f.cached[h] = true
	}
	f.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/torrents/checkcached" {
			http.NotFound(w, r)
			return
		}
		if f.fail.Load() {
			w.WriteHeader(http.StatusBadGateway)
			return
		}
		hashes := strings.Split(r.URL.Query().Get("hash"), ",")
		f.mu.Lock()
		f.calls = append(f.calls, hashes)
		f.mu.Unlock()
		var items []string
		for _, h := range hashes {
			if f.cached[h] {
				items = append(items, fmt.Sprintf(`{"name":"x","size":1,"hash":%q,"files":[]}`, h))
			}
		}
		fmt.Fprintf(w, `{"success":true,"data":[%s]}`, strings.Join(items, ","))
	}))
	t.Cleanup(f.srv.Close)
	return f
}

func (f *instantTorBox) asked() [][]string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([][]string(nil), f.calls...)
}

func instantHash(i int) string { return fmt.Sprintf("%040x", 0xabc000+i) }

// newInstantService has a fake Jackett answering results and TorBox (key set unless off).
func newInstantService(t *testing.T, tb *instantTorBox, results []JackettResult, off bool) *Service {
	t.Helper()
	jackett := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Query().Get("t") == "indexers":
			w.Write([]byte(`<indexers>
<indexer id="tpb" configured="true"><caps><searching><movie-search available="yes" supportedParams="q" /><tv-search available="yes" supportedParams="q,season,ep" /></searching><categories><category id="2000"/><category id="5000"/></categories></caps></indexer>
</indexers>`))
		case strings.HasSuffix(r.URL.Path, "/results"):
			json.NewEncoder(w).Encode(JackettResponse{Results: results})
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(jackett.Close)
	db := memDB(t)
	if err := db.Create(&IndexerSettings{JackettURL: jackett.URL, JackettAPIKey: "k"}).Error; err != nil {
		t.Fatal(err)
	}
	s := NewService(db, Config{JackettURL: jackett.URL, JackettAPIKey: "k", TorBoxURL: tb.srv.URL, TorrServerURL: "http://127.0.0.1:1"})
	if !off {
		in := DebridSettingsInput{}
		in.TorBox = &struct {
			APIKey      optString `json:"api_key"`
			Enabled     *bool     `json:"enabled"`
			Prefer      *bool     `json:"prefer"`
			AddUncached *bool     `json:"add_uncached"`
		}{APIKey: optString{Value: tbKey}}
		if _, err := s.UpdateDebridSettings(in); err != nil {
			t.Fatal(err)
		}
	}
	return s
}

func instantRelease(name, hash string, seeders int) JackettResult {
	return JackettResult{Title: name, Seeders: seeders, Size: 3 * tsGB, MagnetUri: "magnet:?xt=urn:btih:" + hash}
}

func boolOf(v *bool) string {
	if v == nil {
		return "null"
	}
	return fmt.Sprint(*v)
}

// The check ranks like resolve: cinema recordings, other years and tiny releases are never
// asked, the best 20 hashes go to TorBox in one call.
func TestInstantCheckAsksTheBestRankedHashes(t *testing.T) {
	cam, wrongYear := instantHash(900), instantHash(901)
	var results []JackettResult
	for i := 0; i < 25; i++ {
		results = append(results, instantRelease(fmt.Sprintf("Movie 2020 1080p WEB-DL x264-G%d", i), instantHash(i), 50-i))
	}
	results = append(results, instantRelease("Movie 2020 HDCAM x264", cam, 900), instantRelease("Movie 2012 1080p BluRay x264", wrongYear, 900))

	tb := newInstantTorBox(t, cam, wrongYear)
	s := newInstantService(t, tb, results, false)
	v, err := s.instantCheck(context.Background(), InstantItem{Type: "movie", TmdbID: 1, Title: "Movie", Year: 2020})
	if err != nil || v == nil || *v {
		t.Fatalf("got %s, %v; want false (only a cam rip and another year are cached)", boolOf(v), err)
	}
	calls := tb.asked()
	if len(calls) != 1 || len(calls[0]) != tsInstantHashes {
		t.Fatalf("checkcached calls %d, first with %d hashes; want one with %d", len(calls), len(calls[0]), tsInstantHashes)
	}
	for _, h := range calls[0] {
		if h == cam || h == wrongYear {
			t.Fatalf("asked about %s", h)
		}
	}

	// One cached release among the top: true, answered from the 30 min check cache next time
	tb2 := newInstantTorBox(t, instantHash(3))
	s2 := newInstantService(t, tb2, results, false)
	for i := 0; i < 2; i++ {
		v, err = s2.instantCheck(context.Background(), InstantItem{Type: "movie", TmdbID: 1, Title: "Movie", Year: 2020})
		if err != nil || v == nil || !*v {
			t.Fatalf("got %s, %v; want true", boolOf(v), err)
		}
	}
	if n := len(tb2.asked()); n != 1 {
		t.Fatalf("checkcached asked %d times, want 1 (answers are cached)", n)
	}

	// TorBox failing leaves the title unknown, never "not cached"
	tb3 := newInstantTorBox(t)
	tb3.fail.Store(true)
	s3 := newInstantService(t, tb3, results, false)
	if v, err = s3.instantCheck(context.Background(), InstantItem{Type: "movie", TmdbID: 1, Title: "Movie", Year: 2020}); v != nil || err == nil {
		t.Fatalf("got %s, %v; want unknown", boolOf(v), err)
	}
}

// TV shows are checked by S01E01 (packs of season 1 count).
func TestInstantCheckTVUsesTheFirstEpisode(t *testing.T) {
	pack, other := instantHash(1), instantHash(2)
	tb := newInstantTorBox(t, pack, other)
	s := newInstantService(t, tb, []JackettResult{
		instantRelease("Show S01 1080p WEB-DL x264", pack, 20),
		instantRelease("Show S02E04 1080p WEB-DL x264", other, 90),
	}, false)
	v, err := s.instantCheck(context.Background(), InstantItem{Type: "tv", TmdbID: 5, Title: "Show"})
	if err != nil || v == nil || !*v {
		t.Fatalf("got %s, %v; want true", boolOf(v), err)
	}
	for _, h := range tb.asked()[0] {
		if h == other {
			t.Fatal("another episode was asked")
		}
	}
}

// fakeInstant replaces the search with answers per key and records the checks.
type fakeInstant struct {
	mu      sync.Mutex
	answers map[string]*bool
	errs    map[string]error
	checks  map[string]int
	running int32
	maxRun  int32
	hold    chan struct{} // nil = answer at once
}

func (f *fakeInstant) check(ctx context.Context, it InstantItem) (*bool, error) {
	n := atomic.AddInt32(&f.running, 1)
	defer atomic.AddInt32(&f.running, -1)
	for {
		m := atomic.LoadInt32(&f.maxRun)
		if n <= m || atomic.CompareAndSwapInt32(&f.maxRun, m, n) {
			break
		}
	}
	f.mu.Lock()
	f.checks[it.key()]++
	hold := f.hold
	f.mu.Unlock()
	if hold != nil {
		select {
		case <-hold:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.answers[it.key()], f.errs[it.key()]
}

func (f *fakeInstant) count(k string) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.checks[k]
}

func newFakeInstant(t *testing.T, s *Service, realGate bool) *fakeInstant {
	f := &fakeInstant{answers: map[string]*bool{}, errs: map[string]error{}, checks: map[string]int{}}
	s.instant.checkFn = f.check
	if !realGate {
		s.instant.busyFn = func() string { return "" }
	}
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	s.instant.start(ctx)
	return f
}

func instantWait(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func (in *tsInstant) idle() bool {
	in.mu.Lock()
	defer in.mu.Unlock()
	return len(in.user)+len(in.warm) == 0 && in.running == 0
}

func setInstantTimes(s *Service, hit, miss, retry, poll time.Duration) {
	in := s.instant
	in.hitTTL, in.missTTL, in.retry, in.busyPoll = hit, miss, retry, poll
}

// Unknown titles answer null at once and are checked in the background; known ones come from
// the cache until their TTL (positive and negative differ) ends, then are served stale and
// checked again.
func TestInstantLookupCachesWithTTLs(t *testing.T) {
	s := newInstantService(t, newInstantTorBox(t), nil, false)
	setInstantTimes(s, 300*time.Millisecond, 100*time.Millisecond, time.Hour, 20*time.Millisecond)
	f := newFakeInstant(t, s, false)
	yes, no := true, false
	f.answers["movie:1"], f.answers["tv:2"] = &yes, &no
	items := []InstantItem{{Type: "movie", TmdbID: 1}, {Type: "tv", TmdbID: 2}, {Type: "movie", TmdbID: 1}}

	got := s.instant.lookup(items)
	if len(got) != 2 || got["movie:1"] != nil || got["tv:2"] != nil {
		t.Fatalf("first answer %v, want two nulls", got)
	}
	instantWait(t, "the checks", func() bool { return f.count("movie:1") == 1 && f.count("tv:2") == 1 && s.instant.idle() })
	got = s.instant.lookup(items)
	if boolOf(got["movie:1"]) != "true" || boolOf(got["tv:2"]) != "false" {
		t.Fatalf("cached answer %s / %s", boolOf(got["movie:1"]), boolOf(got["tv:2"]))
	}
	if !s.instant.idle() || f.count("movie:1") != 1 {
		t.Fatal("a fresh answer was checked again")
	}

	// The negative answer expires first; the stale value is still served while it is checked again
	time.Sleep(150 * time.Millisecond)
	got = s.instant.lookup(items)
	if boolOf(got["tv:2"]) != "false" {
		t.Fatalf("stale answer %s, want false", boolOf(got["tv:2"]))
	}
	instantWait(t, "the re-check", func() bool { return f.count("tv:2") == 2 && s.instant.idle() })
	if f.count("movie:1") != 1 {
		t.Fatal("the positive answer expired with the negative TTL")
	}
}

// A check that cannot tell (search or TorBox failed) stays null and is not retried before
// tsInstantRetry.
func TestInstantUnknownIsRetriedLater(t *testing.T) {
	s := newInstantService(t, newInstantTorBox(t), nil, false)
	setInstantTimes(s, time.Hour, time.Hour, 200*time.Millisecond, 20*time.Millisecond)
	f := newFakeInstant(t, s, false)
	f.errs["movie:7"] = errInstantUnknown
	items := []InstantItem{{Type: "movie", TmdbID: 7}}
	s.instant.lookup(items)
	instantWait(t, "the check", func() bool { return f.count("movie:7") == 1 && s.instant.idle() })
	if got := s.instant.lookup(items); got["movie:7"] != nil || !s.instant.idle() {
		t.Fatalf("answer %s, queued again at once", boolOf(got["movie:7"]))
	}
	time.Sleep(250 * time.Millisecond)
	yes := true
	f.mu.Lock()
	f.errs["movie:7"], f.answers["movie:7"] = nil, &yes
	f.mu.Unlock()
	s.instant.lookup(items)
	instantWait(t, "the retry", func() bool { return f.count("movie:7") == 2 && s.instant.idle() })
	if got := s.instant.lookup(items); boolOf(got["movie:7"]) != "true" {
		t.Fatalf("after the retry %s", boolOf(got["movie:7"]))
	}
}

// TorBox off: every answer is null and nothing is searched.
func TestInstantOffDoesNoWork(t *testing.T) {
	s := newInstantService(t, newInstantTorBox(t), nil, true)
	f := newFakeInstant(t, s, false)
	got := s.instant.lookup([]InstantItem{{Type: "movie", TmdbID: 1}, {Type: "tv", TmdbID: 2}})
	if len(got) != 2 || got["movie:1"] != nil || got["tv:2"] != nil {
		t.Fatalf("answers %v", got)
	}
	time.Sleep(50 * time.Millisecond)
	if f.count("movie:1")+f.count("tv:2") != 0 || !s.instant.idle() {
		t.Fatal("work was queued with TorBox off")
	}
}

// At most tsInstantWorkers titles are checked at once, a client's titles before prewarm ones.
func TestInstantWorkersAreLimited(t *testing.T) {
	s := newInstantService(t, newInstantTorBox(t), nil, false)
	f := newFakeInstant(t, s, false)
	f.hold = make(chan struct{})
	s.instant.mu.Lock()
	for i := 1; i <= 3; i++ {
		it := InstantItem{Type: "movie", TmdbID: 100 + i}
		s.instant.warm = append(s.instant.warm, it)
		s.instant.queued[it.key()] = true
	}
	s.instant.mu.Unlock()
	var items []InstantItem
	for i := 1; i <= 6; i++ {
		items = append(items, InstantItem{Type: "movie", TmdbID: i})
	}
	s.instant.lookup(items)
	instantWait(t, "two running checks", func() bool { return atomic.LoadInt32(&f.running) == tsInstantWorkers })
	time.Sleep(50 * time.Millisecond)
	if n := atomic.LoadInt32(&f.running); n != tsInstantWorkers {
		t.Fatalf("%d checks at once", n)
	}
	close(f.hold)
	instantWait(t, "every check", func() bool { return s.instant.idle() })
	if m := atomic.LoadInt32(&f.maxRun); m > tsInstantWorkers {
		t.Fatalf("%d checks ran at once", m)
	}
	for i := 1; i <= 6; i++ {
		if f.count(fmt.Sprintf("movie:%d", i)) != 1 {
			t.Fatalf("movie:%d checked %d times", i, f.count(fmt.Sprintf("movie:%d", i)))
		}
	}
}

// Playback first: no check starts while a stream plays, and one that runs when a stream
// starts is stopped and done again afterwards.
func TestInstantGivesWayToPlayback(t *testing.T) {
	s := newInstantService(t, newInstantTorBox(t), nil, false)
	setInstantTimes(s, time.Hour, time.Hour, time.Hour, 20*time.Millisecond)
	f := newFakeInstant(t, s, true) // the real playback gate
	yes := true
	f.answers["movie:1"], f.answers["movie:2"] = &yes, &yes

	const hash = "0123456789abcdef0123456789abcdef01234567"
	s.warmMu.Lock()
	s.playing[hash] = 1
	s.warmMu.Unlock()
	if why := s.instant.busy(); why != "a stream plays" {
		t.Fatalf("busy = %q", why)
	}
	s.instant.lookup([]InstantItem{{Type: "movie", TmdbID: 1}})
	time.Sleep(100 * time.Millisecond)
	if f.count("movie:1") != 0 {
		t.Fatal("checked while a stream plays")
	}
	s.warmMu.Lock()
	s.playing[hash] = 0
	s.playEndAt[hash] = time.Now().Add(-time.Hour) // ended long ago
	s.warmMu.Unlock()
	instantWait(t, "the check after playback", func() bool { return f.count("movie:1") == 1 && s.instant.idle() })

	// A running check stops when a stream starts and runs again once it is quiet
	f.mu.Lock()
	f.hold = make(chan struct{})
	f.mu.Unlock()
	s.instant.lookup([]InstantItem{{Type: "movie", TmdbID: 2}})
	instantWait(t, "the running check", func() bool { return f.count("movie:2") == 1 })
	s.warmMu.Lock()
	s.starting[hash] = 1
	s.warmMu.Unlock()
	instantWait(t, "the check to stop", func() bool { return atomic.LoadInt32(&f.running) == 0 })
	if got := s.instant.lookup([]InstantItem{{Type: "movie", TmdbID: 2}}); got["movie:2"] != nil {
		t.Fatal("a stopped check stored an answer")
	}
	f.mu.Lock()
	f.hold = nil
	f.mu.Unlock()
	s.warmMu.Lock()
	s.starting[hash] = 0
	s.warmMu.Unlock()
	instantWait(t, "the check again", func() bool { return f.count("movie:2") == 2 && s.instant.idle() })
	if got := s.instant.lookup([]InstantItem{{Type: "movie", TmdbID: 2}}); boolOf(got["movie:2"]) != "true" {
		t.Fatalf("after playback %s", boolOf(got["movie:2"]))
	}

	// A resolve (Play, detail page) holds checks too
	f2, _ := s.flights.start("k", func() (*tsResolveResult, error) { time.Sleep(80 * time.Millisecond); return nil, errors.New("x") })
	if why := s.instant.busy(); why != "a resolve runs" {
		t.Fatalf("busy during a resolve = %q", why)
	}
	<-f2.done
}

// Prewarm queues the feed's titles without an answer only when idle, at most
// tsInstantPrewarmPerHour an hour.
func TestInstantPrewarmBudget(t *testing.T) {
	s := newInstantService(t, newInstantTorBox(t), nil, false)
	s.instant.busyFn = func() string { return "" }
	var feed []InstantItem
	for i := 1; i <= 100; i++ {
		feed = append(feed, InstantItem{Type: "movie", TmdbID: i})
	}
	s.instant.answers["movie:1"] = tsInstantAnswer{cached: true, at: time.Now()}
	if n := s.instant.prewarmOnce(func() []InstantItem { return feed }); n != tsInstantPrewarmPerHour {
		t.Fatalf("queued %d, want %d", n, tsInstantPrewarmPerHour)
	}
	if s.instant.warm[0].TmdbID != 2 {
		t.Fatal("a title with a fresh answer was queued")
	}
	if n := s.instant.prewarmOnce(func() []InstantItem { return feed }); n != 0 {
		t.Fatalf("queued %d while the queue is not empty", n)
	}
	s.instant.mu.Lock()
	for _, it := range s.instant.warm {
		delete(s.instant.queued, it.key())
	}
	s.instant.warm = nil
	s.instant.mu.Unlock()
	if n := s.instant.prewarmOnce(func() []InstantItem { return feed }); n != 0 {
		t.Fatalf("queued %d past the hourly budget", n)
	}
}

func TestInstantHandler(t *testing.T) {
	gin.SetMode(gin.TestMode)
	s := newInstantService(t, newInstantTorBox(t), nil, false)
	f := newFakeInstant(t, s, false)
	f.hold = make(chan struct{})
	t.Cleanup(func() { close(f.hold) })
	s.instant.answers["movie:3"] = tsInstantAnswer{cached: true, at: time.Now()}
	r := gin.New()
	Register(r.Group("/api"), s)

	items := []map[string]any{{"type": "movie", "tmdb_id": 3}, {"type": "tv", "tmdb_id": 4, "season": 2, "episode": 1},
		{"type": "book", "tmdb_id": 5}, {"type": "movie", "tmdb_id": 0}}
	for i := 0; i < 150; i++ {
		items = append(items, map[string]any{"type": "movie", "tmdb_id": 1000 + i})
	}
	body, _ := json.Marshal(map[string]any{"items": items})
	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest(http.MethodPost, "/api/torrent-stream/instant", bytes.NewReader(body)))
	if w.Code != http.StatusOK {
		t.Fatalf("status %d: %s", w.Code, w.Body)
	}
	var resp struct {
		Enabled bool             `json:"enabled"`
		Results map[string]*bool `json:"results"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	if !resp.Enabled || boolOf(resp.Results["movie:3"]) != "true" || resp.Results["tv:4"] != nil {
		t.Fatalf("answer %s", w.Body)
	}
	if _, ok := resp.Results["book:5"]; ok || len(resp.Results) != tsInstantMaxItems-2 {
		t.Fatalf("%d results (invalid items must be dropped, at most %d asked)", len(resp.Results), tsInstantMaxItems)
	}

	w = httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest(http.MethodPost, "/api/torrent-stream/instant", strings.NewReader(`{"items":"x"}`)))
	if w.Code != http.StatusBadRequest {
		t.Fatalf("bad body: status %d", w.Code)
	}
}
