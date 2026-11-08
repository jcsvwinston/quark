// Copyright 2026 jcsvwinston
// SPDX-License-Identifier: Apache-2.0

package render

import (
	"context"
	"database/sql/driver"
	"io"
	"strings"
	"sync"
)

// recorder is a database/sql driver that executes nothing. Quark pings it,
// prepares and queries through it, and never learns there is no engine
// behind: every statement it sends is kept, with its bound arguments, and
// answered with an empty result. The playground reads the last statement
// back and shows it.
//
// It is a driver.Connector rather than a registered driver so each dialect
// can own one without touching database/sql's global registry.
type recorder struct {
	mu   sync.Mutex
	sql  string
	args []any
}

func (r *recorder) record(q string, nv []driver.NamedValue) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.sql = q
	r.args = make([]any, len(nv))
	for i, v := range nv {
		r.args[i] = v.Value
	}
}

func (r *recorder) recordValues(q string, vs []driver.Value) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.sql = q
	r.args = make([]any, len(vs))
	for i, v := range vs {
		r.args[i] = v
	}
}

func (r *recorder) last() (string, []any) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.sql, append([]any(nil), r.args...)
}

func (r *recorder) reset() {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.sql, r.args = "", nil
}

// driver.Connector
func (r *recorder) Connect(context.Context) (driver.Conn, error) { return &conn{r: r}, nil }
func (r *recorder) Driver() driver.Driver                        { return r }

// driver.Driver
func (r *recorder) Open(string) (driver.Conn, error) { return &conn{r: r}, nil }

type conn struct{ r *recorder }

func (c *conn) Prepare(q string) (driver.Stmt, error) { return &stmt{r: c.r, q: q}, nil }
func (c *conn) Close() error                          { return nil }
func (c *conn) Begin() (driver.Tx, error)             { return tx{}, nil }
func (c *conn) Ping(context.Context) error            { return nil }
func (c *conn) QueryContext(_ context.Context, q string, nv []driver.NamedValue) (driver.Rows, error) {
	c.r.record(q, nv)
	return rowsFor(q), nil
}
func (c *conn) ExecContext(_ context.Context, q string, nv []driver.NamedValue) (driver.Result, error) {
	c.r.record(q, nv)
	return result{}, nil
}

type stmt struct {
	r *recorder
	q string
}

func (s *stmt) Close() error  { return nil }
func (s *stmt) NumInput() int { return -1 }
func (s *stmt) Exec(vs []driver.Value) (driver.Result, error) {
	s.r.recordValues(s.q, vs)
	return result{}, nil
}
func (s *stmt) Query(vs []driver.Value) (driver.Rows, error) {
	s.r.recordValues(s.q, vs)
	return rowsFor(s.q), nil
}

type tx struct{}

func (tx) Commit() error   { return nil }
func (tx) Rollback() error { return nil }

// rowsFor answers a SELECT with no rows, except an aggregate — Count(),
// Sum() and the rest scan exactly one row and would otherwise report
// sql.ErrNoRows, which is an error about the stub, not about the query.
func rowsFor(q string) driver.Rows {
	if isAggregate(q) {
		return &oneRow{}
	}
	return rows{}
}

func isAggregate(q string) bool {
	for _, fn := range []string{"SELECT COUNT(", "SELECT SUM(", "SELECT AVG(", "SELECT MIN(", "SELECT MAX(", "SELECT COALESCE("} {
		if strings.HasPrefix(q, fn) {
			return true
		}
	}
	return false
}

type rows struct{}

func (rows) Columns() []string         { return nil }
func (rows) Close() error              { return nil }
func (rows) Next([]driver.Value) error { return io.EOF }

// oneRow yields a single row with a single zero.
type oneRow struct{ done bool }

func (*oneRow) Columns() []string { return []string{"n"} }
func (*oneRow) Close() error      { return nil }
func (r *oneRow) Next(dest []driver.Value) error {
	if r.done {
		return io.EOF
	}
	r.done = true
	dest[0] = int64(0)
	return nil
}

type result struct{}

func (result) LastInsertId() (int64, error) { return 0, nil }
func (result) RowsAffected() (int64, error) { return 0, nil }
