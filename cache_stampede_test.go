// Copyright 2026 jcsvwinston
// SPDX-License-Identifier: Apache-2.0

package quark

import (
	"context"
	"errors"
	"math/rand"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// fakeStore is a minimal in-memory CacheStore for wrapper tests. It is
// NOT meant to replace cache/memory.Store — it has no TTL, no tags, no
// concurrency beyond a single mutex. Just enough to exercise stampedeStore
// against a known-good backing.
type fakeStore struct {
	mu     sync.Mutex
	data   map[string][]byte
	getN   int32 // observable Get call count
	setN   int32 // observable Set call count
	missOn map[string]bool
}

func newFakeStore() *fakeStore { return &fakeStore{data: map[string][]byte{}} }

func (f *fakeStore) Get(_ context.Context, key string) ([]byte, error) {
	atomic.AddInt32(&f.getN, 1)
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.missOn != nil && f.missOn[key] {
		return nil, errors.New("forced miss")
	}
	if v, ok := f.data[key]; ok {
		return v, nil
	}
	return nil, errors.New("not found")
}
func (f *fakeStore) Set(_ context.Context, key string, val []byte, _ time.Duration, _ ...string) error {
	atomic.AddInt32(&f.setN, 1)
	f.mu.Lock()
	defer f.mu.Unlock()
	f.data[key] = val
	return nil
}
func (f *fakeStore) Delete(_ context.Context, key string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	delete(f.data, key)
	return nil
}
func (f *fakeStore) InvalidateTags(_ context.Context, _ ...string) error { return nil }

// TestXfetchEntry_RoundTrip pins the encode/decode contract: every
// stampedeStore-written entry must round-trip cleanly through the inner
// store as opaque []byte.
func TestXfetchEntry_RoundTrip(t *testing.T) {
	want := xfetchEntry{
		data:       []byte("hello payload"),
		deltaNs:    12345,
		computedAt: 99999,
		expiresAt:  1000000,
	}
	raw := encodeXfetchEntry(want.data, want.deltaNs, want.computedAt, want.expiresAt)
	got, err := decodeXfetchEntry(raw)
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	if string(got.data) != string(want.data) || got.deltaNs != want.deltaNs ||
		got.computedAt != want.computedAt || got.expiresAt != want.expiresAt {
		t.Errorf("round-trip mismatch:\n want %+v\n  got %+v", want, got)
	}
}

// TestDecodeXfetchEntry_RejectsForeign verifies that bytes lacking the
// QSPD magic prefix are rejected so Get treats them as a miss instead
// of mis-interpreting cross-product cache contents.
func TestDecodeXfetchEntry_RejectsForeign(t *testing.T) {
	cases := []struct {
		name string
		in   []byte
	}{
		{"too short", []byte("hi")},
		{"no magic", append(make([]byte, 0, xfetchEntryHeaderLen+5), []byte("XXXX\x01\x00\x00\x00\x00\x00\x00\x00\x00\x00\x00\x00\x00\x00\x00\x00\x00\x00\x00\x00\x00\x00\x00\x00\x00\x00\x00\x00\x00\x00\x00\x00\x05\x00\x00\x00\x00hello")...)},
		{"unknown version", []byte{0x51, 0x53, 0x50, 0x44, 0xFF, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0}},
		{"length mismatch", append(append([]byte{0x51, 0x53, 0x50, 0x44, 0x01}, make([]byte, 24)...), 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x09, 'x', 'y', 'z')},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := decodeXfetchEntry(tc.in); err == nil {
				t.Errorf("expected decode error, got nil")
			}
		})
	}
}

// TestStampedeStore_GetSetRoundTrip: bytes set through stampedeStore.Set
// come back unchanged from stampedeStore.Get — the wrapper is
// transparent to user payload.
func TestStampedeStore_GetSetRoundTrip(t *testing.T) {
	ss := newStampedeStore(newFakeStore(), 0, false, 0, nil)
	ctx := context.Background()
	if err := ss.Set(ctx, "k", []byte("hello"), time.Hour); err != nil {
		t.Fatalf("Set: %v", err)
	}
	got, err := ss.Get(ctx, "k")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if string(got) != "hello" {
		t.Errorf("Get = %q, want hello", got)
	}
}

// TestStampedeStore_GetReportsCorruptAsMiss: foreign bytes the wrapper
// didn't write (legacy entries, third-party writes) surface as
// errStampedeMiss instead of being returned as data.
func TestStampedeStore_GetReportsCorruptAsMiss(t *testing.T) {
	inner := newFakeStore()
	inner.data["legacy"] = []byte("raw payload, no QSPD prefix")
	ss := newStampedeStore(inner, 0, false, 0, nil)
	if _, err := ss.Get(context.Background(), "legacy"); !errors.Is(err, errStampedeMiss) {
		t.Errorf("want errStampedeMiss for foreign bytes, got %v", err)
	}
}

// TestJitterTTL pins the jitter contract: with jitterPct=0 the output
// is the input; with jitterPct>0 the output stays inside the band.
func TestJitterTTL(t *testing.T) {
	t.Run("zero pct is identity", func(t *testing.T) {
		ss := newStampedeStore(newFakeStore(), 0, false, 0, nil)
		for _, ttl := range []time.Duration{0, time.Millisecond, time.Hour} {
			if got := ss.jitterTTL(ttl); got != ttl {
				t.Errorf("jitterPct=0: ttl %v → %v, want %v", ttl, got, ttl)
			}
		}
	})
	t.Run("ten percent stays in band", func(t *testing.T) {
		ss := newStampedeStore(newFakeStore(), 0.1, false, 0, nil)
		ss.rng = rand.New(rand.NewSource(1)) // deterministic for the bound check
		base := 100 * time.Millisecond
		for i := 0; i < 100; i++ {
			got := ss.jitterTTL(base)
			if got < 90*time.Millisecond || got > 110*time.Millisecond {
				t.Errorf("jittered %v outside [90ms, 110ms]", got)
			}
		}
	})
}

// TestShouldEarlyRefresh: with deltaNs=0 never; with deltaNs>0 and time
// past expiry always; with deltaNs>0 and time well-before expiry rarely.
func TestShouldEarlyRefresh(t *testing.T) {
	ss := newStampedeStore(newFakeStore(), 0, true, 1.0, nil)
	now := time.Date(2026, 6, 1, 12, 0, 0, 0, time.UTC).UnixNano()
	ss.nowFn = func() time.Time { return time.Unix(0, now) }

	t.Run("delta zero never refreshes", func(t *testing.T) {
		e := xfetchEntry{deltaNs: 0, expiresAt: now - int64(time.Second)} // already expired
		if ss.shouldEarlyRefresh(e) {
			t.Error("delta=0 must skip XFetch even past expiry")
		}
	})

	t.Run("expired always refreshes", func(t *testing.T) {
		e := xfetchEntry{deltaNs: int64(time.Millisecond), expiresAt: now - int64(time.Second)}
		if !ss.shouldEarlyRefresh(e) {
			t.Error("already-expired entry must refresh")
		}
	})

	t.Run("far before expiry rarely refreshes", func(t *testing.T) {
		// Entry expires in 10 hours, delta is 1ms. Probability of early
		// refresh on a single draw is ~0; sample 500 times and require
		// almost all to skip.
		e := xfetchEntry{deltaNs: int64(time.Millisecond), expiresAt: now + int64(10*time.Hour)}
		refreshed := 0
		for i := 0; i < 500; i++ {
			if ss.shouldEarlyRefresh(e) {
				refreshed++
			}
		}
		if refreshed > 5 {
			t.Errorf("far-from-expiry: %d/500 refreshed, want near 0", refreshed)
		}
	})
}

// TestGetOrCompute_Singleflight is the F4-5 headline: N concurrent
// callers for the same key collapse to ONE compute. Stresses the
// wrapper to make the race condition visible if the singleflight ever
// regresses. Marked t.Parallel() so the race detector exercises this
// alongside other concurrent tests when `go test -race ./...` runs.
func TestGetOrCompute_Singleflight(t *testing.T) {
	t.Parallel()
	inner := newFakeStore()
	ss := newStampedeStore(inner, 0, false, 0, nil)
	ctx := context.Background()

	const N = 50
	var computeCalls int32
	compute := func(ctx context.Context) ([]byte, error) {
		atomic.AddInt32(&computeCalls, 1)
		time.Sleep(10 * time.Millisecond) // make the race window real
		return []byte("computed"), nil
	}

	var wg sync.WaitGroup
	results := make([][]byte, N)
	errs := make([]error, N)
	wg.Add(N)
	for i := 0; i < N; i++ {
		go func(i int) {
			defer wg.Done()
			results[i], errs[i] = ss.getOrCompute(ctx, "k", time.Hour, nil, compute)
		}(i)
	}
	wg.Wait()

	if got := atomic.LoadInt32(&computeCalls); got != 1 {
		t.Errorf("compute ran %d times, want 1 (singleflight failed)", got)
	}
	for i, r := range results {
		if errs[i] != nil {
			t.Errorf("caller %d err: %v", i, errs[i])
		}
		if string(r) != "computed" {
			t.Errorf("caller %d got %q, want \"computed\"", i, r)
		}
	}
}

// TestGetOrCompute_HitsAfterFirstCompute: a second call returns from
// cache without invoking compute again — the standard hot-cache path.
func TestGetOrCompute_HitsAfterFirstCompute(t *testing.T) {
	inner := newFakeStore()
	ss := newStampedeStore(inner, 0, false, 0, nil)
	ctx := context.Background()

	var n int32
	compute := func(_ context.Context) ([]byte, error) {
		atomic.AddInt32(&n, 1)
		return []byte("v"), nil
	}
	if _, err := ss.getOrCompute(ctx, "k", time.Hour, nil, compute); err != nil {
		t.Fatalf("first call: %v", err)
	}
	if _, err := ss.getOrCompute(ctx, "k", time.Hour, nil, compute); err != nil {
		t.Fatalf("second call: %v", err)
	}
	if n != 1 {
		t.Errorf("compute called %d times across two getOrCompute, want 1", n)
	}
}

// TestGetOrCompute_ErrorDoesNotPoison pins that a failing compute does
// NOT cache the error or leave the wrapper in a stuck state — the next
// caller retries from scratch. singleflight.Do propagates the error to
// every concurrent waiter and then forgets it, which is the correct
// behaviour: a transient DB hiccup must not block all future Gets.
func TestGetOrCompute_ErrorDoesNotPoison(t *testing.T) {
	inner := newFakeStore()
	ss := newStampedeStore(inner, 0, false, 0, nil)
	ctx := context.Background()

	var calls int32
	failOnce := func(_ context.Context) ([]byte, error) {
		n := atomic.AddInt32(&calls, 1)
		if n == 1 {
			return nil, errors.New("transient db error")
		}
		return []byte("ok"), nil
	}

	if _, err := ss.getOrCompute(ctx, "k", time.Hour, nil, failOnce); err == nil {
		t.Fatal("first getOrCompute must propagate the compute error")
	}
	// Second call: same key, same wrapper. The cache stayed empty, so a
	// new singleflight group starts and failOnce returns the happy
	// branch.
	got, err := ss.getOrCompute(ctx, "k", time.Hour, nil, failOnce)
	if err != nil {
		t.Fatalf("second getOrCompute should succeed, got: %v", err)
	}
	if string(got) != "ok" {
		t.Errorf("second getOrCompute = %q, want \"ok\"", got)
	}
	if calls != 2 {
		t.Errorf("compute should have run exactly twice (one fail, one success), ran %d", calls)
	}
}

// TestNewStampedeStore_ClampsConfig: out-of-range knobs are clamped to
// safe values so user-supplied options can't break the wrapper.
func TestNewStampedeStore_ClampsConfig(t *testing.T) {
	ss := newStampedeStore(newFakeStore(), -0.5, true, -2.0, nil)
	if ss.jitterPct != 0 {
		t.Errorf("negative jitterPct should clamp to 0, got %v", ss.jitterPct)
	}
	if ss.beta != 0 {
		t.Errorf("negative beta should clamp to 0, got %v", ss.beta)
	}

	ss2 := newStampedeStore(newFakeStore(), 5, true, 1, nil)
	if ss2.jitterPct != 1 {
		t.Errorf("jitterPct > 1 should clamp to 1, got %v", ss2.jitterPct)
	}
}

// TestNewStampedeStore_PanicsOnNilInner is a defensive check: the
// wrapper is only meant to be installed by WithCacheStore around a
// real backing.
func TestNewStampedeStore_PanicsOnNilInner(t *testing.T) {
	defer func() {
		if recover() == nil {
			t.Error("expected panic on nil inner store")
		}
	}()
	_ = newStampedeStore(nil, 0.1, true, 1.0, nil)
}
