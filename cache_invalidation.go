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

// invalidateInsert emits the cache invalidation for a just-completed INSERT
// whose PK was only revealed after the exec (Create assigns the auto-increment
// ID via RETURNING / LastInsertId). It invalidates the TABLE tag — so cached
// table-level reads (lists, filtered queries, aggregates) see the new row —
// plus the new row's row tag when the PK is a usable scalar.
//
// Why the table tag is invalidated HERE and not only in executeExec: the
// RETURNING / OUTPUT insert paths (Postgres, SQLite, MariaDB, MSSQL) run the
// INSERT through executeQueryRow, which invalidates nothing. Only the
// LastInsertId paths (MySQL, Oracle) go through executeExec, which already
// invalidates the table tag. Doing it here makes invalidation uniform across
// every dialect; re-invalidating the table tag on the executeExec paths is an
// idempotent no-op.
//
// No-op only when there's no cache or no table. A composite-PK insert (no
// scalar rowTag) still invalidates the table tag.
func (q *BaseQuery) invalidateInsert(ctx context.Context, pkValue any) {
	if q.client == nil || q.client.cacheStore == nil || q.table == "" {
		return
	}
	if tag := q.rowTag(pkValue); tag != "" {
		_ = q.client.cacheStore.InvalidateTags(ctx, q.table, tag)
		return
	}
	_ = q.client.cacheStore.InvalidateTags(ctx, q.table)
}

// invalidateBatchInsert is the batch sibling of invalidateInsert: it drops the
// TABLE tag plus every inserted row's row tag in a SINGLE InvalidateTags call,
// for a CreateBatch chunk that back-filled its PKs. The RETURNING scan path
// (executeQueryPrimary) invalidates nothing — same gap as single Create's
// RETURNING path — so without this a cached table-level read (list, filtered
// query, aggregate) goes stale after a batch insert on the RETURNING dialects
// (the batch sibling of BB-15). One call rather than one per row keeps a remote
// cache to a single round-trip on large batches. PKs without a scalar row tag
// (composite) contribute only the table tag. No-op without a cache or table.
func (q *BaseQuery) invalidateBatchInsert(ctx context.Context, pkValues []any) {
	if q.client == nil || q.client.cacheStore == nil || q.table == "" {
		return
	}
	tags := make([]string, 0, len(pkValues)+1)
	tags = append(tags, q.table)
	for _, pk := range pkValues {
		if t := q.rowTag(pk); t != "" {
			tags = append(tags, t)
		}
	}
	_ = q.client.cacheStore.InvalidateTags(ctx, tags...)
}
