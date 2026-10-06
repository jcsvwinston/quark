// Copyright 2026 jcsvwinston
// SPDX-License-Identifier: Apache-2.0

package enginesuite

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jcsvwinston/quark"
)

// WithStatementCache on every engine (QK-37, A12 Q3, ADR-0027): the same
// reads and writes, inside and outside transactions, from many goroutines,
// with a cache small enough that statements are evicted while others run;
// a failed statement leaves its cached statement usable; a column added to
// the table under a cached statement does not break it; and on MySQL and
// MariaDB the server's count of prepared statements stays bounded and
// returns to where it was when the client closes.
//
// PostgreSQL ignores the option (pgx keeps its own cache); its leg runs the
// same workload to show that asking for it there changes nothing.

type stmtCacheRow struct {
	ID   int64  `db:"id" pk:"true"`
	Name string `db:"name,size=120" quark:"unique"`
	Age  int    `db:"age"`
}

func (stmtCacheRow) TableName() string { return "qk37_stmt_rows" }

type stmtCacheVersioned struct {
	ID      int64  `db:"id" pk:"true"`
	Owner   string `db:"owner,size=40"`
	Name    string `db:"name,size=40"`
	Version int64  `db:"version" quark:"version"`
}

func (stmtCacheVersioned) TableName() string { return "qk37_stmt_versioned" }

func TestStatementCacheAllEngines(t *testing.T) {
	if testing.Short() {
		t.Skip("engine test")
	}
	legs := []struct {
		name    string
		env     string
		resolve func(*testing.T) string
		drv     string
	}{
		{"SQLite", "", func(*testing.T) string { return "file:qk37?mode=memory&cache=shared" }, "sqlite"},
		{"Postgres", "QUARK_TEST_POSTGRES_DSN", resolvePostgresDSN, "pgx"},
		{"MySQL", "QUARK_TEST_MYSQL_DSN", resolveMySQLDSN, "mysql"},
		{"MariaDB", "QUARK_TEST_MARIADB_DSN", resolveMariaDBDSN, "mysql"},
		{"MSSQL", "QUARK_TEST_MSSQL_DSN", resolveMSSQLDSN, "sqlserver"},
		{"Oracle", "QUARK_TEST_ORACLE_DSN", resolveOracleDSN, "oracle"},
	}
	for _, lg := range legs {
		t.Run(lg.name, func(t *testing.T) {
			dsn := lg.resolve(t)
			if dsn == "" {
				t.Skipf("%s not set (rebuild with -tags=integration to spin up a container); %s leg skipped", lg.env, lg.name)
			}
			testStatementCache(t, lg.drv, dsn)
		})
	}
}

func testStatementCache(t *testing.T, drv, dsn string) {
	ctx := context.Background()
	limits := quark.DefaultLimits()
	limits.AllowRawQueries = true
	admin, err := quark.New(drv, dsn, quark.WithLimits(limits))
	if err != nil {
		t.Fatal(err)
	}
	defer admin.Close()
	_ = admin.Exec(ctx, "DROP TABLE qk37_stmt_rows")
	_ = admin.Exec(ctx, "DROP TABLE qk37_stmt_versioned")
	if err := admin.Migrate(ctx, &stmtCacheRow{}, &stmtCacheVersioned{}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = admin.Exec(context.Background(), "DROP TABLE qk37_stmt_rows")
		_ = admin.Exec(context.Background(), "DROP TABLE qk37_stmt_versioned")
	})

	mysqlLike := drv == "mysql"
	before := 0
	if mysqlLike {
		before = preparedOnServer(t, admin)
	}

	const size, conns = 4, 4
	c, err := quark.New(drv, dsn, quark.WithLimits(limits), quark.WithStatementCache(size), quark.WithMaxOpenConns(conns))
	if err != nil {
		t.Fatal(err)
	}
	closed := false
	defer func() {
		if !closed {
			_ = c.Close()
		}
	}()

	writers := 8
	if drv == "sqlite" {
		writers = 1 // one writer at a time: SQLite would answer SQLITE_BUSY
	}

	// Rows to read back, each with a name its key determines.
	const rows = 40
	ids := make([]int64, rows)
	for i := range ids {
		r := stmtCacheRow{Name: fmt.Sprintf("row-%03d", i), Age: i}
		if err := quark.For[stmtCacheRow](ctx, c).Create(&r); err != nil {
			t.Fatal(err)
		}
		ids[i] = r.ID
	}
	byID := map[int64]int{}
	for i, id := range ids {
		byID[id] = i
	}

	// Many goroutines, more distinct statements than the cache holds.
	var wg sync.WaitGroup
	var failures atomic.Int64
	fail := func(format string, args ...any) {
		if failures.Add(1) <= 5 {
			t.Errorf(format, args...)
		}
	}
	for g := 0; g < writers; g++ {
		wg.Add(1)
		go func(g int) {
			defer wg.Done()
			for i := 0; i < 60; i++ {
				k := (g*7 + i) % rows
				switch i % 6 {
				case 0, 1:
					r, err := quark.For[stmtCacheRow](ctx, c).Find(ids[k])
					if err != nil || r.Name != fmt.Sprintf("row-%03d", k) {
						fail("Find(%d): %+v, %v", ids[k], r, err)
					}
				case 2:
					out, err := quark.For[stmtCacheRow](ctx, c).Where("age", ">=", 0).Where("name", "LIKE", "row-%").OrderBy("id", "ASC").Limit(rows).List()
					if err != nil || len(out) != rows {
						fail("List: %d rows, %v", len(out), err)
					}
				case 3:
					n, err := quark.For[stmtCacheRow](ctx, c).Where("age", "<", 1000).Where("name", "LIKE", "row-%").Count()
					if err != nil || n != rows {
						fail("Count: %d, %v", n, err)
					}
				case 4:
					if _, err := quark.For[stmtCacheRow](ctx, c).Where("id", "=", ids[k]).UpdateMap(map[string]any{"age": k}); err != nil {
						fail("UpdateMap: %v", err)
					}
				case 5:
					r := stmtCacheRow{Name: fmt.Sprintf("tmp-%d-%d", g, i), Age: -1}
					if err := quark.For[stmtCacheRow](ctx, c).Create(&r); err != nil {
						fail("Create: %v", err)
						continue
					}
					if _, err := quark.For[stmtCacheRow](ctx, c).Delete(&r); err != nil {
						fail("Delete: %v", err)
					}
				}
			}
		}(g)
	}
	wg.Wait()
	if failures.Load() > 0 {
		t.Fatalf("%d operations failed under the statement cache", failures.Load())
	}

	// A statement that fails leaves the cached one usable.
	dup := stmtCacheRow{Name: "row-000", Age: 1}
	if err := quark.For[stmtCacheRow](ctx, c).Create(&dup); err == nil || !quark.IsUniqueViolation(err) {
		t.Fatalf("a duplicate name: %v, want a unique violation", err)
	}
	fresh := stmtCacheRow{Name: "after-dup", Age: 2}
	if err := quark.For[stmtCacheRow](ctx, c).Create(&fresh); err != nil {
		t.Fatalf("the insert after a rejected one: %v", err)
	}

	// Transactions: rolled back, committed, and a rejected statement inside
	// one that goes on.
	for _, commit := range []bool{false, true} {
		name := fmt.Sprintf("tx-%v", commit)
		err := c.Tx(ctx, func(tx *quark.Tx) error {
			r := stmtCacheRow{Name: name, Age: 7}
			if err := quark.ForTx[stmtCacheRow](ctx, tx).Create(&r); err != nil {
				return err
			}
			for i := 0; i < 3; i++ {
				got, err := quark.ForTx[stmtCacheRow](ctx, tx).Find(r.ID)
				if err != nil || got.Name != name {
					return fmt.Errorf("Find inside the transaction: %+v, %v", got, err)
				}
				if _, err := quark.ForTx[stmtCacheRow](ctx, tx).Where("id", "=", r.ID).UpdateMap(map[string]any{"age": 8 + i}); err != nil {
					return err
				}
			}
			if drv != "pgx" { // a failed statement aborts a PostgreSQL transaction
				d := stmtCacheRow{Name: "row-001", Age: 1}
				if err := quark.ForTx[stmtCacheRow](ctx, tx).Create(&d); err == nil {
					return fmt.Errorf("a duplicate inside the transaction was accepted")
				}
				if _, err := quark.ForTx[stmtCacheRow](ctx, tx).Find(r.ID); err != nil {
					return fmt.Errorf("Find after a rejected insert inside the transaction: %v", err)
				}
			}
			if !commit {
				return errRollbackOnPurpose
			}
			return nil
		})
		if commit && err != nil {
			t.Fatal(err)
		}
		if !commit && err != errRollbackOnPurpose {
			t.Fatalf("rolled-back transaction: %v", err)
		}
		n, err := quark.For[stmtCacheRow](ctx, c).Where("name", "=", name).Count()
		if err != nil {
			t.Fatal(err)
		}
		if want := map[bool]int64{false: 0, true: 1}[commit]; n != want {
			t.Fatalf("after the transaction (commit=%v): %d rows, want %d", commit, n, want)
		}
	}

	// Optimistic locking tells a stale entity from an excluded one by
	// counting the row the conditions admit, with a statement the cache
	// keeps. Three rounds with the table emptied and refilled between them:
	// the sequence on which go-ora v2.9.0 answered the count from a stale
	// result, and the reason the option is ignored on Oracle (ADR-0027).
	//
	// The table is emptied with raw SQL without arguments on the client's own
	// pool: a statement sent unprepared on the connection between two
	// executions of one the cache keeps is what go-ora needed to go wrong.
	// With the option forced on for Oracle, round 1 fails here.
	for round := 0; round < 3; round++ {
		if err := c.Exec(ctx, "DELETE FROM qk37_stmt_versioned"); err != nil {
			t.Fatalf("round %d: empty the versioned table: %v", round, err)
		}
		seed := stmtCacheVersioned{Owner: "alice", Name: "a", Version: 1}
		if err := quark.For[stmtCacheVersioned](ctx, c).Create(&seed); err != nil {
			t.Fatal(err)
		}
		e := stmtCacheVersioned{ID: seed.ID, Name: "x", Version: 1}
		if n, err := quark.For[stmtCacheVersioned](ctx, c).Where("owner", "=", "bob").UpdateFields(&e, "name"); err != nil || n != 0 {
			t.Fatalf("round %d: excluded write = (%d, %v), want (0, nil)", round, n, err)
		}
		e = stmtCacheVersioned{ID: seed.ID, Name: "x", Version: 7}
		if _, err := quark.For[stmtCacheVersioned](ctx, c).Where("owner", "=", "alice").UpdateFields(&e, "name"); !errors.Is(err, quark.ErrStaleEntity) {
			t.Fatalf("round %d: stale write = %v, want ErrStaleEntity", round, err)
		}
		e = stmtCacheVersioned{ID: seed.ID, Name: "x", Version: 1}
		if n, err := quark.For[stmtCacheVersioned](ctx, c).Where("owner", "=", "alice").UpdateFields(&e, "name"); err != nil || n != 1 {
			t.Fatalf("round %d: admitted write = (%d, %v), want (1, nil)", round, n, err)
		}
	}

	// A column added under a cached statement. Not on PostgreSQL, where the
	// option is ignored and the statement is pgx's: pgx answers the first
	// statement after the column change with "cached plan must not change
	// result type" (SQLSTATE 0A000) and prepares it afresh on the next — its
	// behaviour with or without this option.
	if drv != "pgx" {
		addColumn := map[string]string{
			"sqlite":    "ALTER TABLE qk37_stmt_rows ADD COLUMN note VARCHAR(20)",
			"mysql":     "ALTER TABLE qk37_stmt_rows ADD COLUMN note VARCHAR(20)",
			"sqlserver": "ALTER TABLE qk37_stmt_rows ADD note VARCHAR(20)",
			"oracle":    "ALTER TABLE qk37_stmt_rows ADD (note VARCHAR2(20))",
		}[drv]
		if err := admin.Exec(ctx, addColumn); err != nil {
			t.Fatalf("add a column: %v", err)
		}
		for i := 0; i < 3; i++ {
			got, err := quark.For[stmtCacheRow](ctx, c).Find(ids[i])
			if err != nil || got.Name != fmt.Sprintf("row-%03d", i) {
				t.Fatalf("Find after a column was added: %+v, %v", got, err)
			}
		}
	}

	if mysqlLike {
		during := preparedOnServer(t, admin) - before
		// Each connection holds at most the cache's statements; a
		// transaction's are closed when it ends.
		if during > size*conns {
			t.Errorf("%d statements prepared on the server with a cache of %d over %d connections", during, size, conns)
		}
		t.Logf("prepared on the server while the client is open: %d (cache %d × %d connections)", during, size, conns)
	}
	if err := c.Close(); err != nil {
		t.Fatal(err)
	}
	closed = true
	if mysqlLike {
		// The server frees a session's statements as it ends the session,
		// which can lag the client's close by a moment.
		after := 0
		for try := 0; try < 50; try++ {
			if after = preparedOnServer(t, admin) - before; after == 0 {
				break
			}
			time.Sleep(100 * time.Millisecond)
		}
		if after != 0 {
			t.Errorf("%d statements still prepared on the server after Close", after)
		}
	}
}

// preparedOnServer is the server's count of prepared statements, every
// session's (MySQL and MariaDB).
func preparedOnServer(t *testing.T, c *quark.Client) int {
	t.Helper()
	rows, err := c.RawQuery(context.Background(), "SHOW GLOBAL STATUS WHERE Variable_name = ?", "Prepared_stmt_count")
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var name, value string
	if !rows.Next() {
		t.Fatal("no Prepared_stmt_count")
	}
	if err := rows.Scan(&name, &value); err != nil {
		t.Fatal(err)
	}
	var n int
	if _, err := fmt.Sscan(strings.TrimSpace(value), &n); err != nil {
		t.Fatal(err)
	}
	return n
}
