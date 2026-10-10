package tmdb

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"sync"
	"time"

	"nextflix/internal/platform/upstream"
)

const (
	baseURL      = "https://api.themoviedb.org/3"
	imageBaseURL = "https://image.tmdb.org/t/p/"
	cacheTTL     = 24 * time.Hour
	cacheMax     = 4000
	// Expired entries are kept this long to answer when TMDB is unreachable.
	staleFor = 7 * 24 * time.Hour
	// After serving stale data (or caching a partial result) TMDB is asked again after this.
	retryAfter = time.Minute
)

// Content languages (profile languages). TMDB is asked for en-US or tr-TR.
const (
	LangEN = "en"
	LangTR = "tr"
)

// ErrNoAPIKey is returned by every call when TMDB_API_KEY is empty.
var ErrNoAPIKey = errors.New("TMDB API key not configured")

// ErrPartial marks a combined result with failed parts: Cached prefers stale complete data
// over it and keeps it only briefly. Fetch functions passed to Cached may return it (wrapped).
var ErrPartial = errors.New("tmdb: partial result")

// Client talks to the TMDB v3 API using a v4 read access token (Bearer auth).
// It owns a 24h in-memory cache shared by everyone holding the client.
// A client asks for English content; WithLang returns a view for another language.
type Client struct {
	apiKey string
	http   *upstream.Client
	cache  *memoryCache
	flight *flightGroup
	base   string // API root (tests point it at a fake server)
	lang   string // "" means LangEN
}

func New(apiKey string) *Client {
	return &Client{
		apiKey: apiKey,
		http: upstream.New(upstream.Options{
			Attempts:   3,
			HedgeAfter: 1500 * time.Millisecond,
			Budget:     8 * time.Second,
			MaxBody:    16 << 20,
			UserAgent:  "Nextflix",
		}),
		cache:  newMemoryCache(cacheTTL, cacheMax),
		flight: &flightGroup{},
		base:   baseURL,
	}
}

// NormalizeLang maps a language code ("tr", "tr-TR", "EN") to LangTR or LangEN (the default).
func NormalizeLang(s string) string {
	s = strings.ToLower(strings.TrimSpace(s))
	if i := strings.IndexAny(s, "-_"); i >= 0 {
		s = s[:i]
	}
	if s == LangTR {
		return LangTR
	}
	return LangEN
}

// WithLang returns a view of the client that asks TMDB for content in lang. It shares the
// HTTP client and the cache; cached values are kept per language.
func (c *Client) WithLang(lang string) *Client {
	if c == nil {
		return nil
	}
	lang = NormalizeLang(lang)
	if lang == c.Lang() {
		return c
	}
	cp := *c
	cp.lang = lang
	return &cp
}

// Lang returns the content language of the client (LangEN or LangTR).
func (c *Client) Lang() string {
	if c == nil || c.lang == "" {
		return LangEN
	}
	return c.lang
}

// Language returns the TMDB language parameter of the client ("en-US" or "tr-TR").
func (c *Client) Language() string {
	if c.Lang() == LangTR {
		return "tr-TR"
	}
	return "en-US"
}

func (c *Client) english() *Client { return c.WithLang(LangEN) }

func (c *Client) localized() bool { return c.Lang() != LangEN }

// Enabled reports whether an API key is configured.
func (c *Client) Enabled() bool {
	return c != nil && c.apiKey != ""
}

// Get performs an authenticated GET on path (e.g. "/movie/550") and decodes the JSON body into dst.
func (c *Client) Get(path string, params url.Values, dst any) error {
	if !c.Enabled() {
		return ErrNoAPIKey
	}
	u := c.base + path
	if len(params) > 0 {
		u += "?" + params.Encode()
	}
	hdr := http.Header{"Authorization": {"Bearer " + c.apiKey}, "Accept": {"application/json"}}
	resp, err := c.http.Get(context.Background(), u, hdr)
	if err != nil {
		return fmt.Errorf("tmdb: GET %s: %w", path, err)
	}
	if resp.Status != http.StatusOK {
		return fmt.Errorf("TMDB API error: %d", resp.Status)
	}
	return json.Unmarshal(resp.Body, dst)
}

// Cached returns the value cached under key, or calls fetch and caches a successful result for 24h.
// When fetch fails, an expired value (up to 7 days old) is returned instead of the error.
// The key is kept per content language of c. Cached values are shared: callers must not mutate them.
// Concurrent misses on one key share a single fetch; fetch must not call Cached with its own key.
func Cached[T any](c *Client, key string, fetch func() (T, error)) (T, error) {
	if c == nil {
		v, err := fetch()
		if errors.Is(err, ErrPartial) {
			err = nil
		}
		return v, err
	}
	key = c.cacheKey(key)
	if v, fresh, ok := c.cache.get(key); ok && fresh {
		if t, ok := v.(T); ok {
			return t, nil
		}
	}
	if c.flight == nil {
		return refresh(c, key, fetch)
	}
	v, err := c.flight.do(key, func() (any, error) { return refresh(c, key, fetch) })
	if t, ok := v.(T); ok {
		return t, err
	}
	var zero T
	return zero, err
}

// refresh fetches key unless a fresh value arrived meanwhile; a failure falls back to stale data.
func refresh[T any](c *Client, key string, fetch func() (T, error)) (T, error) {
	var stale T
	hasStale := false
	if v, fresh, ok := c.cache.get(key); ok {
		if t, ok := v.(T); ok {
			if fresh {
				return t, nil
			}
			stale, hasStale = t, true
		}
	}
	v, err := fetch()
	switch {
	case err == nil:
		c.cache.set(key, v, cacheTTL)
		return v, nil
	case hasStale:
		log.Printf("tmdb: %s: serving stale data: %v", key, err)
		c.cache.retryLater(key, retryAfter)
		return stale, nil
	case errors.Is(err, ErrPartial):
		c.cache.set(key, v, retryAfter)
		return v, nil
	}
	return v, err
}

// ImageURL builds a full TMDB image URL; empty paths stay empty and absolute URLs are returned as-is.
func ImageURL(path, size string) string {
	if path == "" {
		return ""
	}
	if strings.HasPrefix(path, "http") {
		return path
	}
	if !strings.HasPrefix(path, "/") {
		path = "/" + path
	}
	return imageBaseURL + size + path
}

func (c *Client) cacheKey(key string) string { return key + "|" + c.Lang() }

// params returns the client's language parameter plus extra key/value pairs.
func (c *Client) params(extra ...string) url.Values {
	v := url.Values{}
	v.Set("language", c.Language())
	for i := 0; i+1 < len(extra); i += 2 {
		v.Set(extra[i], extra[i+1])
	}
	return v
}

// mediaLanguages returns the include_image_language / include_video_language value: a
// localized client prefers its own language, then English and language-less items.
func (c *Client) mediaLanguages() string {
	if c.localized() {
		return c.Lang() + ",en,null"
	}
	return ""
}

// withVideoLanguages adds include_video_language to v for a localized client; TMDB lists
// only videos in the request language otherwise (no trailer for most titles in Turkish).
func (c *Client) withVideoLanguages(v url.Values) url.Values {
	if l := c.mediaLanguages(); l != "" {
		v.Set("include_video_language", l)
	}
	return v
}

type cacheEntry struct {
	value   any
	expires time.Time // fresh until
	stale   time.Time // kept for stale-on-error until
}

type memoryCache struct {
	mu    sync.Mutex
	ttl   time.Duration
	max   int
	items map[string]cacheEntry
}

func newMemoryCache(ttl time.Duration, max int) *memoryCache {
	return &memoryCache{ttl: ttl, max: max, items: map[string]cacheEntry{}}
}

// get returns the value under key and whether it is still fresh.
func (m *memoryCache) get(key string) (v any, fresh, ok bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	e, ok := m.items[key]
	if !ok {
		return nil, false, false
	}
	now := time.Now()
	if now.After(e.stale) {
		delete(m.items, key)
		return nil, false, false
	}
	return e.value, !now.After(e.expires), true
}

// set stores v as fresh for ttl (the cache's TTL when 0).
func (m *memoryCache) set(key string, v any, ttl time.Duration) {
	if ttl <= 0 {
		ttl = m.ttl
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, exists := m.items[key]; !exists && len(m.items) >= m.max {
		m.evict()
	}
	now := time.Now()
	m.items[key] = cacheEntry{value: v, expires: now.Add(ttl), stale: now.Add(ttl + staleFor)}
}

// retryLater marks an expired entry fresh for d, without moving its stale deadline.
func (m *memoryCache) retryLater(key string, d time.Duration) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if e, ok := m.items[key]; ok {
		e.expires = time.Now().Add(d)
		m.items[key] = e
	}
}

// evict drops entries past their stale time, then the ones that expired (or expire) first
// until the cache is at 90% of max. Long-expired stale copies go first; recent ones survive.
func (m *memoryCache) evict() {
	now := time.Now()
	for k, e := range m.items {
		if now.After(e.stale) {
			delete(m.items, k)
		}
	}
	target := m.max * 9 / 10
	if len(m.items) <= target {
		return
	}
	type aged struct {
		key     string
		expires time.Time
	}
	all := make([]aged, 0, len(m.items))
	for k, e := range m.items {
		all = append(all, aged{k, e.expires})
	}
	sort.Slice(all, func(i, j int) bool { return all[i].expires.Before(all[j].expires) })
	for _, a := range all[:len(all)-target] {
		delete(m.items, a.key)
	}
}

// flightGroup lets concurrent callers of one key share a single call (a tiny singleflight).
type flightGroup struct {
	mu    sync.Mutex
	calls map[string]*flightCall
}

type flightCall struct {
	done chan struct{}
	v    any
	err  error
}

func (g *flightGroup) do(key string, fn func() (any, error)) (any, error) {
	g.mu.Lock()
	if g.calls == nil {
		g.calls = map[string]*flightCall{}
	}
	if cl, ok := g.calls[key]; ok {
		g.mu.Unlock()
		<-cl.done
		return cl.v, cl.err
	}
	cl := &flightCall{done: make(chan struct{}), err: errors.New("tmdb: fetch panicked")}
	g.calls[key] = cl
	g.mu.Unlock()
	defer func() {
		g.mu.Lock()
		delete(g.calls, key)
		g.mu.Unlock()
		close(cl.done)
	}()
	cl.v, cl.err = fn()
	return cl.v, cl.err
}
