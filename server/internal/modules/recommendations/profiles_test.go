package recommendations

import (
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestUnknownProfileIsRefused(t *testing.T) {
	var calls atomic.Int32
	s := NewService(RatingsFunc(func(uint) ([]Signal, error) {
		calls.Add(1)
		return nil, nil
	}), &fakeCatalog{})
	s.SetProfiles(func(id uint) (bool, error) { return id == 1, nil })

	if _, err := s.For(99, "en"); !errors.Is(err, ErrUnknownProfile) {
		t.Fatalf("For: %v", err)
	}
	if _, err := s.Surprise(99, "en", nil); !errors.Is(err, ErrUnknownProfile) {
		t.Fatalf("Surprise: %v", err)
	}
	if calls.Load() != 0 {
		t.Fatalf("unknown profile reached the ratings source %d times", calls.Load())
	}
	if _, err := s.For(1, "en"); errors.Is(err, ErrUnknownProfile) {
		t.Fatal("known profile refused")
	}
}

func TestConcurrentSurprisesKeepEveryPick(t *testing.T) {
	f := &fakeCatalog{related: map[titleKey]relatedLists{keyOf("movie", 1): {recs: arts(100, 40, 878)}}}
	s := surpriseService(f, []Signal{{MediaType: "movie", TmdbID: 1, Title: "A", Rating: "love", UpdatedAt: time.Now()}}, nil)
	if _, err := s.Surprise(1, "en", nil); err != nil { // builds the pool
		t.Fatal(err)
	}
	const presses = 20
	var wg sync.WaitGroup
	for i := 0; i < presses; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, err := s.Surprise(1, "en", nil); err != nil {
				t.Error(err)
			}
		}()
	}
	wg.Wait()
	recent := s.surprise.recent[1]
	if len(recent) != presses+1 {
		t.Fatalf("memory has %d picks, want %d", len(recent), presses+1)
	}
	seen := map[titleKey]bool{}
	for _, p := range recent {
		if seen[p.key] {
			t.Fatalf("title %v picked twice", p.key)
		}
		seen[p.key] = true
	}
}
