package main

import (
	"context"
	"log/slog"
	"strconv"
	"time"

	"github.com/redis/go-redis/v9"
)

// Cache stores ready-made JSON responses for lists. If Redis is down,
// the API keeps working directly with the database: the cache only speeds things up.
type Cache interface {
	Get(ctx context.Context, key string) ([]byte, bool)
	Set(ctx context.Context, key string, val []byte)
	Invalidate(ctx context.Context)
}

type noCache struct{}

func (noCache) Get(context.Context, string) ([]byte, bool) { return nil, false }
func (noCache) Set(context.Context, string, []byte)        {}
func (noCache) Invalidate(context.Context)                 {}

const cacheTTL = 60 * time.Second

type redisCache struct{ rdb *redis.Client }

func newRedisCache(addr, password string) *redisCache {
	// Short timeouts: if Redis is down, the request quickly falls back to the database
	// instead of waiting 5 seconds for a connection (the default).
	return &redisCache{rdb: redis.NewClient(&redis.Options{
		Addr:         addr,
		Password:     password,
		DialTimeout:  300 * time.Millisecond,
		ReadTimeout:  200 * time.Millisecond,
		WriteTimeout: 200 * time.Millisecond,
		MaxRetries:   1,
	})}
}

// The key includes a data "version". Adding a meme bumps the version,
// so all old keys simply stop being read (TTL removes them later).
func (c *redisCache) key(ctx context.Context, key string) (string, error) {
	ver, err := c.rdb.Get(ctx, "memes:ver").Int()
	if err == redis.Nil {
		ver, err = 0, nil
	}
	return "memes:v" + strconv.Itoa(ver) + ":" + key, err
}

func (c *redisCache) Get(ctx context.Context, key string) ([]byte, bool) {
	k, err := c.key(ctx, key)
	if err != nil {
		slog.Warn("cache get", "err", err)
		return nil, false
	}
	val, err := c.rdb.Get(ctx, k).Bytes()
	if err != nil {
		if err != redis.Nil {
			slog.Warn("cache get", "err", err)
		}
		return nil, false
	}
	return val, true
}

func (c *redisCache) Set(ctx context.Context, key string, val []byte) {
	k, err := c.key(ctx, key)
	if err == nil {
		err = c.rdb.Set(ctx, k, val, cacheTTL).Err()
	}
	if err != nil {
		slog.Warn("cache set", "err", err)
	}
}

func (c *redisCache) Invalidate(ctx context.Context) {
	if err := c.rdb.Incr(ctx, "memes:ver").Err(); err != nil {
		slog.Warn("cache invalidate", "err", err)
	}
}
