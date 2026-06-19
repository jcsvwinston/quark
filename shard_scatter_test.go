// Copyright 2026 jcsvwinston
// SPDX-License-Identifier: Apache-2.0

package quark_test

import (
	"context"
	"errors"
	"testing"

	"github.com/jcsvwinston/quark"

	_ "modernc.org/sqlite"
)

// seedScatterShards builds a 2-shard router and writes names u01..u06 routed by
// name. The a/b split is whatever the hash decides — scatter results must be
// correct regardless of how rows distribute.
func seedScatterShards(t *testing.T, tag string) (*quark.ShardRouter, []string) {
	t.Helper()
	ctx := context.Background()
	shards := map[string]*quark.Client{
		"a": newShard(t, "file:sc_"+tag+"_a?mode=memory&cache=shared"),
		"b": newShard(t, "file:sc_"+tag+"_b?mode=memory&cache=shared"),
	}
	router, err := quark.NewShardRouter(shards, quark.DefaultShardResolver, quark.HashShardFunc([]string{"a", "b"}))
	if err != nil {
		t.Fatalf("NewShardRouter: %v", err)
	}
	names := []string{"u01", "u02", "u03", "u04", "u05", "u06"}
	for _, n := range names {
		u := shUser{Name: n}
		if err := quark.For[shUser](quark.WithShardKey(ctx, n), router).Create(&u); err != nil {
			t.Fatalf("create %s: %v", n, err)
		}
	}
	return router, names
}

// TestScatterGatherOrderedMergeWithLimit: the global top-N read. Each shard
// returns its own top-3 by name DESC; the merge orders across shards and the
// global limit keeps the 3 best overall — independent of how rows sharded.
func TestScatterGatherOrderedMergeWithLimit(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	router, _ := seedScatterShards(t, "merge")

	got, err := quark.ScatterGather(ctx, router,
		func(q *quark.Query[shUser]) *quark.Query[shUser] {
			return q.OrderBy("name", "DESC").Limit(3)
		},
		quark.ScatterMerge[shUser]{
			Less:  func(a, b shUser) bool { return a.Name > b.Name },
			Limit: 3,
		},
	)
	if err != nil {
		t.Fatalf("ScatterGather: %v", err)
	}
	want := []string{"u06", "u05", "u04"}
	if len(got) != len(want) {
		t.Fatalf("got %d rows %v, want %d %v", len(got), namesOf(got), len(want), want)
	}
	for i, w := range want {
		if got[i].Name != w {
			t.Errorf("row %d = %q, want %q (full: %v)", i, got[i].Name, w, namesOf(got))
		}
	}
}

// TestScatterGatherConcatNoOrder: without WithScatterOrder, every shard's rows
// come back concatenated — all 6, no row lost or duplicated (shards are disjoint).
func TestScatterGatherConcatNoOrder(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	router, names := seedScatterShards(t, "concat")

	got, err := quark.ScatterGather(ctx, router,
		func(q *quark.Query[shUser]) *quark.Query[shUser] { return q.Limit(100) },
		quark.ScatterMerge[shUser]{})
	if err != nil {
		t.Fatalf("ScatterGather: %v", err)
	}
	if len(got) != len(names) {
		t.Fatalf("got %d rows, want %d (%v)", len(got), len(names), namesOf(got))
	}
	seen := map[string]int{}
	for _, u := range got {
		seen[u.Name]++
	}
	for _, n := range names {
		if seen[n] != 1 {
			t.Errorf("name %q appeared %d times, want exactly 1", n, seen[n])
		}
	}
}

// TestScatterCountSums: ScatterCount sums per-shard counts (exact, since shards
// are disjoint) — full count and a filtered count.
func TestScatterCountSums(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	router, names := seedScatterShards(t, "count")

	total, err := quark.ScatterCount[shUser](ctx, router, nil)
	if err != nil {
		t.Fatalf("ScatterCount: %v", err)
	}
	if total != int64(len(names)) {
		t.Errorf("ScatterCount=%d, want %d", total, len(names))
	}

	one, err := quark.ScatterCount[shUser](ctx, router,
		func(q *quark.Query[shUser]) *quark.Query[shUser] { return q.Where("name", "=", "u01") })
	if err != nil {
		t.Fatalf("ScatterCount filtered: %v", err)
	}
	if one != 1 {
		t.Errorf("filtered ScatterCount=%d, want 1", one)
	}
}

// TestScatterGuardsAndErrorPropagation: the setup guards reject nil router/build,
// and a per-shard query error propagates rather than yielding a partial result.
func TestScatterGuardsAndErrorPropagation(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	router, _ := seedScatterShards(t, "guard")

	build := func(q *quark.Query[shUser]) *quark.Query[shUser] { return q }
	if _, err := quark.ScatterGather(ctx, nil, build, quark.ScatterMerge[shUser]{}); !errors.Is(err, quark.ErrInvalidQuery) {
		t.Errorf("nil router: err = %v, want ErrInvalidQuery", err)
	}
	if _, err := quark.ScatterGather[shUser](ctx, router, nil, quark.ScatterMerge[shUser]{}); !errors.Is(err, quark.ErrInvalidQuery) {
		t.Errorf("nil build: err = %v, want ErrInvalidQuery", err)
	}
	if _, err := quark.ScatterCount[shUser](ctx, nil, nil); !errors.Is(err, quark.ErrInvalidQuery) {
		t.Errorf("nil router (count): err = %v, want ErrInvalidQuery", err)
	}

	// A hostile identifier is rejected by SQLGuard on every shard; the error
	// surfaces (not a silently incomplete result) and carries the guard sentinel.
	if _, err := quark.ScatterGather(ctx, router,
		func(q *quark.Query[shUser]) *quark.Query[shUser] { return q.Where("bad;ident", "=", 1) },
		quark.ScatterMerge[shUser]{}); !errors.Is(err, quark.ErrInvalidIdentifier) {
		t.Errorf("hostile identifier: err = %v, want ErrInvalidIdentifier", err)
	}
}

func namesOf(us []shUser) []string {
	out := make([]string, len(us))
	for i, u := range us {
		out[i] = u.Name
	}
	return out
}
