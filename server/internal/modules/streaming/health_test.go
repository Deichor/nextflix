package streaming

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

const mbps = float64(tsMB) // bytes/s in 1 MB/s

// measured builds a result whose swarm delivered rate MB/s for a file needing need MB/s
func measured(name, quality string, rate, need float64) *tsResolveResult {
	return &tsResolveResult{Hash: name, Quality: quality, Health: newHealth(int64(rate*mbps), int64(need*mbps), 3, 2)}
}

func TestNeedRate(t *testing.T) {
	// Night of the Living Dead 2160p YTS: 4.67 GB, 96 min
	need := float64(tsNeedRate(4670*tsMB, 96, "movie")) / mbps
	if need < 0.78 || need > 0.84 {
		t.Fatalf("need %.2f MB/s, want about 0.81", need)
	}
	if got, want := tsNeedRate(110*60*tsMB, 0, "movie"), tsMB; got != want {
		t.Fatalf("movie default runtime: got %d, want %d", got, want)
	}
	if got, want := tsNeedRate(50*60*tsMB, 0, "tv"), tsMB; got != want {
		t.Fatalf("episode default runtime: got %d, want %d", got, want)
	}
	if tsNeedRate(0, 90, "movie") != 0 {
		t.Fatal("unknown size must not need a rate")
	}
}

func TestKeepUpThreshold(t *testing.T) {
	if !newHealth(130, 100, 0, 0).KeepsUp {
		t.Fatal("1.3x the needed rate keeps up")
	}
	if newHealth(129, 100, 0, 0).KeepsUp {
		t.Fatal("below 1.3x the needed rate is starving")
	}
}

func TestChooseHealthy4KWins(t *testing.T) {
	res := []*tsResolveResult{measured("4k", "2160p", 6, 2.5), measured("hd", "1080p", 9, 0.4)}
	if i, final := tsChoose(res, []bool{true, true}); i != 0 || !final {
		t.Fatalf("got %d final=%v, want the healthy 4K", i, final)
	}
	// It wins as soon as it is done, without waiting for worse-ranked checks
	if i, final := tsChoose([]*tsResolveResult{res[0], nil}, []bool{true, false}); i != 0 || !final {
		t.Fatalf("got %d final=%v, want an early 4K pick", i, final)
	}
}

func TestChooseStarving4KLosesToHealthy1080p(t *testing.T) {
	// Measured live: 0.29 MB/s from 3 active peers for a 4K needing 0.81 MB/s
	starving := measured("4k", "2160p", 0.29, 0.81)
	healthy := measured("hd", "1080p", 2.0, 0.3)

	// The 1080p check is still running: the starving 4K is not final
	if i, final := tsChoose([]*tsResolveResult{starving, nil}, []bool{true, false}); i != 0 || final {
		t.Fatalf("got %d final=%v, want to wait for the 1080p", i, final)
	}
	if i, final := tsChoose([]*tsResolveResult{starving, healthy}, []bool{true, true}); i != 1 || !final {
		t.Fatalf("got %d final=%v, want the healthy 1080p", i, final)
	}
	// An unmeasured source counts as healthy, so a failed measurement never hides it
	unknown := &tsResolveResult{Hash: "x"}
	if i, _ := tsChoose([]*tsResolveResult{starving, unknown}, []bool{true, true}); i != 1 {
		t.Fatalf("got %d, want the unmeasured source", i)
	}
}

func TestChooseClosestWhenNoneKeepsUp(t *testing.T) {
	res := []*tsResolveResult{measured("4k", "2160p", 0.3, 2.5), nil, measured("hd", "1080p", 0.3, 0.4)}
	if i, final := tsChoose(res, []bool{true, true, true}); i != 2 || !final {
		t.Fatalf("got %d final=%v, want the one closest to keeping up", i, final)
	}
}

func TestPickBestStarvingWaitsForHealthy(t *testing.T) {
	ms := time.Millisecond
	check := func(delays []time.Duration, res []*tsResolveResult) func(context.Context, int) (*tsResolveResult, error) {
		return func(ctx context.Context, i int) (*tsResolveResult, error) {
			select {
			case <-time.After(delays[i]):
				return res[i], nil
			case <-ctx.Done():
				return nil, ctx.Err()
			}
		}
	}
	res := []*tsResolveResult{measured("4k", "2160p", 0.29, 0.81), measured("hd", "1080p", 2, 0.3)}

	// The starving 4K answers first; the healthy 1080p still wins
	i, got, _ := tsPickBest(context.Background(), 2, 10*ms, check([]time.Duration{5 * ms, 60 * ms}, res))
	if i != 1 || got.Hash != "hd" {
		t.Fatalf("got %d %v, want the healthy 1080p", i, got)
	}

	// Nothing healthy arrives within tsStarvingGrace: the starving one is better than nothing
	old := tsStarvingGrace
	tsStarvingGrace = 50 * ms
	defer func() { tsStarvingGrace = old }()
	start := time.Now()
	i, _, _ = tsPickBest(context.Background(), 2, 10*ms, check([]time.Duration{5 * ms, 5 * time.Second}, res))
	if i != 0 || time.Since(start) > time.Second {
		t.Fatalf("got %d after %s, want the starving 4K after the grace", i, time.Since(start))
	}
}

func TestRankMaxQualityCap(t *testing.T) {
	raw := fixture(
		300, "Dune 2021 2160p WEB-DL DDP5.1 HDR10 H265", 15.0,
		200, "Dune 2021 1080p BluRay x264", 3.0,
		100, "Dune 2021 720p BluRay x264", 1.2,
	)
	q := tsResolveQuery{mediaType: "movie", title: "Dune", year: 2021, hevc: true}
	if c := tsRankCandidates(raw, q); len(c) != 3 || c[0].quality != 2160 {
		t.Fatalf("uncapped: want the 4K first, got %q", titles(c))
	}
	q.maxQ = 1080
	c := tsRankCandidates(raw, q)
	if len(c) != 2 || c[0].quality != 1080 {
		t.Fatalf("maxq=1080: got %q", titles(c))
	}
	q.maxQ = 720
	if c := tsRankCandidates(raw, q); len(c) != 1 || c[0].quality != 720 {
		t.Fatalf("maxq=720: got %q", titles(c))
	}
}

func TestResolveKeyIncludesCap(t *testing.T) {
	q := tsResolveQuery{mediaType: "movie", title: "Night of the Living Dead", year: 1968, hevc: true}
	base := tsResolveKey(q)
	q.runtime = 96
	if tsResolveKey(q) != base {
		t.Fatal("runtime must not split the cache")
	}
	q.maxQ = 1080
	k1080 := tsResolveKey(q)
	q.maxQ = 720
	if k1080 == base || tsResolveKey(q) == base || tsResolveKey(q) == k1080 {
		t.Fatal("each quality cap needs its own cache key")
	}
}

// fakeTorrServer serves a file at rate bytes/s and a stat with the given download speed
func fakeTorrServer(rate int, speed float64) *httptest.Server {
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if _, ok := r.URL.Query()["stat"]; ok {
			fmt.Fprintf(w, `{"download_speed":%f,"active_peers":3,"connected_seeders":2}`, speed)
			return
		}
		w.WriteHeader(http.StatusPartialContent)
		chunk := make([]byte, rate/20)
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
}

func TestMeasureHealth(t *testing.T) {
	fastWarm(t)
	hash := strings.Repeat("a", 40)
	need := int64(tsMB) // 1 MB/s

	// Fast swarm: stops early once the rate clearly keeps up
	fast := fakeTorrServer(8<<20, 0)
	defer fast.Close()
	start := time.Now()
	h := NewService(nil, Config{TorrServerURL: fast.URL}).measureHealth(context.Background(), hash, 1, need, 3*time.Second)
	if h == nil || !h.KeepsUp || time.Since(start) > 2500*time.Millisecond {
		t.Fatalf("fast swarm: %v after %s", h, time.Since(start))
	}

	// Thin swarm: 200 KB/s read, TorrServer reports 300 KB/s
	slow := fakeTorrServer(200<<10, 300<<10)
	defer slow.Close()
	h = NewService(nil, Config{TorrServerURL: slow.URL}).measureHealth(context.Background(), hash, 1, need, 2*time.Second)
	if h == nil || h.KeepsUp || h.Rate < 250<<10 || h.ActivePeers != 3 {
		t.Fatalf("thin swarm: %v", h)
	}
}

// Plan 9 from Outer Space: at the cap a BDRemux whose probe still ran (unknown length) won
// over the YTS release of verified length on a better rate / need, and the player waited
// ~11 s for that probe. A verified source that plays with some buffering (x0.5) wins; one
// that delivers less gives way to a clearly faster one (His Girl Friday: a verified x0.13
// lost to a YTS at x0.71 whose probe finished a second after the cap).
func TestCapPrefersVerifiedAlive(t *testing.T) {
	remux := measured("remux", "1080p", 2.4, 2.69) // x0.89
	remux.durationChecked, remux.partial = true, true
	remux.Health.partial, remux.Health.sampled = true, 5*time.Second
	yts := measured("yts", "1080p", 0.16, 0.29) // x0.55
	yts.durationChecked, yts.DurationVerified = true, true
	if i := tsChoosePlayable([]*tsResolveResult{remux, yts}); i != 1 {
		t.Fatalf("got %d, want the verified YTS", i)
	}
	yts.Health = measured("yts", "1080p", 0.04, 0.29).Health // x0.13
	if i := tsChoosePlayable([]*tsResolveResult{remux, yts}); i != 0 {
		t.Fatalf("got %d, want the clearly faster remux over a verified x0.13", i)
	}
	remux.Health = measured("remux", "1080p", 0.5, 2.69).Health // x0.19: not clearly faster
	remux.Health.partial, remux.Health.sampled = true, 5*time.Second
	if i := tsChoosePlayable([]*tsResolveResult{remux, yts}); i != 1 {
		t.Fatalf("got %d, want the verified YTS", i)
	}
}
