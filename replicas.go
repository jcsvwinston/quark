// Copyright 2026 jcsvwinston
// SPDX-License-Identifier: Apache-2.0

package quark

import (
	"context"
	"database/sql"
)

// Read-replica routing (F6-5, ADR-0015). Multi-row reads route to a replica
// pool (registered via [WithReplicas]) round-robin; writes always use the
// primary. The routing decision lives in [BaseQuery.readExec], called from
// executeQuery (the multi-row read path). executeQueryRow is deliberately NOT
// routed — it is shared with the INSERT...RETURNING write path. See ADR-0015
// for the consistency model and exclusions.

type stickyKey struct{}

// Sticky returns a context that pins subsequent read queries to the primary
// connection instead of a read replica. Use it for read-your-writes: a read
// that must observe a write you just made (replicas are eventually consistent,
// so a normal read may be stale). Reads inside [Client.Tx] already use the
// transaction's connection and need no Sticky.
//
// It is a no-op when no replicas are configured.
func Sticky(ctx context.Context) context.Context {
	return context.WithValue(ctx, stickyKey{}, true)
}

// isSticky reports whether ctx was marked by [Sticky].
func isSticky(ctx context.Context) bool {
	v, _ := ctx.Value(stickyKey{}).(bool)
	return v
}

// pickReplica returns the next read-replica pool round-robin, or nil when no
// replicas are configured. The counter is atomic so concurrent readers spread
// across replicas without a lock.
func (c *Client) pickReplica() *sql.DB {
	n := uint64(len(c.replicas))
	if n == 0 {
		return nil
	}
	i := c.replicaRR.Add(1) - 1
	return c.replicas[i%n]
}

// readExec chooses the Executor for a read. It returns a read replica only
// when routing is both safe and enabled:
//
//   - the query's exec is the Client's primary *sql.DB — so a transaction's
//     *sql.Tx and the native-RLS executor (ADR-0012) pass through untouched,
//     keeping the read on the connection that holds its transactional or
//     session state;
//   - the context is not [Sticky]; and
//   - at least one replica is configured.
//
// Otherwise it returns q.exec unchanged. Writes never call this — they use
// q.exec (the primary) directly.
func (q *BaseQuery) readExec(ctx context.Context) Executor {
	if q.client == nil || isSticky(ctx) {
		return q.exec
	}
	db, ok := q.exec.(*sql.DB)
	if !ok || db != q.client.db {
		return q.exec
	}
	if r := q.client.pickReplica(); r != nil {
		return r
	}
	return q.exec
}
