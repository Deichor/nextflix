package catalog

import (
	"errors"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"
)

func waitTasteIdle(t *testing.T, s *Service) {
	t.Helper()
	for range 400 {
		s.taste.mu.Lock()
		n := len(s.taste.running)
		s.taste.mu.Unlock()
		if n == 0 {
			return
		}
		time.Sleep(2 * time.Millisecond)
	}
	t.Fatal("taste computes did not finish")
}

func TestTasteSkipsUnknownProfilesAndFailures(t *testing.T) {
	s := NewService(genreTMDB(t))
	var reads atomic.Int32
	s.SetRatings(func(id uint) ([]RatedTitle, error) {
		reads.Add(1)
		if id == 2 {
			return nil, errors.New("db down")
		}
		return []RatedTitle{{MediaType: "movie", TmdbID: 28, Rating: "love"}}, nil
	})
	s.SetProfiles(func(id uint) (bool, error) { return id < 1000, nil })

	s.taste.affinity(5000) // no such profile: its ratings are not even read
	waitTasteIdle(t, s)
	s.taste.affinity(2) // the read fails: nothing is stored
	waitTasteIdle(t, s)
	s.taste.mu.Lock()
	n := len(s.taste.entries)
	s.taste.mu.Unlock()
	if n != 0 || reads.Load() != 1 {
		t.Fatalf("%d entries, %d reads", n, reads.Load())
	}
	s.taste.affinity(3)
	waitTaste(t, s, 3, 28)
}

func TestTasteCacheStaysBounded(t *testing.T) {
	s := NewService(genreTMDB(t))
	s.SetRatings(func(uint) ([]RatedTitle, error) {
		return []RatedTitle{{MediaType: "movie", TmdbID: 28, Rating: "love"}}, nil
	})
	for id := uint(1); id <= tasteMaxProfiles+40; id++ {
		s.taste.affinity(id)
		waitTasteIdle(t, s)
	}
	s.taste.mu.Lock()
	n := len(s.taste.entries)
	_, newest := s.taste.entries[tasteMaxProfiles+40]
	s.taste.mu.Unlock()
	if n > tasteMaxProfiles || !newest {
		t.Fatalf("%d entries (newest kept: %v)", n, newest)
	}
}

func TestSaveAliasesRetriesAfterFailure(t *testing.T) {
	dir := t.TempDir()
	blocker := filepath.Join(dir, "file")
	if err := os.WriteFile(blocker, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	ix := NewTitleIndex(fixtureTitles())
	l := NewIndexLoader(ix, filepath.Join(blocker, "sub")) // a dir under a file cannot be made
	ix.Learn("movie", 238, "tr", "Baba")
	l.saveAliases()
	ix.mu.Lock()
	dirty := ix.dirty
	ix.mu.Unlock()
	if !dirty {
		t.Fatal("a failed save cleared dirty")
	}
	l.Dir = dir
	l.saveAliases()
	if _, err := os.Stat(filepath.Join(dir, aliasFile)); err != nil {
		t.Fatalf("aliases not saved on the retry: %v", err)
	}
}
