// Copyright 2026 jcsvwinston
// SPDX-License-Identifier: Apache-2.0

package quark

// Cache stampede protection (F4-5, ADR-0011).
//
// stampedeStore is a CacheStore wrapper that adds three in-process
// protections on top of any concrete store (memory.Store, redis.Store, or
// a third-party implementation):
//
//   - **Singleflight** — N concurrent Get-or-compute calls for the same
//     key collapse to ONE compute. The others wait on the result.
//   - **TTL jitter** (±jitterPct) — adjacent Sets don't expire in the
//     same instant, so batched cache warmups don't converge on expiry.
//   - **XFetch / probabilistic early refresh** — near expiry, a Get
//     probabilistically signals "refresh me now" instead of waiting for
//     the deterministic miss. Re-computes the value while the cached
//     copy is still valid, smoothing the load curve.
//
// The wrapper still implements CacheStore (Get/Set/Delete/InvalidateTags)
// so existing code paths and third-party CacheStores keep working. The
// quark query path detects *stampedeStore via type assertion and uses
// the richer getOrCompute API when available; everything else falls back
// to the plain cache-aside Get/Set.
//
// Cross-instance stampede (N processes all missing the same key) is NOT
// addressed — the singleflight is in-process only. Documented gap; an
// ADR successor adds a DistributedLock hook if real demand surfaces.

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"log/slog"
	"math"
	"math/rand"
	"sync"
	"time"

	"golang.org/x/sync/singleflight"
)

// xfetchMagic identifies a stampedeStore-encoded entry. Bytes that don't
// start with this prefix are treated as a cache miss (legacy entries
// from before the wrapper was installed, or a third-party store writing
// through the wrapper's interface).
var xfetchMagic = []byte{0x51, 0x53, 0x50, 0x44} // "QSPD"

const xfetchEntryHeaderLen = 4 /*magic*/ + 1 /*version*/ + 8*3 /*deltaNs, computedAt, expiresAt*/ + 8 /*data len*/

// errStampedeMiss is returned when the encoded entry can't be decoded
// (corrupt or pre-wrapper). Tested with errors.Is by the Get path,
// which then treats it as a regular miss.
var errStampedeMiss = errors.New("stampede: encoded entry not recognised")

const (
	// stampedeLockKey prefixes the per-key cross-instance recompute lock
	// (ADR-0020) so it never collides with the cached value's own key.
	stampedeLockKey = "quark:stampede-lock:"
	// stampedeLockTTL bounds how long one process may hold recompute rights and
	// how long losers wait for its result. A recompute slower than this lets the
	// lock auto-expire so another process can take over — degrading to a few
	// computes, never the full herd. Generous enough for typical query+serialise.
	stampedeLockTTL = 5 * time.Second
	// stampedeWaitPoll is how often a waiting loser re-reads the cache for the
	// holder's published value.
	stampedeWaitPoll = 25 * time.Millisecond
)

// stampedeStore wraps any CacheStore with singleflight + jitter + XFetch.
type stampedeStore struct {
	inner         CacheStore
	jitterPct     float64 // 0..1; 0.1 = ±10%
	xfetchOn      bool
	beta          float64      // XFetch tuning; >0; default 1.0
	crossInstance bool         // ADR-0020: coordinate via CacheLocker across processes
	logger        *slog.Logger // optional; nil = silent

	sf     singleflight.Group
	randMu sync.Mutex // guards rng — math/rand is not goroutine-safe
	rng    *rand.Rand
	nowFn  func() time.Time // injectable for tests
}

// newStampedeStore wraps inner with the three stampede protections.
// jitterPct is clamped to [0, 1]; beta is clamped to >= 0; xfetchOn=false
// (or beta=0) disables XFetch but keeps singleflight + jitter active.
// logger is optional — passed nil, the wrapper is silent (suitable for
// tests). The Client wires its own *slog.Logger when installing the
// wrapper from WithCacheStore.
//
// inner == nil is a programming error and panics — the wrapper is only
// installed by WithCacheStore, which never passes nil.
func newStampedeStore(inner CacheStore, jitterPct float64, xfetchOn bool, beta float64, crossInstance bool, logger *slog.Logger) *stampedeStore {
	if inner == nil {
		panic("newStampedeStore: inner must not be nil")
	}
	if jitterPct < 0 {
		jitterPct = 0
	}
	if jitterPct > 1 {
		jitterPct = 1
	}
	if beta < 0 {
		beta = 0
	}
	return &stampedeStore{
		inner:         inner,
		jitterPct:     jitterPct,
		xfetchOn:      xfetchOn,
		beta:          beta,
		crossInstance: crossInstance,
		logger:        logger,
		// time-seeded rng is fine: XFetch only needs non-pathological
		// uniform draws; seeded for-uniqueness across processes.
		rng:   rand.New(rand.NewSource(time.Now().UnixNano())),
		nowFn: time.Now,
	}
}

// Get implements CacheStore. It decodes the xfetchEntry, evaluates the
// XFetch refresh probability, and either returns the cached payload or
// signals miss (errors.Is(err, errStampedeMiss)).
//
// Callers that use the richer getOrCompute API never see errStampedeMiss
// directly — that path issues the compute itself. Plain cache-aside
// callers that go through Get just receive the error from the inner
// store; an errStampedeMiss is treated by the query path as a miss and
// the compute proceeds.
func (s *stampedeStore) Get(ctx context.Context, key string) ([]byte, error) {
	raw, err := s.inner.Get(ctx, key)
	if err != nil {
		return nil, err
	}
	entry, decodeErr := decodeXfetchEntry(raw)
	if decodeErr != nil {
		return nil, fmt.Errorf("%w: %v", errStampedeMiss, decodeErr)
	}
	if s.xfetchOn && s.shouldEarlyRefresh(entry) {
		return nil, errStampedeMiss
	}
	return entry.data, nil
}

// Set implements CacheStore. The payload is wrapped with metadata
// (delta + computedAt + expiresAt) and the TTL is jittered before
// reaching the inner store. The delta is unknown when callers come
// through the plain Set surface — recorded as 0, which makes XFetch a
// no-op for that entry (zero delta → threshold zero → never early).
// Callers that go through getOrCompute supply the real delta.
func (s *stampedeStore) Set(ctx context.Context, key string, val []byte, ttl time.Duration, tags ...string) error {
	return s.setWithDelta(ctx, key, val, ttl, 0, tags...)
}

// setWithDelta is Set with the real compute delta — used by getOrCompute
// after it has measured the source operation.
func (s *stampedeStore) setWithDelta(ctx context.Context, key string, val []byte, ttl time.Duration, deltaNs int64, tags ...string) error {
	jittered := s.jitterTTL(ttl)
	now := s.nowFn()
	expiresAt := now.Add(jittered).UnixNano()
	encoded := encodeXfetchEntry(val, deltaNs, now.UnixNano(), expiresAt)
	return s.inner.Set(ctx, key, encoded, jittered, tags...)
}

// Delete implements CacheStore — pure pass-through.
func (s *stampedeStore) Delete(ctx context.Context, key string) error {
	return s.inner.Delete(ctx, key)
}

// InvalidateTags implements CacheStore — pure pass-through.
func (s *stampedeStore) InvalidateTags(ctx context.Context, tags ...string) error {
	return s.inner.InvalidateTags(ctx, tags...)
}

// computeFunc is the source-of-truth callback the query path passes to
// getOrCompute: it executes the underlying SQL (or whatever produces the
// cacheable bytes) and returns the serialised payload.
type computeFunc func(ctx context.Context) ([]byte, error)

// getOrCompute is the singleflight-protected fetch path. N concurrent
// callers for the same key collapse to one compute; the result feeds
// every waiter and is cached for ttl (jittered).
//
// Errors propagate from the inner store and from compute; the cache is
// not poisoned on compute error — a failure just bubbles up, the next
// call retries.
func (s *stampedeStore) getOrCompute(ctx context.Context, key string, ttl time.Duration, tags []string, compute computeFunc) ([]byte, error) {
	// Fast path: a hit that is NOT due for an early refresh.
	if data, err := s.Get(ctx, key); err == nil {
		return data, nil
	} else if !errors.Is(err, errStampedeMiss) {
		// Inner store error: fall through to compute, but log nothing —
		// the query path already logs DB errors. Treat any non-miss
		// inner error as if the entry didn't exist.
		_ = err
	}

	// Slow path: collapse concurrent missing/refreshing callers within THIS
	// process via singleflight. The single winner then coordinates ACROSS
	// processes when cross-instance is enabled and the store supports it.
	v, err, _ := s.sf.Do(key, func() (any, error) {
		if s.crossInstance {
			if locker, ok := s.inner.(CacheLocker); ok {
				return s.computeWithLock(ctx, locker, key, ttl, tags, compute)
			}
		}
		return s.computeAndSet(ctx, key, ttl, tags, compute)
	})
	if err != nil {
		return nil, err
	}
	return v.([]byte), nil
}

// computeAndSet runs compute, measures its delta (for XFetch), and writes the
// result to the inner store. A failed Set is logged, not fatal to this call —
// the value still returns; subsequent calls just re-pay the compute.
func (s *stampedeStore) computeAndSet(ctx context.Context, key string, ttl time.Duration, tags []string, compute computeFunc) (any, error) {
	start := s.nowFn()
	data, err := compute(ctx)
	if err != nil {
		return nil, err
	}
	deltaNs := s.nowFn().Sub(start).Nanoseconds()
	if setErr := s.setWithDelta(ctx, key, data, ttl, deltaNs, tags...); setErr != nil {
		if s.logger != nil {
			s.logger.Warn("cache set failed after compute", "key", key, "error", setErr)
		}
	}
	return data, nil
}

// computeWithLock coordinates the recompute ACROSS processes (ADR-0020). The
// in-process singleflight already reduced this process to one caller; here it
// races other processes for a per-key lock. The winner recomputes; losers
// wait-and-reread the winner's value, falling through to their own compute only
// if the wait times out (winner slow or dead — the lock auto-expires).
func (s *stampedeStore) computeWithLock(ctx context.Context, locker CacheLocker, key string, ttl time.Duration, tags []string, compute computeFunc) (any, error) {
	acquired, release, err := locker.AcquireLock(ctx, stampedeLockKey+key, stampedeLockTTL)
	if err != nil {
		// Lock backend hiccup: degrade to an uncoordinated compute rather than
		// fail or block the caller — cross-instance coordination is best-effort.
		if s.logger != nil {
			s.logger.Warn("cache cross-instance lock failed; computing without coordination",
				"key", key, "error", err)
		}
		return s.computeAndSet(ctx, key, ttl, tags, compute)
	}
	if acquired {
		if release != nil {
			defer func() { _ = release() }()
		}
		return s.computeAndSet(ctx, key, ttl, tags, compute)
	}
	// A peer holds the lock and is recomputing — wait for its result instead of
	// stampeding the source.
	if data, ok := s.waitForValue(ctx, key); ok {
		return data, nil
	}
	// Timed out (holder slow/dead; the lock auto-expires). Compute rather than
	// block indefinitely — degrades to a few computes under a pathologically
	// slow holder, never the full N-process herd.
	if s.logger != nil {
		s.logger.Debug("cache cross-instance wait timed out; computing", "key", key)
	}
	return s.computeAndSet(ctx, key, ttl, tags, compute)
}

// waitForValue polls Get until the lock holder publishes the value or the wait
// budget (stampedeLockTTL — the holder cannot keep the lock longer) elapses.
// Honors ctx cancellation. Returns ok=false on timeout/cancel.
func (s *stampedeStore) waitForValue(ctx context.Context, key string) ([]byte, bool) {
	deadline := time.Now().Add(stampedeLockTTL)
	ticker := time.NewTicker(stampedeWaitPoll)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return nil, false
		case <-ticker.C:
			if data, err := s.Get(ctx, key); err == nil {
				return data, true
			}
			if time.Now().After(deadline) {
				return nil, false
			}
		}
	}
}

// jitterTTL multiplies ttl by a uniform random factor in
// [1 - jitterPct, 1 + jitterPct]. Negative results are clamped to 0
// (a no-op TTL — inner store decides whether to keep it).
func (s *stampedeStore) jitterTTL(ttl time.Duration) time.Duration {
	if s.jitterPct == 0 || ttl <= 0 {
		return ttl
	}
	s.randMu.Lock()
	factor := 1 + (s.rng.Float64()*2-1)*s.jitterPct
	s.randMu.Unlock()
	out := time.Duration(float64(ttl) * factor)
	if out < 0 {
		return 0
	}
	return out
}

// shouldEarlyRefresh implements the XFetch probabilistic test (Vattani
// et al., "Optimal Probabilistic Cache Stampede Prevention"):
//
//	refresh ⇔ timeLeft <= delta * beta * (-ln(rand()))
//
// The right-hand side is a draw from Exp(1) scaled by the compute delta.
// As timeLeft → 0 the probability of early refresh → 1; far from expiry
// it stays near 0. With deltaNs = 0 (delta unknown) the threshold is 0
// and no early refresh ever fires — explicit safe behaviour for entries
// that came through the bare Set path.
func (s *stampedeStore) shouldEarlyRefresh(e xfetchEntry) bool {
	if e.deltaNs == 0 {
		return false
	}
	now := s.nowFn().UnixNano()
	timeLeftSec := float64(e.expiresAt-now) / 1e9
	if timeLeftSec <= 0 {
		return true
	}
	deltaSec := float64(e.deltaNs) / 1e9

	s.randMu.Lock()
	r := s.rng.Float64()
	s.randMu.Unlock()
	// rand.Float64 ∈ [0, 1); guard against the (vanishingly rare) zero
	// so -math.Log doesn't return +Inf.
	if r == 0 {
		return false
	}
	threshold := deltaSec * s.beta * -math.Log(r)
	return timeLeftSec <= threshold
}

// xfetchEntry is the in-memory shape of a stampedeStore-encoded entry.
type xfetchEntry struct {
	data       []byte
	deltaNs    int64
	computedAt int64 // unix nanos
	expiresAt  int64 // unix nanos
}

// encodeXfetchEntry packs an entry into bytes that any CacheStore can
// store as an opaque blob. Layout (big-endian):
//
//	[4] magic "QSPD"
//	[1] version (0x01)
//	[8] deltaNs (int64)
//	[8] computedAt (int64, unix nanos)
//	[8] expiresAt (int64, unix nanos)
//	[8] len(data) (uint64)
//	[N] data
func encodeXfetchEntry(data []byte, deltaNs, computedAt, expiresAt int64) []byte {
	out := make([]byte, xfetchEntryHeaderLen+len(data))
	copy(out[:4], xfetchMagic)
	out[4] = 0x01
	binary.BigEndian.PutUint64(out[5:13], uint64(deltaNs))
	binary.BigEndian.PutUint64(out[13:21], uint64(computedAt))
	binary.BigEndian.PutUint64(out[21:29], uint64(expiresAt))
	binary.BigEndian.PutUint64(out[29:37], uint64(len(data)))
	copy(out[xfetchEntryHeaderLen:], data)
	return out
}

// decodeXfetchEntry reverses encodeXfetchEntry. Returns an error when
// the magic prefix is absent, the version is unknown, or the data
// length doesn't match — Get treats any such error as a cache miss.
func decodeXfetchEntry(raw []byte) (xfetchEntry, error) {
	if len(raw) < xfetchEntryHeaderLen {
		return xfetchEntry{}, fmt.Errorf("entry too short: %d bytes", len(raw))
	}
	for i, b := range xfetchMagic {
		if raw[i] != b {
			return xfetchEntry{}, fmt.Errorf("magic prefix mismatch")
		}
	}
	if raw[4] != 0x01 {
		return xfetchEntry{}, fmt.Errorf("unknown stampede entry version: 0x%02x", raw[4])
	}
	e := xfetchEntry{
		deltaNs:    int64(binary.BigEndian.Uint64(raw[5:13])),
		computedAt: int64(binary.BigEndian.Uint64(raw[13:21])),
		expiresAt:  int64(binary.BigEndian.Uint64(raw[21:29])),
	}
	dataLen := binary.BigEndian.Uint64(raw[29:37])
	if uint64(len(raw)-xfetchEntryHeaderLen) != dataLen {
		return xfetchEntry{}, fmt.Errorf("data length mismatch: header says %d, body has %d", dataLen, len(raw)-xfetchEntryHeaderLen)
	}
	e.data = raw[xfetchEntryHeaderLen:]
	return e, nil
}
