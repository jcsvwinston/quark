// Copyright 2026 jcsvwinston
// SPDX-License-Identifier: Apache-2.0

package memory

import (
	"context"
	"testing"
	"time"
)

// TestAcquireLock_MutualExclusionReleaseTTL pins the CacheLocker contract
// (ADR-0020) for the in-memory store: exclusive hold, release frees it, a stale
// release is token-safe, and an unreleased lock auto-expires.
func TestAcquireLock_MutualExclusionReleaseTTL(t *testing.T) {
	s := New()
	defer s.Close()
	ctx := context.Background()

	ok1, rel1, err := s.AcquireLock(ctx, "k", 5*time.Second)
	if err != nil || !ok1 {
		t.Fatalf("first acquire: ok=%v err=%v", ok1, err)
	}
	if ok2, rel2, _ := s.AcquireLock(ctx, "k", 5*time.Second); ok2 || rel2 != nil {
		t.Fatalf("second acquire must fail while held: ok=%v rel!=nil=%v", ok2, rel2 != nil)
	}
	if err := rel1(); err != nil {
		t.Fatalf("release: %v", err)
	}
	ok3, _, err := s.AcquireLock(ctx, "k", 5*time.Second)
	if err != nil || !ok3 {
		t.Fatalf("re-acquire after release: ok=%v err=%v", ok3, err)
	}
	// Stale release from the original holder must not free the new holder.
	_ = rel1()
	if ok4, _, _ := s.AcquireLock(ctx, "k", time.Second); ok4 {
		t.Error("token check failed: stale release freed a newer holder's lock")
	}

	// TTL expiry: a separate key, unreleased, becomes acquirable after its TTL.
	ok, _, err := s.AcquireLock(ctx, "ttl", 200*time.Millisecond)
	if err != nil || !ok {
		t.Fatalf("ttl acquire: %v", err)
	}
	time.Sleep(300 * time.Millisecond)
	if ok, _, _ := s.AcquireLock(ctx, "ttl", time.Second); !ok {
		t.Error("lock must be re-acquirable after TTL expiry")
	}
}
