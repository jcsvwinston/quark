// Copyright 2026 jcsvwinston
// SPDX-License-Identifier: Apache-2.0

package enginesuite

import (
	"context"
	"fmt"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/jcsvwinston/quark"
	"github.com/jcsvwinston/quark/cache/memory"
)

// QK-66: after a write through the query builder, a cached read answers what
// the database holds. Each case warms a cached List of the table (tagged with
// the table) and, where the write knows the key of a row it changed, a cached
// read of that row tagged with its row tag alone; then it writes, and asks the
// cached reads again. A cached read that still answers the warm result while
// the table holds another is a tag the write did not drop.
//
// Upsert through RETURNING (PostgreSQL, SQLite, MariaDB with its own dialect)
// dropped nothing: the statement reads its key back through the single-row
// query primitive, which invalidates nothing, and nothing after it did. A
// cached List kept answering one row after an upsert inserted a second.
// MySQL's path went through the exec primitive, which drops the table tag.
//
// The same gap, found by going through every write path:
//
//   - Upsert under RowLevelSecurityClient on PostgreSQL and SQLite (the
//     guarded RETURNING path).
//   - CreateBatch on SQL Server when a row fails: the rows before it stay
//     inserted, and the per-row form reads each key with a single-row query,
//     so nothing dropped the table tag for them.
//   - A write inside a transaction dropped its tags when the statement ran,
//     before the commit. A read from outside the transaction in between
//     cached what was committed then, and that stayed after the commit.
//   - Restore and DeleteBatch know the keys of the rows they write and did
//     not drop their row tags; Upsert did not either where it reads the key.
//
// Raw SQL (Client.Exec) is not parsed: it drops nothing, as documented, and
// the case below pins that.
//
// Every engine lane runs it through TestSuite<Engine>; the lanes with a
// statement cache run it again with WithStatementCache, and the MariaDB lane
// once more through the MySQL dialect.

type cwpRow struct {
	ID        int64      `db:"id" pk:"true"`
	TenantID  string     `db:"tenant_id,size=20"`
	Code      string     `db:"code,size=20" quark:"unique"`
	Name      string     `db:"name,size=40"`
	DeletedAt *time.Time `db:"deleted_at"`
}

func (cwpRow) TableName() string { return "qk66_cache_rows" }

const cwpTable = "qk66_cache_rows"

type cwpTenantKey struct{}

// cwpStore is the cache the cases read through: a memory store that reset
// swaps for an empty one, so no case answers from an entry an earlier case
// left. SQLite hands a key out again once the table is empty, and a row tag
// of a deleted row would otherwise name the new row.
type cwpStore struct {
	mu    sync.Mutex
	inner *memory.Store
}

func newCWPStore() *cwpStore { return &cwpStore{inner: memory.New()} }

func (s *cwpStore) cur() *memory.Store {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.inner
}

func (s *cwpStore) Get(ctx context.Context, key string) ([]byte, error) {
	return s.cur().Get(ctx, key)
}

func (s *cwpStore) Set(ctx context.Context, key string, val []byte, ttl time.Duration, tags ...string) error {
	return s.cur().Set(ctx, key, val, ttl, tags...)
}

func (s *cwpStore) Delete(ctx context.Context, key string) error {
	return s.cur().Delete(ctx, key)
}

func (s *cwpStore) InvalidateTags(ctx context.Context, tags ...string) error {
	return s.cur().InvalidateTags(ctx, tags...)
}

func (s *cwpStore) reset() {
	s.mu.Lock()
	old := s.inner
	s.inner = memory.New()
	s.mu.Unlock()
	old.Close()
}

func (s *cwpStore) close() { s.cur().Close() }

// testCacheWritePaths runs the cases on a cache-enabled client over the
// suite's database. opts are added to the client's options: the statement
// cache suites pass WithStatementCache, which WithOptions does not carry over.
func testCacheWritePaths(ctx context.Context, t *testing.T, client *quark.Client, opts ...any) {
	store := newCWPStore()
	defer store.close()
	limits := quark.DefaultLimits()
	limits.AllowRawQueries = true
	// XFetch off: a cached read recomputes only when a write dropped its
	// tag, never because the early refresh happened to fire.
	all := append([]any{quark.WithCacheStore(store), quark.WithCacheXFetchBeta(0), quark.WithLimits(limits)}, opts...)
	c, err := client.WithOptions(all...)
	if err != nil {
		t.Fatalf("a cache-enabled client: %v", err)
	}
	defer c.Close()
	runCacheWritePaths(ctx, t, c, store)

	// MariaDB answers RETURNING, which quark's MariaDB dialect uses; through
	// the MySQL dialect it takes MySQL's paths.
	if c.Dialect().Name() == "mariadb" {
		t.Run("MySQLDialectOnMariaDB", func(t *testing.T) {
			viaMySQL, err := quark.NewWithDB("mysql", c.Raw(), append([]any{quark.WithDialect(quark.MySQL())}, all...)...)
			if err != nil {
				t.Fatalf("a MySQL-dialect client over the MariaDB pool: %v", err)
			}
			defer viaMySQL.Close()
			runCacheWritePaths(ctx, t, viaMySQL, store)
		})
	}
}

func runCacheWritePaths(ctx context.Context, t *testing.T, c *quark.Client, store *cwpStore) {
	engine := c.Dialect().Name()
	quote := c.Dialect().Quote
	dropTable(c, cwpTable)
	if err := c.Migrate(ctx, &cwpRow{}); err != nil {
		t.Fatalf("migrate on %s: %v", engine, err)
	}
	t.Cleanup(func() { dropTable(c, cwpTable) })

	cfg := quark.DefaultTenantConfig()
	cfg.Strategy = quark.RowLevelSecurityClient
	cfg.BaseClient = c
	router := quark.NewTenantRouter(cfg, func(ctx context.Context) string {
		tenant, _ := ctx.Value(cwpTenantKey{}).(string)
		return tenant
	}, nil)
	ta := context.WithValue(ctx, cwpTenantKey{}, "ta")

	// seed empties the table, inserts a, b and c under tenant ta, and starts
	// the cache empty.
	seed := func(t *testing.T) map[string]int64 {
		t.Helper()
		if _, err := c.Raw().ExecContext(ctx, "DELETE FROM "+quote(cwpTable)); err != nil {
			t.Fatalf("clear on %s: %v", engine, err)
		}
		ids := make(map[string]int64, 3)
		for _, code := range []string{"a", "b", "c"} {
			r := cwpRow{TenantID: "ta", Code: code, Name: code + "0"}
			if err := quark.For[cwpRow](ctx, c).Create(&r); err != nil {
				t.Fatalf("seed %s on %s: %v", code, engine, err)
			}
			ids[code] = r.ID
		}
		store.reset()
		return ids
	}

	// list is the table as a List sees it, through the cache or not.
	list := func(t *testing.T, cached bool) []string {
		t.Helper()
		q := quark.For[cwpRow](ctx, c).OrderBy("code", "ASC")
		if cached {
			q = q.Cache(time.Minute)
		}
		rows, err := q.List()
		if err != nil {
			t.Fatalf("list (cached=%v) on %s: %v", cached, engine, err)
		}
		out := make([]string, len(rows))
		for i, r := range rows {
			out[i] = fmt.Sprintf("%d:%s:%s", r.ID, r.Code, r.Name)
		}
		return out
	}
	// row is one row read by its key, through the cache under its row tag
	// alone or not, trashed rows included.
	row := func(t *testing.T, id int64, cached bool) string {
		t.Helper()
		q := quark.For[cwpRow](ctx, c).Unscoped().Where("id", "=", id)
		if cached {
			q = q.Cache(time.Minute, fmt.Sprintf("%s:%d", cwpTable, id))
		}
		rows, err := q.List()
		if err != nil {
			t.Fatalf("read of %d (cached=%v) on %s: %v", id, cached, engine, err)
		}
		if len(rows) == 0 {
			return "absent"
		}
		r := rows[0]
		return fmt.Sprintf("%d:%s:%s trashed=%v", r.ID, r.Code, r.Name, r.DeletedAt != nil)
	}

	type writeCase struct {
		name string
		// setup runs before the cache is warmed.
		setup func(t *testing.T, ids map[string]int64)
		write func(ids map[string]int64) error
		// failing: the write returns an error, and what it leaves depends on
		// the engine — possibly nothing.
		failing bool
		// row is the code of the row whose row tag the write drops; "" when
		// it drops the table tag only.
		row string
	}
	q := func() *quark.Query[cwpRow] { return quark.For[cwpRow](ctx, c) }
	guarded := func() *quark.Query[cwpRow] { return quark.For[cwpRow](ta, router) }
	cases := []writeCase{
		{name: "Create", write: func(map[string]int64) error {
			return q().Create(&cwpRow{TenantID: "ta", Code: "d", Name: "d0"})
		}},
		{name: "CreateBatch", write: func(map[string]int64) error {
			return q().CreateBatch([]*cwpRow{{TenantID: "ta", Code: "d", Name: "d0"}, {TenantID: "ta", Code: "e", Name: "e0"}})
		}},
		// The second row repeats a's code. On MySQL, SQL Server and Oracle
		// the row before it stays inserted (the per-row form); on the
		// engines with RETURNING the batch is one statement and leaves
		// nothing.
		{name: "CreateBatchFailingOnARow", failing: true, write: func(map[string]int64) error {
			return q().CreateBatch([]*cwpRow{{TenantID: "ta", Code: "d", Name: "d0"}, {TenantID: "ta", Code: "a", Name: "dup"}, {TenantID: "ta", Code: "e", Name: "e0"}})
		}},
		{name: "Update", row: "a", write: func(ids map[string]int64) error {
			_, err := q().Update(&cwpRow{ID: ids["a"], Name: "a1"})
			return err
		}},
		{name: "UpdateFields", row: "a", write: func(ids map[string]int64) error {
			_, err := q().UpdateFields(&cwpRow{ID: ids["a"], Name: "a1"}, "name")
			return err
		}},
		{name: "UpdateMap", write: func(map[string]int64) error {
			_, err := q().Where("code", "=", "a").UpdateMap(map[string]any{"name": "a1"})
			return err
		}},
		{name: "UpdateBatch", row: "a", write: func(ids map[string]int64) error {
			return q().UpdateBatch([]*cwpRow{{ID: ids["a"], Name: "a1"}, {ID: ids["b"], Name: "b1"}})
		}},
		{name: "TrackedSave", row: "a", write: func(ids map[string]int64) error {
			tr, err := q().Track().Find(ids["a"])
			if err != nil {
				return err
			}
			tr.Entity.Name = "a1"
			_, err = tr.Save(ctx)
			return err
		}},
		{name: "UpsertUpdatingARow", row: "a", write: func(map[string]int64) error {
			return q().Upsert(&cwpRow{TenantID: "ta", Code: "a", Name: "a1"}, []string{"code"}, []string{"name"})
		}},
		{name: "UpsertInsertingARow", write: func(map[string]int64) error {
			return q().Upsert(&cwpRow{TenantID: "ta", Code: "d", Name: "d0"}, []string{"code"}, []string{"name"})
		}},
		{name: "UpsertBatch", write: func(map[string]int64) error {
			return q().UpsertBatch([]*cwpRow{{TenantID: "ta", Code: "a", Name: "a1"}, {TenantID: "ta", Code: "d", Name: "d0"}}, []string{"code"}, []string{"name"})
		}},
		{name: "UpsertUnderTenantGuard", row: "a", write: func(map[string]int64) error {
			return guarded().Upsert(&cwpRow{Code: "a", Name: "a1"}, []string{"code"}, []string{"name"})
		}},
		{name: "UpsertInsertingUnderTenantGuard", write: func(map[string]int64) error {
			return guarded().Upsert(&cwpRow{Code: "d", Name: "d0"}, []string{"code"}, []string{"name"})
		}},
		{name: "UpsertBatchUnderTenantGuard", write: func(map[string]int64) error {
			return guarded().UpsertBatch([]*cwpRow{{Code: "a", Name: "a1"}, {Code: "d", Name: "d0"}}, []string{"code"}, []string{"name"})
		}},
		{name: "SoftDelete", row: "a", write: func(ids map[string]int64) error {
			_, err := q().Delete(&cwpRow{ID: ids["a"]})
			return err
		}},
		{name: "Restore", row: "a", setup: func(t *testing.T, ids map[string]int64) {
			if _, err := q().Delete(&cwpRow{ID: ids["a"]}); err != nil {
				t.Fatalf("trash a on %s: %v", engine, err)
			}
		}, write: func(ids map[string]int64) error {
			_, err := q().Restore(&cwpRow{ID: ids["a"]})
			return err
		}},
		{name: "HardDelete", row: "a", write: func(ids map[string]int64) error {
			_, err := q().HardDelete(&cwpRow{ID: ids["a"]})
			return err
		}},
		{name: "DeleteBy", write: func(map[string]int64) error {
			_, err := q().Where("code", "=", "a").DeleteBy()
			return err
		}},
		{name: "DeleteBatch", row: "a", write: func(ids map[string]int64) error {
			_, err := q().DeleteBatch([]any{ids["a"], ids["b"]})
			return err
		}},
	}

	for _, wc := range cases {
		t.Run(wc.name, func(t *testing.T) {
			ids := seed(t)
			if wc.setup != nil {
				wc.setup(t, ids)
				store.reset()
			}
			warm := list(t, true)
			var id int64
			var warmRow string
			if wc.row != "" {
				id = ids[wc.row]
				warmRow = row(t, id, true)
			}

			err := wc.write(ids)
			if wc.failing != (err != nil) {
				t.Fatalf("%s on %s returned %v, want an error: %v", wc.name, engine, err, wc.failing)
			}

			db := list(t, false)
			if got := list(t, true); !slices.Equal(got, db) {
				t.Errorf("after %s on %s a cached List answers %v; the table holds %v — the table tag was not dropped", wc.name, engine, got, db)
			}
			if !wc.failing && slices.Equal(db, warm) {
				t.Fatalf("%s on %s changed nothing a List sees (%v): the case proves nothing", wc.name, engine, db)
			}
			if wc.row == "" {
				return
			}
			dbRow := row(t, id, false)
			if got := row(t, id, true); got != dbRow {
				t.Errorf("after %s on %s a read cached under %s:%d answers %q; the row is %q — the row tag was not dropped", wc.name, engine, cwpTable, id, got, dbRow)
			}
			if dbRow == warmRow {
				t.Fatalf("%s on %s did not change row %s (%q): the case proves nothing", wc.name, engine, wc.row, dbRow)
			}
		})
	}

	// A read from outside the transaction between its write and its commit
	// caches what is committed then. The write's tags have to be dropped
	// again when the transaction commits, or that entry outlives the commit.
	// On SQL Server the read waits for the transaction's locks, and SQLite
	// refuses it; there the read ends after the commit, or fails, and caches
	// nothing old.
	t.Run("InTransactionWithAReadBeforeTheCommit", func(t *testing.T) {
		ids := seed(t)
		warmList := list(t, true)
		warmRow := row(t, ids["a"], true)

		tx, err := c.BeginTx(ctx, nil)
		if err != nil {
			t.Fatalf("begin on %s: %v", engine, err)
		}
		defer func() { _ = tx.Rollback() }()
		if err := quark.ForTx[cwpRow](ctx, tx).Upsert(&cwpRow{TenantID: "ta", Code: "a", Name: "a1"}, []string{"code"}, []string{"name"}); err != nil {
			t.Fatalf("Upsert in the transaction on %s: %v", engine, err)
		}
		if err := quark.ForTx[cwpRow](ctx, tx).Create(&cwpRow{TenantID: "ta", Code: "d", Name: "d0"}); err != nil {
			t.Fatalf("Create in the transaction on %s: %v", engine, err)
		}

		// The outside read, on another connection of the pool, through the
		// cache. Its errors are the engine refusing it, which is not under
		// test.
		done := make(chan struct{})
		go func() {
			defer close(done)
			rctx, cancel := context.WithTimeout(ctx, 20*time.Second)
			defer cancel()
			_, _ = quark.For[cwpRow](rctx, c).OrderBy("code", "ASC").Cache(time.Minute).List()
			_, _ = quark.For[cwpRow](rctx, c).Unscoped().Where("id", "=", ids["a"]).Cache(time.Minute, fmt.Sprintf("%s:%d", cwpTable, ids["a"])).List()
		}()
		select {
		case <-done:
		case <-time.After(3 * time.Second):
			// Waiting for the transaction's locks: it reads after the commit.
		}
		if err := tx.Commit(); err != nil {
			t.Fatalf("commit on %s: %v", engine, err)
		}
		<-done

		db := list(t, false)
		if slices.Equal(db, warmList) {
			t.Fatalf("the transaction on %s changed nothing a List sees: the case proves nothing", engine)
		}
		if got := list(t, true); !slices.Equal(got, db) {
			t.Errorf("after the commit on %s a cached List answers %v; the table holds %v — the read between the write and the commit stayed", engine, got, db)
		}
		dbRow := row(t, ids["a"], false)
		if dbRow == warmRow {
			t.Fatalf("the transaction on %s did not change row a: the case proves nothing", engine)
		}
		if got := row(t, ids["a"], true); got != dbRow {
			t.Errorf("after the commit on %s a read cached under its row tag answers %q; the row is %q", engine, got, dbRow)
		}
	})

	// Raw SQL is not parsed for the tables it writes, and drops nothing:
	// the caller drops the tags (documented in reference/api/caching.mdx).
	t.Run("RawExecDropsNothing", func(t *testing.T) {
		seed(t)
		warm := list(t, true)
		d := c.Dialect()
		insert := fmt.Sprintf("INSERT INTO %s (%s, %s, %s) VALUES (%s, %s, %s)", quote(cwpTable),
			quote("tenant_id"), quote("code"), quote("name"), d.Placeholder(1), d.Placeholder(2), d.Placeholder(3))
		if err := c.Exec(ctx, insert, "ta", "d", "d0"); err != nil {
			t.Fatalf("raw insert on %s: %v", engine, err)
		}
		if got := list(t, true); !slices.Equal(got, warm) {
			t.Errorf("a raw Exec on %s dropped the table tag (the cached List answers %v, warm %v): update reference/api/caching.mdx, which says it drops nothing", engine, got, warm)
		}
		if err := store.InvalidateTags(ctx, cwpTable); err != nil {
			t.Fatal(err)
		}
		if got, db := list(t, true), list(t, false); !slices.Equal(got, db) {
			t.Errorf("after dropping the table tag by hand on %s the cached List answers %v; the table holds %v", engine, got, db)
		}
	})
}
