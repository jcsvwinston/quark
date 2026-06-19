// Copyright 2026 jcsvwinston
// SPDX-License-Identifier: Apache-2.0

package quark

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// lockingStore is a fakeStore that ALSO implements CacheLocker (ADR-0020), with
// a process-shared lock table. Wrapping ONE lockingStore in several
// stampedeStores models several processes sharing a Redis: each wrapper has its
// own in-process singleflight, but they all race for the same distributed lock.
type lockingStore struct {
	*fakeStore
	lockMu  sync.Mutex
	expiry  map[string]time.Time
	tokens  map[string]uint64
	lockSeq uint64
}

var _ CacheLocker = (*lockingStore)(nil)

func newLockingStore() *lockingStore {
	return &lockingStore{
		fakeStore: newFakeStore(),
		expiry:    map[string]time.Time{},
		tokens:    map[string]uint64{},
	}
}

func (l *lockingStore) AcquireLock(_ context.Context, key string, ttl time.Duration) (bool, func() error, error) {
	l.lockMu.Lock()
	defer l.lockMu.Unlock()
	now := time.Now()
	if exp, held := l.expiry[key]; held && now.Before(exp) {
		return false, nil, nil
	}
	l.lockSeq++
	tok := l.lockSeq
	l.expiry[key] = now.Add(ttl)
	l.tokens[key] = tok
	release := func() error {
		l.lockMu.Lock()
		defer l.lockMu.Unlock()
		if l.tokens[key] == tok {
			delete(l.expiry, key)
			delete(l.tokens, key)
		}
		return nil
	}
	return true, release, nil
}

// TestStampede_CrossInstanceDedupesAcrossWrappers: N stampedeStores sharing one
// locker-capable backing must collapse a concurrent hot-key miss to a SINGLE
// compute — the lock winner recomputes, the losers wait-and-reread its value.
// Without cross-instance coordination each wrapper's singleflight would let all
// N recompute (one per process).
func TestStampede_CrossInstanceDedupesAcrossWrappers(t *testing.T) {
	shared := newLockingStore()
	const wrappers = 8

	var computeN int32
	compute := func(ctx context.Context) ([]byte, error) {
		atomic.AddInt32(&computeN, 1)
		// Hold long enough that the losers race the lock while the winner is
		// still computing (and far below the 5s lock TTL / wait budget).
		time.Sleep(40 * time.Millisecond)
		return []byte("value"), nil
	}

	var wg sync.WaitGroup
	results := make([][]byte, wrappers)
	errs := make([]error, wrappers)
	start := make(chan struct{})
	for i := 0; i < wrappers; i++ {
		// Each wrapper is a distinct "process": its own singleflight, the shared
		// backing + lock table. crossInstance on.
		ss := newStampedeStore(shared, 0, false, 0, true, nil)
		wg.Add(1)
		go func(i int, ss *stampedeStore) {
			defer wg.Done()
			<-start // release all goroutines together
			results[i], errs[i] = ss.getOrCompute(context.Background(), "hot", time.Minute, nil, compute)
		}(i, ss)
	}
	close(start)
	wg.Wait()

	for i := range results {
		if errs[i] != nil {
			t.Fatalf("wrapper %d: %v", i, errs[i])
		}
		if string(results[i]) != "value" {
			t.Errorf("wrapper %d got %q, want %q", i, results[i], "value")
		}
	}
	if n := atomic.LoadInt32(&computeN); n != 1 {
		t.Errorf("cross-instance coordination failed: compute ran %d times across %d wrappers, want 1", n, wrappers)
	}
}

// TestStampede_CrossInstanceFallbackNoLocker: with cross-instance enabled but a
// backing that does NOT implement CacheLocker (plain fakeStore), getOrCompute
// must degrade gracefully to the singleflight path — no panic, correct value.
func TestStampede_CrossInstanceFallbackNoLocker(t *testing.T) {
	ss := newStampedeStore(newFakeStore(), 0, false, 0, true, nil)
	var computeN int32
	data, err := ss.getOrCompute(context.Background(), "k", time.Minute, nil, func(ctx context.Context) ([]byte, error) {
		atomic.AddInt32(&computeN, 1)
		return []byte("v"), nil
	})
	if err != nil {
		t.Fatalf("getOrCompute: %v", err)
	}
	if string(data) != "v" {
		t.Errorf("got %q, want %q", data, "v")
	}
	if n := atomic.LoadInt32(&computeN); n != 1 {
		t.Errorf("compute ran %d times, want 1", n)
	}
}
