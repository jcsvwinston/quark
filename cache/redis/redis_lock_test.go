// Copyright 2026 jcsvwinston
// SPDX-License-Identifier: Apache-2.0

package redis

import (
	"context"
	"fmt"
	"os"
	"testing"
	"time"
)

// lockTestStore connects to the Redis named by QUARK_TEST_REDIS_ADDR (e.g.
// "localhost:6379"); the test skips when unset, matching the engine integration
// tests' opt-in pattern.
func lockTestStore(t *testing.T) *Store {
	t.Helper()
	addr := os.Getenv("QUARK_TEST_REDIS_ADDR")
	if addr == "" {
		t.Skip("set QUARK_TEST_REDIS_ADDR (e.g. localhost:6379) to run redis lock tests")
	}
	s := New(Options{Addr: addr})
	if err := s.Ping(context.Background()); err != nil {
		t.Fatalf("redis ping %s: %v", addr, err)
	}
	return s
}

func lockKey(prefix string) string {
	return fmt.Sprintf("quark:test:lock:%s:%d", prefix, time.Now().UnixNano())
}

// TestAcquireLock_MutualExclusionReleaseTokenSafe exercises the CacheLocker
// contract (ADR-0020): exclusive hold, release frees it, and a stale release
// from a previous holder must NOT free a newer holder's lock (token check).
func TestAcquireLock_MutualExclusionReleaseTokenSafe(t *testing.T) {
	s := lockTestStore(t)
	ctx := context.Background()
	key := lockKey("mutex")

	ok1, rel1, err := s.AcquireLock(ctx, key, 5*time.Second)
	if err != nil || !ok1 {
		t.Fatalf("first acquire: ok=%v err=%v", ok1, err)
	}
	ok2, rel2, err := s.AcquireLock(ctx, key, 5*time.Second)
	if err != nil {
		t.Fatalf("second acquire err: %v", err)
	}
	if ok2 {
		t.Fatal("second acquire must fail while the lock is held")
	}
	if rel2 != nil {
		t.Error("loser release must be nil")
	}

	if err := rel1(); err != nil {
		t.Fatalf("release holder: %v", err)
	}
	// After release a new holder claims it.
	ok3, _, err := s.AcquireLock(ctx, key, 5*time.Second)
	if err != nil || !ok3 {
		t.Fatalf("re-acquire after release: ok=%v err=%v", ok3, err)
	}
	// A stale release from the FIRST holder must be a no-op (token mismatch),
	// not free the third holder's lock.
	if err := rel1(); err != nil {
		t.Fatalf("stale release err: %v", err)
	}
	ok4, rel4, err := s.AcquireLock(ctx, key, time.Second)
	if err != nil {
		t.Fatalf("post-stale-release acquire err: %v", err)
	}
	if ok4 {
		t.Error("token check failed: a stale release freed a newer holder's lock")
	}
	if rel4 != nil {
		_ = rel4()
	}
}

// TestAcquireLock_TTLExpiry: an unreleased lock must auto-expire so a crashed
// holder cannot wedge the key.
func TestAcquireLock_TTLExpiry(t *testing.T) {
	s := lockTestStore(t)
	ctx := context.Background()
	key := lockKey("ttl")

	ok, _, err := s.AcquireLock(ctx, key, 300*time.Millisecond)
	if err != nil || !ok {
		t.Fatalf("acquire: ok=%v err=%v", ok, err)
	}
	// Deliberately do NOT release; wait past the TTL.
	time.Sleep(450 * time.Millisecond)
	ok2, rel2, err := s.AcquireLock(ctx, key, time.Second)
	if err != nil {
		t.Fatalf("post-expiry acquire err: %v", err)
	}
	if !ok2 {
		t.Fatal("lock must be re-acquirable after TTL expiry")
	}
	if rel2 != nil {
		_ = rel2()
	}
}
