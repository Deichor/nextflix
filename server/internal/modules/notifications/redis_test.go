package notifications

import (
	"fmt"
	"os"
	"sync"
	"testing"
	"time"

	"nextflix/internal/platform/cache"
	"nextflix/internal/platform/tmdb"
)

// redisService needs NEXTFLIX_TEST_REDIS pointing at a throwaway redis: the test flushes that database.
func redisService(t *testing.T) *Service {
	t.Helper()
	url := os.Getenv("NEXTFLIX_TEST_REDIS")
	if url == "" {
		t.Skip("NEXTFLIX_TEST_REDIS not set")
	}
	c, err := cache.New(url)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { c.Close() })
	s := NewService(c, tmdb.New(""))
	if err := s.repo.rdb.FlushDB(t.Context()).Err(); err != nil {
		t.Fatal(err)
	}
	return s
}

func TestRedisMarkReadOnlyFeedIDs(t *testing.T) {
	s := redisService(t)
	ctx := t.Context()
	s.SetProfiles(func(id uint) (bool, error) { return id == 1, nil })
	n := Notification{ID: "n1", Type: TypeTMDBTrending, TMDBIDs: []int{1}, Timestamp: time.Now().Unix()}
	if err := s.add(ctx, n); err != nil {
		t.Fatal(err)
	}
	if err := s.MarkAsRead(ctx, 1, "junk"); err != nil {
		t.Fatal(err)
	}
	if err := s.MarkAsRead(ctx, 1, "n1"); err != nil {
		t.Fatal(err)
	}
	if ids, _ := s.repo.readIDs(ctx, "1"); len(ids) != 1 || !ids["n1"] {
		t.Fatalf("read ids = %v", ids)
	}
	// Unknown profiles fall back to the shared state, no new keys
	if err := s.MarkAsRead(ctx, 999, "n1"); err != nil {
		t.Fatal(err)
	}
	if err := s.MarkSeen(ctx, 999, 0); err != nil {
		t.Fatal(err)
	}
	if n, _ := s.repo.rdb.Exists(ctx, readKeyPrefix+"999", seenKeyPrefix+"999").Result(); n != 0 {
		t.Fatalf("%d keys created for an unknown profile", n)
	}
	if ids, _ := s.repo.readIDs(ctx, "shared"); !ids["n1"] {
		t.Fatal("shared read state not written")
	}
}

func TestRedisSetSeenNeverMovesBack(t *testing.T) {
	s := redisService(t)
	ctx := t.Context()
	var wg sync.WaitGroup
	for i := 1; i <= 50; i++ {
		wg.Add(1)
		go func(v int64) {
			defer wg.Done()
			if err := s.repo.setSeen(ctx, "1", v); err != nil {
				t.Error(err)
			}
		}(int64(i))
	}
	wg.Wait()
	if v, err := s.repo.seenAt(ctx, "1"); err != nil || v != 50 {
		t.Fatalf("seen = %d %v, want 50", v, err)
	}
	if ttl := s.repo.rdb.TTL(ctx, seenKeyPrefix+"1").Val(); ttl <= 0 {
		t.Fatalf("ttl = %v", ttl)
	}
}

func TestRedisConcurrentAddStoresOnce(t *testing.T) {
	s := redisService(t)
	ctx := t.Context()
	var wg sync.WaitGroup
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			n := Notification{ID: fmt.Sprintf("n%d", i), Type: TypeTMDBTrending, TMDBIDs: []int{42}, Timestamp: time.Now().Unix()}
			if err := s.add(ctx, n); err != nil {
				t.Error(err)
			}
		}(i)
	}
	wg.Wait()
	if all, _ := s.repo.latest(ctx, maxNotifications); len(all) != 1 {
		t.Fatalf("stored %d notifications, want 1", len(all))
	}
}
