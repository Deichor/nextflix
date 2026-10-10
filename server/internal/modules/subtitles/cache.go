package subtitles

import (
	"crypto/sha1"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

// listCache keeps provider search results in memory for a while.
type listCache struct {
	mu    sync.Mutex
	items map[string]listEntry
	max   int
}

type listEntry struct {
	value   []candidate
	expires time.Time
}

func newListCache(max int) *listCache {
	return &listCache{items: map[string]listEntry{}, max: max}
}

func (c *listCache) get(key string) ([]candidate, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	e, ok := c.items[key]
	if !ok || time.Now().After(e.expires) {
		return nil, false
	}
	return e.value, true
}

func (c *listCache) clear() {
	c.mu.Lock()
	c.items = map[string]listEntry{}
	c.mu.Unlock()
}

func (c *listCache) set(key string, v []candidate, ttl time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if len(c.items) >= c.max {
		now := time.Now()
		for k, e := range c.items {
			if now.After(e.expires) {
				delete(c.items, k)
			}
		}
		if len(c.items) >= c.max {
			c.items = map[string]listEntry{}
		}
	}
	c.items[key] = listEntry{value: v, expires: time.Now().Add(ttl)}
}

// flightGroup runs one call per key at a time; later callers wait for the running one.
type flightGroup struct {
	mu    sync.Mutex
	calls map[string]*flightCall
}

type flightCall struct {
	done chan struct{}
	val  any
	err  error
}

// start launches fn in the background (once per key) and returns the call to wait on.
func (g *flightGroup) start(key string, fn func() (any, error)) *flightCall {
	g.mu.Lock()
	if g.calls == nil {
		g.calls = map[string]*flightCall{}
	}
	if c, ok := g.calls[key]; ok {
		g.mu.Unlock()
		return c
	}
	c := &flightCall{done: make(chan struct{})}
	g.calls[key] = c
	g.mu.Unlock()

	go func() {
		defer func() {
			g.mu.Lock()
			delete(g.calls, key)
			g.mu.Unlock()
			close(c.done)
		}()
		c.val, c.err = fn()
	}()
	return c
}

// diskCache stores converted subtitles and scraped pages under dir.
type diskCache struct{ dir string }

const (
	cacheMaxAge     = 30 * 24 * time.Hour // files not used this long are removed
	cacheMaxBytes   = 1 << 30             // above this the least recently used files go
	cacheSweepEvery = 6 * time.Hour
)

// cacheKinds are the folders under the cache dir (diskCache kinds).
var cacheKinds = []string{SourceTorrent, SourceEmbedded, SourceOpenSubtitles, SourceSubDL, SourceSubSource,
	SourceTurkceAltyazi, "turkcealtyazi-pages", "synced", "syncref"}

// sweep removes files not used for maxAge (leftover temp files after an hour), then the least
// recently used ones while the cache holds more than maxBytes. Only regular files count.
func (d diskCache) sweep(maxAge time.Duration, maxBytes int64) int {
	if d.dir == "" {
		return 0
	}
	type entry struct {
		path string
		size int64
		mod  time.Time
	}
	var keep []entry
	var total int64
	removed := 0
	now := time.Now()
	// Only the folders this cache writes: dir may be shared with other data
	for _, kind := range cacheKinds {
		list, _ := os.ReadDir(filepath.Join(d.dir, kind))
		for _, de := range list {
			if !de.Type().IsRegular() {
				continue
			}
			info, err := de.Info()
			if err != nil {
				continue
			}
			p := filepath.Join(d.dir, kind, de.Name())
			age := now.Sub(info.ModTime())
			if age > maxAge || (strings.HasPrefix(de.Name(), ".tmp-") && age > time.Hour) {
				if os.Remove(p) == nil {
					removed++
				}
				continue
			}
			keep = append(keep, entry{p, info.Size(), info.ModTime()})
			total += info.Size()
		}
	}
	if total > maxBytes {
		sort.Slice(keep, func(i, j int) bool { return keep[i].mod.Before(keep[j].mod) })
		for _, e := range keep {
			if total <= maxBytes {
				break
			}
			if os.Remove(e.path) == nil {
				total -= e.size
				removed++
			}
		}
	}
	return removed
}

func hashKey(parts ...string) string {
	h := sha1.New()
	for _, p := range parts {
		h.Write([]byte(p))
		h.Write([]byte{0})
	}
	return hex.EncodeToString(h.Sum(nil))
}

func (d diskCache) path(kind, key, ext string) string {
	return filepath.Join(d.dir, kind, hashKey(key)+ext)
}

// read returns the file when it exists and is younger than maxAge (0 = forever).
func (d diskCache) read(kind, key, ext string, maxAge time.Duration) ([]byte, bool) {
	if d.dir == "" {
		return nil, false
	}
	p := d.path(kind, key, ext)
	st, err := os.Stat(p)
	if err != nil || (maxAge > 0 && time.Since(st.ModTime()) > maxAge) {
		return nil, false
	}
	b, err := os.ReadFile(p)
	if err != nil {
		return nil, false
	}
	if maxAge == 0 && time.Since(st.ModTime()) > 24*time.Hour {
		// Kept entries age by their last use (sweep); entries with a max age keep their write time
		now := time.Now()
		_ = os.Chtimes(p, now, now)
	}
	return b, true
}

func (d diskCache) has(kind, key, ext string) bool {
	if d.dir == "" {
		return false
	}
	_, err := os.Stat(d.path(kind, key, ext))
	return err == nil
}

// write stores data atomically (temp file + rename).
func (d diskCache) write(kind, key, ext string, data []byte) error {
	if d.dir == "" {
		return errors.New("no cache dir")
	}
	p := d.path(kind, key, ext)
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(p), ".tmp-*")
	if err != nil {
		return err
	}
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		os.Remove(tmp.Name())
		return err
	}
	if err := tmp.Close(); err != nil {
		os.Remove(tmp.Name())
		return err
	}
	return os.Rename(tmp.Name(), p)
}
