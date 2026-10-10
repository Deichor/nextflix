package notifications

import (
	"context"
	"testing"

	"nextflix/internal/platform/cache"
	"nextflix/internal/platform/tmdb"
)

// A redis that is down at boot must not disable the feed for good.
func TestFeedStaysEnabledWhenRedisIsDown(t *testing.T) {
	c, err := cache.New("redis://127.0.0.1:1/0")
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	s := NewService(c, tmdb.New(""))
	if !s.enabled() {
		t.Fatal("feed disabled")
	}
	if _, _, err := s.Notifications(context.Background(), 1, 10, "en"); err == nil {
		t.Fatal("expected an error while redis is down")
	}
	if s.repo.purgeNonTMDB(context.Background()) {
		t.Fatal("purge reported success without redis")
	}
}
