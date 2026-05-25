// Copyright 2026 jcsvwinston
// SPDX-License-Identifier: Apache-2.0

package quark

import (
	"context"
	"database/sql"
	"math/rand/v2"
	"time"
)

// ReplicaStrategy selects which healthy read replica serves a routed read when
// more than one is configured (F6-5, ADR-0015). Set it with
// [WithReplicaStrategy]; the zero value is [ReplicaRoundRobin]. Every strategy
// honours the F6-6 health cooldown — a replica taken out of rotation by
// [Client.markReplicaDown] is never chosen until its cooldown expires.
type ReplicaStrategy int

const (
	// ReplicaRoundRobin spreads reads evenly by advancing an atomic cursor one
	// slot per read. Default (zero value); the most predictable distribution
	// under steady concurrency.
	ReplicaRoundRobin ReplicaStrategy = iota
	// ReplicaRandom picks a replica at random per read — uniform across all
	// replicas when they are healthy, and a replica in cooldown is skipped.
	// Needs no shared cursor, so it avoids the single contended atomic of
	// round-robin at the cost of a less even short-term distribution.
	ReplicaRandom
	// ReplicaLeastConn picks the healthy replica with the fewest in-use pool
	// connections (sql.DB.Stats().InUse) at selection time. Best when replica
	// query latencies are uneven, since it steers new reads toward the least
	// busy pool; it reads each replica's Stats per call (a cheap mutex-guarded
	// snapshot).
	ReplicaLeastConn
)

// Read-replica routing (F6-5, ADR-0015). Reads route to a replica pool
// (registered via [WithReplicas]) by a pluggable [ReplicaStrategy]
// (round-robin / random / least-conn); writes always use the primary. The
// routing decision lives in [BaseQuery.readExec], called from the multi-row
// read path (executeQuery) and the single-row read path (executeReadRow:
// Count and the aggregates). [BaseQuery.executeQueryRow] is the write-path
// single-row primitive — shared with INSERT...RETURNING / SCOPE_IDENTITY() —
// and is deliberately NOT routed. See ADR-0015 for the consistency model and
// exclusions.

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

// pickReplica returns a healthy read-replica pool chosen per c.replicaStrategy,
// or nil when no replicas are configured or all are currently marked unhealthy
// (F6-6) — in which case the caller reads from the primary. Every strategy
// skips any replica whose cooldown has not yet expired.
func (c *Client) pickReplica() *sql.DB {
	n := len(c.replicas)
	if n == 0 {
		return nil
	}
	now := time.Now().UnixNano()
	switch c.replicaStrategy {
	case ReplicaRandom:
		return c.pickReplicaRandom(n, now)
	case ReplicaLeastConn:
		return c.pickReplicaLeastConn(n, now)
	default:
		return c.pickReplicaRoundRobin(n, now)
	}
}

// pickReplicaRoundRobin scans up to n slots from the atomic round-robin cursor,
// returning the first healthy replica. Lock-free: the cursor is atomic and the
// health deadlines are atomics indexed in place.
func (c *Client) pickReplicaRoundRobin(n int, now int64) *sql.DB {
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

// pickReplicaRandom scans n slots from a random start, returning the first
// healthy replica. The random start makes the choice uniform when all replicas
// are healthy (no skips → returns replicas[start]); the forward scan reuses the
// same skip-unhealthy logic as round-robin, which slightly biases selection
// toward the replica following a cooled-down one while the cooldown lasts. It
// always returns a healthy replica when one exists.
func (c *Client) pickReplicaRandom(n int, now int64) *sql.DB {
	start := rand.IntN(n)
	for off := 0; off < n; off++ {
		i := (start + off) % n
		if c.replicaUnhealthyUntil[i].Load() <= now {
			return c.replicas[i]
		}
	}
	return nil
}

// pickReplicaLeastConn returns the healthy replica with the fewest in-use pool
// connections, or nil if all are in cooldown. Stats() is a cheap mutex-guarded
// snapshot; reading it per call is acceptable for a routing decision.
func (c *Client) pickReplicaLeastConn(n int, now int64) *sql.DB {
	var best *sql.DB
	bestInUse := int(^uint(0) >> 1) // max int
	for i := 0; i < n; i++ {
		if c.replicaUnhealthyUntil[i].Load() > now {
			continue
		}
		if inUse := c.replicas[i].Stats().InUse; inUse < bestInUse {
			bestInUse = inUse
			best = c.replicas[i]
		}
	}
	return best
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
