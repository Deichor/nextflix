package streaming

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
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

// A read that ends (EOF) before the verdict window could judge it is judged by the rate of
// the whole read: a fast source finishes the rest of a short episode in a few seconds.
func TestMeasureHealthShortFastRead(t *testing.T) {
	fastWarm(t)
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if _, ok := r.URL.Query()["stat"]; ok {
			fmt.Fprint(w, `{"download_speed":0,"active_peers":3,"connected_seeders":2}`)
			return
		}
		w.WriteHeader(http.StatusPartialContent)
		w.Write(make([]byte, 4<<20))
	}))
	defer ts.Close()
	h := NewService(nil, Config{TorrServerURL: ts.URL}).measureHealth(context.Background(), strings.Repeat("a", 40), 1, int64(tsMB), 3*time.Second)
	if h == nil || !h.KeepsUp || h.partial || h.Rate < 2*int64(tsMB) {
		t.Fatalf("short fast read: %v", h)
	}
}

// cacheTorrServer is TorrServer for one torrent (file 1 of size bytes) whose RAM cache holds
// the bytes from cacheFrom to cacheTo: a read starting there gets them at once and the torrent
// downloads nothing; elsewhere the swarm delivers rate bytes/s, counted in TorrServer's
// downloaded bytes (bytes_read_useful_data, 500 MB before).
func cacheTorrServer(t *testing.T, hash string, size, cacheFrom, cacheTo int64, rate, peers, seeders int) *httptest.Server {
	var got atomic.Int64
	got.Store(500 << 20)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if _, ok := r.URL.Query()["stat"]; ok {
			fmt.Fprintf(w, `{"hash":%q,"name":"Movie.2020.1080p.WEB-DL.x264-GRP","download_speed":%d,"active_peers":%d,"connected_seeders":%d,`+
				`"bytes_read_useful_data":%d,"file_stats":[{"id":1,"path":"Movie.2020.1080p.WEB-DL.x264-GRP.mkv","length":%d}]}`,
				hash, rate, peers, seeders, got.Load(), size)
			return
		}
		var off int64
		fmt.Sscanf(r.Header.Get("Range"), "bytes=%d-", &off)
		w.WriteHeader(http.StatusPartialContent)
		cached := off >= cacheFrom && off < cacheTo
		chunk := make([]byte, max(rate/20, 1))
		if cached {
			chunk = make([]byte, 1<<20)
		}
		for {
			if _, err := w.Write(chunk); err != nil {
				return
			}
			w.(http.Flusher).Flush()
			if cached {
				continue
			}
			got.Add(int64(len(chunk)))
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

// The Mentalist S02E10: a reuse check read a file whose swarm had one peer at 1157 MB/s,
// "keeps up". Those were bytes TorrServer still held from the playback minutes before; the
// torrent downloaded nothing meanwhile. Such a read is no swarm measurement.
func TestMeasureHealthIgnoresTorrServerCache(t *testing.T) {
	fastWarm(t)
	hash := strings.Repeat("a", 40)
	ts := cacheTorrServer(t, hash, 6000<<20, 0, 6000<<20, 0, 1, 0)
	h := NewService(nil, Config{TorrServerURL: ts.URL}).measureHealth(context.Background(), hash, 1, int64(tsMB), 2*time.Second)
	if h == nil || h.KeepsUp || !h.cached || h.Rate >= int64(tsMB) {
		t.Fatalf("cached read: %v", h)
	}

	// A swarm that really delivers (TorrServer's downloaded bytes grow with the read) keeps up
	ts = cacheTorrServer(t, hash, 6000<<20, 0, 0, 8<<20, 9, 4)
	s := NewService(nil, Config{TorrServerURL: ts.URL})
	h = s.measureHealth(context.Background(), hash, 1, int64(tsMB), 2*time.Second)
	if h == nil || !h.KeepsUp || h.cached {
		t.Fatalf("live swarm: %v", h)
	}
	if _, ok := s.recentSwarm(hash, 1); !ok {
		t.Fatal("the swarm verdict was not recorded")
	}
}

// A saved source read from TorrServer's cache where playback resumes is measured again half
// the file away: a dead swarm there sends the player to a fresh resolve; when everything read
// was cached the source is unmeasured and plays.
func TestReuseMeasuresBeyondTorrServerCache(t *testing.T) {
	f := newFakeTorBox(t)
	hash := strings.Repeat("c", 40)
	// need = 600 MB / 100 min = 100 KB/s; the resume point (about 164 MB) is cached, the swarm
	// delivers 40 KB/s
	ts := cacheTorrServer(t, hash, 600_000_000, 100<<20, 300<<20, 40_000, 1, 0)
	s := reuseSetup(t, f, ts.URL, false)
	ans := reuseRun(s, hash, 1)
	if ans.Action != "resolve" || !strings.Contains(ans.Reason, "does not keep up") {
		t.Fatalf("answer %+v, want resolve", ans)
	}

	all := cacheTorrServer(t, hash, 600_000_000, 0, 600_000_000, 40_000, 1, 0)
	s = reuseSetup(t, f, all.URL, false)
	ans = reuseRun(s, hash, 1)
	if ans.Action != "play" || ans.Source == nil || ans.Source.Health != nil {
		t.Fatalf("answer %+v %+v, want an unmeasured play", ans, ans.Source)
	}
	if _, ok := s.recentSwarm(hash, 1); ok {
		t.Fatal("a cached read was recorded as a swarm verdict")
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

// A health before its verdict says so in the answer; a finished one leaves the field out.
func TestHealthJSONMeasuring(t *testing.T) {
	h := &tsHealth{Rate: 100, Need: 200, partial: true}
	b, err := json.Marshal(h)
	if err != nil || !strings.Contains(string(b), `"measuring":true`) || !strings.Contains(string(b), `"need":200`) {
		t.Fatalf("partial: %s %v", b, err)
	}
	h.partial = false
	if b, _ := json.Marshal(struct{ H *tsHealth }{h}); strings.Contains(string(b), "measuring") {
		t.Fatalf("final: %s", b)
	}
	var back tsHealth
	if err := json.Unmarshal(b, &back); err != nil || back.Need != 200 {
		t.Fatalf("round trip: %+v %v", back, err)
	}
}
