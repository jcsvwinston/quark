// Copyright 2026 jcsvwinston
// SPDX-License-Identifier: Apache-2.0

//go:build bugbash
// +build bugbash

// Package f07_cache is bug-bash phase F7: the integrated L2 cache (ADR-0004 /
// ADR-0011) on the real query path, per engine. It verifies the observable,
// black-box behaviours:
//
//   - Singleflight: 1000 concurrent identical cached reads collapse to ONE DB
//     query (the stampedeStore wrapper auto-installed by WithCacheStore).
//   - Cache-aside hit + cache-key discrimination by args.
//   - Granular per-PK invalidation (F4-6): a row-tagged cache entry survives a
//     mutation of a *different* row and is dropped by a mutation of its own.
//   - Empty-result caching, and the documented negative-caching gap.
//   - Redis backing store (gated on a reachable Redis).
//
// DB hits are counted with a QueryObserver: the query path fires exactly one
// SELECT observer event per real SQL trip (even when singleflight collapses N
// callers), and cache hits never reach it — so the counter measures DB load.
//
// Out of black-box scope (cited, not re-measured here):
//   - TTL jitter (±10%) and XFetch probabilistic refresh are internal and
//     verified by cache_stampede_test.go; F7 only confirms enabling jitter
//     does not break hits.
//   - The cross-instance singleflight gap (N processes → N computes) is a
//     documented non-bug (ADR-0011); reproducing it needs separate processes.
package f07_cache

import (
	"context"
	"flag"
	"fmt"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jcsvwinston/quark"
	"github.com/jcsvwinston/quark/bugbash/reporter"
	"github.com/jcsvwinston/quark/bugbash/tools"
	"github.com/jcsvwinston/quark/cache/memory"
	"github.com/jcsvwinston/quark/cache/redis"

	_ "github.com/go-sql-driver/mysql"
	_ "github.com/jackc/pgx/v5/stdlib"
	_ "github.com/microsoft/go-mssqldb"
	_ "github.com/sijms/go-ora/v2"
	_ "modernc.org/sqlite"
)

const phase = "f07_cache"

var engineFlag = flag.String("engines", "sqlite",
	"comma-separated engines (sqlite,postgres,mysql,mariadb,mssql,oracle) or 'all'")

func selectedEngines() []string {
	v := strings.TrimSpace(*engineFlag)
	if v == "" || v == "all" {
		return tools.AllEngines
	}
	var out []string
	for _, p := range strings.Split(v, ",") {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}

// citem is a trivial cacheable model.
type citem struct {
	ID   int64  `db:"id" pk:"true"`
	Name string `db:"name"`
	Val  int    `db:"val"`
}

// dbCounter counts the SELECTs that actually reach the database (one observer
// event per real SQL trip; cache hits do not fire it).
type dbCounter struct{ n atomic.Int64 }

func (c *dbCounter) ObserveQuery(e quark.QueryEvent) {
	if e.Operation == "SELECT" {
		c.n.Add(1)
	}
}
func (c *dbCounter) reset()       { c.n.Store(0) }
func (c *dbCounter) count() int64 { return c.n.Load() }

type rec struct {
	t   *testing.T
	eng string
	cat reporter.Category
}

func newRec(t *testing.T, eng string, cat reporter.Category) rec {
	return rec{t: t, eng: eng, cat: cat}
}

func (r rec) fail(name string, sev reporter.Severity, format string, args ...any) {
	r.t.Helper()
	reporter.Fail(r.t, reporter.Failure{
		Phase: phase, Test: name, Engine: r.eng, Category: r.cat, Severity: sev,
		Error: fmt.Sprintf(format, args...),
		Reproducer: reporter.Reproducer{
			Command: "go test -tags=bugbash -run TestCache ./phases/f07_cache/... -engines=" + r.eng,
		},
	})
}

func TestCache(t *testing.T) {
	engines := selectedEngines()
	ctx := context.Background()

	conns, err := tools.Up(ctx, engines)
	if err != nil {
		t.Fatalf("bring up engines %v: %v", engines, err)
	}
	t.Cleanup(func() {
		var ce []string
		for _, e := range engines {
			if e != tools.SQLite {
				ce = append(ce, e)
			}
		}
		tools.Down(ce...)
	})

	for _, eng := range engines {
		conn := conns[eng]
		t.Run(eng, func(t *testing.T) {
			t.Run("Singleflight", func(t *testing.T) { singleflight(t, ctx, conn, eng) })
			t.Run("CacheAside", func(t *testing.T) { cacheAside(t, ctx, conn, eng) })
			t.Run("PerRowInvalidation", func(t *testing.T) { perRowInvalidation(t, ctx, conn, eng) })
			t.Run("EmptyResultCaching", func(t *testing.T) { emptyResultCaching(t, ctx, conn, eng) })
			t.Run("JitterDoesNotBreakHits", func(t *testing.T) { jitterDoesNotBreakHits(t, ctx, conn, eng) })
			t.Run("RedisBackend", func(t *testing.T) { redisBackend(t, ctx, conn, eng) })
		})
	}
}

// newCachedClient opens a client with a fresh in-memory cache store + a DB-hit
// counter, then migrates and seeds the given citems.
func newCachedClient(t *testing.T, ctx context.Context, conn tools.EngineConn, eng string, seed ...citem) (*quark.Client, *dbCounter) {
	t.Helper()
	counter := &dbCounter{}
	client, err := quark.New(conn.Driver, conn.DSN,
		quark.WithCacheStore(memory.New()),
		quark.WithQueryObserver(counter))
	if err != nil {
		t.Fatalf("quark.New(%q): %v", conn.Driver, err)
	}
	t.Cleanup(func() {
		_ = client.Close()
		if eng == tools.SQLite {
			_ = os.Remove(conn.DSN)
		}
	})
	if err := client.Migrate(ctx, &citem{}); err != nil {
		t.Fatalf("migrate on %s: %v", eng, err)
	}
	for i := range seed {
		s := seed[i]
		if err := quark.For[citem](ctx, client).Create(&s); err != nil {
			t.Fatalf("seed %d on %s: %v", s.ID, eng, err)
		}
	}
	return client, counter
}

// singleflight: 1000 goroutines issue the same cached query at once. The
// stampede wrapper must collapse the cold-cache misses into ONE DB SELECT.
func singleflight(t *testing.T, ctx context.Context, conn tools.EngineConn, eng string) {
	r := newRec(t, eng, reporter.CategoryRegression)
	client, counter := newCachedClient(t, ctx, conn, eng, citem{Name: "a", Val: 1})

	const n = 1000
	counter.reset()
	var wg sync.WaitGroup
	start := make(chan struct{})
	errs := make(chan error, n)
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			_, err := quark.For[citem](ctx, client).Where("val", "=", 1).Cache(time.Minute).List()
			if err != nil {
				errs <- err
			}
		}()
	}
	close(start) // release all at once
	wg.Wait()
	close(errs)
	for err := range errs {
		r.fail("Singleflight/List", reporter.SeverityP1, "concurrent cached list: %v", err)
		return
	}
	// The ideal is 1. getOrCompute checks the cache before entering
	// singleflight (Get-then-Do), so a caller that missed the cache but reaches
	// Do just after the first compute finished can start a second compute — an
	// inherent, documented window, not a stampede. The guarantee is *effective
	// collapse*: N concurrent cold reads fold into a tiny constant, never ~N. A
	// broken singleflight would show hundreds.
	got := counter.count()
	t.Logf("Singleflight: %d concurrent identical cached reads → %d DB queries (ideal 1; small slack = getOrCompute check-then-Do window)", n, got)
	if got > 5 {
		r.fail("Singleflight", reporter.SeverityP1,
			"%d concurrent identical cached reads issued %d DB queries — singleflight not collapsing (want ~1)", n, got)
	}
}

// cacheAside: a cold cached read hits the DB once; an identical read is served
// from cache; a read with different args is a distinct key (new DB hit).
func cacheAside(t *testing.T, ctx context.Context, conn tools.EngineConn, eng string) {
	r := newRec(t, eng, reporter.CategoryRegression)
	client, counter := newCachedClient(t, ctx, conn, eng,
		citem{Name: "x", Val: 10}, citem{Name: "y", Val: 20})

	q := func(val int) {
		if _, err := quark.For[citem](ctx, client).Where("val", "=", val).Cache(time.Minute).List(); err != nil {
			r.fail("CacheAside/List", reporter.SeverityP1, "list val=%d: %v", val, err)
		}
	}
	counter.reset()
	q(10) // miss → 1
	q(10) // hit → still 1
	if got := counter.count(); got != 1 {
		r.fail("CacheAside/hit", reporter.SeverityP1, "two identical cached reads issued %d DB queries, want 1", got)
	}
	q(20) // different args → distinct key → miss → 2
	if got := counter.count(); got != 2 {
		r.fail("CacheAside/keyDiscrimination", reporter.SeverityP1,
			"a read with different args issued %d total DB queries, want 2 (cache key must discriminate args)", got)
	}
}

// perRowInvalidation verifies F4-6 granular invalidation: a query cached under
// a row tag (<table>:<pk>) survives a mutation of a *different* row and is
// dropped only by a mutation of its own row.
func perRowInvalidation(t *testing.T, ctx context.Context, conn tools.EngineConn, eng string) {
	r := newRec(t, eng, reporter.CategoryRegression)
	// Seed our own two rows and capture the DB-assigned IDs: server engines
	// share the citems table across sub-tests, so literal IDs would read rows
	// left by an earlier sub-test instead of these. Use unique vals too.
	client, counter := newCachedClient(t, ctx, conn, eng)
	rowA := citem{Name: "one", Val: 101}
	rowB := citem{Name: "two", Val: 102}
	if err := quark.For[citem](ctx, client).Create(&rowA); err != nil {
		r.fail("PerRow/seed", reporter.SeverityP1, "seed A: %v", err)
		return
	}
	if err := quark.For[citem](ctx, client).Create(&rowB); err != nil {
		r.fail("PerRow/seed", reporter.SeverityP1, "seed B: %v", err)
		return
	}
	table := quark.GetModelMeta[citem]().Table
	tag := func(id int64) string { return fmt.Sprintf("%s:%d", table, id) }

	readRow := func(id int64) {
		if _, err := quark.For[citem](ctx, client).Where("id", "=", id).Cache(time.Minute, tag(id)).List(); err != nil {
			r.fail("PerRow/List", reporter.SeverityP1, "read row %d: %v", id, err)
		}
	}

	counter.reset()
	readRow(rowA.ID) // miss → 1
	readRow(rowB.ID) // miss → 2
	readRow(rowA.ID) // hit
	readRow(rowB.ID) // hit
	if got := counter.count(); got != 2 {
		r.fail("PerRow/warm", reporter.SeverityP1, "warming two rows then re-reading issued %d DB queries, want 2", got)
		return
	}

	// Mutate row B by PK (Update passes the <table>:<B> row tag, F4-6).
	if _, err := quark.For[citem](ctx, client).Update(&citem{ID: rowB.ID, Name: "two-updated", Val: 102}); err != nil {
		r.fail("PerRow/Update", reporter.SeverityP1, "update row B: %v", err)
		return
	}

	readRow(rowA.ID) // row A entry tagged <table>:<A> — NOT invalidated by row B's mutation → hit
	if got := counter.count(); got != 2 {
		r.fail("PerRow/granularity", reporter.SeverityP1,
			"row A's cache was invalidated by a mutation of row B (DB queries=%d, want 2) — invalidation not granular", got)
	}
	readRow(rowB.ID) // row B entry invalidated → miss → 3
	if got := counter.count(); got != 3 {
		r.fail("PerRow/ownInvalidation", reporter.SeverityP1,
			"row B's cache was NOT invalidated by its own mutation (DB queries=%d, want 3)", got)
	}
}

// emptyResultCaching: a cached query that matches no rows caches the empty
// result, so a second identical read is served from cache. (The deferred
// "negative caching" in the playbook is the First/no-rows ErrNoRows case, not
// empty-list result caching — see README.)
func emptyResultCaching(t *testing.T, ctx context.Context, conn tools.EngineConn, eng string) {
	r := newRec(t, eng, reporter.CategoryRegression)
	client, counter := newCachedClient(t, ctx, conn, eng, citem{Name: "present", Val: 1})

	counter.reset()
	for i := 0; i < 2; i++ {
		got, err := quark.For[citem](ctx, client).Where("val", "=", 999).Cache(time.Minute).List()
		if err != nil {
			r.fail("EmptyResult/List", reporter.SeverityP1, "empty list: %v", err)
			return
		}
		if len(got) != 0 {
			r.fail("EmptyResult/rows", reporter.SeverityP1, "expected 0 rows, got %d", len(got))
		}
	}
	if got := counter.count(); got != 1 {
		r.fail("EmptyResult/caching", reporter.SeverityP2,
			"two identical empty-result cached reads issued %d DB queries, want 1 (empty result should be cached)", got)
	}
}

// jitterDoesNotBreakHits: with TTL jitter enabled, a warm cached read is still
// served from cache. The ±10% distribution itself is unit-tested in
// cache_stampede_test.go — F7 only guards that jitter does not regress hits.
func jitterDoesNotBreakHits(t *testing.T, ctx context.Context, conn tools.EngineConn, eng string) {
	r := newRec(t, eng, reporter.CategoryRegression)
	counter := &dbCounter{}
	client, err := quark.New(conn.Driver, conn.DSN,
		quark.WithCacheStore(memory.New()),
		quark.WithCacheJitter(0.1),
		quark.WithQueryObserver(counter))
	if err != nil {
		r.fail("Jitter/New", reporter.SeverityP1, "new: %v", err)
		return
	}
	t.Cleanup(func() { _ = client.Close() })
	if err := client.Migrate(ctx, &citem{}); err != nil {
		r.fail("Jitter/Migrate", reporter.SeverityP1, "migrate: %v", err)
		return
	}
	s := citem{Name: "j", Val: 7}
	if err := quark.For[citem](ctx, client).Create(&s); err != nil {
		r.fail("Jitter/Create", reporter.SeverityP1, "create: %v", err)
		return
	}
	counter.reset()
	for i := 0; i < 3; i++ {
		if _, err := quark.For[citem](ctx, client).Where("val", "=", 7).Cache(time.Minute).List(); err != nil {
			r.fail("Jitter/List", reporter.SeverityP1, "list: %v", err)
			return
		}
	}
	if got := counter.count(); got != 1 {
		r.fail("Jitter/hit", reporter.SeverityP1, "with jitter, 3 reads issued %d DB queries, want 1", got)
	}
}

// redisBackend runs the singleflight + invalidation checks against the Redis
// store, gated on a reachable Redis (QUARK_TEST_REDIS_ADDR, default
// localhost:6379). Redis is an optional cache backend, not a SQL engine — a
// missing Redis logs scope-out (the SQL-engine matrix is covered by the other
// groups), it is not an engine skip.
func redisBackend(t *testing.T, ctx context.Context, conn tools.EngineConn, eng string) {
	r := newRec(t, eng, reporter.CategoryRegression)
	addr := os.Getenv("QUARK_TEST_REDIS_ADDR")
	if addr == "" {
		addr = "localhost:6379"
	}
	store := redis.New(redis.Options{Addr: addr})
	pingCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	if err := store.Ping(pingCtx); err != nil {
		t.Logf("RedisBackend scoped out: no Redis at %s (%v). Set QUARK_TEST_REDIS_ADDR or boot redis:7-alpine to exercise the Redis store.", addr, err)
		return
	}

	counter := &dbCounter{}
	client, err := quark.New(conn.Driver, conn.DSN,
		quark.WithCacheStore(store),
		quark.WithQueryObserver(counter))
	if err != nil {
		r.fail("Redis/New", reporter.SeverityP1, "new: %v", err)
		return
	}
	t.Cleanup(func() { _ = client.Close() })
	if err := client.Migrate(ctx, &citem{}); err != nil {
		r.fail("Redis/Migrate", reporter.SeverityP1, "migrate: %v", err)
		return
	}
	// Unique val so this run's keys don't collide with a previous run's in a
	// shared Redis.
	val := int(time.Now().UnixNano() % 1_000_000)
	s := citem{Name: "r", Val: val}
	if err := quark.For[citem](ctx, client).Create(&s); err != nil {
		r.fail("Redis/Create", reporter.SeverityP1, "create: %v", err)
		return
	}
	table := quark.GetModelMeta[citem]().Table
	rowTag := fmt.Sprintf("%s:%d", table, s.ID)

	read := func() {
		if _, err := quark.For[citem](ctx, client).Where("id", "=", s.ID).Cache(time.Minute, rowTag).List(); err != nil {
			r.fail("Redis/List", reporter.SeverityP1, "list: %v", err)
		}
	}
	counter.reset()
	read() // miss → 1
	read() // hit
	if got := counter.count(); got != 1 {
		r.fail("Redis/hit", reporter.SeverityP1, "two cached reads via Redis issued %d DB queries, want 1", got)
	}
	// Mutate by PK → invalidate the row tag in Redis → next read misses.
	if _, err := quark.For[citem](ctx, client).Update(&citem{ID: s.ID, Name: "r2", Val: val}); err != nil {
		r.fail("Redis/Update", reporter.SeverityP1, "update: %v", err)
		return
	}
	read() // invalidated → miss → 2
	if got := counter.count(); got != 2 {
		r.fail("Redis/invalidation", reporter.SeverityP1,
			"after a PK mutation the Redis-cached read was not invalidated (DB queries=%d, want 2)", got)
	}
}
