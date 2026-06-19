package redis

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"time"

	"github.com/jcsvwinston/quark"
	"github.com/redis/go-redis/v9"
)

// Store is a professional Redis implementation of quark.CacheStore.
// It leverages Redis Sets for tag-based invalidation (reverse index).
var (
	_ quark.CacheStore  = (*Store)(nil)
	_ quark.CacheLocker = (*Store)(nil)
)

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

// releaseLockScript atomically deletes the lock key only if it still holds our
// token, so a release that fires after the lock auto-expired (and a peer
// re-acquired it) never clobbers the new holder.
var releaseLockScript = redis.NewScript(
	`if redis.call("get", KEYS[1]) == ARGV[1] then return redis.call("del", KEYS[1]) else return 0 end`)

// AcquireLock implements quark.CacheLocker (ADR-0020) via SET NX with a TTL: the
// first caller across all processes claims the key with a unique token and gets
// acquired=true; concurrent callers get false. release runs a token-checked Lua
// delete so only the current holder unlocks. The TTL guarantees a crashed holder
// cannot wedge the key. The caller (the stampede wrapper) supplies an
// already-namespaced key.
func (s *Store) AcquireLock(ctx context.Context, key string, ttl time.Duration) (bool, func() error, error) {
	tok := make([]byte, 16)
	if _, err := rand.Read(tok); err != nil {
		return false, nil, fmt.Errorf("redis lock token: %w", err)
	}
	token := hex.EncodeToString(tok)
	ok, err := s.rdb.SetNX(ctx, key, token, ttl).Result()
	if err != nil {
		return false, nil, fmt.Errorf("redis SETNX %q: %w", key, err)
	}
	if !ok {
		return false, nil, nil
	}
	release := func() error {
		// context.Background(): release is typically deferred and must run even
		// if the caller's ctx has since expired, so the lock is freed promptly
		// for the next holder rather than waiting out its TTL.
		return releaseLockScript.Run(context.Background(), s.rdb, []string{key}, token).Err()
	}
	return true, release, nil
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
