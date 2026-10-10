package streaming

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
)

func hx(c byte) string { return strings.Repeat(string(c), 40) }

// dropRecorder replaces TorrServer's drop and lists what was dropped.
type dropRecorder struct {
	mu      sync.Mutex
	dropped map[string]int
}

func newWarmService(t *testing.T) (*Service, *dropRecorder) {
	settle := tsReleaseSettle
	tsReleaseSettle = 0
	t.Cleanup(func() { tsReleaseSettle = settle })
	s := NewService(nil, Config{TorrServerURL: "http://127.0.0.1:1"})
	rec := &dropRecorder{dropped: map[string]int{}}
	s.dropFn = func(h string) {
		rec.mu.Lock()
		rec.dropped[h]++
		rec.mu.Unlock()
	}
	s.pingFn = func(string) {}
	return s, rec
}

// list waits a moment for the drops (they run in goroutines) and returns them sorted.
func (r *dropRecorder) list() string {
	time.Sleep(60 * time.Millisecond)
	r.mu.Lock()
	defer r.mu.Unlock()
	var out []string
	for h := range r.dropped {
		out = append(out, h[:1])
	}
	sort.Strings(out)
	return strings.Join(out, ",")
}

func (r *dropRecorder) reset() {
	r.mu.Lock()
	r.dropped = map[string]int{}
	r.mu.Unlock()
}

func result(hash, quality string, rate float64) *tsResolveResult {
	r := measured(hash, quality, rate, 1)
	r.Quality = quality
	return r
}

// A finished resolve: the pick stays, two alternatives of its resolution become spares, a
// third and a 720p are dropped at once.
func TestAfterPickDropsAllButPickAndSpares(t *testing.T) {
	s, rec := newWarmService(t)
	q := tsResolveQuery{mediaType: "movie", title: "Night of the Living Dead", year: 1968}
	key := tsResolveKey(q)
	q.warm = s.holdWarm(key)
	res := []*tsResolveResult{result(hx('a'), "1080p", 2), result(hx('b'), "1080p", 3), result(hx('c'), "1080p", 3), result(hx('d'), "1080p", 3), result(hx('e'), "720p", 3)}
	cands := make([]tsCandidate, len(res))
	for i, r := range res {
		cands[i] = tsCandidate{quality: tsQualityHeight(r.Quality)}
		s.trackTorrent(q.warm, r.Hash)
	}
	p := &tsPick{best: 0, res: res[0], results: res, kept: make([]bool, len(res)), errs: make([]error, len(res)), later: make(chan tsOutcome)}
	ctx, cancel := context.WithCancel(context.Background())
	s.afterPick(key, res[0], &tsUpgradeSeed{q: q, cands: cands, pick: p, ctx: ctx, cancel: cancel})
	if got := rec.list(); got != "d,e" {
		t.Fatalf("dropped %q, want the third alternative and the 720p", got)
	}
	if len(q.warm.spares) != 2 || q.warm.source != hx('a') {
		t.Fatalf("spares %d source %s", len(q.warm.spares), q.warm.source[:1])
	}
	if ctx.Err() == nil {
		t.Fatal("background warm-up context still running without an upgrade or pending check")
	}
}

// A higher resolution warming for an upgrade stays until its window ends unused.
func TestAfterPickUpgradeWindowEndDropsCandidate(t *testing.T) {
	s, rec := newWarmService(t)
	q := tsResolveQuery{mediaType: "movie", title: "Big Buck Bunny", hevc: true}
	key := tsResolveKey(q)
	q.warm = s.holdWarm(key)
	hd, uhd := result(hx('a'), "1080p", 2), result(hx('f'), "2160p", 0.5) // the 4K starves
	for _, r := range []*tsResolveResult{hd, uhd} {
		s.trackTorrent(q.warm, r.Hash)
	}
	p := &tsPick{best: 1, res: hd, results: []*tsResolveResult{uhd, hd}, kept: []bool{false, false}, errs: make([]error, 2), later: make(chan tsOutcome)}
	ctx, cancel := context.WithCancel(context.Background())
	s.afterPick(key, hd, &tsUpgradeSeed{q: q, cands: []tsCandidate{{quality: 2160}, {quality: 1080}}, pick: p, ctx: ctx, cancel: cancel})
	if got := rec.list(); got != "" {
		t.Fatalf("dropped %q while the 4K is still warming", got)
	}
	cancel() // the upgrade window ends
	deadline := time.Now().Add(2 * time.Second)
	for rec.list() != "f" && time.Now().Before(deadline) {
	}
	if got := rec.list(); got != "f" {
		t.Fatalf("dropped %q, want the unused 4K once its window ended", got)
	}
}

func TestReleaseTitleDropsAllButPlaying(t *testing.T) {
	s, rec := newWarmService(t)
	q := tsResolveQuery{mediaType: "tv", title: "Pioneer One", season: 1, episode: 1, hevc: true}
	w := s.holdWarm(tsResolveKey(q))
	s.setSource(w, hx('a'))
	s.addSpare(w, result(hx('b'), "1080p", 3))
	s.trackTorrent(w, hx('c'))
	// A capped variant of the same episode (after a step down) goes with it
	capped := q
	capped.maxQ = 1080
	w2 := s.holdWarm(tsResolveKey(capped))
	s.setSource(w2, hx('d'))
	// c played and its player closed a moment ago: the guard keeps it
	s.playBegin(hx('c'))
	s.playEnd(hx('c'))

	if n := s.releaseTitle(tsResolveBaseKey(q), 0); n != 2 {
		t.Fatalf("released %d titles, want both capability variants", n)
	}
	if got := rec.list(); got != "a,b,d" {
		t.Fatalf("dropped %q, want everything but the playing torrent", got)
	}
	if w.ctx.Err() == nil || w2.ctx.Err() == nil {
		t.Fatal("background work of the released title not cancelled")
	}
	// A torrent a cancelled check adds after the release is dropped at once
	rec.reset()
	s.trackTorrent(w, hx('e'))
	if got := rec.list(); got != "e" {
		t.Fatalf("late torrent of a released title: dropped %q", got)
	}
}

func TestReleaseKeepsTorrentAnotherTitleHolds(t *testing.T) {
	s, rec := newWarmService(t)
	ep1 := tsResolveQuery{mediaType: "tv", title: "Pioneer One", season: 1, episode: 1}
	ep2 := ep1
	ep2.episode = 2
	pack := hx('p') // a season pack serves both episodes
	s.setSource(s.holdWarm(tsResolveKey(ep1)), pack)
	s.setSource(s.holdWarm(tsResolveKey(ep2)), pack)
	s.releaseTitle(tsResolveBaseKey(ep1), 0)
	if got := rec.list(); got != "" {
		t.Fatalf("dropped %q although the next episode holds the pack", got)
	}
	s.releaseTitle(tsResolveBaseKey(ep2), 0)
	if got := rec.list(); got != "p" {
		t.Fatalf("dropped %q, want the pack once no title holds it", got)
	}
}

func TestDelayedReleaseAndIntent(t *testing.T) {
	s, rec := newWarmService(t)
	q := tsResolveQuery{mediaType: "movie", title: "Night of the Living Dead", year: 1968}
	key := tsResolveKey(q)
	s.setSource(s.holdWarm(key), hx('a'))
	s.releaseTitle(tsResolveBaseKey(q), time.Minute) // detail page left
	s.warmTick(time.Now().Add(30 * time.Second))
	if got := rec.list(); got != "" {
		t.Fatalf("dropped %q during the grace", got)
	}
	s.holdWarm(key) // the viewer came back
	s.warmTick(time.Now().Add(2 * time.Minute))
	if got := rec.list(); got != "" {
		t.Fatalf("dropped %q after the viewer came back", got)
	}
	s.releaseTitle(tsResolveBaseKey(q), time.Minute)
	s.warmTick(time.Now().Add(61 * time.Second))
	if got := rec.list(); got != "a" {
		t.Fatalf("dropped %q, want the source after the grace", got)
	}
}

func TestHoldExpiresWithoutPlayback(t *testing.T) {
	s, rec := newWarmService(t)
	q := tsResolveQuery{mediaType: "movie", title: "Big Buck Bunny"}
	s.setSource(s.holdWarm(tsResolveKey(q)), hx('a'))
	s.lastUse[hx('a')] = time.Now().Add(-time.Hour)
	if ping := s.warmTick(time.Now()); len(ping) != 1 || ping[0] != hx('a') {
		t.Fatalf("held source not pinged: %v", ping)
	}
	s.warmTick(time.Now().Add(tsHoldTime + time.Second))
	if got := rec.list(); got != "a" {
		t.Fatalf("dropped %q, want the held source after the hold", got)
	}
}

func TestSparesLiveOnlyEarlyInPlayback(t *testing.T) {
	s, rec := newWarmService(t)
	q := tsResolveQuery{mediaType: "movie", title: "Big Buck Bunny"}
	// No playback within tsSpareIdle: spares go
	w := s.holdWarm(tsResolveKey(q))
	s.setSource(w, hx('a'))
	s.addSpare(w, result(hx('b'), "1080p", 3))
	s.warmTick(time.Now().Add(tsSpareIdle + time.Second))
	if got := rec.list(); got != "b" {
		t.Fatalf("dropped %q, want the spare without playback", got)
	}
	// Playback started: spares stay for its first minutes, then go; the source stays
	rec.reset()
	s.addSpare(w, result(hx('c'), "1080p", 3))
	s.playBegin(hx('a'))
	s.warmTick(time.Now().Add(2 * time.Minute))
	if got := rec.list(); got != "" {
		t.Fatalf("dropped %q early in playback", got)
	}
	s.warmTick(time.Now().Add(tsSpareWindow + time.Second))
	if got := rec.list(); got != "c" {
		t.Fatalf("dropped %q, want the spare after the window", got)
	}
}

func TestReleaseHashesAfterPlayerClose(t *testing.T) {
	s, rec := newWarmService(t)
	q := tsResolveQuery{mediaType: "movie", title: "Big Buck Bunny"}
	w := s.holdWarm(tsResolveKey(q))
	s.setSource(w, hx('a'))
	s.streamKeep = 40 * time.Millisecond
	s.playBegin(hx('a'))
	s.playEnd(hx('a'))
	s.releaseHashes([]string{hx('a')})
	if got := rec.list(); got != "" {
		t.Fatalf("dropped %q, its stream ended a moment ago", got)
	}
	// The janitor drops it once the stream guard ran out
	s.retryDrops(time.Now())
	if got := rec.list(); got != "a" {
		t.Fatalf("dropped %q, want the closed player's torrent after the guard", got)
	}
	rec.reset()
	s.playBegin(hx('b'))
	s.releaseHashes([]string{hx('b')})
	if got := rec.list(); got != "" {
		t.Fatalf("dropped %q while it plays", got)
	}
}

func TestSidegradeOffersFreshSpare(t *testing.T) {
	s, _ := newWarmService(t)
	q := tsResolveQuery{mediaType: "tv", title: "Pioneer One", season: 1, episode: 1}
	key := tsResolveKey(q)
	w := s.holdWarm(key)
	s.setSource(w, hx('a'))
	s.addSpare(w, result(hx('b'), "720p", 5))  // lower than playing: never
	s.addSpare(w, result(hx('c'), "1080p", 3)) // keeps up
	up := s.sidegradeFor(key, tsResolveQuery{}, hx('a'), 1080, tsSideOpts{})
	if up == nil || up.Hash != hx('c') {
		t.Fatalf("sidegrade %v, want the 1080p spare", up)
	}
	if c, ok := s.cachedSource(key); !ok || c.Hash != hx('c') {
		t.Fatal("sidegrade not stored as the next source")
	}
	s.addSpare(w, result(hx('d'), "1080p", 3))
	if s.sidegradeFor(key, tsResolveQuery{}, hx('c'), 1080, tsSideOpts{}) != nil {
		t.Fatal("second sidegrade within tsSidegradeEvery")
	}
}

func TestNextEpisodeWarmWindow(t *testing.T) {
	cases := []struct {
		pos, dur float64
		want     bool
	}{
		{30 * 60, 60 * 60, false},     // half way through a 60 min episode
		{49 * 60, 60 * 60, false},     // 11 min left
		{50 * 60, 60 * 60, true},      // 10 min left
		{58 * 60, 60 * 60, true},      // credits
		{17 * 60, 22 * 60, false},     // 77 % of a 22 min episode
		{18.7 * 60, 22 * 60, true},    // 85 %
		{0, 60 * 60, false},           // not started
		{10, 0, false},                // duration unknown
		{14.9 * 60, 24.9 * 60, false}, // 60 % (the old prefetch point)
	}
	for _, c := range cases {
		if got := tsNextWarmDue(c.pos, c.dur); got != c.want {
			t.Errorf("pos %.0f of %.0f: due=%v, want %v", c.pos, c.dur, got, c.want)
		}
	}
	gin.SetMode(gin.TestMode)
	s, _ := newWarmService(t)
	r := gin.New()
	Register(r.Group("/api"), s)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest(http.MethodPost, "/api/torrent-stream/prefetch?type=tv&title=Pioneer+One&season=1&episode=2&next_pos=1800&next_dur=3600", nil))
	if w.Code != http.StatusAccepted || !strings.Contains(w.Body.String(), `"early"`) {
		t.Fatalf("early next-episode prefetch: %d %s", w.Code, w.Body.String())
	}
	if len(s.warms) != 0 {
		t.Fatal("an early next-episode prefetch held the title")
	}
}

func TestReleaseEndpoint(t *testing.T) {
	gin.SetMode(gin.TestMode)
	s, rec := newWarmService(t)
	r := gin.New()
	Register(r.Group("/api"), s)
	post := func(query string) *httptest.ResponseRecorder {
		w := httptest.NewRecorder()
		r.ServeHTTP(w, httptest.NewRequest(http.MethodPost, "/api/torrent-stream/release?"+query, nil))
		return w
	}
	if w := post(""); w.Code != http.StatusBadRequest {
		t.Fatalf("empty release: %d", w.Code)
	}
	q := tsResolveQuery{mediaType: "movie", title: "Night of the Living Dead", year: 1968, hevc: true}
	s.setSource(s.holdWarm(tsResolveKey(q)), hx('a'))
	if w := post("type=movie&title=Night+of+the+Living+Dead&year=1968&delay=60"); w.Code != http.StatusAccepted || !strings.Contains(w.Body.String(), "scheduled") {
		t.Fatalf("delayed release: %d %s", w.Code, w.Body.String())
	}
	if got := rec.list(); got != "" {
		t.Fatalf("delayed release dropped %q at once", got)
	}
	// A delay too large for a Duration is capped, not wrapped into an immediate release
	if w := post("type=movie&title=Night+of+the+Living+Dead&year=1968&delay=9223372037"); w.Code != http.StatusAccepted || !strings.Contains(w.Body.String(), "scheduled") {
		t.Fatalf("huge delay: %d %s", w.Code, w.Body.String())
	}
	if got := rec.list(); got != "" {
		t.Fatalf("huge delay dropped %q at once", got)
	}
	if w := post("type=movie&title=Night+of+the+Living+Dead&year=1968&hash=" + hx('b')); w.Code != http.StatusAccepted {
		t.Fatalf("release: %d", w.Code)
	}
	if got := rec.list(); got != "a,b" {
		t.Fatalf("dropped %q, want the title's source and the given hash", got)
	}
}

func TestSpareThatKeepsUpReplacesStarvingOne(t *testing.T) {
	s, rec := newWarmService(t)
	w := s.holdWarm(tsResolveKey(tsResolveQuery{mediaType: "movie", title: "Big Buck Bunny"}))
	s.setSource(w, hx('a'))
	s.addSpare(w, result(hx('b'), "1080p", 0.5)) // starving
	s.addSpare(w, result(hx('c'), "1080p", 3))
	if s.addSpare(w, result(hx('d'), "1080p", 0.6)) {
		t.Fatal("a starving spare replaced another")
	}
	if !s.addSpare(w, result(hx('e'), "1080p", 4)) || w.spares[hx('b')] != nil {
		t.Fatal("a spare that keeps up did not replace the starving one")
	}
	if got := rec.list(); got != "b" {
		t.Fatalf("dropped %q, want the replaced spare", got)
	}
}
