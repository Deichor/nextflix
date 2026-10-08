package notifications

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"time"

	"github.com/go-redis/redis/v8"
)

const (
	feedKey          = "nextflix:notifications:global"
	feedTTL          = 7 * 24 * time.Hour
	maxNotifications = 100
)

// repository stores the feed as a redis sorted set scored by timestamp.
type repository struct {
	rdb *redis.Client
}

func (r *repository) since(ctx context.Context, unix int64) ([]Notification, error) {
	raw, err := r.rdb.ZRevRangeByScore(ctx, feedKey, &redis.ZRangeBy{
		Min: fmt.Sprintf("%d", unix),
		Max: "+inf",
	}).Result()
	if err != nil {
		return nil, err
	}
	return decode(raw), nil
}

// latest returns the newest notifications first.
func (r *repository) latest(ctx context.Context, n int64) ([]Notification, error) {
	raw, err := r.rdb.ZRevRange(ctx, feedKey, 0, n-1).Result()
	if err != nil {
		return nil, err
	}
	return decode(raw), nil
}

func (r *repository) add(ctx context.Context, n Notification) error {
	data, err := json.Marshal(n)
	if err != nil {
		return err
	}
	if err := r.rdb.ZAdd(ctx, feedKey, &redis.Z{Score: float64(n.Timestamp), Member: string(data)}).Err(); err != nil {
		return fmt.Errorf("add notification: %w", err)
	}
	r.rdb.Expire(ctx, feedKey, feedTTL)
	r.trim(ctx)
	return nil
}

// trim keeps only the newest maxNotifications entries.
func (r *repository) trim(ctx context.Context) {
	r.rdb.ZRemRangeByRank(ctx, feedKey, 0, -int64(maxNotifications+1))
}

func (r *repository) count(ctx context.Context) (int64, error) {
	return r.rdb.ZCard(ctx, feedKey).Result()
}

// purgeNonTMDB removes leftovers of the old local-media generators.
func (r *repository) purgeNonTMDB(ctx context.Context) {
	raw, err := r.rdb.ZRange(ctx, feedKey, 0, -1).Result()
	if err != nil {
		return
	}
	var stale []any
	for _, member := range raw {
		var n Notification
		if json.Unmarshal([]byte(member), &n) != nil || !n.Type.isTMDB() {
			stale = append(stale, member)
		}
	}
	if len(stale) > 0 {
		r.rdb.ZRem(ctx, feedKey, stale...)
		log.Printf("notifications: removed %d old local-media notifications", len(stale))
	}
}

func decode(raw []string) []Notification {
	out := make([]Notification, 0, len(raw))
	for _, member := range raw {
		var n Notification
		if err := json.Unmarshal([]byte(member), &n); err != nil {
			continue
		}
		out = append(out, n)
	}
	return out
}
