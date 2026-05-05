package redis

import (
	"context"
	"fmt"
	"time"

	"github.com/jcsvwinston/quark"
	"github.com/redis/go-redis/v9"
)

// Store is a professional Redis implementation of quark.CacheStore.
// It leverages Redis Sets for tag-based invalidation (reverse index).
var _ quark.CacheStore = (*Store)(nil)

type Store struct {
	rdb *redis.Client
}

// Options defines the connection parameters for Redis.
type Options struct {
	Addr     string
	Password string
	DB       int
}

// New creates a new Redis cache store.
func New(opts Options) *Store {
	rdb := redis.NewClient(&redis.Options{
		Addr:     opts.Addr,
		Password: opts.Password,
		DB:       opts.DB,
	})
	return &Store{rdb: rdb}
}

// Ping checks the connectivity to the Redis server.
func (s *Store) Ping(ctx context.Context) error {
	return s.rdb.Ping(ctx).Err()
}

func (s *Store) Get(ctx context.Context, key string) ([]byte, error) {
	val, err := s.rdb.Get(ctx, "quark:cache:"+key).Bytes()
	if err == redis.Nil {
		return nil, fmt.Errorf("cache miss")
	}
	return val, err
}

func (s *Store) Set(ctx context.Context, key string, val []byte, ttl time.Duration, tags ...string) error {
	cacheKey := "quark:cache:" + key
	pipe := s.rdb.Pipeline()
	pipe.Set(ctx, cacheKey, val, ttl)

	for _, tag := range tags {
		tagKey := "quark:tag:" + tag
		pipe.SAdd(ctx, tagKey, cacheKey)
		pipe.Expire(ctx, tagKey, ttl+(24*time.Hour)) // Keep tags slightly longer
	}

	_, err := pipe.Exec(ctx)
	return err
}

func (s *Store) Delete(ctx context.Context, key string) error {
	return s.rdb.Del(ctx, "quark:cache:"+key).Err()
}

func (s *Store) InvalidateTags(ctx context.Context, tags ...string) error {
	for _, tag := range tags {
		tagKey := "quark:tag:" + tag
		// Get all keys associated with this tag
		keys, err := s.rdb.SMembers(ctx, tagKey).Result()
		if err != nil {
			continue
		}

		if len(keys) > 0 {
			// Delete all cached entries and the tag set itself
			pipe := s.rdb.Pipeline()
			pipe.Del(ctx, keys...)
			pipe.Del(ctx, tagKey)
			_, _ = pipe.Exec(ctx)
		}
	}
	return nil
}
