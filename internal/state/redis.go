// Package state defines the state store abstraction for rate-limiting counters
// and other ephemeral state. Implementations include in-memory (dev-friendly)
// and Redis (production).
package state

import (
	"context"
	"strconv"
	"time"

	"github.com/redis/go-redis/v9"
)

// minIncrWithTTL is the minimum TTL in milliseconds for IncrementWithTTL.
// Redis expires with second granularity, so values below 1s are clamped.
const minIncrWithTTL int64 = 1000

// Compile-time check that RedisBackend implements Backend.
var _ Backend = (*RedisBackend)(nil)

// RedisBackend implements the Backend interface using Redis. It stores counter
// values as strings and uses atomic INCRBY operations for thread-safe
// increments across multiple proxy instances.
type RedisBackend struct {
	client *redis.Client
}

// NewRedis creates a RedisBackend from a Redis URL (e.g.
// redis://localhost:6379/0). It parses the URL, verifies connectivity by
// pinging the server, and returns an error if the connection fails.
func NewRedis(url string) (*RedisBackend, error) {
	opt, err := redis.ParseURL(url)
	if err != nil {
		return nil, err
	}
	client := redis.NewClient(opt)
	if err := client.Ping(context.Background()).Err(); err != nil {
		client.Close()
		return nil, err
	}
	return &RedisBackend{client: client}, nil
}

// Increment adds delta to the counter identified by key using INCRBY and
// returns the new value.
func (r *RedisBackend) Increment(ctx context.Context, key string, delta int64) (int64, error) {
	return r.client.IncrBy(ctx, key, delta).Result()
}

// Get returns the current value of the counter identified by key. Returns 0
// if the key does not exist (redis.Nil).
func (r *RedisBackend) Get(ctx context.Context, key string) (int64, error) {
	val, err := r.client.Get(ctx, key).Result()
	if err == redis.Nil {
		return 0, nil
	}
	if err != nil {
		return 0, err
	}
	n, err := strconv.ParseInt(val, 10, 64)
	if err != nil {
		return 0, err
	}
	return n, nil
}

// Reset deletes the counter identified by key, effectively resetting it to
// zero.
func (r *RedisBackend) Reset(ctx context.Context, key string) error {
	return r.client.Del(ctx, key).Err()
}

// IncrementWithTTL adds delta to the counter identified by key and sets the
// key's TTL to ttlMs milliseconds. Both operations are performed atomically
// in a Redis pipeline.
func (r *RedisBackend) IncrementWithTTL(ctx context.Context, key string, delta int64, ttlMs int64) (int64, error) {
	if ttlMs < minIncrWithTTL {
		ttlMs = minIncrWithTTL
	}
	pipe := r.client.Pipeline()
	incr := pipe.IncrBy(ctx, key, delta)
	pipe.Expire(ctx, key, time.Duration(ttlMs)*time.Millisecond)
	_, err := pipe.Exec(ctx)
	if err != nil {
		return 0, err
	}
	return incr.Val(), nil
}

// GetMany returns the current values for multiple counters in a single MGET
// call. Keys that do not exist are omitted from the result map.
func (r *RedisBackend) GetMany(ctx context.Context, keys []string) (map[string]int64, error) {
	vals, err := r.client.MGet(ctx, keys...).Result()
	if err != nil {
		return nil, err
	}
	result := make(map[string]int64, len(keys))
	for i, v := range vals {
		if v == nil {
			continue
		}
		s, ok := v.(string)
		if !ok {
			continue
		}
		n, err := strconv.ParseInt(s, 10, 64)
		if err != nil {
			continue
		}
		result[keys[i]] = n
	}
	return result, nil
}

// Close closes the Redis connection pool.
func (r *RedisBackend) Close() error {
	return r.client.Close()
}
