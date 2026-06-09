package quark_test

import (
	"context"
	"testing"
	"time"

	"github.com/jcsvwinston/quark"
	"github.com/jcsvwinston/quark/cache/memory"
)

// cacheInvModel is a minimal table for the insert-invalidation regression.
// Auto-increment int PK so Create takes each engine's native insert path
// (RETURNING on PG/SQLite/MariaDB, OUTPUT/SCOPE_IDENTITY on MSSQL, LastInsertId
// on MySQL, BEGIN...RETURNING INTO on Oracle).
type cacheInvModel struct {
	ID  int64  `db:"id" pk:"true"`
	Tag string `db:"tag"`
}

func (cacheInvModel) TableName() string { return "cache_inv_t" }

// testCacheInsertInvalidation is the cross-engine regression for the cache
// freshness bug: a single-row Create must invalidate the TABLE tag so a
// table-level cached read (a list / filtered query / aggregate) reflects the
// new row. The RETURNING and OUTPUT/SCOPE_IDENTITY insert paths
// (PostgreSQL, SQLite, MariaDB, MSSQL) run through executeQueryRow, which
// invalidates nothing — so before the fix those four engines served a stale
// cached result after an INSERT. MySQL and Oracle insert via executeExec,
// which already invalidated the table tag. The fix invalidates the table tag
// uniformly in saveAny's post-insert step (idempotent for the executeExec
// paths).
//
// The assertion is observable without a statement counter: warm a cached
// list, insert a second matching row, re-run the identical cached query, and
// require it to see 2 rows. A stale cache returns the cached 1.
func testCacheInsertInvalidation(ctx context.Context, t *testing.T, baseClient *quark.Client) {
	t.Helper()

	dropTable(baseClient, "cache_inv_t")
	defer dropTable(baseClient, "cache_inv_t")

	store := memory.New()
	defer store.Close()

	// A cache-enabled clone on the same DSN. WithOptions re-opens the client
	// with the cache store installed; everything below runs on the clone.
	client, err := baseClient.WithOptions(quark.WithCacheStore(store))
	if err != nil {
		t.Fatalf("WithOptions(cache store): %v", err)
	}
	defer client.Close()

	if err := client.Migrate(ctx, &cacheInvModel{}); err != nil {
		t.Fatalf("migrate: %v", err)
	}

	if err := quark.For[cacheInvModel](ctx, client).Create(&cacheInvModel{Tag: "x"}); err != nil {
		t.Fatalf("seed first row: %v", err)
	}

	// Warm a table-level cached query (auto-tagged with the table name).
	// The 1-minute TTL is far longer than the few milliseconds this test
	// runs, so neither the memory store's background eviction nor the
	// default XFetch probabilistic early-refresh (β≈1.0) can turn the
	// re-query into a recompute for the wrong reason — the only way it
	// recomputes is the table-tag invalidation under test.
	first, err := quark.For[cacheInvModel](ctx, client).
		Where("tag", "=", "x").Cache(time.Minute).List()
	if err != nil {
		t.Fatalf("warm cached query: %v", err)
	}
	if len(first) != 1 {
		t.Fatalf("warm: want 1 row, got %d", len(first))
	}

	// Insert a second matching row — must invalidate the table tag.
	if err := quark.For[cacheInvModel](ctx, client).Create(&cacheInvModel{Tag: "x"}); err != nil {
		t.Fatalf("insert second row: %v", err)
	}

	// The identical cached query must now reflect the new row.
	second, err := quark.For[cacheInvModel](ctx, client).
		Where("tag", "=", "x").Cache(time.Minute).List()
	if err != nil {
		t.Fatalf("re-query after insert: %v", err)
	}
	if len(second) != 2 {
		t.Errorf("Create did not invalidate the table-level cache: re-query returned %d rows, want 2 (served a stale cache hit)", len(second))
	}
}
