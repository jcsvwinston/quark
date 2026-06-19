// Copyright 2026 jcsvwinston
// SPDX-License-Identifier: Apache-2.0

package quark

import (
	"context"
	"fmt"
	"sort"
	"sync"
)

// Scatter-gather (ADR-0022, follow-up of ADR-0016). The cross-shard READ that
// ADR-0016 deliberately kept off the default path: a normal For[T] without a
// shard key still errors — fan-out never happens by accident. ScatterGather and
// ScatterCount are the EXPLICIT opt-in. They run the same read on every shard
// concurrently and merge. Read-only by construction: there is no cross-shard
// write or transaction (ADR-0016) — a Tx still belongs to a single shard.

// ScatterMerge controls how ScatterGather merges the per-shard results. The
// zero value (no Less, Limit 0) concatenates every shard's rows in shard-name
// order with no global cap.
type ScatterMerge[T any] struct {
	// Less, if set, merges the per-shard rows into a globally ordered slice
	// (reports whether a sorts before b). Apply the SAME OrderBy in the per-shard
	// query (build) so each shard returns its own rows sorted; Less then orders
	// across shards.
	Less func(a, b T) bool
	// Limit, if > 0, caps the merged result to the first Limit rows AFTER
	// ordering — the global top-N. For a correct global top-N also Limit(N) the
	// per-shard query in build, so each shard returns its own N candidates and
	// the merge keeps the N best overall.
	Limit int
}

// ScatterGather runs the read built by build on every shard CONCURRENTLY and
// merges the rows — the explicit cross-shard fan-out of ADR-0016 (never the
// implicit fallback of a forgotten shard key). build configures the per-shard
// query (the same query runs on each shard); merge orders and caps the combined
// result:
//
//	users, err := quark.ScatterGather(ctx, router,
//	    func(q *quark.Query[User]) *quark.Query[User] {
//	        return q.Where("active", "=", true).OrderBy("created_at", "DESC").Limit(20)
//	    },
//	    quark.ScatterMerge[User]{
//	        Less:  func(a, b User) bool { return a.CreatedAt.After(b.CreatedAt) },
//	        Limit: 20, // global top-20 across all shards
//	    },
//	)
//
// If any shard errors, ScatterGather returns that error (from the lowest-named
// shard that failed) — a partial result over disjoint shards is incomplete, so
// it is reported rather than silently truncated. Aggregates beyond count
// (AVG/MIN/MAX) and cross-shard GROUP BY are NOT merged across shards — run
// those per shard. The merged rows are materialised in memory.
func ScatterGather[T any](ctx context.Context, r *ShardRouter, build func(*Query[T]) *Query[T], merge ScatterMerge[T]) ([]T, error) {
	if r == nil {
		return nil, fmt.Errorf("%w: ScatterGather requires a non-nil ShardRouter", ErrInvalidQuery)
	}
	if build == nil {
		return nil, fmt.Errorf("%w: ScatterGather requires a non-nil build func", ErrInvalidQuery)
	}

	// Stable shard-name order: deterministic concat when no comparator is given,
	// and a deterministic shard to blame on error.
	names := r.ShardNames()
	sort.Strings(names)

	rows := make([][]T, len(names))
	errs := make([]error, len(names))
	var wg sync.WaitGroup
	for i, name := range names {
		wg.Add(1)
		// Each goroutine touches a distinct shard Client, its own fresh Query, and
		// distinct result slots — no shared mutable state, so this is race-free.
		go func(i int, c *Client) {
			defer wg.Done()
			rows[i], errs[i] = build(For[T](ctx, c)).List()
		}(i, r.shards[name])
	}
	wg.Wait()

	var merged []T
	for i, name := range names {
		if errs[i] != nil {
			return nil, fmt.Errorf("scatter shard %q: %w", name, errs[i])
		}
		merged = append(merged, rows[i]...)
	}

	if merge.Less != nil {
		sort.SliceStable(merged, func(a, b int) bool { return merge.Less(merged[a], merged[b]) })
	}
	if merge.Limit > 0 && len(merged) > merge.Limit {
		merged = merged[:merge.Limit]
	}
	return merged, nil
}

// ScatterCount runs the count built by build on every shard concurrently and
// SUMS the per-shard counts — the cross-shard COUNT of ADR-0016. build may add a
// WHERE, or be nil for a full count. Any shard error is returned (the sum would
// otherwise be silently low). Disjoint shards make the sum exact — a row lives
// on exactly one shard.
func ScatterCount[T any](ctx context.Context, r *ShardRouter, build func(*Query[T]) *Query[T]) (int64, error) {
	if r == nil {
		return 0, fmt.Errorf("%w: ScatterCount requires a non-nil ShardRouter", ErrInvalidQuery)
	}
	names := r.ShardNames()
	sort.Strings(names)

	counts := make([]int64, len(names))
	errs := make([]error, len(names))
	var wg sync.WaitGroup
	for i, name := range names {
		wg.Add(1)
		go func(i int, c *Client) {
			defer wg.Done()
			q := For[T](ctx, c)
			if build != nil {
				q = build(q)
			}
			counts[i], errs[i] = q.Count()
		}(i, r.shards[name])
	}
	wg.Wait()

	var total int64
	for i, name := range names {
		if errs[i] != nil {
			return 0, fmt.Errorf("scatter-count shard %q: %w", name, errs[i])
		}
		total += counts[i]
	}
	return total, nil
}
