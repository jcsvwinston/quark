// Copyright 2026 jcsvwinston
// SPDX-License-Identifier: Apache-2.0

package quark_test

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"io"
	"log/slog"
	"strings"
	"sync"
	"testing"

	"github.com/jcsvwinston/quark"

	moderncsqlite "modernc.org/sqlite"
)

// The engine's side of the observation tests: modernc's SQLite driver with a
// recording layer at the driver.Conn boundary — below Quark and below
// database/sql's pool, where nothing Quark sends can go around it. What it
// records is what the engine received, in order; the tests compare the
// middleware chain and the observers against it.
const observeWireDriver = "quark-observe-wire"

var (
	observeWireOnce sync.Once
	observeWireMu   sync.Mutex
	observeWireLog  []string
	observeWireOn   bool
)

func observeWireRecord(query string) {
	observeWireMu.Lock()
	defer observeWireMu.Unlock()
	if observeWireOn {
		observeWireLog = append(observeWireLog, query)
	}
}

// observeWireCapture starts a recording and returns the function that ends
// it and returns what reached the engine.
func observeWireCapture() func() []string {
	observeWireMu.Lock()
	observeWireLog, observeWireOn = nil, true
	observeWireMu.Unlock()
	return func() []string {
		observeWireMu.Lock()
		defer observeWireMu.Unlock()
		observeWireOn = false
		out := observeWireLog
		observeWireLog = nil
		return out
	}
}

type observeWire struct{ inner driver.Driver }

func (d observeWire) Open(name string) (driver.Conn, error) {
	c, err := d.inner.Open(name)
	if err != nil {
		return nil, err
	}
	return &observeWireConn{inner: c}, nil
}

// observeWireConn records the statement on each of the three paths one can
// take — ExecContext, QueryContext, PrepareContext — and forwards the rest.
type observeWireConn struct{ inner driver.Conn }

func (c *observeWireConn) Prepare(query string) (driver.Stmt, error) {
	observeWireRecord(query)
	return c.inner.Prepare(query)
}

func (c *observeWireConn) PrepareContext(ctx context.Context, query string) (driver.Stmt, error) {
	observeWireRecord(query)
	if p, ok := c.inner.(driver.ConnPrepareContext); ok {
		return p.PrepareContext(ctx, query)
	}
	return c.inner.Prepare(query)
}

func (c *observeWireConn) Close() error              { return c.inner.Close() }
func (c *observeWireConn) Begin() (driver.Tx, error) { return c.inner.Begin() }

func (c *observeWireConn) BeginTx(ctx context.Context, opts driver.TxOptions) (driver.Tx, error) {
	if b, ok := c.inner.(driver.ConnBeginTx); ok {
		return b.BeginTx(ctx, opts)
	}
	return c.inner.Begin()
}

func (c *observeWireConn) ExecContext(ctx context.Context, query string, args []driver.NamedValue) (driver.Result, error) {
	e, ok := c.inner.(driver.ExecerContext)
	if !ok {
		return nil, driver.ErrSkip
	}
	observeWireRecord(query)
	return e.ExecContext(ctx, query, args)
}

func (c *observeWireConn) QueryContext(ctx context.Context, query string, args []driver.NamedValue) (driver.Rows, error) {
	q, ok := c.inner.(driver.QueryerContext)
	if !ok {
		return nil, driver.ErrSkip
	}
	observeWireRecord(query)
	return q.QueryContext(ctx, query, args)
}

func (c *observeWireConn) ResetSession(ctx context.Context) error {
	if r, ok := c.inner.(driver.SessionResetter); ok {
		return r.ResetSession(ctx)
	}
	return nil
}

// seen is one statement as an extension point saw it.
type seen struct {
	sql  string
	kind quark.StatementKind
}

// kindMiddleware records each statement the chain hands it, with the kind
// quark.StatementKindFromContext reports.
type kindMiddleware struct {
	mu   sync.Mutex
	seen []seen
}

func (m *kindMiddleware) note(ctx context.Context, s string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.seen = append(m.seen, seen{s, quark.StatementKindFromContext(ctx)})
}

func (m *kindMiddleware) WrapExec(next quark.ExecFunc) quark.ExecFunc {
	return func(ctx context.Context, ex quark.Executor, s string, a []any) (sql.Result, error) {
		m.note(ctx, s)
		return next(ctx, ex, s, a)
	}
}

func (m *kindMiddleware) WrapQuery(next quark.QueryFunc) quark.QueryFunc {
	return func(ctx context.Context, ex quark.Executor, s string, a []any) (*sql.Rows, error) {
		m.note(ctx, s)
		return next(ctx, ex, s, a)
	}
}

func (m *kindMiddleware) WrapQueryRow(next quark.QueryRowFunc) quark.QueryRowFunc {
	return func(ctx context.Context, ex quark.Executor, s string, a []any) *sql.Row {
		m.note(ctx, s)
		return next(ctx, ex, s, a)
	}
}

// eventLog records the observer's events.
type eventLog struct {
	mu     sync.Mutex
	events []quark.QueryEvent
}

func (l *eventLog) ObserveQuery(ev quark.QueryEvent) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.events = append(l.events, ev)
}

type obsAuthor struct {
	ID    int64     `db:"id" pk:"true"`
	Name  string    `db:"name" quark:"unique"`
	Books []obsBook `rel:"has_many" join:"author_id"`
}

func (obsAuthor) TableName() string { return "obs_authors" }

type obsBook struct {
	ID       int64  `db:"id" pk:"true"`
	AuthorID int64  `db:"author_id"`
	Title    string `db:"title"`
}

func (obsBook) TableName() string { return "obs_books" }

// obsBookV2 is the book after a schema change: a column, an index and a
// CHECK, which ApplyPlan writes through SQLite's table rebuild.
type obsBookV2 struct {
	ID       int64  `db:"id" pk:"true"`
	AuthorID int64  `db:"author_id"`
	Title    string `db:"title" quark:"index"`
	Pages    int64  `db:"pages" quark:"check=pages >= 0"`
}

func (obsBookV2) TableName() string { return "obs_books" }

// obsMissing has no table: reading it fails at the engine.
type obsMissing struct {
	ID int64 `db:"id" pk:"true"`
}

func (obsMissing) TableName() string { return "obs_missing" }

// TestEveryStatementPassesTheSeam runs a battery wider than the extension
// bench's (CON-04) — reads of every shape, writes, a failing read, a
// transaction with savepoints and a nested Tx, raw SQL, the audit log,
// introspection, a plan applied through SQLite's table rebuild, Sync and a
// Backfill — under a recording driver, and holds the middleware chain and
// the observers to what the engine received: the same statements, in the
// same order, each with the same kind.
func TestEveryStatementPassesTheSeam(t *testing.T) {
	observeWireOnce.Do(func() {
		sql.Register(observeWireDriver, observeWire{inner: &moderncsqlite.Driver{}})
	})
	ctx := context.Background()
	mw := &kindMiddleware{}
	obs := &eventLog{}
	limits := quark.DefaultLimits()
	limits.AllowRawQueries = true
	c, err := quark.New(observeWireDriver, "file:observe_seam?mode=memory&cache=shared",
		quark.WithDialect(quark.SQLite()), quark.WithLimits(limits),
		quark.WithLogger(slog.New(slog.NewTextHandler(io.Discard, nil))),
		quark.WithMiddleware(mw), quark.WithQueryObserver(obs))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { _ = c.Close() })

	must := func(what string, err error) {
		t.Helper()
		if err != nil {
			t.Fatalf("%s: %v", what, err)
		}
	}
	stop := observeWireCapture()

	must("migrate", c.Migrate(ctx, &obsAuthor{}, &obsBook{}))
	must("audit", c.EnableAuditLog(ctx, quark.AuditConfig{IncludeTables: []string{"obs_authors"}}))
	a := obsAuthor{Name: "ada"}
	must("create", quark.For[obsAuthor](ctx, c).Create(&a))
	must("batch", quark.For[obsBook](ctx, c).CreateBatch([]*obsBook{{AuthorID: a.ID, Title: "one"}, {AuthorID: a.ID, Title: "two"}}))
	_, err = quark.For[obsAuthor](ctx, c).Preload("Books").Limit(10).List()
	must("list with preload", err)
	_, err = quark.For[obsBook](ctx, c).Find(1)
	must("find", err)
	_, err = quark.For[obsBook](ctx, c).Count()
	must("count", err)
	must("iter", quark.For[obsBook](ctx, c).Limit(10).Iter(func(obsBook) error { return nil }))
	cur, err := quark.For[obsBook](ctx, c).Limit(10).Cursor()
	must("cursor", err)
	for cur.Next() {
	}
	must("cursor close", cur.Close())
	a.Name = "ada lovelace"
	_, err = quark.For[obsAuthor](ctx, c).Update(&a)
	must("update", err)
	must("upsert", quark.For[obsAuthor](ctx, c).Upsert(&obsAuthor{Name: "grace"}, []string{"name"}, []string{"name"}))
	if _, err := quark.For[obsMissing](ctx, c).Limit(1).List(); err == nil {
		t.Fatal("a read of a table that does not exist succeeded")
	}
	must("tx", c.Tx(ctx, func(tx *quark.Tx) error {
		if err := quark.ForTx[obsBook](ctx, tx).Create(&obsBook{AuthorID: a.ID, Title: "three"}); err != nil {
			return err
		}
		if err := tx.Savepoint("sp"); err != nil {
			return err
		}
		if err := quark.ForTx[obsBook](ctx, tx).Create(&obsBook{AuthorID: a.ID, Title: "four"}); err != nil {
			return err
		}
		if err := tx.RollbackTo("sp"); err != nil {
			return err
		}
		return tx.Tx(ctx, func(tx *quark.Tx) error { // SAVEPOINT … RELEASE SAVEPOINT
			_, err := quark.ForTx[obsBook](ctx, tx).Count()
			return err
		})
	}))
	must("raw exec", c.Exec(ctx, `UPDATE "obs_books" SET "title" = ? WHERE "title" = ?`, "THREE", "three"))
	rows, err := c.RawQuery(ctx, `SELECT "id" FROM "obs_books" WHERE "id" > ?`, 0)
	must("raw query", err)
	_ = rows.Close()
	_, err = c.IntrospectSchema(ctx)
	must("introspect", err)
	must("create index", c.CreateIndex(ctx, "obs_books", "idx_obs_books_author", []string{"author_id"}, false))
	plan, err := c.PlanMigration(ctx, &obsAuthor{}, &obsBookV2{})
	must("plan", err)
	if len(plan.Ops) == 0 {
		t.Fatal("the plan is empty: the battery would not exercise ApplyPlan")
	}
	must("apply", c.ApplyPlan(ctx, plan))
	must("sync", c.Sync(ctx, quark.SyncOptions{}, &obsBookV2{}))
	must("backfill", c.Backfill(ctx, quark.BackfillSpec{
		Name: "obs", Table: "obs_books", BatchSize: 2,
		Process: func(context.Context, []int64) error { return nil },
	}))
	_, err = quark.For[obsAuthor](ctx, c).Delete(&a)
	must("delete", err)

	wire := stop()
	mw.mu.Lock()
	obs.mu.Lock()
	defer mw.mu.Unlock()
	defer obs.mu.Unlock()

	if len(wire) < 40 {
		t.Fatalf("the engine received %d statements: the recording driver is not wired, or the battery shrank", len(wire))
	}
	if len(mw.seen) != len(wire) || len(obs.events) != len(wire) {
		t.Errorf("the engine received %d statements; the middleware saw %d and the observer %d", len(wire), len(mw.seen), len(obs.events))
	}
	count := map[quark.StatementKind]int{}
	for i := range wire {
		var m seen
		var ev quark.QueryEvent
		if i < len(mw.seen) {
			m = mw.seen[i]
		}
		if i < len(obs.events) {
			ev = obs.events[i]
		}
		switch {
		case m.sql != wire[i]:
			t.Fatalf("statement %d: the engine received %q, the middleware saw %q", i, wire[i], m.sql)
		case ev.SQL != wire[i]:
			t.Fatalf("statement %d: the engine received %q, the observer saw %q", i, wire[i], ev.SQL)
		case ev.Kind == "" || m.kind != ev.Kind:
			t.Fatalf("statement %d %q: the middleware is told kind %q, the event says %q", i, wire[i], m.kind, ev.Kind)
		}
		count[ev.Kind]++
	}
	for _, k := range []quark.StatementKind{
		quark.StatementQuery, quark.StatementExec, quark.StatementDDL,
		quark.StatementIntrospection, quark.StatementSavepoint, quark.StatementRaw,
	} {
		if count[k] == 0 {
			t.Errorf("no statement of kind %s: %v", k, count)
		}
	}
	t.Logf("%d statements, each seen by the middleware and the observer in the engine's order; by kind: %v", len(wire), count)

	// The failed read reached the engine and is reported, with its error.
	var failed bool
	for _, ev := range obs.events {
		if strings.Contains(ev.SQL, `"obs_missing"`) && ev.Error != nil && ev.Operation == "SELECT" {
			failed = true
		}
	}
	if !failed {
		t.Error("the read of a missing table reached the engine but no event carries its error")
	}
}

// TestStatementKindFromContextOutsideAChain pins the documented answer for a
// context that did not come through a client's chain.
func TestStatementKindFromContextOutsideAChain(t *testing.T) {
	if k := quark.StatementKindFromContext(context.Background()); k != "" {
		t.Errorf("StatementKindFromContext(Background) = %q, want empty", k)
	}
	if k := quark.StatementKindFromContext(nil); k != "" {
		t.Errorf("StatementKindFromContext(nil) = %q, want empty", k)
	}
}

// TestSeamReportsWithoutMiddleware checks the path the query builder takes
// when no middleware is registered — the innermost link called directly —
// still reports every statement, savepoints and DDL included.
func TestSeamReportsWithoutMiddleware(t *testing.T) {
	ctx := context.Background()
	obs := &eventLog{}
	c, err := quark.New("sqlite", "file:observe_nomw?mode=memory&cache=shared",
		quark.WithLogger(slog.New(slog.NewTextHandler(io.Discard, nil))),
		quark.WithQueryObserver(obs))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { _ = c.Close() })
	if err := c.Migrate(ctx, &obsBook{}); err != nil {
		t.Fatal(err)
	}
	if err := c.Tx(ctx, func(tx *quark.Tx) error {
		if err := tx.Savepoint("a"); err != nil {
			return err
		}
		return tx.ReleaseSavepoint("a")
	}); err != nil {
		t.Fatal(err)
	}
	var got []quark.StatementKind
	for _, ev := range obs.events {
		got = append(got, ev.Kind)
	}
	want := []quark.StatementKind{quark.StatementDDL, quark.StatementSavepoint, quark.StatementSavepoint}
	if len(got) != len(want) {
		t.Fatalf("kinds = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("kinds = %v, want %v", got, want)
		}
	}
}
