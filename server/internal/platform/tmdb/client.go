package tmdb

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"
)

const (
	baseURL      = "https://api.themoviedb.org/3"
	imageBaseURL = "https://image.tmdb.org/t/p/"
	cacheTTL     = 24 * time.Hour
	cacheMax     = 2000
)

// ErrNoAPIKey is returned by every call when TMDB_API_KEY is empty.
var ErrNoAPIKey = errors.New("TMDB API key not configured")

// Client talks to the TMDB v3 API using a v4 read access token (Bearer auth).
// It owns a 24h in-memory cache shared by everyone holding the client.
type Client struct {
	apiKey string
	http   *http.Client
	cache  *memoryCache
}

func New(apiKey string) *Client {
	return &Client{
		apiKey: apiKey,
		http:   &http.Client{Timeout: 15 * time.Second},
		cache:  newMemoryCache(cacheTTL, cacheMax),
	}
}

// Enabled reports whether an API key is configured.
func (c *Client) Enabled() bool {
	return c != nil && c.apiKey != ""
}

// Get performs an authenticated GET on path (e.g. "/movie/550") and decodes the JSON body into dst.
func (c *Client) Get(path string, params url.Values, dst any) error {
	if !c.Enabled() {
		return ErrNoAPIKey
	}
	u := baseURL + path
	if len(params) > 0 {
		u += "?" + params.Encode()
	}
	req, err := http.NewRequest(http.MethodGet, u, nil)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+c.apiKey)
	req.Header.Set("Content-Type", "application/json")

	resp, err := c.http.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		io.Copy(io.Discard, resp.Body)
		return fmt.Errorf("TMDB API error: %d", resp.StatusCode)
	}
	return json.NewDecoder(resp.Body).Decode(dst)
}

// Cached returns the value cached under key, or calls fetch and caches a successful result for 24h.
// Cached values are shared: callers must not mutate them.
func Cached[T any](c *Client, key string, fetch func() (T, error)) (T, error) {
	if c == nil {
		return fetch()
	}
	if v, ok := c.cache.get(key); ok {
		if t, ok := v.(T); ok {
			return t, nil
		}
	}
	v, err := fetch()
	if err != nil {
		return v, err
	}
	c.cache.set(key, v)
	return v, nil
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

func lang(extra ...string) url.Values {
	v := url.Values{}
	v.Set("language", "en-US")
	for i := 0; i+1 < len(extra); i += 2 {
		v.Set(extra[i], extra[i+1])
	}
	return v
}

type cacheEntry struct {
	value   any
	expires time.Time
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

func (m *memoryCache) get(key string) (any, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	e, ok := m.items[key]
	if !ok {
		return nil, false
	}
	if time.Now().After(e.expires) {
		delete(m.items, key)
		return nil, false
	}
	return e.value, true
}

func (m *memoryCache) set(key string, v any) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if len(m.items) >= m.max {
		now := time.Now()
		for k, e := range m.items {
			if now.After(e.expires) {
				delete(m.items, k)
			}
		}
		// Still full: start over rather than grow without bound.
		if len(m.items) >= m.max {
			m.items = map[string]cacheEntry{}
		}
	}
	m.items[key] = cacheEntry{value: v, expires: time.Now().Add(m.ttl)}
}
