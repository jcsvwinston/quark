// Copyright 2026 jcsvwinston
// SPDX-License-Identifier: Apache-2.0

package quark

// Per-row cache invalidation (F4-6).
//
// Mutations that know the affected primary key emit, in addition to the
// historical table tag, a `<table>:<pk>` tag. Callers can cache by-PK
// queries with that tag so a row-scoped Update / Delete invalidates
// exactly those entries instead of every cached SELECT on the table.
//
// The table tag stays as the fallback for mutations that don't (or
// can't) know the affected rows up front — DeleteBatch with a complex
// WHERE, raw Exec, batch upsert in some engines. That preserves
// correctness for cached listings: even when row-level invalidation is
// available, the table tag is ALSO invalidated by every mutation, so
// listings are never left stale.
//
// rowTag formatting uses fmt.Sprintf("%v", pk) — deliberately simple.
// Composite PKs aren't supported by this helper yet (they would
// require a stable, length-prefixed encoding to avoid the same kind
// of collision the cache key has guarded against since F4-4). A
// composite-PK row falls back to the table tag, same as a mutation
// with unknown PK.

import (
	"context"
	"fmt"
)

// rowTag returns `<table>:<pk>` for a known scalar primary key, or ""
// when the table is empty, the pk is nil, or the row carries a
// composite PK (caller passes the model meta to detect that).
func (q *BaseQuery) rowTag(pkValue any) string {
	if q.table == "" || pkValue == nil {
		return ""
	}
	if q.meta != nil && q.meta.HasCompositePK {
		// Composite PKs need a stable encoding to be safely interned
		// in a tag; the table tag covers them for now.
		return ""
	}
	return q.table + ":" + fmt.Sprintf("%v", pkValue)
}

// invalidateRowTag emits a single InvalidateTags call carrying just the
// row tag for an already-executed mutation whose PK was only revealed
// after the exec (typically Create, where the database assigns the
// auto-increment ID via RETURNING / LastInsertId). The table tag has
// already been invalidated by executeExec; this call adds the row tag.
//
// No-op when there's no cache, no table, or no usable rowTag.
func (q *BaseQuery) invalidateRowTag(ctx context.Context, pkValue any) {
	if q.client == nil || q.client.cacheStore == nil {
		return
	}
	tag := q.rowTag(pkValue)
	if tag == "" {
		return
	}
	_ = q.client.cacheStore.InvalidateTags(ctx, tag)
}
