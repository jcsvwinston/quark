// Copyright 2026 jcsvwinston
// SPDX-License-Identifier: Apache-2.0

//go:build bugbash
// +build bugbash

// Package f10_sharding is bug-bash phase F10: per-shard-key routing via
// ShardRouter (F6-7, ADR-0016).
//
//   - HashDistribution: HashShardFunc spreads keys roughly uniformly across the
//     shards (chi-square over the per-shard row counts).
//   - NoShardKeyErrors: an operation with no shard key in context errors —
//     there is no implicit cross-shard fan-out.
//   - CrossShardNoLeak: a row written under one shard key is visible only on its
//     owning shard, never when reading under a key that maps elsewhere.
//   - TxBoundToShard: a Tx obtained for one shard writes only to that shard; the
//     router hands different shards different *Client, so a Tx can never span
//     shards (no cross-shard transaction — ADR-0016).
//   - ReshardingAPIStable: adding a 5th shard and rebuilding the router with a
//     new HashShardFunc keeps the API working (resharding does not migrate data
//     — that is the operator's job — but routing stays sound).
//
// Shards are 4 (then 5) independent SQLite files — the spec's "4 SQLite files or
// 4 PG schemas". Routing is engine-agnostic (each shard is just a *Client), so
// SQLite is sufficient and needs no container; the per-shard engine is
// irrelevant to the routing logic under test.
//
// Scaled down from the spec (logged): 100k routed ops → a few thousand, enough
// for a meaningful distribution on SQLite without the soak-tier volume.
package f10_sharding

import (
	"context"
	"errors"
	"fmt"
	"math"
	"path/filepath"
	"testing"

	"github.com/jcsvwinston/quark"
	"github.com/jcsvwinston/quark/bugbash/reporter"

	_ "modernc.org/sqlite"
)

const (
	phase     = "f10_sharding"
	numShards = 4
	// routedKeys is scaled down from the spec's 100k (logged); deterministic
	// (fixed keys + FNV hash), so the distribution is reproducible, not flaky.
	routedKeys = 4000
)

type item struct {
	ID   int64  `db:"id" pk:"true"`
	Name string `db:"name"`
}

type rec struct {
	t   *testing.T
	cat reporter.Category
}

func newRec(t *testing.T, cat reporter.Category) rec { return rec{t: t, cat: cat} }

func (r rec) fail(name string, sev reporter.Severity, format string, args ...any) {
	r.t.Helper()
	reporter.Fail(r.t, reporter.Failure{
		Phase: phase, Test: name, Engine: "sqlite", Category: r.cat, Severity: sev,
		Error: fmt.Sprintf(format, args...),
		Reproducer: reporter.Reproducer{
			Command: "go test -tags=bugbash -run TestSharding ./phases/f10_sharding/...",
		},
	})
}

// shardSet holds the shard clients and the router over them.
type shardSet struct {
	names    []string
	clients  map[string]*quark.Client
	router   *quark.ShardRouter
	shardFor quark.ShardFunc
}

func TestSharding(t *testing.T) {
	ctx := context.Background()
	set := newShardSet(t, ctx, numShards)

	// Sub-tests share the same shardSet and accumulate rows, so they run
	// sequentially (no t.Parallel) — HashDistribution counts before the later
	// subtests write their own rows.
	t.Run("HashDistribution", func(t *testing.T) { hashDistribution(t, ctx, set) })
	t.Run("NoShardKeyErrors", func(t *testing.T) { noShardKeyErrors(t, ctx, set) })
	t.Run("CrossShardNoLeak", func(t *testing.T) { crossShardNoLeak(t, ctx, set) })
	t.Run("TxBoundToShard", func(t *testing.T) { txBoundToShard(t, ctx, set) })
	t.Run("ReshardingAPIStable", func(t *testing.T) { reshardingAPIStable(t, ctx) })
}

// newShardSet builds n SQLite-file shards, migrates the model on each, and wires
// a ShardRouter with HashShardFunc.
func newShardSet(t *testing.T, ctx context.Context, n int) shardSet {
	t.Helper()
	names := make([]string, n)
	clients := make(map[string]*quark.Client, n)
	for i := 0; i < n; i++ {
		name := fmt.Sprintf("shard%d", i)
		names[i] = name
		clients[name] = newShardClient(t, ctx, name)
	}
	shardFor := quark.HashShardFunc(names)
	router, err := quark.NewShardRouter(clients, quark.DefaultShardResolver, shardFor)
	if err != nil {
		t.Fatalf("NewShardRouter: %v", err)
	}
	return shardSet{names: names, clients: clients, router: router, shardFor: shardFor}
}

func newShardClient(t *testing.T, ctx context.Context, name string) *quark.Client {
	t.Helper()
	dsn := filepath.Join(t.TempDir(), name+".db")
	c, err := quark.New("sqlite", dsn)
	if err != nil {
		t.Fatalf("open shard %s: %v", name, err)
	}
	t.Cleanup(func() { _ = c.Close() })
	if err := c.Migrate(ctx, &item{}); err != nil {
		t.Fatalf("migrate shard %s: %v", name, err)
	}
	return c
}

func key(i int) string { return fmt.Sprintf("cust-%d", i) }

// repKeys returns, for each shard name, one shard key that routes to it.
func (s shardSet) repKeys(t *testing.T) map[string]string {
	t.Helper()
	rep := make(map[string]string, len(s.names))
	for i := 0; len(rep) < len(s.names) && i < 100000; i++ {
		k := key(i)
		name := s.shardFor(k)
		if _, ok := rep[name]; !ok {
			rep[name] = k
		}
	}
	if len(rep) != len(s.names) {
		t.Fatalf("could not find a representative key for every shard (got %d/%d)", len(rep), len(s.names))
	}
	return rep
}

// hashDistribution: write one row per key (routed by WithShardKey), then count
// rows per shard and assert the spread is roughly uniform via a chi-square test.
func hashDistribution(t *testing.T, ctx context.Context, s shardSet) {
	r := newRec(t, reporter.CategoryRegression)
	t.Logf("HashDistribution: routing %d keys over %d shards (spec target 100k is soak-tier)", routedKeys, len(s.names))

	for i := 0; i < routedKeys; i++ {
		kctx := quark.WithShardKey(ctx, key(i))
		if err := quark.For[item](kctx, s.router).Create(&item{Name: key(i)}); err != nil {
			r.fail("HashDistribution", reporter.SeverityP1, "routed create %d: %v", i, err)
			return
		}
	}

	// Per-shard counts, read through a representative key per shard.
	rep := s.repKeys(t)
	exp := float64(routedKeys) / float64(len(s.names))
	var chiSq float64
	total := int64(0)
	for name, k := range rep {
		// Filter to this phase's own rows ("cust-*") so the count is robust even
		// if later subtests (which write "leak-*"/"tx-*") were ever reordered.
		got, err := quark.For[item](quark.WithShardKey(ctx, k), s.router).Where("name", "LIKE", "cust-%").Count()
		if err != nil {
			r.fail("HashDistribution", reporter.SeverityP1, "count shard %s: %v", name, err)
			return
		}
		total += got
		chiSq += math.Pow(float64(got)-exp, 2) / exp
		t.Logf("  shard %s: %d rows (expected ~%.0f)", name, got, exp)
	}
	if total != routedKeys {
		r.fail("HashDistribution", reporter.SeverityP1, "shards hold %d rows total, want %d (rows lost or duplicated across shards)", total, routedKeys)
	}
	// df = numShards-1 = 3; chi-square critical value at p=0.05 is 7.815.
	const chiSqCritical = 7.815
	t.Logf("  chi-square = %.3f (df=%d, uniform if < %.3f)", chiSq, len(s.names)-1, chiSqCritical)
	if chiSq >= chiSqCritical {
		r.fail("HashDistribution", reporter.SeverityP1,
			"chi-square %.3f ≥ %.3f — HashShardFunc distribution is not uniform across shards", chiSq, chiSqCritical)
	}
}

// noShardKeyErrors: an operation without a shard key in context must error
// (ErrInvalidQuery) — there is no implicit cross-shard fan-out.
func noShardKeyErrors(t *testing.T, ctx context.Context, s shardSet) {
	r := newRec(t, reporter.CategoryRegression)

	// Write with no shard key.
	if err := quark.For[item](ctx, s.router).Create(&item{Name: "nokey"}); err == nil {
		r.fail("NoShardKeyErrors", reporter.SeverityP1, "create with no shard key succeeded, want an error (no implicit fan-out)")
	} else if !errors.Is(err, quark.ErrInvalidQuery) {
		r.fail("NoShardKeyErrors", reporter.SeverityP1, "create with no shard key errored with %v, want ErrInvalidQuery", err)
	}

	// Read with no shard key.
	if _, err := quark.For[item](ctx, s.router).Count(); err == nil {
		r.fail("NoShardKeyErrors", reporter.SeverityP1, "count with no shard key succeeded, want an error")
	} else if !errors.Is(err, quark.ErrInvalidQuery) {
		r.fail("NoShardKeyErrors", reporter.SeverityP1, "count with no shard key errored with %v, want ErrInvalidQuery", err)
	}

	// GetClient with no shard key errors directly too.
	if _, err := s.router.GetClient(ctx); err == nil {
		r.fail("NoShardKeyErrors", reporter.SeverityP1, "GetClient with no shard key succeeded, want an error")
	}
}

// crossShardNoLeak: a row written under shard key kA lives only on shard(kA); a
// read under a key that maps to a different shard never sees it.
func crossShardNoLeak(t *testing.T, ctx context.Context, s shardSet) {
	r := newRec(t, reporter.CategoryRegression)
	rep := s.repKeys(t)

	// Write one uniquely-named row per shard, under that shard's representative key.
	for name, k := range rep {
		rowName := "leak-" + name
		if err := quark.For[item](quark.WithShardKey(ctx, k), s.router).Create(&item{Name: rowName}); err != nil {
			r.fail("CrossShardNoLeak", reporter.SeverityP1, "write %s: %v", rowName, err)
			return
		}
	}

	// Each shard must contain only its own row, none of the others'.
	for name, k := range rep {
		own := "leak-" + name
		gotOwn, err := quark.For[item](quark.WithShardKey(ctx, k), s.router).Where("name", "=", own).Count()
		if err != nil {
			r.fail("CrossShardNoLeak", reporter.SeverityP1, "read own row on %s: %v", name, err)
			return
		}
		if gotOwn != 1 {
			r.fail("CrossShardNoLeak", reporter.SeverityP1, "shard %s has %d of its own row %q, want 1", name, gotOwn, own)
		}
		for otherName := range rep {
			if otherName == name {
				continue
			}
			other := "leak-" + otherName
			leaked, err := quark.For[item](quark.WithShardKey(ctx, k), s.router).Where("name", "=", other).Count()
			if err != nil {
				r.fail("CrossShardNoLeak", reporter.SeverityP1, "read foreign row on %s: %v", name, err)
				return
			}
			if leaked != 0 {
				r.fail("CrossShardNoLeak", reporter.SeverityP1,
					"shard %s leaked %d rows named %q (owned by shard %s) — cross-shard leak", name, leaked, other, otherName)
			}
		}
	}
}

// txBoundToShard: a Tx obtained for one shard writes only to that shard, and the
// router resolves distinct shards to distinct *Client — so a Tx can never span
// shards (no cross-shard transaction, ADR-0016).
func txBoundToShard(t *testing.T, ctx context.Context, s shardSet) {
	r := newRec(t, reporter.CategoryRegression)
	rep := s.repKeys(t)

	// Pick two keys that map to two different shards.
	var nameA, nameB, kA, kB string
	for name, k := range rep {
		if nameA == "" {
			nameA, kA = name, k
		} else if name != nameA {
			nameB, kB = name, k
			break
		}
	}

	cA, err := s.router.GetClient(quark.WithShardKey(ctx, kA))
	if err != nil {
		r.fail("TxBoundToShard", reporter.SeverityP1, "GetClient kA: %v", err)
		return
	}
	cB, err := s.router.GetClient(quark.WithShardKey(ctx, kB))
	if err != nil {
		r.fail("TxBoundToShard", reporter.SeverityP1, "GetClient kB: %v", err)
		return
	}
	if cA == cB {
		r.fail("TxBoundToShard", reporter.SeverityP1,
			"keys for shards %s and %s resolved to the SAME *Client — a Tx could silently span shards", nameA, nameB)
		return
	}

	// A Tx on shard A writes only to shard A. ForTx bypasses the router — the Tx
	// is already bound to cA's connection, so no shard key is needed here.
	const txRow = "tx-only-A"
	if err := cA.Tx(ctx, func(tx *quark.Tx) error {
		return quark.ForTx[item](ctx, tx).Create(&item{Name: txRow})
	}); err != nil {
		r.fail("TxBoundToShard", reporter.SeverityP1, "tx on shard A: %v", err)
		return
	}
	gotA, _ := quark.For[item](quark.WithShardKey(ctx, kA), s.router).Where("name", "=", txRow).Count()
	if gotA != 1 {
		r.fail("TxBoundToShard", reporter.SeverityP1, "tx row not on shard A (got %d, want 1)", gotA)
	}
	gotB, _ := quark.For[item](quark.WithShardKey(ctx, kB), s.router).Where("name", "=", txRow).Count()
	if gotB != 0 {
		r.fail("TxBoundToShard", reporter.SeverityP1, "tx row leaked to shard B (got %d, want 0) — tx not bound to a single shard", gotB)
	}
}

// reshardingAPIStable: adding a 5th shard and rebuilding the router with a fresh
// HashShardFunc keeps the API working. (Data is not migrated — that is the
// operator's job; this checks the routing/onboarding API does not break.)
func reshardingAPIStable(t *testing.T, ctx context.Context) {
	r := newRec(t, reporter.CategoryRegression)
	set5 := newShardSet(t, ctx, numShards+1)

	if len(set5.router.ShardNames()) != numShards+1 {
		r.fail("ReshardingAPIStable", reporter.SeverityP1, "router reports %d shards after resharding, want %d", len(set5.router.ShardNames()), numShards+1)
	}

	// Every shard must be reachable: write+read a row routed to each.
	rep := set5.repKeys(t)
	for name, k := range rep {
		rowName := "reshard-" + name
		if err := quark.For[item](quark.WithShardKey(ctx, k), set5.router).Create(&item{Name: rowName}); err != nil {
			r.fail("ReshardingAPIStable", reporter.SeverityP1, "write to reshard shard %s: %v", name, err)
			return
		}
		got, err := quark.For[item](quark.WithShardKey(ctx, k), set5.router).Where("name", "=", rowName).Count()
		if err != nil {
			r.fail("ReshardingAPIStable", reporter.SeverityP1, "read from reshard shard %s: %v", name, err)
			return
		}
		if got != 1 {
			r.fail("ReshardingAPIStable", reporter.SeverityP1, "reshard shard %s has %d rows, want 1", name, got)
		}
	}
}
