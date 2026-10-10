package config

import "testing"

// An unset REDIS_URL must stay empty so the cache and notifications turn themselves off.
func TestLoadRedisURLHasNoDefault(t *testing.T) {
	t.Setenv("REDIS_URL", "")
	if got := Load().RedisURL; got != "" {
		t.Fatalf("RedisURL = %q, want empty", got)
	}
	t.Setenv("REDIS_URL", "redis://redis:6379/0")
	if got := Load().RedisURL; got != "redis://redis:6379/0" {
		t.Fatalf("RedisURL = %q", got)
	}
}

func TestLoadTVAppKey(t *testing.T) {
	t.Setenv("TV_APP_KEY", " abc123 \r")
	if got := Load().TVAppKey; got != "abc123" {
		t.Fatalf("TVAppKey = %q, want abc123", got)
	}
}
