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
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"
)

// --- fake driver -----------------------------------------------------

// errFakeCommit is what fakeRLSTx.Commit returns when the DSN opts into
// commit failures ("failcommit").
var errFakeCommit = errors.New("fake rls driver: commit refused")

// errFakeBegin / errFakeSetConfig back the QK7-3 modes below.
var (
	errFakeBegin     = errors.New("fake rls driver: begin refused")
	errFakeSetConfig = errors.New("fake rls driver: exec refused")
)

type fakeRLSDriver struct{}

func (fakeRLSDriver) Open(name string) (driver.Conn, error) {
	return &fakeRLSConn{mode: name}, nil
}

// fakeRLSConn's behaviour is selected by the DSN:
//
//   - "failcommit":    every tx.Commit fails (QK6-3)
//   - "failbegin":     BeginTx fails (QK7-3)
//   - "failsetconfig": ExecContext fails — the first statement inside the
//     implicit tx is set_config, so this simulates its failure (QK7-3)
//   - "panicexec":     ExecContext panics — the driver dies on the second
//     operation of the implicit tx, after BeginTx handed one out (QK7-1)
//   - "panicblockrollback": ExecContext panics AND Rollback blocks until
//     the test releases blockRollbackGate — the deterministic stand-in
//     for database/sql internals that never release their locks after a
//     panic, so the detached cleanup stays blocked (QK8-1)
//   - "norows":        queries succeed and return zero rows (QK7-3 control)
type fakeRLSConn struct{ mode string }

func (c *fakeRLSConn) Prepare(string) (driver.Stmt, error) {
	return nil, errors.New("fake rls driver: prepared statements not supported")
}
func (c *fakeRLSConn) Close() error { return nil }
func (c *fakeRLSConn) Begin() (driver.Tx, error) {
	return c.BeginTx(context.Background(), driver.TxOptions{})
}
func (c *fakeRLSConn) BeginTx(context.Context, driver.TxOptions) (driver.Tx, error) {
	if c.mode == "failbegin" {
		return nil, errFakeBegin
	}
	return &fakeRLSTx{conn: c}, nil
}
func (c *fakeRLSConn) ExecContext(context.Context, string, []driver.NamedValue) (driver.Result, error) {
	switch c.mode {
	case "failsetconfig":
		return nil, errFakeSetConfig
	case "panicexec", "panicblockrollback":
		panic("fake rls driver: exec panic")
	}
	return driver.RowsAffected(1), nil
}
func (c *fakeRLSConn) QueryContext(context.Context, string, []driver.NamedValue) (driver.Rows, error) {
	return &fakeRLSRows{done: c.mode == "norows"}, nil
}

type fakeRLSTx struct{ conn *fakeRLSConn }

func (t *fakeRLSTx) Commit() error {
	if t.conn.mode == "failcommit" {
		return errFakeCommit
	}
	return nil
}
func (t *fakeRLSTx) Rollback() error {
	if t.conn.mode == "panicblockrollback" {
		if ch := getBlockRollbackGate(); ch != nil {
			<-ch
		}
	}
	return nil
}

// blockRollbackGate parks every "panicblockrollback" Rollback until the
// owning test closes it (registered in t.Cleanup so the blocked cleanup
// goroutines always drain before the process exits). Mutex-guarded
// because the detached cleanup goroutine reads it while the test — and
// under -race, later tests — may write it.
var (
	blockRollbackMu   sync.Mutex
	blockRollbackGate chan struct{}
)

func setBlockRollbackGate(ch chan struct{}) {
	blockRollbackMu.Lock()
	defer blockRollbackMu.Unlock()
	blockRollbackGate = ch
}

func getBlockRollbackGate() chan struct{} {
	blockRollbackMu.Lock()
	defer blockRollbackMu.Unlock()
	return blockRollbackGate
}

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

// --- QK6-3: a failed deferred commit must leave a trace ---------------

// waitForCounter polls fn until it returns want or the deadline passes.
func waitForCounter(t *testing.T, want uint64, fn func() uint64) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if fn() == want {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("counter = %d, want %d (deferred-commit failure left no trace)", fn(), want)
}

// TestNativeRLSDeferredCommitFailureLeavesTrace forces the deferred
// commit (the context.AfterFunc that finalizes the implicit tx after the
// request ctx ends) to fail, and asserts the failure is not swallowed:
// an ERROR log line through the Client's logger plus one increment of
// Client.DeferredCommitFailures. Pre-QK6-3 the failure only produced a
// Warn IF a logger happened to be non-nil, and no counter existed — an
// operator had no aggregate signal that committed-looking writes were
// being rolled back.
func TestNativeRLSDeferredCommitFailureLeavesTrace(t *testing.T) {
	db := fakeRLSDB(t, "failcommit")

	var buf syncBuffer
	c := &Client{logger: slog.New(slog.NewTextHandler(&buf, nil))}
	e := &nativeRLSExecutor{db: db, tenantID: "ta", varName: "app.tenant_id", client: c}

	ctx, cancel := context.WithCancel(context.Background())
	rows, err := e.QueryContext(ctx, "SELECT 1")
	if err != nil {
		cancel()
		t.Fatalf("QueryContext: %v", err)
	}
	_ = rows.Close()

	if got := c.DeferredCommitFailures(); got != 0 {
		cancel()
		t.Fatalf("counter moved before the deferred commit ran: %d", got)
	}

	cancel() // end of "request" → AfterFunc → Commit fails

	waitForCounter(t, 1, c.DeferredCommitFailures)
	if out := buf.String(); !strings.Contains(out, "implicit-tx deferred commit failed") ||
		!strings.Contains(out, "commit refused") {
		t.Fatalf("commit failure not logged through the client logger; log output:\n%s", out)
	}

	// Second failure accumulates — the counter is an aggregate, not a flag.
	ctx2, cancel2 := context.WithCancel(context.Background())
	var v int64
	_ = e.QueryRowContext(ctx2, "SELECT 1").Scan(&v)
	cancel2()
	waitForCounter(t, 2, c.DeferredCommitFailures)
}

// TestNativeRLSDeferredCommitFailureFallbackLogger pins the "never
// silent" half of QK6-3: with a nil Client logger (or no Client at all)
// the failure must still be logged — through slog.Default(), the same
// fallback New() installs — and, when a Client exists, still counted.
func TestNativeRLSDeferredCommitFailureFallbackLogger(t *testing.T) {
	db := fakeRLSDB(t, "failcommit")

	// Capture slog's default output. Serial test (no t.Parallel): the
	// default logger is process-global.
	var buf syncBuffer
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&buf, nil)))
	t.Cleanup(func() { slog.SetDefault(prev) })

	c := &Client{} // logger nil: the pre-QK6-3 code stayed silent here
	e := &nativeRLSExecutor{db: db, tenantID: "ta", varName: "app.tenant_id", client: c}

	ctx, cancel := context.WithCancel(context.Background())
	rows, err := e.QueryContext(ctx, "SELECT 1")
	if err != nil {
		cancel()
		t.Fatalf("QueryContext: %v", err)
	}
	_ = rows.Close()
	cancel()

	waitForCounter(t, 1, c.DeferredCommitFailures)
	if out := buf.String(); !strings.Contains(out, "implicit-tx deferred commit failed") {
		t.Fatalf("nil client logger silenced the commit failure; default-logger output:\n%s", out)
	}
}

// --- QK7-3: acquisition failures must not read as ErrNoRows ------------

// assertRealRowError asserts the Scan error for a forced infrastructure
// failure on the QueryRow path: non-nil, NOT sql.ErrNoRows, carrying the
// driver's original error in its chain, and prefixed with the failing
// stage.
func assertRealRowError(t *testing.T, scanErr, want error, stage string) {
	t.Helper()
	if scanErr == nil {
		t.Fatalf("expected the %s failure to surface on Scan, got nil", stage)
	}
	if errors.Is(scanErr, sql.ErrNoRows) {
		t.Fatalf("%s failure masked as sql.ErrNoRows: %v", stage, scanErr)
	}
	if want != nil && !errors.Is(scanErr, want) {
		t.Fatalf("Scan lost the real %s error chain: %v", stage, scanErr)
	}
	if !strings.Contains(scanErr.Error(), stage) {
		t.Fatalf("Scan error does not name the failing stage %q: %v", stage, scanErr)
	}
}

// TestNativeRLSQueryRowSurfacesRealErrors forces each pre-statement
// failure of the QueryRow path (BeginTx, set_config, conn acquisition)
// and asserts the caller's Scan receives the real error instead of the
// pre-fix sentinel behaviour, where every one of them scanned as
// sql.ErrNoRows and the cause survived only in the log.
func TestNativeRLSQueryRowSurfacesRealErrors(t *testing.T) {
	t.Run("BeginTx", func(t *testing.T) {
		db := fakeRLSDB(t, "failbegin")
		e := &nativeRLSExecutor{db: db, tenantID: "ta", varName: "app.tenant_id"}

		var v int64
		err := e.QueryRowContext(context.Background(), "SELECT 1").Scan(&v)
		assertRealRowError(t, err, errFakeBegin, "native rls: begin tx")
	})

	t.Run("SetConfig", func(t *testing.T) {
		db := fakeRLSDB(t, "failsetconfig")
		e := &nativeRLSExecutor{db: db, tenantID: "ta", varName: "app.tenant_id"}

		var v int64
		err := e.QueryRowContext(context.Background(), "SELECT 1").Scan(&v)
		assertRealRowError(t, err, errFakeSetConfig, "native rls: set_config")
	})

	t.Run("AcquireConn", func(t *testing.T) {
		db := fakeRLSDB(t, "")
		_ = db.Close() // every acquisition now fails with sql: database is closed
		e := &nativeRLSExecutor{db: db, tenantID: "ta", varName: "app.tenant_id"}

		var v int64
		err := e.QueryRowContext(context.Background(), "SELECT 1").Scan(&v)
		assertRealRowError(t, err, nil, "native rls: acquire conn")
	})
}

// TestNativeRLSQueryRowNoRowsIsStillErrNoRows is the control for QK7-3:
// with the infrastructure healthy and a genuinely empty result, Scan
// must keep returning sql.ErrNoRows — the fix separates the two cases,
// it does not repaint them both.
func TestNativeRLSQueryRowNoRowsIsStillErrNoRows(t *testing.T) {
	db := fakeRLSDB(t, "norows")
	e := &nativeRLSExecutor{db: db, tenantID: "ta", varName: "app.tenant_id"}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel() // ends the "request": deferred commit + conn hand-back

	var v int64
	err := e.QueryRowContext(ctx, "SELECT 1").Scan(&v)
	if !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("empty result must scan as sql.ErrNoRows, got %v", err)
	}
}

// --- QK7-1: a panicking driver must not leak the conn/tx ---------------

// TestNativeRLSPanicDuringImplicitTxDoesNotLeakConn kills the driver on
// the second operation of the implicit tx (the set_config ExecContext,
// right after BeginTx handed a transaction out) and asserts two things
// for each executor path: the panic still propagates to the caller, and
// the pool is NOT left exhausted. Pre-fix there was no defer between
// acquiring the conn/tx and the point where the AfterFunc (or the
// synchronous Commit) takes ownership, so the panic abandoned both — on
// a MaxOpenConns=1 pool every later acquisition blocked forever.
func TestNativeRLSPanicDuringImplicitTxDoesNotLeakConn(t *testing.T) {
	paths := []struct {
		name string
		op   func(e *nativeRLSExecutor, ctx context.Context)
	}{
		{"QueryContext", func(e *nativeRLSExecutor, ctx context.Context) {
			_, _ = e.QueryContext(ctx, "SELECT 1")
		}},
		{"QueryRowContext", func(e *nativeRLSExecutor, ctx context.Context) {
			var v int64
			_ = e.QueryRowContext(ctx, "SELECT 1").Scan(&v)
		}},
		{"ExecContext", func(e *nativeRLSExecutor, ctx context.Context) {
			_, _ = e.ExecContext(ctx, "UPDATE t SET x = 1")
		}},
	}
	for _, p := range paths {
		t.Run(p.name, func(t *testing.T) {
			db := fakeRLSDB(t, "panicexec")
			db.SetMaxOpenConns(1)
			e := &nativeRLSExecutor{db: db, tenantID: "ta", varName: "app.tenant_id"}

			func() {
				defer func() {
					if recover() == nil {
						t.Fatal("expected the driver panic to propagate to the caller")
					}
				}()
				p.op(e, context.Background())
			}()

			// The panic-path cleanup runs on a detached goroutine, so
			// give the hand-back a bounded window: if the conn never
			// returns, this acquisition times out and fails the test.
			acquireCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
			defer cancel()
			conn, err := db.Conn(acquireCtx)
			if err != nil {
				t.Fatalf("pool exhausted after a driver panic mid implicit tx: %v", err)
			}
			_ = conn.Close()
		})
	}
}

// --- QK8-1: a blocked panic cleanup must leave a trace -----------------

// panicCleanupPaths are the three executor operations whose QK7-1 defer
// guard detaches the cleanup on a driver panic. Shared by the blocked
// and the completed QK8-1 tests below.
var panicCleanupPaths = []struct {
	name string
	op   func(e *nativeRLSExecutor, ctx context.Context)
}{
	{"QueryContext", func(e *nativeRLSExecutor, ctx context.Context) {
		_, _ = e.QueryContext(ctx, "SELECT 1")
	}},
	{"QueryRowContext", func(e *nativeRLSExecutor, ctx context.Context) {
		var v int64
		_ = e.QueryRowContext(ctx, "SELECT 1").Scan(&v)
	}},
	{"ExecContext", func(e *nativeRLSExecutor, ctx context.Context) {
		_, _ = e.ExecContext(ctx, "UPDATE t SET x = 1")
	}},
}

// mustPanic runs op and asserts the driver panic still propagates to the
// caller — the QK8-1 watchdog must not change the QK7-1 semantics.
func mustPanic(t *testing.T, op func()) {
	t.Helper()
	defer func() {
		if recover() == nil {
			t.Fatal("expected the driver panic to propagate to the caller")
		}
	}()
	op()
}

// TestNativeRLSBlockedPanicCleanupLeavesTrace pins the QK8-1 operator
// signal: when the detached QK7-1 cleanup cannot finish — here because
// the driver's Rollback parks on a gate that never opens within the
// test, the deterministic stand-in for database/sql internals that a
// panic left locked — the watchdog must increment
// Client.BlockedPanicCleanups and log once at ERROR. Before QK8-1 the
// blockage was invisible: the goroutine hung silently and the held
// tx/conn pair could only be inferred from pool exhaustion.
func TestNativeRLSBlockedPanicCleanupLeavesTrace(t *testing.T) {
	for _, p := range panicCleanupPaths {
		t.Run(p.name, func(t *testing.T) {
			gate := make(chan struct{})
			setBlockRollbackGate(gate)
			t.Cleanup(func() {
				close(gate) // drain the parked cleanup goroutine
				setBlockRollbackGate(nil)
			})

			db := fakeRLSDB(t, "panicblockrollback")
			var buf syncBuffer
			c := &Client{logger: slog.New(slog.NewTextHandler(&buf, nil))}
			e := &nativeRLSExecutor{
				db: db, tenantID: "ta", varName: "app.tenant_id", client: c,
				// Short watchdog so the blocked case is fast; production
				// derives the deadline from QueryTimeout instead.
				panicCleanupDeadline: 20 * time.Millisecond,
			}

			mustPanic(t, func() { p.op(e, context.Background()) })

			waitForCounter(t, 1, c.BlockedPanicCleanups)
			if out := buf.String(); !strings.Contains(out, "panic-path cleanup still blocked") {
				t.Fatalf("blocked cleanup not logged through the client logger; log output:\n%s", out)
			}
		})
	}
}

// TestNativeRLSCompletedPanicCleanupLeavesNoTrace is the control: with a
// driver whose Rollback returns normally (the "panicexec" mode already
// used by the QK7-1 test), the detached cleanup completes, so the
// watchdog must NOT count or log anything — the counter stays 0. The
// deliberately huge deadline makes the assertion deterministic: if the
// watchdog were to fire despite a completed cleanup, it would be a
// logic bug, not a timing accident.
func TestNativeRLSCompletedPanicCleanupLeavesNoTrace(t *testing.T) {
	for _, p := range panicCleanupPaths {
		t.Run(p.name, func(t *testing.T) {
			db := fakeRLSDB(t, "panicexec")
			db.SetMaxOpenConns(1)
			var buf syncBuffer
			c := &Client{logger: slog.New(slog.NewTextHandler(&buf, nil))}
			e := &nativeRLSExecutor{
				db: db, tenantID: "ta", varName: "app.tenant_id", client: c,
				panicCleanupDeadline: time.Hour,
			}

			mustPanic(t, func() { p.op(e, context.Background()) })

			// Reacquiring the single pooled conn proves the cleanup
			// COMPLETED (same bounded-window pattern as the QK7-1 test).
			acquireCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
			defer cancel()
			conn, err := db.Conn(acquireCtx)
			if err != nil {
				t.Fatalf("pool exhausted after a driver panic mid implicit tx: %v", err)
			}
			_ = conn.Close()

			if got := c.BlockedPanicCleanups(); got != 0 {
				t.Fatalf("BlockedPanicCleanups = %d after a cleanup that completed, want 0", got)
			}
			if out := buf.String(); strings.Contains(out, "panic-path cleanup still blocked") {
				t.Fatalf("completed cleanup logged as blocked; log output:\n%s", out)
			}
		})
	}
}

// TestNativeRLSCleanupWatchdogDeadline pins the deadline derivation
// documented on cleanupWatchdogDeadline: the client's QueryTimeout when
// positive, DefaultLimits().QueryTimeout when there is no client or the
// timeout is disabled, and the test override winning over both.
func TestNativeRLSCleanupWatchdogDeadline(t *testing.T) {
	def := DefaultLimits().QueryTimeout

	e := &nativeRLSExecutor{client: &Client{limits: Limits{QueryTimeout: 7 * time.Second}}}
	if got := e.cleanupWatchdogDeadline(); got != 7*time.Second {
		t.Errorf("client QueryTimeout not used: got %v, want 7s", got)
	}

	e = &nativeRLSExecutor{} // no client at all
	if got := e.cleanupWatchdogDeadline(); got != def {
		t.Errorf("nil client: got %v, want DefaultLimits().QueryTimeout %v", got, def)
	}

	e = &nativeRLSExecutor{client: &Client{limits: Limits{QueryTimeout: -1}}}
	if got := e.cleanupWatchdogDeadline(); got != def {
		t.Errorf("disabled QueryTimeout: got %v, want DefaultLimits().QueryTimeout %v", got, def)
	}

	e = &nativeRLSExecutor{client: &Client{limits: Limits{QueryTimeout: time.Minute}}, panicCleanupDeadline: time.Millisecond}
	if got := e.cleanupWatchdogDeadline(); got != time.Millisecond {
		t.Errorf("test override lost: got %v, want 1ms", got)
	}
}
