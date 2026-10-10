package tmdb

import (
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestEvictKeepsMostEntries(t *testing.T) {
	m := newMemoryCache(time.Hour, 100)
	now := time.Now()
	for i := range 100 {
		m.set(fmt.Sprint(i), i, 0)
	}
	// 0-9 expired long ago (stale backups), 10-19 recently; the rest are fresh
	m.mu.Lock()
	for i := range 20 {
		e := m.items[fmt.Sprint(i)]
		e.expires = now.Add(-time.Duration(100-i) * time.Hour)
		m.items[fmt.Sprint(i)] = e
	}
	m.mu.Unlock()

	m.set("new", 1, 0)
	m.mu.Lock()
	n := len(m.items)
	_, oldest := m.items["0"]
	_, recent := m.items["19"]
	_, fresh := m.items["50"]
	_, added := m.items["new"]
	m.mu.Unlock()
	if n != 91 {
		t.Errorf("size after eviction = %d, want 91", n)
	}
	if oldest || !recent || !fresh || !added {
		t.Errorf("oldest kept=%v recent=%v fresh=%v new=%v", oldest, recent, fresh, added)
	}
}

func TestEvictDropsPastStaleFirst(t *testing.T) {
	m := newMemoryCache(time.Hour, 10)
	for i := range 10 {
		m.set(fmt.Sprint(i), i, 0)
	}
	m.mu.Lock()
	for i := range 3 {
		e := m.items[fmt.Sprint(i)]
		e.expires, e.stale = time.Now().Add(-2*time.Hour), time.Now().Add(-time.Hour)
		m.items[fmt.Sprint(i)] = e
	}
	m.mu.Unlock()
	m.set("x", 1, 0)
	m.mu.Lock()
	defer m.mu.Unlock()
	if len(m.items) != 8 {
		t.Errorf("size = %d, want 8 (7 fresh + new)", len(m.items))
	}
}

func TestCachedSharesConcurrentFetch(t *testing.T) {
	c := New("k")
	var calls atomic.Int32
	release := make(chan struct{})
	var wg sync.WaitGroup
	got := make([]int, 8)
	for i := range got {
		wg.Add(1)
		go func() {
			defer wg.Done()
			v, err := Cached(c, "same", func() (int, error) {
				calls.Add(1)
				<-release
				return 42, nil
			})
			if err != nil {
				t.Error(err)
			}
			got[i] = v
		}()
	}
	time.Sleep(50 * time.Millisecond)
	close(release)
	wg.Wait()
	if n := calls.Load(); n != 1 {
		t.Errorf("fetch ran %d times, want 1", n)
	}
	for _, v := range got {
		if v != 42 {
			t.Fatalf("results %v", got)
		}
	}
}

func TestCachedFlightError(t *testing.T) {
	c := New("k")
	boom := errors.New("boom")
	if _, err := Cached(c, "e", func() (*int, error) { return nil, boom }); !errors.Is(err, boom) {
		t.Fatalf("err = %v", err)
	}
	// Errors are not cached: the next call fetches again
	v, err := Cached(c, "e", func() (*int, error) { x := 1; return &x, nil })
	if err != nil || v == nil || *v != 1 {
		t.Fatalf("%v %v", v, err)
	}
}

func TestCachedPartialWrapped(t *testing.T) {
	c := New("k")
	v, err := Cached(c, "p", func() (int, error) { return 3, fmt.Errorf("x: %w", ErrPartial) })
	if err != nil || v != 3 {
		t.Fatalf("%v %v", v, err)
	}
	c.cache.mu.Lock()
	e := c.cache.items[c.cacheKey("p")]
	c.cache.mu.Unlock()
	if time.Until(e.expires) > retryAfter {
		t.Error("partial result cached too long")
	}
}
