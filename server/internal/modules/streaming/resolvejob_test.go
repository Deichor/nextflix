package streaming

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
)

func parseQueryOf(s *Service, query string) (tsResolveQuery, bool, int) {
	gin.SetMode(gin.TestMode)
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest("GET", "/?"+query, nil)
	q, ok := handler{s: s}.parseQuery(c)
	return q, ok, w.Code
}

// A title in another script is fine with a tmdb_id: the English title replaces it.
func TestParseQueryNonLatinTitle(t *testing.T) {
	jp := url.QueryEscape("オデッセイ")
	s := NewService(nil, Config{TMDB: odysseyTMDB{}})
	q, ok, _ := parseQueryOf(s, "type=movie&tmdb_id=7&title="+jp)
	if !ok || q.title != "The Odyssey" {
		t.Fatalf("title %q ok %v", q.title, ok)
	}
	// Without a tmdb_id, or when TMDB has no title, nothing is searchable
	if _, ok, code := parseQueryOf(s, "type=movie&title="+jp); ok || code != http.StatusBadRequest {
		t.Fatalf("no tmdb_id: ok %v code %d", ok, code)
	}
	if _, ok, code := parseQueryOf(NewService(nil, Config{}), "type=movie&tmdb_id=7&title="+jp); ok || code != http.StatusBadRequest {
		t.Fatalf("no TMDB: ok %v code %d", ok, code)
	}
	if _, ok, code := parseQueryOf(s, "type=movie&tmdb_id=7&title="); ok || code != http.StatusBadRequest {
		t.Fatalf("empty title: ok %v code %d", ok, code)
	}
}

func doneFlight(hash string) *tsFlight {
	f := &tsFlight{done: make(chan struct{}), res: &tsResolveResult{Hash: hash}}
	close(f.done)
	return f
}

// A refresh with ?avoid= that joins a job started without it gets a fresh job, not the
// avoided torrent back.
func TestJoinAvoidingRunsAgain(t *testing.T) {
	bad, good := strings.Repeat("a", 40), strings.Repeat("b", 40)
	avoid := []string{bad}
	var starts int
	var dropped string
	start := func() (*tsFlight, bool) {
		starts++
		if starts == 1 {
			return doneFlight(bad), false // joined an older job
		}
		return doneFlight(good), true
	}
	f, ok := tsJoinAvoiding(context.Background(), start, avoid, func(h string) { dropped = h })
	if !ok || f.res.Hash != good || starts != 2 || dropped != bad {
		t.Fatalf("got %+v ok %v after %d starts (dropped %q)", f.res, ok, starts, dropped)
	}

	// A job this request started knew the list: its pick stands (everything else failed)
	starts = 0
	own := func() (*tsFlight, bool) { starts++; return doneFlight(bad), true }
	if f, ok := tsJoinAvoiding(context.Background(), own, avoid, func(string) { t.Fatal("dropped") }); !ok || f.res.Hash != bad || starts != 1 {
		t.Fatalf("own job: %+v after %d starts", f.res, starts)
	}

	// At most one fresh job
	starts = 0
	always := func() (*tsFlight, bool) { starts++; return doneFlight(bad), false }
	if _, ok := tsJoinAvoiding(context.Background(), always, avoid, func(string) {}); !ok || starts != 2 {
		t.Fatalf("%d starts", starts)
	}

	// The viewer gives up
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	running := &tsFlight{done: make(chan struct{})}
	if _, ok := tsJoinAvoiding(ctx, func() (*tsFlight, bool) { return running, false }, avoid, func(string) {}); ok || running.waiters.Load() != 0 {
		t.Fatalf("ok %v waiters %d", ok, running.waiters.Load())
	}
}

// cachedResolve stores res as the cached source of a movie and calls GET /resolve for it; the
// search a fresh resolve would run returns fresh and records its avoid list.
func cachedResolve(t *testing.T, s *Service, res tsResolveResult, fresh string) (got tsResolveResult, avoid []string, searched bool) {
	t.Helper()
	gin.SetMode(gin.TestMode)
	s.dropFn = func(string) {}
	query := "type=movie&title=Movie&year=2020&runtime=100&hevc=1&t=1646"
	q, ok, _ := parseQueryOf(s, query)
	if !ok {
		t.Fatal("query")
	}
	s.resolveCache.Store(tsResolveKey(q), tsCacheEntry{res: res, expires: time.Now().Add(time.Hour)})
	s.findFn = func(ctx context.Context, q tsResolveQuery) (*tsResolveResult, *tsUpgradeSeed, error) {
		searched, avoid = true, q.avoid
		return &tsResolveResult{Hash: fresh, Index: 11, Quality: "1080p", Magnet: tsBuildMagnet(fresh, "Movie", nil)}, nil, nil
	}
	r := gin.New()
	Register(r.Group("/api"), s)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest("GET", "/api/torrent-stream/resolve?"+query, nil))
	if w.Code != http.StatusOK || json.Unmarshal(w.Body.Bytes(), &got) != nil {
		t.Fatalf("resolve: %d %s", w.Code, w.Body.String())
	}
	return got, avoid, searched
}

func cachedTorrent(hash string, size int64) tsResolveResult {
	return tsResolveResult{Magnet: tsBuildMagnet(hash, "Movie (2020)", nil), Hash: hash, Index: 1, FileName: "Movie.2020.1080p.mkv",
		TorrentTitle: "Movie (2020)", Quality: "1080p", Size: size, DurationVerified: true,
		Health: newHealth(2_000_000, 100_000, 1, 1)} // 2 MB/s for 0.1 MB/s, hours ago
}

// The Mentalist S02E10 (TorBox paused): resolve answered with the cached 52d6688e, measured
// hours before, without a look at its swarm; the TV got no data for 11.6 s and searched
// again. A cached swarm source not measured lately is measured first, and one that starves
// now gives way to a fresh resolve without it.
func TestResolveCachedStaleSwarmStarvingResolvesFresh(t *testing.T) {
	f := newFakeTorBox(t)
	cached, fresh := strings.Repeat("5", 40), strings.Repeat("6", 40)
	var hits atomic.Int32
	// need = 600 MB / 100 min = 100 KB/s; the swarm delivers 40 KB/s now
	ts := reuseTorrServer(t, cached, 600_000_000, 40_000, &hits)
	s := reuseSetup(t, f, ts.URL, false)
	got, avoid, searched := cachedResolve(t, s, cachedTorrent(cached, 600_000_000), fresh)
	if got.Hash != fresh || !searched || !tsHashIn(cached, avoid) {
		t.Fatalf("answer %s, searched %v avoiding %v; want a fresh resolve without %s", got.Hash, searched, avoid, cached)
	}
	if hits.Load() == 0 {
		t.Fatal("the cached swarm was not measured")
	}
}

// A cached source measured a minute ago (the pick of a prefetch) plays without a new measurement.
func TestResolveCachedRecentlyMeasuredPlays(t *testing.T) {
	f := newFakeTorBox(t)
	cached := strings.Repeat("5", 40)
	var hits atomic.Int32
	ts := reuseTorrServer(t, cached, 600_000_000, 40_000, &hits)
	s := reuseSetup(t, f, ts.URL, false)
	res := cachedTorrent(cached, 600_000_000)
	s.noteSwarm(cached, 1, res.Health)
	got, _, searched := cachedResolve(t, s, res, strings.Repeat("6", 40))
	if got.Hash != cached || !got.Cached || searched || hits.Load() != 0 {
		t.Fatalf("answer %s cached %v, searched %v, TorrServer asked %d times; want the cached source as it is", got.Hash, got.Cached, searched, hits.Load())
	}

	// A swarm that keeps up when measured again plays too, with the new health
	fast := reuseTorrServer(t, cached, 600_000_000, 2_000_000, &hits)
	s = reuseSetup(t, f, fast.URL, false)
	got, _, searched = cachedResolve(t, s, cachedTorrent(cached, 600_000_000), strings.Repeat("6", 40))
	if got.Hash != cached || searched || got.Health == nil || !got.Health.KeepsUp || got.Health.ActivePeers != 4 {
		t.Fatalf("answer %s %v, searched %v; want the measured cached source", got.Hash, got.Health, searched)
	}
	if _, ok := s.recentSwarm(cached, 1); !ok {
		t.Fatal("the measurement was not recorded")
	}
}

// A cached source TorBox plays (TorBox on, not paused) never waits for a swarm check.
func TestResolveCachedTorBoxSourceSkipsSwarmCheck(t *testing.T) {
	f := newFakeTorBox(t)
	var hits atomic.Int32
	ts := reuseTorrServer(t, tbHash, 3<<20, 40_000, &hits)
	s := reuseSetup(t, f, ts.URL, true)
	registerVideo(t, s)
	res := cachedTorrent(tbHash, 3<<20)
	res.Debrid, res.Source = true, tsSourceTorBox
	got, _, searched := cachedResolve(t, s, res, strings.Repeat("6", 40))
	if got.Hash != tbHash || !got.Debrid || searched || hits.Load() != 0 {
		t.Fatalf("answer %s debrid %v, searched %v, TorrServer asked %d times", got.Hash, got.Debrid, searched, hits.Load())
	}
}

// The player appends the torrents it leaves to ?avoid=: the newest tsAvoidMax count.
func TestAvoidKeepsTheNewest(t *testing.T) {
	hs := []string{hx('1'), hx('2'), hx('3'), hx('4'), hx('5'), hx('6'), hx('7')}
	q, ok := parseResolveQuery(func() *gin.Context {
		gin.SetMode(gin.TestMode)
		c, _ := gin.CreateTestContext(httptest.NewRecorder())
		c.Request = httptest.NewRequest("GET", "/?type=movie&title=Detour&avoid="+strings.Join(hs, ",")+",bad", nil)
		return c
	}())
	if !ok || len(q.avoid) != tsAvoidMax || q.avoid[0] != hx('3') || q.avoid[tsAvoidMax-1] != hx('7') {
		t.Fatalf("avoid %v", q.avoid)
	}
}
