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
// can't) know the affected rows up front — DeleteBy and UpdateMap with
// their WHERE, the multi-row UpsertBatch of PostgreSQL, SQLite, MySQL and
// MariaDB. That preserves correctness for cached listings: even when
// row-level invalidation is available, the table tag is ALSO invalidated
// by every mutation, so listings are never left stale. Raw SQL
// (Client.Exec) is not parsed for the tables it writes and drops nothing.
//
// Every write path drops its tags once its statement has written, whatever
// primitive sent it (QK-66). The exec primitive does it inside the seam
// (stmt.write); a write that reads its keys back through a query —
// INSERT … RETURNING, OUTPUT, SCOPE_IDENTITY() — calls invalidateInsert or
// invalidateBatchInsert once it has them, and so does a write that fails
// after earlier rows of the same call stayed written. Upsert through
// RETURNING called neither, and a cached List kept the row count it had
// before the upsert.
//
// Inside a transaction the tags are dropped when the statement runs and
// again when the transaction commits (Client.invalidate, Tx.Commit): a read
// from outside the transaction between the two caches what is committed
// then, and the commit makes it stale.
//
// rowTag formatting uses fmt.Sprintf("%v", pk) — deliberately simple.
// Composite PKs aren't supported by this helper yet (they would
// require a stable, length-prefixed encoding to avoid the same kind
// of collision the cache key has guarded against since F4-4). A
// composite-PK row falls back to the table tag, same as a mutation
// with unknown PK.

import (
	"context"
	"database/sql"
	"fmt"
	"slices"
	"sync"
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
		q.client.invalidate(ctx, q.exec, []string{q.table, tag})
		return
	}
	q.client.invalidate(ctx, q.exec, []string{q.table})
}

// invalidateRowTags drops the row tags of the keys, without the table tag,
// for a write whose statement went through executeExec — which dropped the
// table tag when it ran — and whose keys were only known after it: the
// upsert of MySQL and MariaDB reads the key of the row it wrote from the
// statement's result. Keys without a scalar row tag contribute nothing.
func (q *BaseQuery) invalidateRowTags(ctx context.Context, pkValues ...any) {
	if q.client == nil || q.client.cacheStore == nil || q.table == "" {
		return
	}
	var tags []string
	for _, pk := range pkValues {
		if t := q.rowTag(pk); t != "" {
			tags = append(tags, t)
		}
	}
	q.client.invalidate(ctx, q.exec, tags)
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
	q.client.invalidate(ctx, q.exec, tags)
}

// invalidate drops tags from the client's cache. When exec is a
// transaction the client began, the tags are also kept with it, and
// Tx.Commit drops them again once the commit succeeds (QK-66): a read
// from outside the transaction between the write and the commit sees what
// was committed before, and caches it under the tags the write had
// already dropped. Dropping them only when the statement ran left that
// entry in place after the commit. The drop when the statement runs stays,
// so a cached read inside the transaction does not answer from an entry
// its own write made stale.
func (c *Client) invalidate(ctx context.Context, exec Executor, tags []string) {
	if c == nil || c.cacheStore == nil || len(tags) == 0 {
		return
	}
	_ = c.cacheStore.InvalidateTags(ctx, tags...)
	if tx, ok := exec.(*sql.Tx); ok {
		if v, ok := c.txCacheTags.Load(tx); ok {
			v.(*txTags).add(tags)
		}
	}
}

// txTags is the set of cache tags the writes of one transaction dropped.
type txTags struct {
	mu   sync.Mutex
	tags map[string]struct{}
}

func (t *txTags) add(tags []string) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.tags == nil {
		t.tags = make(map[string]struct{}, len(tags))
	}
	for _, tag := range tags {
		t.tags[tag] = struct{}{}
	}
}

// beginTxCacheTags starts keeping the cache tags of a transaction the client
// began. Without a cache store there is nothing to keep.
func (c *Client) beginTxCacheTags(tx *sql.Tx) {
	if c.cacheStore != nil {
		c.txCacheTags.Store(tx, &txTags{})
	}
}

// endTxCacheTags stops keeping the cache tags of a transaction that ended
// and returns them, sorted, so the commit drops them in one call.
func (c *Client) endTxCacheTags(tx *sql.Tx) []string {
	v, ok := c.txCacheTags.LoadAndDelete(tx)
	if !ok {
		return nil
	}
	t := v.(*txTags)
	t.mu.Lock()
	defer t.mu.Unlock()
	out := make([]string, 0, len(t.tags))
	for tag := range t.tags {
		out = append(out, tag)
	}
	slices.Sort(out)
	return out
}
