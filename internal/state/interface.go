package state

import "context"

type Backend interface {
	Increment(ctx context.Context, key string, delta int64) (int64, error)
	Get(ctx context.Context, key string) (int64, error)
	Reset(ctx context.Context, key string) error
	IncrementWithTTL(ctx context.Context, key string, delta int64, ttlMs int64) (int64, error)
	GetMany(ctx context.Context, keys []string) (map[string]int64, error)
	Close() error
}
