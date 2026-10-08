//go:build live

// Live comparison against TMDB (needs TMDB_API_KEY and internet):
//
//	go test -tags live -run Live -v -count=1 ./internal/platform/upstream/
package upstream

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"
)

type mode struct {
	name    string
	get     func(ctx context.Context, url string, h http.Header) (int, error)
	opts    *Client // nil for the plain client
	tryErrs *errCount
}

type errCount struct {
	mu sync.Mutex
	m  map[string]int
}

func (e *errCount) add(err error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.m[kind(err)]++
}

func kind(err error) string {
	msg := err.Error()
	for _, k := range []string{"reset", "stalled", "EOF", "handshake timeout", "awaiting headers", "i/o timeout", "deadline", "canceled", "status"} {
		if strings.Contains(msg, k) {
			return k
		}
	}
	return msg
}

// plainGet mirrors the old clients: one try, HTTP/2, long timeouts.
func plainGet(timeout time.Duration) func(context.Context, string, http.Header) (int, error) {
	c := &http.Client{
		Timeout: timeout,
		Transport: &http.Transport{
			Proxy:                 http.ProxyFromEnvironment,
			DialContext:           (&net.Dialer{Timeout: 10 * time.Second, KeepAlive: 30 * time.Second}).DialContext,
			ForceAttemptHTTP2:     true,
			MaxIdleConns:          32,
			MaxIdleConnsPerHost:   12,
			IdleConnTimeout:       90 * time.Second,
			TLSHandshakeTimeout:   10 * time.Second,
			ResponseHeaderTimeout: 15 * time.Second,
		},
	}
	return func(ctx context.Context, url string, h http.Header) (int, error) {
		req, _ := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
		if h != nil {
			req.Header = h.Clone()
		}
		resp, err := c.Do(req)
		if err != nil {
			return 0, err
		}
		defer resp.Body.Close()
		if _, err := io.Copy(io.Discard, resp.Body); err != nil {
			return 0, err
		}
		return resp.StatusCode, nil
	}
}

func resilient(o Options) mode {
	errs := &errCount{m: map[string]int{}}
	o.OnTryError = errs.add
	c := New(o)
	name := "resilient h1.1"
	if o.HTTP2 {
		name = "resilient h2"
	}
	return mode{name: name, opts: c, tryErrs: errs, get: func(ctx context.Context, url string, h http.Header) (int, error) {
		r, err := c.Get(ctx, url, h)
		if err != nil {
			return 0, err
		}
		return r.Status, nil
	}}
}

type sample struct {
	d   time.Duration
	err error
}

func run(m mode, urls []string, h http.Header, workers int) []sample {
	out := make([]sample, len(urls))
	jobs := make(chan int)
	var wg sync.WaitGroup
	for w := 0; w < workers; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := range jobs {
				start := time.Now()
				status, err := m.get(context.Background(), urls[i], h)
				if err == nil && status != 200 {
					err = fmt.Errorf("status %d", status)
				}
				out[i] = sample{time.Since(start), err}
			}
		}()
	}
	for i := range urls {
		jobs <- i
	}
	close(jobs)
	wg.Wait()
	return out
}

func report(t *testing.T, what string, m mode, s []sample) {
	ds := make([]time.Duration, 0, len(s))
	fails := map[string]int{}
	for _, x := range s {
		ds = append(ds, x.d)
		if x.err != nil {
			fails[kind(x.err)]++
		}
	}
	sort.Slice(ds, func(i, j int) bool { return ds[i] < ds[j] })
	pct := func(p float64) time.Duration { return ds[int(p*float64(len(ds)-1))].Round(time.Millisecond) }
	nfail := 0
	for _, n := range fails {
		nfail += n
	}
	extra := ""
	if m.opts != nil {
		st := m.opts.Stats()
		extra = fmt.Sprintf(" tries=%d retries=%d hedges=%d tryErrs=%v", st.Tries, st.Retries, st.Hedges, m.tryErrs.m)
	}
	t.Logf("%-6s %-15s n=%d fail=%d p50=%s p95=%s max=%s%s %v", what, m.name, len(s), nfail,
		pct(0.5), pct(0.95), ds[len(ds)-1].Round(time.Millisecond), extra, fails)
}

func TestLive(t *testing.T) {
	key := os.Getenv("TMDB_API_KEY")
	if key == "" {
		t.Skip("TMDB_API_KEY not set")
	}
	t.Logf("IPv6 routable: %v", IPv6Routable())
	auth := http.Header{"Authorization": {"Bearer " + key}, "Accept": {"application/json"}}
	api := New(Options{Budget: 15 * time.Second, HedgeAfter: 1500 * time.Millisecond})

	// Collect poster paths and movie ids from list endpoints.
	var posters []string
	var movieIDs []int
	seenP, seenID := map[string]bool{}, map[int]bool{}
	for _, list := range []string{"trending/movie/day", "trending/movie/week", "trending/tv/day", "trending/tv/week",
		"movie/popular", "tv/popular", "movie/top_rated", "tv/top_rated", "movie/now_playing", "movie/upcoming"} {
		for page := 1; page <= 5; page++ {
			r, err := api.Get(context.Background(), fmt.Sprintf("https://api.themoviedb.org/3/%s?page=%d", list, page), auth)
			if err != nil || r.Status != 200 {
				t.Logf("list %s: %v", list, err)
				continue
			}
			var res struct {
				Results []struct {
					ID         int    `json:"id"`
					PosterPath string `json:"poster_path"`
				} `json:"results"`
			}
			json.Unmarshal(r.Body, &res)
			for _, x := range res.Results {
				if x.PosterPath != "" && !seenP[x.PosterPath] {
					seenP[x.PosterPath] = true
					posters = append(posters, x.PosterPath)
				}
				if strings.HasPrefix(list, "movie/") && !seenID[x.ID] {
					seenID[x.ID] = true
					movieIDs = append(movieIDs, x.ID)
				}
			}
		}
	}
	t.Logf("collected %d posters, %d movie ids", len(posters), len(movieIDs))

	newModes := func() []mode {
		return []mode{
			{name: "plain (old)", get: plainGet(30 * time.Second)},
			resilient(Options{Attempts: 3, HedgeAfter: 1200 * time.Millisecond, Budget: 4 * time.Second, MaxBody: 20 << 20, ReadIdleTimeout: 1500 * time.Millisecond, PoolSize: 12, HTTP2: true}),
			resilient(Options{Attempts: 3, HedgeAfter: 1200 * time.Millisecond, Budget: 4 * time.Second, MaxBody: 20 << 20, ReadIdleTimeout: 1500 * time.Millisecond, PoolSize: 12}),
		}
	}

	// Images: every run gets its own 50 posters (w92 + w342 = 100 URLs), so no run
	// profits from CDN edge caching another run caused. Rounds alternate the mode order.
	per := 50
	rounds := len(posters) / (per * 3)
	if rounds > 4 {
		rounds = 4
	}
	next := 0
	for round := 0; round < rounds; round++ {
		modes := newModes()
		if round%2 == 1 {
			for i, j := 0, len(modes)-1; i < j; i, j = i+1, j-1 {
				modes[i], modes[j] = modes[j], modes[i]
			}
		}
		for _, m := range modes {
			var urls []string
			for _, p := range posters[next : next+per] {
				urls = append(urls, "https://image.tmdb.org/t/p/w92"+p, "https://image.tmdb.org/t/p/w342"+p)
			}
			next += per
			report(t, fmt.Sprintf("img#%d", round+1), m, run(m, urls, nil, 12))
		}
	}

	// API: movie details (distinct ids per mode), 5 at a time like withVideos.
	perAPI := len(movieIDs) / 3
	if perAPI > 60 {
		perAPI = 60
	}
	apiModes := []mode{
		{name: "plain (old)", get: plainGet(15 * time.Second)},
		resilient(Options{Attempts: 3, HedgeAfter: 1500 * time.Millisecond, Budget: 8 * time.Second, HTTP2: true}),
		resilient(Options{Attempts: 3, HedgeAfter: 1500 * time.Millisecond, Budget: 8 * time.Second}),
	}
	for i, m := range apiModes {
		var urls []string
		for _, id := range movieIDs[i*perAPI : (i+1)*perAPI] {
			urls = append(urls, fmt.Sprintf("https://api.themoviedb.org/3/movie/%d?language=tr-TR&append_to_response=credits", id))
		}
		report(t, "api", m, run(m, urls, auth, 5))
	}
}
