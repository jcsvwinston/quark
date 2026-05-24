// Copyright 2026 jcsvwinston
// SPDX-License-Identifier: Apache-2.0

package quark

import (
	"context"
	"database/sql"
	"time"
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

// pickReplica returns the next healthy read-replica pool round-robin, or nil
// when no replicas are configured or all are currently marked unhealthy (F6-6)
// — in which case the caller reads from the primary. It scans up to n slots
// from the atomic round-robin cursor, skipping any replica whose cooldown has
// not yet expired. Lock-free: the cursor is atomic and the health deadlines are
// atomics indexed in place.
func (c *Client) pickReplica() *sql.DB {
	n := len(c.replicas)
	if n == 0 {
		return nil
	}
	now := time.Now().UnixNano()
	// One atomic Add per call gives each concurrent caller a unique starting
	// slot, which spreads load evenly under concurrency — the property that
	// matters most for a read balancer. The cursor also advances on the
	// all-unhealthy (return nil) path; that is benign, since round-robin only
	// promises spreading, not a fixed sequence, and the case is transient.
	start := c.replicaRR.Add(1) - 1
	for off := 0; off < n; off++ {
		i := int((start + uint64(off)) % uint64(n))
		if c.replicaUnhealthyUntil[i].Load() <= now {
			return c.replicas[i]
		}
	}
	return nil
}

// markReplicaDown takes a replica out of rotation for replicaDownCooldown after
// a transient connection failure (F6-6). pickReplica skips it until the
// cooldown expires, after which it is tried again (passive recovery — no active
// health-check goroutine). No-op if rdb is not one of this Client's replicas.
func (c *Client) markReplicaDown(rdb *sql.DB) {
	until := time.Now().Add(c.replicaDownCooldown).UnixNano()
	for i, r := range c.replicas {
		if r == rdb {
			c.replicaUnhealthyUntil[i].Store(until)
			if c.logger != nil {
				c.logger.Warn("read replica marked unhealthy after transient error; routing reads to primary until cooldown expires",
					"event", "quark.replica.down",
					"cooldown", c.replicaDownCooldown)
			}
			return
		}
	}
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
