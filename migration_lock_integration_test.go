// Copyright 2026 jcsvwinston
// SPDX-License-Identifier: Apache-2.0

package quark_test

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jcsvwinston/quark"
)

// testMigrationLock runs the F3-1 contract against a live database
// from the SharedSuite. Three subtests:
//
//  1. **AcquireRelease** — a single caller takes the lock and releases
//     it cleanly. Both operations return nil error; double-Release is
//     a no-op.
//  2. **ConcurrentAcquireSerialises** — two goroutines race for the
//     same lock. Exactly one succeeds at any moment; the other either
//     blocks on the call or receives ErrLockTimeout if the wait was
//     shorter than the first holder's grip.
//  3. **UnsupportedOnSQLite** — `Client.AcquireMigrationLock` on a
//     SQLite client surfaces ErrUnsupportedFeature. Same expected
//     behaviour on Oracle (deferred); covered by the unit test on the
//     dialect interface assertion.
func testMigrationLock(ctx context.Context, t *testing.T, baseClient *quark.Client) {
	t.Helper()

	// SQLite doesn't implement MigrationLocker (intentional — there's
	// no distributed primitive to back the lock). The SharedSuite runs
	// the lock subtest only on dialects that DO implement it.
	dialect := baseClient.Dialect().Name()
	if dialect == "sqlite" {
		t.Run("UnsupportedOnSQLite", func(t *testing.T) {
			_, err := baseClient.AcquireMigrationLock(ctx, "test-lock", 100*time.Millisecond)
			if !errors.Is(err, quark.ErrUnsupportedFeature) {
				t.Errorf("expected ErrUnsupportedFeature on SQLite, got %v", err)
			}
		})
		return
	}

	t.Run("AcquireRelease", func(t *testing.T) {
		lock, err := baseClient.AcquireMigrationLock(ctx, "f3-1-acquire-release", 5*time.Second)
		if err != nil {
			t.Fatalf("AcquireMigrationLock: %v", err)
		}
		if err := lock.Release(ctx); err != nil {
			t.Errorf("Release: %v", err)
		}
		// Idempotent — second Release is a no-op.
		if err := lock.Release(ctx); err != nil {
			t.Errorf("second Release should be no-op, got %v", err)
		}
	})

	t.Run("ConcurrentAcquireSerialises", func(t *testing.T) {
		// Two goroutines compete for the same lock name. The first
		// holder sleeps briefly to ensure the second sees a held lock
		// and either waits or times out. We assert: at no point are
		// both holding (`heldCount` never exceeds 1).
		const lockName = "f3-1-concurrent"
		var heldCount int32
		var maxHeld int32

		acquire := func(holdFor time.Duration, timeout time.Duration) (bool, error) {
			lock, err := baseClient.AcquireMigrationLock(ctx, lockName, timeout)
			if err != nil {
				return false, err
			}
			now := atomic.AddInt32(&heldCount, 1)
			for {
				m := atomic.LoadInt32(&maxHeld)
				if now <= m || atomic.CompareAndSwapInt32(&maxHeld, m, now) {
					break
				}
			}
			time.Sleep(holdFor)
			atomic.AddInt32(&heldCount, -1)
			return true, lock.Release(ctx)
		}

		var wg sync.WaitGroup
		results := make([]error, 2)
		acquired := make([]bool, 2)
		wg.Add(2)
		go func() {
			defer wg.Done()
			acquired[0], results[0] = acquire(500*time.Millisecond, 5*time.Second)
		}()
		go func() {
			defer wg.Done()
			// Stagger by 50ms so the first goroutine wins the race.
			time.Sleep(50 * time.Millisecond)
			acquired[1], results[1] = acquire(50*time.Millisecond, 5*time.Second)
		}()
		wg.Wait()

		if max := atomic.LoadInt32(&maxHeld); max > 1 {
			t.Errorf("mutual exclusion broken: max concurrent holders = %d", max)
		}
		for i, err := range results {
			if err != nil {
				t.Errorf("goroutine %d: %v", i, err)
			}
			if !acquired[i] {
				t.Errorf("goroutine %d failed to acquire", i)
			}
		}
	})

	t.Run("TimeoutWhenAlreadyHeld", func(t *testing.T) {
		const lockName = "f3-1-timeout"

		// First holder takes the lock and keeps it for the duration of
		// the second attempt.
		first, err := baseClient.AcquireMigrationLock(ctx, lockName, 5*time.Second)
		if err != nil {
			t.Fatalf("first acquire: %v", err)
		}
		defer first.Release(ctx)

		// Second attempt with a short timeout should fail with
		// ErrLockTimeout. We use 1 second because MySQL GET_LOCK's
		// resolution is whole seconds — anything < 1s rounds up.
		started := time.Now()
		_, err = baseClient.AcquireMigrationLock(ctx, lockName, 1*time.Second)
		elapsed := time.Since(started)

		if !errors.Is(err, quark.ErrLockTimeout) {
			t.Errorf("expected ErrLockTimeout, got %v", err)
		}
		// The timeout should be roughly the requested duration. We
		// allow a generous upper bound because container overhead can
		// add latency. Lower bound is 800ms so an instant return
		// (i.e. a bug returning timeout without actually waiting)
		// surfaces.
		if elapsed < 800*time.Millisecond || elapsed > 3*time.Second {
			t.Errorf("timeout latency = %v, expected ~1s", elapsed)
		}
	})
}
