// Copyright 2026 jcsvwinston
// SPDX-License-Identifier: Apache-2.0

package quark

import (
	"context"
	"fmt"
	"sync"
)

// StrictReadsMode selects how much enforcement [WithStrictReads] applies
// to unbounded read entrypoints (#247). The zero value is [StrictReadsOff].
type StrictReadsMode int

const (
	// StrictReadsOff disables strict-read enforcement — the default and
	// the historical behaviour. The only cost the feature leaves on the
	// read path in this mode is one integer comparison per call: no
	// allocation, no context lookup.
	StrictReadsOff StrictReadsMode = iota

	// StrictReadsWarn logs a structured WARN — through the same client
	// logger that already announces List() without Limit() — when Iter()
	// or Cursor() run without an explicit Limit() and without
	// [Query.AllowUnbounded]. It also enables N+1 detection inside
	// contexts prepared with [TrackReads].
	StrictReadsWarn

	// StrictReadsReject upgrades the unbounded Iter()/Cursor() WARN to an
	// [ErrInvalidQuery] error. N+1 detection stays WARN-only: it is a
	// heuristic (a threshold over a per-context counter), and a heuristic
	// must never fail an otherwise valid query.
	StrictReadsReject
)

// WithStrictReads enables the opt-in strict-read mode (#247).
//
// Rationale and scope, decided at design time:
//
//   - List() is NOT affected: it keeps its historical safe-default cap of
//     100 rows plus its own WARN. Strict reads covers the entrypoints
//     that have no implicit cap — Iter() and Cursor() — where forgetting
//     Limit() silently streams the whole table.
//
//   - Escalation is per client, not per query: WARN by default
//     ([StrictReadsWarn]), reject with [ErrInvalidQuery] under
//     [StrictReadsReject]. Legitimately unbounded reads (exports,
//     backfills) opt out per query with [Query.AllowUnbounded].
//
//   - N+1 detection is per context, because "same read repeated" is only
//     meaningful within one request/operation scope: prepare the context
//     with [TrackReads] and every single-row read by primary key is
//     counted per table; at the threshold (10) the client logs ONE
//     structured WARN per context+table suggesting Preload. Without a
//     TrackReads context nothing is counted. Detection never rejects.
//
//   - Cost when off: a single integer comparison per read entrypoint —
//     no map, no allocation, no context traversal.
//
//     client, _ := quark.New("pgx", dsn, quark.WithStrictReads(quark.StrictReadsWarn))
func WithStrictReads(mode StrictReadsMode) Option {
	return func(c *Client) {
		c.strictReads = mode
	}
}

// nPlusOneThreshold is the number of single-row primary-key reads on the
// same table, within one [TrackReads] context, at which the N+1 WARN
// fires. Fixed on purpose: the value only needs to separate "a handful of
// point reads" from "a read per row of a previous List", and a tunable
// threshold would grow the option surface for no decision a caller can
// meaningfully make.
const nPlusOneThreshold = 10

// readTrackerKey is the context key for the per-request read tracker.
type readTrackerKey struct{}

// readTracker accumulates single-row PK reads per table within one
// context tree. Mutex-guarded: one request scope may run queries from
// several goroutines.
type readTracker struct {
	mu     sync.Mutex
	counts map[string]int  // table → point reads seen
	warned map[string]bool // table → N+1 WARN already emitted
}

// TrackReads returns a context that counts single-row primary-key reads
// per table for N+1 detection (#247). Attach it at a request/operation
// boundary; every quark read built from the returned context (or a child
// of it) shares the same counters. When a table accumulates 10 point
// reads by primary key in one tracked context — the classic "First/Find
// per row of a previous List" loop — the client logs one structured WARN
// for that context+table suggesting Preload.
//
// Only active when the client runs with [WithStrictReads] at
// [StrictReadsWarn] or [StrictReadsReject]; on an untracked context, or
// under [StrictReadsOff], nothing is counted and nothing is allocated on
// the read path. Cache-served reads count too: the pattern costs a round
// trip per row again the moment the entries expire.
func TrackReads(ctx context.Context) context.Context {
	return context.WithValue(ctx, readTrackerKey{}, &readTracker{
		counts: make(map[string]int),
		warned: make(map[string]bool),
	})
}

// strictReadCheck enforces WithStrictReads on the entrypoints without an
// implicit cap (Iter/Cursor). Returns nil under StrictReadsOff (single
// integer comparison), when the query carries an explicit Limit(), or
// when the caller declared the read intentionally unbounded via
// AllowUnbounded(). Otherwise it warns (StrictReadsWarn) or rejects with
// ErrInvalidQuery (StrictReadsReject).
func (q *BaseQuery) strictReadCheck(entrypoint string) error {
	if q.client == nil || q.client.strictReads == StrictReadsOff {
		return nil
	}
	if q.hasLimit || q.allowUnbounded {
		return nil
	}
	if q.client.strictReads == StrictReadsReject {
		return fmt.Errorf("%w: %s without explicit Limit() under strict reads; call Limit(n), or AllowUnbounded() for an intentional export/backfill",
			ErrInvalidQuery, entrypoint)
	}
	q.client.logger.Warn("unbounded read under strict reads: no explicit Limit(); the query streams every matching row. Call Limit(n), or AllowUnbounded() for an intentional export/backfill.",
		"entrypoint", entrypoint,
		"table", q.table,
	)
	return nil
}

// noteNPlusOneRead feeds the per-context N+1 detector. Called from List()
// only when strict reads is enabled; the cheap shape checks (single WHERE
// condition on the primary key with '=', LIMIT 1 — the First/Find point
// read) run before the context lookup so untracked or non-point reads pay
// as little as possible.
func (q *BaseQuery) noteNPlusOneRead() {
	if q.limit != 1 || len(q.where) != 1 {
		return
	}
	cond := q.where[0]
	if cond.column != q.pk.Column || cond.operator != "=" || len(cond.group) > 0 {
		return
	}
	tracker, _ := q.ctx.Value(readTrackerKey{}).(*readTracker)
	if tracker == nil {
		return
	}

	tracker.mu.Lock()
	tracker.counts[q.table]++
	n := tracker.counts[q.table]
	fire := n >= nPlusOneThreshold && !tracker.warned[q.table]
	if fire {
		tracker.warned[q.table] = true
	}
	tracker.mu.Unlock()

	if fire {
		q.client.logger.Warn("possible N+1 read pattern: repeated single-row reads by primary key on the same table within one tracked context. Preload the relation (or batch with WhereIn) to fetch it in one query.",
			"table", q.table,
			"reads", n,
		)
	}
}
