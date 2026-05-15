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
		// F4-6: extend the tag-set TTL toward the MAX of the current
		// and the new value — never deliberately SHORTEN it. The
		// historical Expire(...) here shrank the tag-set TTL whenever
		// a key with a shorter TTL was tagged, leaving cached keys
		// with no surviving tag entry (so InvalidateTags couldn't
		// reach them). The pair below covers both states:
		//   ExpireNX: set TTL when the tag set has no TTL yet (the
		//             SADD just created it).
		//   ExpireGT: extend TTL only when the new value > current,
		//             so a later short-lived key can't shorten it.
		//
		// Atomicity caveat: a Redis pipeline batches the commands but
		// does NOT wrap them in MULTI/EXEC — between the NX and GT
		// another client can mutate the tag-set TTL. Under heavy
		// concurrency on the SAME tag, the resulting TTL is best-
		// effort MAX, not a strict guarantee. Closing the gap fully
		// requires a Lua script; deferred until the imperfect
		// guarantee bites a real workload.
		//
		// Both commands require Redis 7.0+; on older servers they are
		// no-ops and the historical (broken) behaviour returns —
		// documented as a known gap in docs/playbooks/cache.md.
		pipe.ExpireNX(ctx, tagKey, ttl+(24*time.Hour))
		pipe.ExpireGT(ctx, tagKey, ttl+(24*time.Hour))
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
