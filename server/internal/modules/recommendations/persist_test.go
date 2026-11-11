package recommendations

import (
	"path/filepath"
	"testing"
	"time"
)

func TestRowsSurviveRestart(t *testing.T) {
	path := filepath.Join(t.TempDir(), persistFile)
	f := &fakeCatalog{trending: movies(700, 25)}

	before := newTestService(f, nil)
	want, err := before.For(1, "en")
	if err != nil {
		t.Fatal(err)
	}
	if err := before.saveRows(path); err != nil {
		t.Fatal(err)
	}

	// A restart past the TTL: the saved rows answer at once, new ones are computed in the background
	after := newTestService(f, nil)
	now := time.Now().Add(cacheTTL + time.Minute)
	after.now = func() time.Time { return now }
	if n, err := after.loadRows(path); err != nil || n != 1 {
		t.Fatalf("loaded %d, %v", n, err)
	}
	// The computation blocks until the gate opens, so For only returns if it did not wait for it
	f.gate = make(chan struct{})
	got, err := after.For(1, "en")
	close(f.gate)
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Rows) != len(want.Rows) || ids(got.Rows[0].Items)[0] != ids(want.Rows[0].Items)[0] {
		t.Fatalf("got %+v, want the saved rows", got.Rows)
	}
	waitIdle(t, after)
}

func TestSavedRowsDropInvalidatedAndOld(t *testing.T) {
	path := filepath.Join(t.TempDir(), persistFile)
	f := &fakeCatalog{trending: movies(700, 25)}
	s := newTestService(f, nil)
	s.For(1, "en")
	s.For(2, "en")
	s.Invalidate(1)
	if err := s.saveRows(path); err != nil {
		t.Fatal(err)
	}

	fresh := newTestService(f, nil)
	if n, _ := fresh.loadRows(path); n != 1 {
		t.Fatalf("loaded %d profile languages, want 1 (profile 1 was invalidated)", n)
	}
	if _, ok := fresh.cache[cacheKey{2, "en"}]; !ok {
		t.Fatal("profile 2 rows missing")
	}

	old := newTestService(f, nil)
	old.now = func() time.Time { return time.Now().Add(cacheTTL + staleFor + time.Minute) }
	if n, _ := old.loadRows(path); n != 0 {
		t.Fatalf("loaded %d rows older than the stale window", n)
	}
}
