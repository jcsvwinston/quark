// Copyright 2026 jcsvwinston
// SPDX-License-Identifier: Apache-2.0

package quark

import (
	"context"
	"database/sql"
	"time"

	"github.com/jcsvwinston/quark/internal/observe"
	"github.com/jcsvwinston/quark/quarkdriver"
)

// StatementKind says what a statement Quark sends to the engine is for. Every
// [QueryEvent] carries one, and a [Middleware] reads it with
// [StatementKindFromContext], so a consumer can tell the query builder's
// reads and writes from schema work, savepoints and SQL the caller wrote.
//
// Query and exec are told apart by the database/sql method the statement
// goes through, the same line [Middleware]'s WrapQuery/WrapQueryRow and
// WrapExec draw; the other four by what the statement is for, whatever the
// method.
type StatementKind string

const (
	// StatementQuery is a statement the query builder, a [Routine] or
	// [Client.Backfill] sends through QueryContext or QueryRowContext: a
	// SELECT, a Count or an aggregate, a preload, the check a guarded write
	// reads first, and a write that reads its result back — INSERT …
	// RETURNING, SQL Server's OUTPUT, the key a Create reads after its
	// INSERT.
	StatementQuery StatementKind = "query"
	// StatementExec is a statement the query builder sends through
	// ExecContext — INSERT, UPDATE, DELETE, an upsert, a batch — and a
	// stored-procedure [Call], [Notify], an audit row, and the tenant
	// variable a RowLevelSecurityNative transaction sets when it opens.
	StatementExec StatementKind = "exec"
	// StatementDDL is schema work: the CREATE, ALTER and DROP of
	// [Client.Migrate], [Client.Sync], [Client.ApplyPlan],
	// [Client.CreateIndex] and [Client.AddForeignKey], and the statements
	// that serve them — the migration lock, the writes to Quark's
	// bookkeeping tables (quark_migration_state, quark_backfill_state,
	// quark_migrations), and the PRAGMAs and row copy of SQLite's table
	// rebuild. The RLS policies quarktenant installs are DDL too.
	StatementDDL StatementKind = "ddl"
	// StatementIntrospection is a read of what the schema is: the catalog
	// queries of a dialect's [SchemaIntrospector] under
	// [Client.IntrospectSchema], [Client.PlanMigration] and [Client.Sync],
	// the catalog reads of ApplyPlan and of the RowLevelSecurityNative
	// policy checks, the reads of Quark's bookkeeping tables, and the server
	// version a client on the mysql driver reads to tell MariaDB apart.
	StatementIntrospection StatementKind = "introspection"
	// StatementSavepoint is a SAVEPOINT, ROLLBACK TO SAVEPOINT or RELEASE
	// SAVEPOINT: [Tx.Savepoint], [Tx.RollbackTo], [Tx.ReleaseSavepoint] and
	// the nested [Tx.Tx].
	StatementSavepoint StatementKind = "savepoint"
	// StatementRaw is SQL the caller wrote: [Client.Exec] and
	// [Client.RawQuery].
	StatementRaw StatementKind = "raw"
)

// statementKindKey is the context key the seam puts a statement's kind under
// on the context it hands the middleware chain.
type statementKindKey struct{}

// StatementKindFromContext returns the kind of the statement a [Middleware]
// is wrapping: Quark puts it on the context it hands the chain. It returns ""
// for a context that did not come through Quark's chain — a middleware
// function called directly from a test, for instance.
func StatementKindFromContext(ctx context.Context) StatementKind {
	if ctx == nil {
		return ""
	}
	k, _ := ctx.Value(statementKindKey{}).(StatementKind)
	return k
}

// withStatementKind is the context the middleware chain receives. The six
// kinds are returned as constants, which an interface holds without an
// allocation; only the context itself is one.
func withStatementKind(ctx context.Context, k StatementKind) context.Context {
	var v any
	switch k {
	case StatementQuery:
		v = StatementQuery
	case StatementExec:
		v = StatementExec
	case StatementDDL:
		v = StatementDDL
	case StatementIntrospection:
		v = StatementIntrospection
	case StatementSavepoint:
		v = StatementSavepoint
	case StatementRaw:
		v = StatementRaw
	default:
		v = k
	}
	return context.WithValue(ctx, statementKindKey{}, v)
}

// --- the seam ------------------------------------------------------------------
//
// Every statement Quark sends to the engine goes through one of execStmt,
// queryStmt and queryRowStmt. Each composes the client's middleware around an
// innermost link that sends the statement and hands its QueryEvent to the
// slow-query log and the observers, so the chain and the observers see the
// same statements, in the order the engine receives them.
//
// The query builder's primitives (executeExec, executeQueryOn, queryRowOn)
// call them, and so does everything else that talks to the engine: schema
// work and introspection through observedExec, savepoints, raw SQL, routines,
// Notify, audit rows, the migration lock, the tenant variable of a native-RLS
// transaction, and — through internal/observe — the migrate and quarktenant
// packages. seam_guard_test.go fails on a statement sent past them.
//
// With no middleware registered the innermost link is called directly: no
// func value is built, no context is derived, and the query builder's hot
// paths allocate nothing they did not before.

// stmt is what the seam knows about a statement besides its text and
// arguments.
type stmt struct {
	kind  StatementKind
	op    string // QueryEvent.Operation
	table string // QueryEvent.Table, when known

	// reported marks a multi-row read whose caller reports it once it has
	// consumed the rows (List, Iter, Cursor, a preload), so the event can
	// carry what only the caller knows — the row count, the time to scan.
	// The seam then reports the statement itself only when it fails, which
	// the caller does not get to.
	reported bool

	// write marks the query builder's writes: the engine's error is
	// classified (wrapDBError), and when the write succeeds the cache tags
	// of the table and of rowTags are invalidated — inside the chain, next
	// to the engine, so a middleware that changes the result cannot leave
	// a stale cache behind.
	write   bool
	rowTags []string
}

// observe hands one event to the slow-query log and to every observer.
func (c *Client) observe(ev QueryEvent) {
	c.logSlowQueryIfNeeded(ev)
	for _, obs := range c.observers {
		obs.ObserveQuery(ev)
	}
}

// execStmt sends a statement through ExecContext on exec, through the
// middleware chain, and reports it.
func (c *Client) execStmt(ctx context.Context, exec Executor, st stmt, sqlStr string, args []any) (sql.Result, error) {
	if len(c.middleware) == 0 {
		return c.execEngine(ctx, exec, &st, sqlStr, args)
	}
	link := st // the func value below captures this copy, not the parameter
	next := ExecFunc(func(ctx context.Context, exec Executor, s string, a []any) (sql.Result, error) {
		return c.execEngine(ctx, exec, &link, s, a)
	})
	for i := len(c.middleware) - 1; i >= 0; i-- {
		next = c.middleware[i].WrapExec(next)
	}
	return next(withStatementKind(ctx, st.kind), exec, sqlStr, args)
}

func (c *Client) execEngine(ctx context.Context, exec Executor, st *stmt, sqlStr string, args []any) (sql.Result, error) {
	start := time.Now()
	var res sql.Result
	var err error
	if l := c.stmts.lease(ctx, exec, st, sqlStr, args); l.stmt != nil {
		res, err = l.stmt.ExecContext(ctx, args...)
		l.release()
	} else {
		res, err = exec.ExecContext(ctx, sqlStr, args...)
	}
	duration := time.Since(start)
	if st.write {
		err = wrapDBError(err)
		// One InvalidateTags call carries the table tag plus any row tag the
		// caller knows, so a store sees one invalidation batch per write.
		// Inside a transaction the tags are dropped again at the commit
		// (Client.invalidate).
		if err == nil && c.cacheStore != nil && st.table != "" {
			tags := make([]string, 0, 1+len(st.rowTags))
			tags = append(tags, st.table)
			for _, t := range st.rowTags {
				if t != "" {
					tags = append(tags, t)
				}
			}
			c.invalidate(ctx, exec, tags)
		}
	}
	var rows int64
	if err == nil {
		rows, _ = res.RowsAffected()
	}
	c.observe(QueryEvent{
		SQL:       sqlStr,
		Args:      args,
		Duration:  duration,
		Rows:      rows,
		Error:     err,
		Table:     st.table,
		Operation: st.op,
		Kind:      st.kind,
	})
	return res, err
}

// queryStmt sends a statement through QueryContext on exec, through the
// middleware chain, and reports it — or, for a read its caller reports
// (stmt.reported), reports it only when it fails.
func (c *Client) queryStmt(ctx context.Context, exec Executor, st stmt, sqlStr string, args []any) (*sql.Rows, error) {
	if len(c.middleware) == 0 {
		return c.queryEngine(ctx, exec, &st, sqlStr, args)
	}
	link := st
	next := QueryFunc(func(ctx context.Context, exec Executor, s string, a []any) (*sql.Rows, error) {
		return c.queryEngine(ctx, exec, &link, s, a)
	})
	for i := len(c.middleware) - 1; i >= 0; i-- {
		next = c.middleware[i].WrapQuery(next)
	}
	return next(withStatementKind(ctx, st.kind), exec, sqlStr, args)
}

func (c *Client) queryEngine(ctx context.Context, exec Executor, st *stmt, sqlStr string, args []any) (*sql.Rows, error) {
	start := time.Now()
	var rows *sql.Rows
	var err error
	if l := c.stmts.lease(ctx, exec, st, sqlStr, args); l.stmt != nil {
		// The lease ends here, not when the rows close: database/sql keeps
		// the driver's statement alive while rows read from it are open.
		rows, err = l.stmt.QueryContext(ctx, args...)
		l.release()
	} else {
		rows, err = exec.QueryContext(ctx, sqlStr, args...)
	}
	if err != nil || !st.reported {
		c.observe(QueryEvent{
			SQL:       sqlStr,
			Args:      args,
			Duration:  time.Since(start),
			Error:     err,
			Table:     st.table,
			Operation: st.op,
			Kind:      st.kind,
		})
	}
	return rows, err
}

// queryRowStmt sends a statement through QueryRowContext on exec, through
// the middleware chain, and reports it. *sql.Row defers its error to Scan,
// so the event carries none, and Rows is 1.
func (c *Client) queryRowStmt(ctx context.Context, exec Executor, st stmt, sqlStr string, args []any) *sql.Row {
	if len(c.middleware) == 0 {
		return c.queryRowEngine(ctx, exec, &st, sqlStr, args)
	}
	link := st
	next := QueryRowFunc(func(ctx context.Context, exec Executor, s string, a []any) *sql.Row {
		return c.queryRowEngine(ctx, exec, &link, s, a)
	})
	for i := len(c.middleware) - 1; i >= 0; i-- {
		next = c.middleware[i].WrapQueryRow(next)
	}
	return next(withStatementKind(ctx, st.kind), exec, sqlStr, args)
}

func (c *Client) queryRowEngine(ctx context.Context, exec Executor, st *stmt, sqlStr string, args []any) *sql.Row {
	start := time.Now()
	var row *sql.Row
	if l := c.stmts.lease(ctx, exec, st, sqlStr, args); l.stmt != nil {
		row = l.stmt.QueryRowContext(ctx, args...)
		l.release()
	} else {
		row = exec.QueryRowContext(ctx, sqlStr, args...)
	}
	c.observe(QueryEvent{
		SQL:       sqlStr,
		Args:      args,
		Duration:  time.Since(start),
		Rows:      1,
		Table:     st.table,
		Operation: st.op,
		Kind:      st.kind,
	})
	return row
}

// --- executors for code that takes an Executor ------------------------------

// observedExec is an Executor whose statements go through the client's seam.
// It is what Quark hands the code that runs statements on an Executor it is
// given — the schema paths, a dialect's SchemaIntrospector, ColumnAlterer
// and TableRebuilder, the migrate and quarktenant packages — so what that
// code sends passes the chain and reaches the observers too. The chain sees
// inner, the *sql.DB, *sql.Tx or *sql.Conn underneath, never this wrapper.
type observedExec struct {
	c        *Client
	inner    Executor
	execKind StatementKind // what ExecContext sends
	readKind StatementKind // what QueryContext and QueryRowContext send
}

func (e observedExec) ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error) {
	return e.c.execStmt(ctx, e.inner, stmt{kind: e.execKind, op: "EXEC"}, query, args)
}

func (e observedExec) QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error) {
	return e.c.queryStmt(ctx, e.inner, stmt{kind: e.readKind, op: "QUERY"}, query, args)
}

func (e observedExec) QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row {
	return e.c.queryRowStmt(ctx, e.inner, stmt{kind: e.readKind, op: "QUERY_ROW"}, query, args)
}

// observed wraps exec so its statements reach the seam: what it executes as
// execKind, what it reads as readKind. An executor that is already wrapped is
// re-wrapped around what it holds, never nested.
func (c *Client) observed(exec Executor, execKind, readKind StatementKind) Executor {
	if o, ok := exec.(observedExec); ok {
		exec = o.inner
	}
	return observedExec{c: c, inner: exec, execKind: execKind, readKind: readKind}
}

// schemaExec wraps exec for schema work: what it executes is DDL, what it
// reads is introspection.
func (c *Client) schemaExec(exec Executor) Executor {
	return c.observed(exec, StatementDDL, StatementIntrospection)
}

func init() {
	observe.Executor = func(client any, exec quarkdriver.Executor, execKind, readKind string) quarkdriver.Executor {
		c, ok := client.(*Client)
		if !ok || c == nil {
			return exec
		}
		return c.observed(exec, StatementKind(execKind), StatementKind(readKind))
	}
}
