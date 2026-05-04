// Copyright 2026 jcsvwinston
// SPDX-License-Identifier: Apache-2.0

package quark

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
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

// generateCacheKey creates a deterministic hash for a query and its parameters.
func (q *BaseQuery) generateCacheKey(sqlStr string, args []any) string {
	h := sha256.New()
	h.Write([]byte(q.dialect.Name()))
	h.Write([]byte(q.tenantID))
	h.Write([]byte(q.schema))
	h.Write([]byte(sqlStr))

	for _, arg := range args {
		h.Write([]byte(fmt.Sprintf("%v", arg)))
	}

	return hex.EncodeToString(h.Sum(nil))
}
