// Copyright 2026 jcsvwinston
// SPDX-License-Identifier: Apache-2.0

package quark

// Internal tests for nativeRLSExecutor edges that need direct access to
// the unexported executor: pool-acquisition cancellation (QK6-2) and the
// deferred-commit failure trace (QK6-3). They run against a minimal fake
// database/sql driver — no engine, no Docker — because the behaviours
// under test live entirely in the executor/pool interaction, not in SQL.

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"io"
	"strings"
	"sync"
	"testing"
	"time"
)

// --- fake driver -----------------------------------------------------

// errFakeCommit is what fakeRLSTx.Commit returns when the DSN opts into
// commit failures ("failcommit").
var errFakeCommit = errors.New("fake rls driver: commit refused")

type fakeRLSDriver struct{}

func (fakeRLSDriver) Open(name string) (driver.Conn, error) {
	return &fakeRLSConn{failCommit: name == "failcommit"}, nil
}

type fakeRLSConn struct{ failCommit bool }

func (c *fakeRLSConn) Prepare(string) (driver.Stmt, error) {
	return nil, errors.New("fake rls driver: prepared statements not supported")
}
func (c *fakeRLSConn) Close() error              { return nil }
func (c *fakeRLSConn) Begin() (driver.Tx, error) { return &fakeRLSTx{conn: c}, nil }
func (c *fakeRLSConn) BeginTx(context.Context, driver.TxOptions) (driver.Tx, error) {
	return &fakeRLSTx{conn: c}, nil
}
func (c *fakeRLSConn) ExecContext(context.Context, string, []driver.NamedValue) (driver.Result, error) {
	return driver.RowsAffected(1), nil
}
func (c *fakeRLSConn) QueryContext(context.Context, string, []driver.NamedValue) (driver.Rows, error) {
	return &fakeRLSRows{}, nil
}

type fakeRLSTx struct{ conn *fakeRLSConn }

func (t *fakeRLSTx) Commit() error {
	if t.conn.failCommit {
		return errFakeCommit
	}
	return nil
}
func (t *fakeRLSTx) Rollback() error { return nil }

type fakeRLSRows struct{ done bool }

func (r *fakeRLSRows) Columns() []string { return []string{"one"} }
func (r *fakeRLSRows) Close() error      { return nil }
func (r *fakeRLSRows) Next(dest []driver.Value) error {
	if r.done {
		return io.EOF
	}
	r.done = true
	dest[0] = int64(1)
	return nil
}

var fakeRLSRegisterOnce sync.Once

// fakeRLSDB opens a *sql.DB over the fake driver. dsn "failcommit" makes
// every tx.Commit fail; anything else commits fine.
func fakeRLSDB(t *testing.T, dsn string) *sql.DB {
	t.Helper()
	fakeRLSRegisterOnce.Do(func() { sql.Register("quark-fake-rls", fakeRLSDriver{}) })
	db, err := sql.Open("quark-fake-rls", dsn)
	if err != nil {
		t.Fatalf("open fake driver: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return db
}

// syncBuffer is a strings.Builder safe for the AfterFunc goroutine to
// write while the test reads.
type syncBuffer struct {
	mu sync.Mutex
	b  strings.Builder
}

func (s *syncBuffer) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.Write(p)
}

func (s *syncBuffer) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.String()
}

// --- QK6-2: pool acquisition must honour the caller's ctx ------------

// TestNativeRLSPoolAcquisitionHonoursCallerContext saturates a 2-conn
// pool and asserts that the implicit-tx paths abort with the caller's
// deadline instead of blocking indefinitely on the pool wait. Pre-fix,
// BeginTx received context.WithoutCancel(ctx) directly, which detached
// the POOL WAIT along with the tx lifecycle: with the pool saturated the
// goroutine kept blocking long after the request timeout expired (the
// 6th-audit repro measured >1.2s past a shorter deadline; unbounded in
// principle).
func TestNativeRLSPoolAcquisitionHonoursCallerContext(t *testing.T) {
	db := fakeRLSDB(t, "")
	db.SetMaxOpenConns(2)

	// Hold both connections for the whole test: the pool is saturated.
	for i := 0; i < 2; i++ {
		held, err := db.Conn(context.Background())
		if err != nil {
			t.Fatalf("hold conn %d: %v", i, err)
		}
		t.Cleanup(func() { _ = held.Close() })
	}

	e := &nativeRLSExecutor{db: db, tenantID: "ta", varName: "app.tenant_id"}

	// The select margin (2s) is what keeps the PRE-fix run failing fast
	// instead of hanging the whole test binary: the blocked goroutine is
	// abandoned and reported.
	const timeout = 100 * time.Millisecond
	const margin = 2 * time.Second

	t.Run("QueryContext", func(t *testing.T) {
		ctx, cancel := context.WithTimeout(context.Background(), timeout)
		defer cancel()

		type result struct {
			rows *sql.Rows
			err  error
		}
		done := make(chan result, 1)
		start := time.Now()
		go func() {
			rows, err := e.QueryContext(ctx, "SELECT 1")
			done <- result{rows, err}
		}()

		select {
		case r := <-done:
			if r.err == nil {
				_ = r.rows.Close()
				t.Fatal("expected an error from a saturated pool, got rows")
			}
			if !errors.Is(r.err, context.DeadlineExceeded) {
				t.Fatalf("want context.DeadlineExceeded, got %v", r.err)
			}
			if elapsed := time.Since(start); elapsed > margin {
				t.Fatalf("aborted, but only after %v (deadline was %v)", elapsed, timeout)
			}
		case <-time.After(margin):
			t.Fatalf("QueryContext still blocked %v after a %v deadline — pool acquisition ignores the caller ctx", margin, timeout)
		}
	})

	t.Run("QueryRowContext", func(t *testing.T) {
		ctx, cancel := context.WithTimeout(context.Background(), timeout)
		defer cancel()

		done := make(chan error, 1)
		start := time.Now()
		go func() {
			var v int64
			done <- e.QueryRowContext(ctx, "SELECT 1").Scan(&v)
		}()

		select {
		case err := <-done:
			if err == nil {
				t.Fatal("expected an error from a saturated pool, got a scanned row")
			}
			if !errors.Is(err, context.DeadlineExceeded) {
				t.Fatalf("want context.DeadlineExceeded, got %v", err)
			}
			if elapsed := time.Since(start); elapsed > margin {
				t.Fatalf("aborted, but only after %v (deadline was %v)", elapsed, timeout)
			}
		case <-time.After(margin):
			t.Fatalf("QueryRowContext still blocked %v after a %v deadline — pool acquisition ignores the caller ctx", margin, timeout)
		}
	})
}

// TestNativeRLSImplicitTxReturnsConnToPool pins the sql.Conn lifecycle
// introduced by the QK6-2 fix: after the deferred commit fires, the
// AfterFunc must hand the connection back to the pool (conn.Close).
// Without it, every implicit-tx operation would leak one pooled
// connection permanently — on a MaxOpenConns=1 pool the very next
// acquisition would block forever.
func TestNativeRLSImplicitTxReturnsConnToPool(t *testing.T) {
	db := fakeRLSDB(t, "")
	db.SetMaxOpenConns(1)

	e := &nativeRLSExecutor{db: db, tenantID: "ta", varName: "app.tenant_id"}

	ctx, cancel := context.WithCancel(context.Background())
	rows, err := e.QueryContext(ctx, "SELECT 1")
	if err != nil {
		cancel()
		t.Fatalf("QueryContext: %v", err)
	}
	_ = rows.Close()
	cancel() // end of the "request": deferred commit + conn hand-back

	// The AfterFunc runs asynchronously; give the hand-back a bounded
	// window. If the conn never returns, this acquisition times out.
	acquireCtx, acquireCancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer acquireCancel()
	conn, err := db.Conn(acquireCtx)
	if err != nil {
		t.Fatalf("pool conn not returned after implicit-tx finished: %v", err)
	}
	_ = conn.Close()
}
