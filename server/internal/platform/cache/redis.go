package cache

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/go-redis/redis/v8"
)

// Cache wraps a redis client. A nil *Cache is valid and behaves as an always-miss cache.
type Cache struct {
	client *redis.Client
}

// New connects to redis and pings it. An empty url disables the cache (nil, nil).
func New(url string) (*Cache, error) {
	if url == "" {
		return nil, nil
	}
	opts, err := redis.ParseURL(url)
	if err != nil {
		return nil, fmt.Errorf("parse redis url: %w", err)
	}
	client := redis.NewClient(opts)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := client.Ping(ctx).Err(); err != nil {
		client.Close()
		return nil, fmt.Errorf("connect to redis: %w", err)
	}
	return &Cache{client: client}, nil
}

// GetJSON decodes the value at key into dst. It reports false on a miss or when the cache is disabled.
func (c *Cache) GetJSON(ctx context.Context, key string, dst any) (bool, error) {
	if c == nil || c.client == nil {
		return false, nil
	}
	val, err := c.client.Get(ctx, key).Bytes()
	if errors.Is(err, redis.Nil) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if err := json.Unmarshal(val, dst); err != nil {
		return false, err
	}
	return true, nil
}

// SetJSON stores v as JSON under key. A ttl of 0 means no expiry.
func (c *Cache) SetJSON(ctx context.Context, key string, v any, ttl time.Duration) error {
	if c == nil || c.client == nil {
		return nil
	}
	data, err := json.Marshal(v)
	if err != nil {
		return err
	}
	return c.client.Set(ctx, key, data, ttl).Err()
}

// Redis returns the raw client, or nil when the cache is disabled.
func (c *Cache) Redis() *redis.Client {
	if c == nil {
		return nil
	}
	return c.client
}

// Close releases the connection pool.
func (c *Cache) Close() error {
	if c == nil || c.client == nil {
		return nil
	}
	return c.client.Close()
}
