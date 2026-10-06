// Copyright 2026 jcsvwinston
// SPDX-License-Identifier: Apache-2.0

package quark

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	moderncsqlite "modernc.org/sqlite"
)

// The statement cache (QK-37, ADR-0027) measured where it acts: a counting
// layer at the driver.Conn boundary, under database/sql, records every
// statement the engine prepares and every one it closes. The cache's promise
// is about that boundary — prepared once, reused, closed when evicted or when
// the client closes — and about nothing above it changing.

const stmtCountDriver = "quark-stmtcount"

var (
	stmtCountOnce sync.Once
	stmtCountMu   sync.Mutex
	stmtPrepared  = map[string]int{}
	stmtClosed    = map[string]int{}
)

func registerStmtCountDriver() {
	stmtCountOnce.Do(func() {
		sql.Register(stmtCountDriver, stmtCountDrv{inner: &moderncsqlite.Driver{}})
	})
}

// resetStmtCounts forgets every count: each test reads its own.
func resetStmtCounts() {
	stmtCountMu.Lock()
	defer stmtCountMu.Unlock()
	stmtPrepared = map[string]int{}
	stmtClosed = map[string]int{}
}

// keys lists the statements the LRU holds, most recently used first.
func (l *stmtLRU) keys() []string {
	l.mu.Lock()
	defer l.mu.Unlock()
	out := make([]string, 0, l.order.Len())
	for el := l.order.Front(); el != nil; el = el.Next() {
		out = append(out, el.Value.(*stmtEntry).query)
	}
	return out
}

// stmtCounts returns how many times query was prepared and closed at the
// driver since the last reset.
func stmtCounts(query string) (prepared, closed int) {
	stmtCountMu.Lock()
	defer stmtCountMu.Unlock()
	return stmtPrepared[query], stmtClosed[query]
}

// stmtTotals returns the prepares and closes of every statement whose text
// contains marker.
func stmtTotals(marker string) (prepared, closed int) {
	stmtCountMu.Lock()
	defer stmtCountMu.Unlock()
	for q, n := range stmtPrepared {
		if strings.Contains(q, marker) {
			prepared += n
		}
	}
	for q, n := range stmtClosed {
		if strings.Contains(q, marker) {
			closed += n
		}
	}
	return prepared, closed
}

type stmtCountDrv struct{ inner driver.Driver }

func (d stmtCountDrv) Open(name string) (driver.Conn, error) {
	c, err := d.inner.Open(name)
	if err != nil {
		return nil, err
	}
	return &stmtCountConn{inner: c}, nil
}

type stmtCountConn struct{ inner driver.Conn }

func (c *stmtCountConn) prepared(query string, s driver.Stmt) driver.Stmt {
	stmtCountMu.Lock()
	stmtPrepared[query]++
	stmtCountMu.Unlock()
	return &stmtCountStmt{Stmt: s, query: query}
}

func (c *stmtCountConn) Prepare(query string) (driver.Stmt, error) {
	s, err := c.inner.Prepare(query)
	if err != nil {
		return nil, err
	}
	return c.prepared(query, s), nil
}

func (c *stmtCountConn) PrepareContext(ctx context.Context, query string) (driver.Stmt, error) {
	var s driver.Stmt
	var err error
	if p, ok := c.inner.(driver.ConnPrepareContext); ok {
		s, err = p.PrepareContext(ctx, query)
	} else {
		s, err = c.inner.Prepare(query)
	}
	if err != nil {
		return nil, err
	}
	return c.prepared(query, s), nil
}

func (c *stmtCountConn) Close() error              { return c.inner.Close() }
func (c *stmtCountConn) Begin() (driver.Tx, error) { return c.inner.Begin() } //nolint:staticcheck // forwarded

func (c *stmtCountConn) BeginTx(ctx context.Context, opts driver.TxOptions) (driver.Tx, error) {
	if b, ok := c.inner.(driver.ConnBeginTx); ok {
		return b.BeginTx(ctx, opts)
	}
	return c.inner.Begin() //nolint:staticcheck // forwarded
}

// The direct paths are forwarded as they are, so a client without the cache
// sends statements the way it always did — and prepares nothing here.
func (c *stmtCountConn) ExecContext(ctx context.Context, query string, args []driver.NamedValue) (driver.Result, error) {
	if e, ok := c.inner.(driver.ExecerContext); ok {
		return e.ExecContext(ctx, query, args)
	}
	return nil, driver.ErrSkip
}

func (c *stmtCountConn) QueryContext(ctx context.Context, query string, args []driver.NamedValue) (driver.Rows, error) {
	if q, ok := c.inner.(driver.QueryerContext); ok {
		return q.QueryContext(ctx, query, args)
	}
	return nil, driver.ErrSkip
}

func (c *stmtCountConn) ResetSession(ctx context.Context) error {
	if r, ok := c.inner.(driver.SessionResetter); ok {
		return r.ResetSession(ctx)
	}
	return nil
}

type stmtCountStmt struct {
	driver.Stmt
	query string
}

func (s *stmtCountStmt) Close() error {
	stmtCountMu.Lock()
	stmtClosed[s.query]++
	stmtCountMu.Unlock()
	return s.Stmt.Close()
}

func (s *stmtCountStmt) ExecContext(ctx context.Context, args []driver.NamedValue) (driver.Result, error) {
	if e, ok := s.Stmt.(driver.StmtExecContext); ok {
		return e.ExecContext(ctx, args)
	}
	return nil, driver.ErrSkip
}

func (s *stmtCountStmt) QueryContext(ctx context.Context, args []driver.NamedValue) (driver.Rows, error) {
	if q, ok := s.Stmt.(driver.StmtQueryContext); ok {
		return q.QueryContext(ctx, args)
	}
	return nil, driver.ErrSkip
}

type scUser struct {
	ID   int64  `db:"id" pk:"true"`
	Name string `db:"name"`
	Age  int    `db:"age"`
}

func (scUser) TableName() string { return "sc_users" }

// newStmtCacheClient opens a client on its own in-memory database through the
// counting driver, with the users table and n rows.
func newStmtCacheClient(t *testing.T, name string, rows int, opts ...any) *Client {
	t.Helper()
	registerStmtCountDriver()
	resetStmtCounts()
	opts = append([]any{WithDialect(SQLite()), WithLogger(rrQuiet)}, opts...)
	c, err := New(stmtCountDriver, "file:"+name+"?mode=memory&cache=shared", opts...)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = c.Close() })
	ctx := context.Background()
	if err := c.Migrate(ctx, &scUser{}); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < rows; i++ {
		u := scUser{Name: fmt.Sprintf("user%03d", i), Age: 20 + i%30}
		if err := For[scUser](ctx, c).Create(&u); err != nil {
			t.Fatal(err)
		}
	}
	return c
}

// findSQL is the statement Find sends for scUser on SQLite, as the cache
// keys it.
func findSQL(t *testing.T, c *Client) string {
	t.Helper()
	for _, q := range c.stmts.pools[c.db].keys() {
		if strings.HasPrefix(q, "SELECT") && strings.Contains(q, `"id" = ?`) {
			return q
		}
	}
	t.Fatalf("no Find statement in the cache: %v", c.stmts.pools[c.db].keys())
	return ""
}

func TestStatementCacheReusesStatements(t *testing.T) {
	c := newStmtCacheClient(t, "sc_reuse", 5, WithStatementCache(8), WithMaxOpenConns(1))
	ctx := context.Background()
	for i := 0; i < 50; i++ {
		id := int64(i%5) + 1
		u, err := For[scUser](ctx, c).Find(id)
		if err != nil {
			t.Fatal(err)
		}
		if u.ID != id || u.Name != fmt.Sprintf("user%03d", id-1) {
			t.Fatalf("Find(%d) read %+v", id, u)
		}
	}
	q := findSQL(t, c)
	if p, cl := stmtCounts(q); p != 1 || cl != 0 {
		t.Fatalf("Find was prepared %d times and closed %d times over 50 calls on one connection; want 1 and 0", p, cl)
	}
	if err := c.Close(); err != nil {
		t.Fatal(err)
	}
	if p, cl := stmtTotals("sc_users"); p != cl {
		t.Fatalf("after Close: %d statements prepared, %d closed — the client left statements open", p, cl)
	}
}

// Without the option nothing changes at the driver: database/sql takes the
// driver's direct paths and prepares nothing.
func TestStatementCacheOffPreparesNothing(t *testing.T) {
	c := newStmtCacheClient(t, "sc_off", 3)
	if c.stmts != nil {
		t.Fatal("a client without WithStatementCache has a statement cache")
	}
	ctx := context.Background()
	for i := 0; i < 10; i++ {
		if _, err := For[scUser](ctx, c).Find(int64(i%3) + 1); err != nil {
			t.Fatal(err)
		}
	}
	if p, _ := stmtTotals("sc_users"); p != 0 {
		t.Fatalf("without the cache %d statements were prepared; the driver's direct path prepares none", p)
	}
}

func TestStatementCacheEvictsLeastRecentlyUsed(t *testing.T) {
	c := newStmtCacheClient(t, "sc_evict", 3, WithStatementCache(2), WithMaxOpenConns(1))
	ctx := context.Background()
	lru := c.stmts.pools[c.db]
	// Three statements that differ in text: each Where adds a condition.
	queries := []func() error{
		func() error { _, err := For[scUser](ctx, c).Where("age", ">=", 0).Limit(10).List(); return err },
		func() error { _, err := For[scUser](ctx, c).Where("age", "<", 100).Limit(10).List(); return err },
		func() error { _, err := For[scUser](ctx, c).Where("name", "<>", "x").Limit(10).List(); return err },
	}
	for _, q := range queries {
		if err := q(); err != nil {
			t.Fatal(err)
		}
	}
	if n := lru.len(); n != 2 {
		t.Fatalf("the LRU holds %d statements; its size is 2", n)
	}
	p, cl := stmtTotals(`"age" >= ?`)
	if p != 1 || cl != 1 {
		t.Fatalf("the least recently used statement was prepared %d times and closed %d times; want 1 and 1 (evicted)", p, cl)
	}
	// Using it again prepares it again, and evicts the next one.
	if err := queries[0](); err != nil {
		t.Fatal(err)
	}
	if p, _ := stmtTotals(`"age" >= ?`); p != 2 {
		t.Fatalf("the evicted statement was prepared %d times after its reuse; want 2", p)
	}
	if p, cl := stmtTotals(`"age" < ?`); p != 1 || cl != 1 {
		t.Fatalf("the next least recently used statement: prepared %d, closed %d; want 1 and 1", p, cl)
	}
}

// A statement evicted while a caller still reads its rows is closed, and the
// rows keep reading: database/sql defers the driver's close until they are
// closed.
func TestStatementCacheEvictionWhileRowsAreOpen(t *testing.T) {
	c := newStmtCacheClient(t, "sc_rowsopen", 20, WithStatementCache(1), WithMaxOpenConns(2))
	ctx := context.Background()
	cur, err := For[scUser](ctx, c).Where("age", ">=", 0).OrderBy("id", "ASC").Limit(100).Cursor()
	if err != nil {
		t.Fatal(err)
	}
	defer cur.Close()
	// Evict the cursor's statement while its rows are open.
	if _, err := For[scUser](ctx, c).Find(int64(1)); err != nil {
		t.Fatal(err)
	}
	if n := c.stmts.pools[c.db].len(); n != 1 {
		t.Fatalf("the LRU holds %d statements; its size is 1", n)
	}
	n := 0
	for cur.Next() {
		var u scUser
		if err := cur.Scan(&u); err != nil {
			t.Fatal(err)
		}
		n++
		if u.ID != int64(n) {
			t.Fatalf("row %d has id %d", n, u.ID)
		}
	}
	if err := cur.Err(); err != nil {
		t.Fatal(err)
	}
	if n != 20 {
		t.Fatalf("read %d rows through a cursor whose statement was evicted; want 20", n)
	}
}

// Inside a transaction a statement is prepared on the transaction's
// connection, and only there. With one connection in the pool, preparing it
// on the pool would wait forever for the connection the transaction holds;
// the deadline below turns that wait into a failure.
func TestStatementCacheInTransaction(t *testing.T) {
	c := newStmtCacheClient(t, "sc_tx", 2, WithStatementCache(8), WithMaxOpenConns(1))
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	run := func(commit bool) int64 {
		tx, err := c.BeginTx(ctx, nil)
		if err != nil {
			t.Fatal(err)
		}
		u := scUser{Name: "in-tx", Age: 99}
		if err := ForTx[scUser](ctx, tx).Create(&u); err != nil {
			t.Fatal(err)
		}
		for i := 0; i < 5; i++ {
			got, err := ForTx[scUser](ctx, tx).Find(u.ID)
			if err != nil {
				t.Fatalf("Find inside the transaction: %v", err)
			}
			if got.Name != "in-tx" {
				t.Fatalf("read %+v inside the transaction", got)
			}
		}
		if _, ok := c.stmts.txs.Load(tx.tx); !ok {
			t.Fatal("the open transaction has no statement set")
		}
		if commit {
			err = tx.Commit()
		} else {
			err = tx.Rollback()
		}
		if err != nil {
			t.Fatal(err)
		}
		if _, ok := c.stmts.txs.Load(tx.tx); ok {
			t.Fatal("the transaction's statement set outlived it")
		}
		return u.ID
	}

	rolledBack := run(false)
	if _, err := For[scUser](ctx, c).Find(rolledBack); err == nil {
		t.Fatal("a row created in a rolled-back transaction is readable")
	}
	committed := run(true)
	if got, err := For[scUser](ctx, c).Find(committed); err != nil || got.Name != "in-tx" {
		t.Fatalf("the committed row: %+v, %v", got, err)
	}
	// The transaction's Find was prepared once per transaction, not per call.
	q := findSQL(t, c)
	if p, _ := stmtCounts(q); p > 3 {
		t.Fatalf("Find was prepared %d times: once per transaction (2) and once for the pool (1) at most", p)
	}
}

// What the cache leaves alone: statements without arguments, schema work,
// raw SQL, and long statements.
func TestStatementCacheSkips(t *testing.T) {
	cases := []struct {
		name string
		st   stmt
		sql  string
		args []any
		want bool
	}{
		{"builder read with args", stmt{kind: StatementQuery}, "SELECT 1 WHERE ? = 1", []any{1}, true},
		{"builder write with args", stmt{kind: StatementExec}, "UPDATE t SET a = ?", []any{1}, true},
		{"no arguments", stmt{kind: StatementQuery}, "SELECT 1", nil, false},
		{"schema work", stmt{kind: StatementDDL}, "CREATE TABLE t (a INT)", []any{1}, false},
		{"introspection", stmt{kind: StatementIntrospection}, "SELECT ? FROM sqlite_master", []any{1}, false},
		{"savepoint", stmt{kind: StatementSavepoint}, "SAVEPOINT sp", []any{1}, false},
		{"raw SQL", stmt{kind: StatementRaw}, "SELECT ?", []any{1}, false},
		{"named argument", stmt{kind: StatementExec}, "BEGIN x(:a); END;", []any{sql.Named("a", 1)}, false},
		{"too long", stmt{kind: StatementQuery}, "SELECT ?" + strings.Repeat(" ", stmtCacheMaxSQL), []any{1}, false},
	}
	for _, tc := range cases {
		if got := cacheable(&tc.st, tc.sql, tc.args); got != tc.want {
			t.Errorf("%s: cacheable = %v, want %v", tc.name, got, tc.want)
		}
	}
}

// PostgreSQL keeps its own cache in pgx; on Oracle, go-ora answered a
// re-executed statement from a stale result. The option is ignored on both.
func TestStatementCacheIgnoredOnPostgresAndOracle(t *testing.T) {
	registerStmtCountDriver()
	for _, d := range []Dialect{PostgreSQL(), Oracle()} {
		c, err := New(stmtCountDriver, "file:sc_ignored?mode=memory&cache=shared",
			WithDialect(d), WithStatementCache(8), WithLogger(rrQuiet))
		if err != nil {
			t.Fatal(err)
		}
		if c.stmts != nil {
			t.Errorf("WithStatementCache took effect on the %s dialect", d.Name())
		}
		_ = c.Close()
	}
}

// Many callers, a cache smaller than the statements they send: every result
// is right, the race detector sees nothing, and after Close every statement
// prepared was closed.
func TestStatementCacheConcurrent(t *testing.T) {
	c := newStmtCacheClient(t, "sc_conc", 10, WithStatementCache(3), WithMaxOpenConns(4))
	ctx := context.Background()
	var wg sync.WaitGroup
	var failures atomic.Int64
	for g := 0; g < 16; g++ {
		wg.Add(1)
		go func(g int) {
			defer wg.Done()
			for i := 0; i < 100; i++ {
				switch (g + i) % 4 {
				case 0:
					id := int64((g+i)%10) + 1
					u, err := For[scUser](ctx, c).Find(id)
					if err != nil || u.ID != id {
						failures.Add(1)
					}
				case 1:
					out, err := For[scUser](ctx, c).Where("age", ">=", 0).Limit(100).List()
					if err != nil || len(out) < 10 {
						failures.Add(1)
					}
				case 2:
					n, err := For[scUser](ctx, c).Where("age", "<", 1000).Count()
					if err != nil || n < 10 {
						failures.Add(1)
					}
				case 3:
					_, err := For[scUser](ctx, c).Where("name", "=", fmt.Sprintf("user%03d", i%10)).UpdateMap(map[string]any{"age": 20 + i%30})
					if err != nil {
						failures.Add(1)
					}
				}
			}
		}(g)
	}
	wg.Wait()
	if n := failures.Load(); n != 0 {
		t.Fatalf("%d operations failed or read the wrong rows", n)
	}
	if n := c.stmts.pools[c.db].len(); n > 3 {
		t.Fatalf("the LRU holds %d statements; its size is 3", n)
	}
	if err := c.Close(); err != nil {
		t.Fatal(err)
	}
	if p, cl := stmtTotals("sc_users"); p != cl {
		t.Fatalf("after Close: %d statements prepared, %d closed", p, cl)
	}
}
