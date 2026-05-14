// Copyright 2026 jcsvwinston
// SPDX-License-Identifier: Apache-2.0

package quark

import (
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"fmt"
	"io"
	"math"
	"time"
)

// CacheStore defines the contract for any caching backend.
// Implementations should be provided in separate packages (e.g., quark/cache/redis).
type CacheStore interface {
	// Get retrieves a value from the cache.
	Get(ctx context.Context, key string) ([]byte, error)
	// Set stores a value in the cache with a specific TTL and associated tags.
	Set(ctx context.Context, key string, val []byte, ttl time.Duration, tags ...string) error
	// Delete removes a specific key.
	Delete(ctx context.Context, key string) error
	// InvalidateTags removes all entries associated with the given tags (usually table names).
	InvalidateTags(ctx context.Context, tags ...string) error
}

// CacheConfig holds the caching parameters for a specific query.
type CacheConfig struct {
	TTL     time.Duration
	Tags    []string
	Enabled bool
}

// cacheArg* are the type tags written before each bind argument in the
// cache-key stream. They make the encoding type-aware so two values that
// render identically under fmt.Sprintf("%v", arg) — int64(1) vs
// string("1"), nil vs "" — no longer collide into the same key.
const (
	cacheArgNil byte = iota
	cacheArgString
	cacheArgBytes
	cacheArgBool
	cacheArgInt
	cacheArgUint
	cacheArgFloat
	cacheArgTime
	cacheArgOther
)

// generateCacheKey creates a deterministic hash for a query and its
// parameters.
//
// Every component is length-prefixed and every bind argument is
// type-tagged. This closes three collision classes that the historical
// fmt.Sprintf("%v", arg) encoding allowed:
//
//   - type collision: int64(1) and string("1") both rendered as "1";
//   - boundary collision: dialect "my" + tenant "sql" hashed the same
//     stream as dialect "mysql" + tenant "", and args "ab"+"" the same
//     as "a"+"b" — there were no separators;
//   - nil collision: nil and "" both rendered as the empty-ish "".
//
// time.Time is keyed by UnixNano so the same instant in different
// time.Location values produces the same key (a legitimate cache hit)
// while distinct instants never collide.
func (q *BaseQuery) generateCacheKey(sqlStr string, args []any) string {
	h := sha256.New()
	writeLenPrefixed(h, q.dialect.Name())
	writeLenPrefixed(h, q.tenantID)
	writeLenPrefixed(h, q.schema)
	writeLenPrefixed(h, sqlStr)

	writeUint64(h, uint64(len(args)))
	for _, arg := range args {
		writeCacheArg(h, arg)
	}
	return hex.EncodeToString(h.Sum(nil))
}

// writeUint64 writes n as 8 big-endian bytes.
func writeUint64(h io.Writer, n uint64) {
	var b [8]byte
	binary.BigEndian.PutUint64(b[:], n)
	_, _ = h.Write(b[:])
}

// writeLenPrefixed writes len(s) as a uint64 followed by the bytes of s,
// so two adjacent string fields can never merge into an ambiguous stream.
func writeLenPrefixed(h io.Writer, s string) {
	writeUint64(h, uint64(len(s)))
	_, _ = io.WriteString(h, s)
}

// writeCacheArg writes a type-tagged, length-prefixed encoding of a single
// bind argument. Unknown types fall back to fmt.Sprintf("%#v", v): unlike
// %v it includes the concrete Go type (so a custom type can't collide
// with a primitive) and it does not invoke a Stringer — closing the
// "predictable Stringer collision" vector noted in docs/playbooks/cache.md.
// The encoding is deliberately reflection-free (ADR-0002): a plain type
// switch over the value.
//
// Width collapse is intentional. All signed-int widths share cacheArgInt,
// all unsigned widths share cacheArgUint, both float widths share
// cacheArgFloat. The cache key must collide exactly when two calls would
// produce the same database result: int(1) and int64(1) bind to the same
// wire value and run identical SQL, so sharing a key is a legitimate hit,
// not a dangerous collision. What MUST stay distinct is cross-kind —
// int64(1) vs uint64(1) vs float64(1) vs string("1") — and the kind tags
// guarantee that. (float32(x) is widened to float64 before hashing, so
// float32(0.1) and float64(0.1) differ — distinct bit patterns — while
// float32(1.0) and float64(1.0) share a key, again a legitimate hit.)
//
// Caveat: a map passed as a bind arg lands in the default branch, and
// fmt.Sprintf("%#v", aMap) does not guarantee key order — the cache key
// would not be stable across runs. Maps are not a normal database/sql
// bind type; callers that pass one own that instability.
func writeCacheArg(h io.Writer, arg any) {
	switch v := arg.(type) {
	case nil:
		_, _ = h.Write([]byte{cacheArgNil})
	case string:
		_, _ = h.Write([]byte{cacheArgString})
		writeLenPrefixed(h, v)
	case []byte:
		_, _ = h.Write([]byte{cacheArgBytes})
		writeUint64(h, uint64(len(v)))
		_, _ = h.Write(v)
	case bool:
		_, _ = h.Write([]byte{cacheArgBool})
		if v {
			_, _ = h.Write([]byte{1})
		} else {
			_, _ = h.Write([]byte{0})
		}
	case int:
		writeCacheInt(h, int64(v))
	case int8:
		writeCacheInt(h, int64(v))
	case int16:
		writeCacheInt(h, int64(v))
	case int32:
		writeCacheInt(h, int64(v))
	case int64:
		writeCacheInt(h, v)
	case uint:
		writeCacheUint(h, uint64(v))
	case uint8:
		writeCacheUint(h, uint64(v))
	case uint16:
		writeCacheUint(h, uint64(v))
	case uint32:
		writeCacheUint(h, uint64(v))
	case uint64:
		writeCacheUint(h, v)
	case float32:
		writeCacheFloat(h, float64(v))
	case float64:
		writeCacheFloat(h, v)
	case time.Time:
		_, _ = h.Write([]byte{cacheArgTime})
		writeUint64(h, uint64(v.UnixNano()))
	default:
		_, _ = h.Write([]byte{cacheArgOther})
		writeLenPrefixed(h, fmt.Sprintf("%#v", v))
	}
}

func writeCacheInt(h io.Writer, n int64) {
	_, _ = h.Write([]byte{cacheArgInt})
	writeUint64(h, uint64(n))
}

func writeCacheUint(h io.Writer, n uint64) {
	_, _ = h.Write([]byte{cacheArgUint})
	writeUint64(h, n)
}

func writeCacheFloat(h io.Writer, f float64) {
	_, _ = h.Write([]byte{cacheArgFloat})
	writeUint64(h, math.Float64bits(f))
}
