// Copyright 2026 jcsvwinston
// SPDX-License-Identifier: Apache-2.0

package extbench

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"sync"

	moderncsqlite "modernc.org/sqlite"
)

// wireDriverName is a database/sql driver that records every statement that
// reaches the engine — the ground truth the observation controls compare a
// QueryObserver and a Middleware against. It is modernc's SQLite driver with
// a recording layer at the driver.Conn boundary: below Quark, below
// database/sql's pool, where nothing Quark does can skip it.
const wireDriverName = "extbench-wire"

var (
	wireOnce sync.Once
	wireMu   sync.Mutex
	wireLog  []string
	wireOn   bool
)

func registerWireDriver() {
	wireOnce.Do(func() {
		sql.Register(wireDriverName, wireDriver{inner: &moderncsqlite.Driver{}})
	})
}

// wireCapture starts a recording and returns a function that stops it and
// returns what reached the engine, in order.
func wireCapture() func() []string {
	wireMu.Lock()
	wireLog, wireOn = nil, true
	wireMu.Unlock()
	return func() []string {
		wireMu.Lock()
		defer wireMu.Unlock()
		wireOn = false
		out := wireLog
		wireLog = nil
		return out
	}
}

func wireRecord(query string) {
	wireMu.Lock()
	defer wireMu.Unlock()
	if wireOn {
		wireLog = append(wireLog, query)
	}
}

type wireDriver struct{ inner driver.Driver }

func (d wireDriver) Open(name string) (driver.Conn, error) {
	c, err := d.inner.Open(name)
	if err != nil {
		return nil, err
	}
	return &wireConn{inner: c}, nil
}

// wireConn forwards every interface modernc's connection implements that
// database/sql consults, recording the statement text on the three paths a
// statement can take: ExecContext, QueryContext and PrepareContext.
type wireConn struct{ inner driver.Conn }

func (c *wireConn) Prepare(query string) (driver.Stmt, error) {
	wireRecord(query)
	return c.inner.Prepare(query)
}

func (c *wireConn) PrepareContext(ctx context.Context, query string) (driver.Stmt, error) {
	wireRecord(query)
	if p, ok := c.inner.(driver.ConnPrepareContext); ok {
		return p.PrepareContext(ctx, query)
	}
	return c.inner.Prepare(query)
}

func (c *wireConn) Close() error { return c.inner.Close() }

// Begin is required by driver.Conn; database/sql calls BeginTx.
func (c *wireConn) Begin() (driver.Tx, error) { return c.inner.Begin() }

func (c *wireConn) BeginTx(ctx context.Context, opts driver.TxOptions) (driver.Tx, error) {
	if b, ok := c.inner.(driver.ConnBeginTx); ok {
		return b.BeginTx(ctx, opts)
	}
	return c.inner.Begin()
}

func (c *wireConn) ExecContext(ctx context.Context, query string, args []driver.NamedValue) (driver.Result, error) {
	e, ok := c.inner.(driver.ExecerContext)
	if !ok {
		return nil, driver.ErrSkip
	}
	wireRecord(query)
	return e.ExecContext(ctx, query, args)
}

func (c *wireConn) QueryContext(ctx context.Context, query string, args []driver.NamedValue) (driver.Rows, error) {
	q, ok := c.inner.(driver.QueryerContext)
	if !ok {
		return nil, driver.ErrSkip
	}
	wireRecord(query)
	return q.QueryContext(ctx, query, args)
}

func (c *wireConn) Ping(ctx context.Context) error {
	if p, ok := c.inner.(driver.Pinger); ok {
		return p.Ping(ctx)
	}
	return nil
}

func (c *wireConn) ResetSession(ctx context.Context) error {
	if r, ok := c.inner.(driver.SessionResetter); ok {
		return r.ResetSession(ctx)
	}
	return nil
}

func (c *wireConn) IsValid() bool {
	if v, ok := c.inner.(driver.Validator); ok {
		return v.IsValid()
	}
	return true
}
