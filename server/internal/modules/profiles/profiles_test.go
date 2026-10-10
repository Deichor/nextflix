package profiles

import (
	"errors"
	"fmt"
	"path/filepath"
	"sync"
	"testing"

	"nextflix/internal/platform/database"

	"gorm.io/gorm"
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

func TestUpdateDoesNotRecreateDeletedProfile(t *testing.T) {
	s := testService(t)
	a, b := "A", "B"
	if _, err := s.Create(Input{Name: &a}); err != nil {
		t.Fatal(err)
	}
	p, err := s.Create(Input{Name: &b})
	if err != nil {
		t.Fatal(err)
	}
	created := p.CreatedAt
	renamed := "B2"
	if err := s.Update(p, Input{Name: &renamed}); err != nil {
		t.Fatal(err)
	}
	if got, _ := s.Get(p.ID); got.Name != "B2" || got.Avatar != defaultProfileAvatar || !got.CreatedAt.Equal(created) {
		t.Fatalf("after update: %+v", got)
	}

	// A PUT that loaded the profile before a concurrent DELETE
	if err := s.Delete(p.ID); err != nil {
		t.Fatal(err)
	}
	if err := s.Update(p, Input{Name: &renamed}); !errors.Is(err, ErrNotFound) {
		t.Fatalf("update of deleted profile: %v", err)
	}
	if ok, _ := s.Exists(p.ID); ok {
		t.Fatal("deleted profile was re-inserted")
	}
}

type hookCleaner struct {
	s       *Service
	fail    bool
	deleted []uint
	existed []bool
}

func (c *hookCleaner) DeleteByProfile(_ *gorm.DB, _ uint) error {
	if c.fail {
		return errors.New("cleaner failed")
	}
	return nil
}

func (c *hookCleaner) ProfileDeleted(id uint) {
	ok, _ := c.s.Exists(id)
	c.deleted = append(c.deleted, id)
	c.existed = append(c.existed, ok)
}

func TestProfileDeletedHookRunsAfterCommit(t *testing.T) {
	s := testService(t)
	c := &hookCleaner{s: s}
	s.cleaners = []ProfileDataCleaner{c}
	var ids []uint
	for _, n := range []string{"A", "B"} {
		p, err := s.Create(Input{Name: &n})
		if err != nil {
			t.Fatal(err)
		}
		ids = append(ids, p.ID)
	}

	c.fail = true
	if err := s.Delete(ids[1]); err == nil {
		t.Fatal("expected cleaner error")
	}
	if len(c.deleted) != 0 {
		t.Fatalf("hook ran on rollback: %v", c.deleted)
	}

	c.fail = false
	if err := s.Delete(ids[1]); err != nil {
		t.Fatal(err)
	}
	if err := s.Delete(ids[0]); !errors.Is(err, ErrLastProfile) {
		t.Fatalf("last profile: %v", err)
	}
	if len(c.deleted) != 1 || c.deleted[0] != ids[1] || c.existed[0] {
		t.Fatalf("hook calls %v, profile still visible %v", c.deleted, c.existed)
	}
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
