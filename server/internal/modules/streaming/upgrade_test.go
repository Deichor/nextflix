package streaming

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
)

// verified marks a measured result as checked against a known runtime
func verified(r *tsResolveResult, ok bool) *tsResolveResult {
	r.durationChecked, r.DurationVerified = true, ok
	return r
}

func TestChooseVerifiedBeatsUnknownDuration(t *testing.T) {
	// The Odyssey: the probe of the better-ranked release timed out, the other one fits
	unknown := verified(measured("lama", "1080p", 5, 1), false)
	fits := verified(measured("ok", "1080p", 5, 1), true)
	if i, final := tsChoose([]*tsResolveResult{unknown, fits}, []bool{true, true}); i != 1 || !final {
		t.Fatalf("got %d final=%v, want the verified one", i, final)
	}
	// ... also over an unknown one of a higher resolution, and when the verified one starves
	unknown4K := verified(measured("4k", "2160p", 9, 2), false)
	starving := verified(measured("slow", "1080p", 0.5, 1), true)
	if i, _ := tsChoose([]*tsResolveResult{unknown4K, starving}, []bool{true, true}); i != 1 {
		t.Fatalf("got %d, want the verified 1080p", i)
	}
	// While a check is running, an unknown one is never final
	if _, final := tsChoose([]*tsResolveResult{unknown, nil}, []bool{true, false}); final {
		t.Fatal("unknown duration picked while a check is still running")
	}
	// Without a verified one the unknown one is picked, and the result says so
	if i, final := tsChoose([]*tsResolveResult{nil, unknown}, []bool{true, true}); i != 1 || !final || unknown.DurationVerified {
		t.Fatalf("got %d final=%v, want the unknown one as a last resort", i, final)
	}
	// No runtime known: nothing is verified, the old order applies
	plain := measured("a", "2160p", 9, 2)
	if i, _ := tsChoose([]*tsResolveResult{plain, fits}, []bool{true, true}); i != 0 {
		t.Fatalf("got %d, want the best-ranked when no runtime is known", i)
	}
}

func TestPickWaitsForVerifiedCandidate(t *testing.T) {
	ms := time.Millisecond
	old := tsStarvingGrace
	tsStarvingGrace = time.Second
	defer func() { tsStarvingGrace = old }()
	res := []*tsResolveResult{verified(measured("unknown", "1080p", 5, 1), false), verified(measured("fits", "1080p", 5, 1), true)}
	delays := []time.Duration{5 * ms, 80 * ms}
	i, got, _ := tsPickBest(context.Background(), 2, 10*ms, func(ctx context.Context, i int) (*tsResolveResult, error) {
		time.Sleep(delays[i])
		return res[i], nil
	})
	if i != 1 || got.Hash != "fits" {
		t.Fatalf("got %d, want the verified candidate that answered later", i)
	}
}

// pickWithSlow4K: candidate 0 is a 4K that is still warming when the healthy 1080p (1)
// answers; it finishes keeping up after slow.
func pickWithSlow4K(slow time.Duration) (*tsPick, context.CancelFunc) {
	cands := []tsCandidate{{quality: 2160}, {quality: 1080}}
	res := []*tsResolveResult{measured("4k", "2160p", 6, 2.5), measured("hd", "1080p", 4, 1)}
	res[0].Quality, res[1].Quality = "2160p", "1080p"
	delays := []time.Duration{slow, 5 * time.Millisecond}
	warmCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	keep := func(i int, pick *tsResolveResult) bool {
		return pick != nil && cands[i].quality > tsQualityHeight(pick.Quality)
	}
	p := tsPickBestKeep(context.Background(), warmCtx, 2, func(int) time.Duration { return 20 * time.Millisecond }, keep,
		func(ctx context.Context, i int) (*tsResolveResult, error) {
			select {
			case <-time.After(delays[i]):
				return res[i], nil
			case <-ctx.Done():
				return nil, ctx.Err()
			}
		}, nil)
	return p, cancel
}

func TestUpgradeReturnsLowerNowAndUpgradesLater(t *testing.T) {
	start := time.Now()
	p, cancel := pickWithSlow4K(300 * time.Millisecond)
	if p.best != 1 || p.res.Hash != "hd" || !p.kept[0] || time.Since(start) > 200*time.Millisecond {
		t.Fatalf("got #%d kept=%v after %s, want the 1080p at once with the 4K still warming", p.best, p.kept, time.Since(start))
	}

	s := NewService(nil, Config{})
	q := tsResolveQuery{mediaType: "movie", title: "Night of the Living Dead", year: 1968, hevc: true}
	key := tsResolveKey(q)
	s.storeSource(key, *p.res)
	seed := &tsUpgradeSeed{q: q, cands: []tsCandidate{{quality: 2160}, {quality: 1080}}, pick: p, ctx: context.Background(), cancel: cancel}
	s.afterPick(key, p.res, seed)

	current := p.res.Hash
	if up := s.upgradeFor(key, q, current, 1080); up != nil {
		t.Fatalf("upgrade offered before the 4K keeps up: %v", up)
	}
	deadline := time.Now().Add(3 * time.Second)
	var up *tsResolveResult
	for up == nil && time.Now().Before(deadline) {
		time.Sleep(20 * time.Millisecond)
		up = s.upgradeFor(key, q, current, 1080)
	}
	if up == nil || up.Hash != "4k" {
		t.Fatalf("no upgrade to the 4K: %v", up)
	}
	// The next play starts in 4K straight from the source cache
	if c, ok := s.cachedSource(key); !ok || c.Hash != "4k" {
		t.Fatalf("source cache not upgraded: %v", c)
	}
	// Never offered to the source it came from, nor to one that already plays 4K
	if s.upgradeFor(key, q, "4k", 1080) != nil || s.upgradeFor(key, q, current, 2160) != nil {
		t.Fatal("upgrade offered to the wrong source")
	}
}

func TestUpgradeSkipsDeadHigherResolution(t *testing.T) {
	s := NewService(nil, Config{})
	q := tsResolveQuery{mediaType: "movie", title: "Night of the Living Dead", year: 1968, hevc: true}
	key := tsResolveKey(q)
	dead := measured(strings.Repeat("d", 40), "2160p", 0, 2.5)
	dead.Health.Dead = true
	hd := measured("hd", "1080p", 4, 1)
	hd.Quality = "1080p"
	p := &tsPick{best: 1, res: hd, results: []*tsResolveResult{dead, hd}, kept: []bool{false, false}, later: make(chan tsOutcome)}
	ctx, cancel := context.WithCancel(context.Background())
	seed := &tsUpgradeSeed{q: q, cands: []tsCandidate{{quality: 2160}, {quality: 1080}}, pick: p, ctx: ctx, cancel: cancel}
	s.afterPick(key, hd, seed)
	if ctx.Err() == nil {
		t.Fatal("a dead 4K was warmed for an upgrade")
	}
	if v, ok := s.upgrades.Load(key); ok && v.(*tsUpgrade).running {
		t.Fatal("upgrade watcher started without a live candidate")
	}
}

func TestUpgradeEndpointNoFlap(t *testing.T) {
	gin.SetMode(gin.TestMode)
	s := NewService(nil, Config{})
	r := gin.New()
	Register(r.Group("/api"), s)
	hash := strings.Repeat("a", 40)
	get := func(query string) int {
		w := httptest.NewRecorder()
		r.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/api/torrent-stream/upgrade?type=movie&title=Big+Buck+Bunny&"+query, nil))
		return w.Code
	}
	// After a step down the player resolves with maxq: never upgrade back up
	if code := get("hevc=1&maxq=1080&current=" + hash); code != http.StatusNoContent {
		t.Fatalf("capped request: %d", code)
	}
	// 4K is only an upgrade for clients that decode HEVC
	if code := get("current=" + hash); code != http.StatusNoContent {
		t.Fatalf("non-HEVC request: %d", code)
	}
	// Already 4K: nothing to search for
	if code := get("hevc=1&current_q=2160&current=" + hash); code != http.StatusNoContent {
		t.Fatalf("4K request: %d", code)
	}
	if code := get("hevc=1&current=nothex"); code != http.StatusBadRequest {
		t.Fatalf("bad hash: %d", code)
	}
}
