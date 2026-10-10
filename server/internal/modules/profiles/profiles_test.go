package profiles

import (
	"errors"
	"fmt"
	"path/filepath"
	"sync"
	"testing"

	"nextflix/internal/platform/database"
)

func testService(t *testing.T) *Service {
	t.Helper()
	db, err := database.Open(filepath.Join(t.TempDir(), "t.db"))
	if err != nil {
		t.Fatal(err)
	}
	if err := database.Migrate(db, Models()...); err != nil {
		t.Fatal(err)
	}
	return NewService(db)
}

func TestConcurrentCreateKeepsLimit(t *testing.T) {
	s := testService(t)
	var wg sync.WaitGroup
	var mu sync.Mutex
	created, limited := 0, 0
	for i := 0; i < 12; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			name := fmt.Sprintf("P%d", i)
			_, err := s.Create(Input{Name: &name})
			mu.Lock()
			defer mu.Unlock()
			switch {
			case err == nil:
				created++
			case errors.Is(err, ErrLimit):
				limited++
			default:
				t.Errorf("create: %v", err)
			}
		}(i)
	}
	wg.Wait()
	list, _ := s.List()
	if created != MaxProfiles || len(list) != MaxProfiles || limited != 12-MaxProfiles {
		t.Fatalf("created %d, limited %d, stored %d", created, limited, len(list))
	}
}

func TestConcurrentDeleteKeepsOne(t *testing.T) {
	s := testService(t)
	var ids []uint
	for _, n := range []string{"A", "B", "C"} {
		p, err := s.Create(Input{Name: &n})
		if err != nil {
			t.Fatal(err)
		}
		ids = append(ids, p.ID)
	}
	var wg sync.WaitGroup
	for _, id := range ids {
		wg.Add(1)
		go func(id uint) {
			defer wg.Done()
			if err := s.Delete(id); err != nil && !errors.Is(err, ErrLastProfile) {
				t.Errorf("delete: %v", err)
			}
		}(id)
	}
	wg.Wait()
	if list, _ := s.List(); len(list) != 1 {
		t.Fatalf("%d profiles left, want 1", len(list))
	}
	if ok, err := s.Exists(ids[0]); err != nil || (ok != (ids[0] == mustFirst(t, s))) {
		t.Fatalf("exists: %v %v", ok, err)
	}
}

func mustFirst(t *testing.T, s *Service) uint {
	list, err := s.List()
	if err != nil || len(list) == 0 {
		t.Fatal("no profiles")
	}
	return list[0].ID
}

func TestNameAndAvatarCaps(t *testing.T) {
	s := testService(t)
	long := "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	if _, err := s.Create(Input{Name: &long}); err == nil {
		t.Fatal("long name accepted")
	}
	name := "Ok"
	if _, err := s.Create(Input{Name: &name, Avatar: &long}); err == nil {
		t.Fatal("long avatar accepted")
	}
}
