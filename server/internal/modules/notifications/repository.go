package notifications

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"strconv"
	"time"

	"github.com/go-redis/redis/v8"
)

const (
	feedKey          = "nextflix:notifications:global"
	feedTTL          = 7 * 24 * time.Hour
	maxNotifications = 100

	// Per-profile state: a sorted set of read ids (scored by when they were read)
	// and a "seen up to" feed timestamp that drives the badge.
	readKeyPrefix = "nextflix:notifications:read:"
	seenKeyPrefix = "nextflix:notifications:seen:"
	stateTTL      = feedTTL
	maxReadIDs    = 500
)

// stateOwner is the redis key suffix for a profile; requests without X-User-ID share one key.
func stateOwner(profile uint) string {
	if profile == 0 {
		return "shared"
	}
	return strconv.FormatUint(uint64(profile), 10)
}

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

// newest returns the timestamp of the newest feed entry (0 when the feed is empty).
func (r *repository) newest(ctx context.Context) (int64, error) {
	z, err := r.rdb.ZRevRangeWithScores(ctx, feedKey, 0, 0).Result()
	if err != nil || len(z) == 0 {
		return 0, err
	}
	return int64(z[0].Score), nil
}

func (r *repository) readIDs(ctx context.Context, owner string) (map[string]bool, error) {
	ids, err := r.rdb.ZRange(ctx, readKeyPrefix+owner, 0, -1).Result()
	if err != nil && err != redis.Nil {
		return nil, err
	}
	out := make(map[string]bool, len(ids))
	for _, id := range ids {
		out[id] = true
	}
	return out, nil
}

// markRead adds ids to the owner's read set, drops entries older than the feed TTL and caps its size.
func (r *repository) markRead(ctx context.Context, owner string, ids ...string) error {
	if len(ids) == 0 {
		return nil
	}
	key := readKeyPrefix + owner
	now := time.Now().Unix()
	members := make([]*redis.Z, len(ids))
	for i, id := range ids {
		members[i] = &redis.Z{Score: float64(now), Member: id}
	}
	pipe := r.rdb.TxPipeline()
	pipe.ZAdd(ctx, key, members...)
	pipe.ZRemRangeByScore(ctx, key, "-inf", fmt.Sprintf("(%d", now-int64(stateTTL/time.Second)))
	pipe.ZRemRangeByRank(ctx, key, 0, -int64(maxReadIDs+1))
	pipe.Expire(ctx, key, stateTTL)
	_, err := pipe.Exec(ctx)
	return err
}

func (r *repository) seenAt(ctx context.Context, owner string) (int64, error) {
	v, err := r.rdb.Get(ctx, seenKeyPrefix+owner).Int64()
	if err == redis.Nil {
		return 0, nil
	}
	return v, err
}

// setSeen moves the owner's "seen up to" timestamp forward (never back).
func (r *repository) setSeen(ctx context.Context, owner string, unix int64) error {
	cur, err := r.seenAt(ctx, owner)
	if err != nil {
		return err
	}
	if unix < cur {
		unix = cur
	}
	return r.rdb.Set(ctx, seenKeyPrefix+owner, unix, stateTTL).Err()
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
