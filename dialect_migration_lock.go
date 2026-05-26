// Copyright 2026 jcsvwinston
// SPDX-License-Identifier: Apache-2.0

package quark

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"sync"
	"time"
)

// sqlDBAdapter / sqlConnAdapter bridge `*sql.DB` and `*sql.Conn` to the
// narrow DBConnector / DBConn / Result / Row interfaces. The lock
// implementations use the interfaces directly so they don't import
// `database/sql` (and so the tests can swap in fakes without the real
// driver).
type sqlDBAdapter struct{ db *sql.DB }

func (a sqlDBAdapter) Conn(ctx context.Context) (DBConn, error) {
	c, err := a.db.Conn(ctx)
	if err != nil {
		return nil, err
	}
	return sqlConnAdapter{conn: c}, nil
}

type sqlConnAdapter struct{ conn *sql.Conn }

func (a sqlConnAdapter) ExecContext(ctx context.Context, q string, args ...any) (Result, error) {
	return a.conn.ExecContext(ctx, q, args...)
}
func (a sqlConnAdapter) QueryRowContext(ctx context.Context, q string, args ...any) Row {
	return a.conn.QueryRowContext(ctx, q, args...)
}
func (a sqlConnAdapter) Close() error { return a.conn.Close() }

// --- PostgreSQL --------------------------------------------------------------

// AcquireMigrationLock uses `pg_advisory_lock(hashtext(name))` on a
// dedicated connection. Session-level (not transaction-level), so the
// caller can run multiple statements under the lock without holding a
// long transaction open. Released by `pg_advisory_unlock` on Release.
//
// Timeout is honoured via `SET lock_timeout` on the connection — PG's
// native way to bound advisory-lock waits. A timeout violation surfaces
// as SQLSTATE `55P03` (`lock_not_available`); we translate it to
// `ErrLockTimeout`.
func (d *PostgresDialect) AcquireMigrationLock(ctx context.Context, db DBConnector, name string, timeout time.Duration) (MigrationLock, error) {
	conn, err := db.Conn(ctx)
	if err != nil {
		return nil, fmt.Errorf("pg migration lock: get conn: %w", err)
	}
	if _, err := conn.ExecContext(ctx, fmt.Sprintf("SET lock_timeout = %d", timeout.Milliseconds())); err != nil {
		_ = conn.Close()
		return nil, fmt.Errorf("pg migration lock: set timeout: %w", err)
	}
	if _, err := conn.ExecContext(ctx, "SELECT pg_advisory_lock(hashtext($1))", name); err != nil {
		_ = conn.Close()
		if isPGLockTimeout(err) {
			return nil, ErrLockTimeout
		}
		return nil, fmt.Errorf("pg migration lock: acquire: %w", err)
	}
	return &pgMigrationLock{conn: conn, name: name}, nil
}

type pgMigrationLock struct {
	conn     DBConn
	name     string
	released bool
	mu       sync.Mutex
}

func (l *pgMigrationLock) Release(ctx context.Context) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.released {
		return nil
	}
	l.released = true
	_, unlockErr := l.conn.ExecContext(ctx, "SELECT pg_advisory_unlock(hashtext($1))", l.name)
	closeErr := l.conn.Close()
	if unlockErr != nil {
		return fmt.Errorf("pg migration lock: release: %w", unlockErr)
	}
	return closeErr
}

// isPGLockTimeout maps the SQLSTATE `55P03` (lock_not_available) error
// emitted by Postgres when `lock_timeout` fires before the advisory
// lock can be taken. Both `lib/pq` and `pgx/v5/stdlib` expose the code
// via a SQLState() method on their error types.
func isPGLockTimeout(err error) bool {
	if err == nil {
		return false
	}
	type sqlStater interface{ SQLState() string }
	var sse sqlStater
	if errors.As(err, &sse) {
		return sse.SQLState() == "55P03"
	}
	return false
}

// --- MySQL / MariaDB ---------------------------------------------------------

// AcquireMigrationLock uses MySQL's `GET_LOCK(name, timeout_seconds)`,
// which is session-bound (released when the connection ends). Returns
// 1 on success, 0 on timeout, NULL on error. We dedicate a connection
// for the lock's lifetime; `Release` calls `RELEASE_LOCK` and returns
// the connection to the pool.
//
// Timeout argument: GET_LOCK accepts seconds (negative = wait forever);
// we round Duration to seconds. Sub-second timeouts are rounded UP to
// 1 second — the next-best approximation of the caller's intent given
// the protocol granularity.
//
// MariaDB shares MySQL's GET_LOCK semantics — same code path.
func (d *MySQLDialect) AcquireMigrationLock(ctx context.Context, db DBConnector, name string, timeout time.Duration) (MigrationLock, error) {
	return acquireMySQLLikeLock(ctx, db, name, timeout, "mysql")
}

func (d *MariaDBDialect) AcquireMigrationLock(ctx context.Context, db DBConnector, name string, timeout time.Duration) (MigrationLock, error) {
	return acquireMySQLLikeLock(ctx, db, name, timeout, "mariadb")
}

func acquireMySQLLikeLock(ctx context.Context, db DBConnector, name string, timeout time.Duration, dialectName string) (MigrationLock, error) {
	conn, err := db.Conn(ctx)
	if err != nil {
		return nil, fmt.Errorf("%s migration lock: get conn: %w", dialectName, err)
	}
	timeoutSec := int64(timeout / time.Second)
	if timeout > 0 && timeoutSec == 0 {
		timeoutSec = 1
	}
	var result sql.NullInt64
	row := conn.QueryRowContext(ctx, "SELECT GET_LOCK(?, ?)", name, timeoutSec)
	if err := row.Scan(&result); err != nil {
		_ = conn.Close()
		return nil, fmt.Errorf("%s migration lock: GET_LOCK: %w", dialectName, err)
	}
	if !result.Valid {
		_ = conn.Close()
		return nil, fmt.Errorf("%s migration lock: GET_LOCK returned NULL (likely deadlock or interrupted)", dialectName)
	}
	if result.Int64 == 0 {
		_ = conn.Close()
		return nil, ErrLockTimeout
	}
	return &mysqlMigrationLock{conn: conn, name: name, dialectName: dialectName}, nil
}

type mysqlMigrationLock struct {
	conn        DBConn
	name        string
	dialectName string
	released    bool
	mu          sync.Mutex
}

func (l *mysqlMigrationLock) Release(ctx context.Context) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.released {
		return nil
	}
	l.released = true
	var result sql.NullInt64
	row := l.conn.QueryRowContext(ctx, "SELECT RELEASE_LOCK(?)", l.name)
	scanErr := row.Scan(&result)
	closeErr := l.conn.Close()
	if scanErr != nil {
		return fmt.Errorf("%s migration lock: RELEASE_LOCK: %w", l.dialectName, scanErr)
	}
	if !result.Valid || result.Int64 == 0 {
		// `0` means the lock was not held by this session — should not
		// happen given our state machine, but log as an error so a bug
		// in the lock lifecycle surfaces.
		return fmt.Errorf("%s migration lock: RELEASE_LOCK reported no lock held (state machine bug?)", l.dialectName)
	}
	return closeErr
}

// --- MSSQL -------------------------------------------------------------------

// AcquireMigrationLock uses `sp_getapplock` with @LockOwner='Session',
// scoped to the dedicated connection's session. Returns an integer
// status: 0 (granted immediately), 1 (granted after wait), -1 (timeout),
// -2 (cancel), -3 (deadlock), -999 (parameter / fatal error). We map
// -1 to `ErrLockTimeout` and the others to descriptive errors.
//
// Timeout is in milliseconds; we round the Go Duration to ms.
func (d *MSSQLDialect) AcquireMigrationLock(ctx context.Context, db DBConnector, name string, timeout time.Duration) (MigrationLock, error) {
	conn, err := db.Conn(ctx)
	if err != nil {
		return nil, fmt.Errorf("mssql migration lock: get conn: %w", err)
	}
	timeoutMS := timeout.Milliseconds()
	if timeoutMS < 0 {
		// sp_getapplock interprets -1 as "wait forever".
		timeoutMS = -1
	}
	var status int
	row := conn.QueryRowContext(ctx,
		"DECLARE @r int; EXEC @r = sp_getapplock @Resource = @p1, @LockMode = 'Exclusive', @LockOwner = 'Session', @LockTimeout = @p2; SELECT @r",
		name, timeoutMS,
	)
	if err := row.Scan(&status); err != nil {
		_ = conn.Close()
		return nil, fmt.Errorf("mssql migration lock: sp_getapplock: %w", err)
	}
	switch {
	case status >= 0:
		return &mssqlMigrationLock{conn: conn, name: name}, nil
	case status == -1:
		_ = conn.Close()
		return nil, ErrLockTimeout
	default:
		_ = conn.Close()
		return nil, fmt.Errorf("mssql migration lock: sp_getapplock returned status %d", status)
	}
}

type mssqlMigrationLock struct {
	conn     DBConn
	name     string
	released bool
	mu       sync.Mutex
}

func (l *mssqlMigrationLock) Release(ctx context.Context) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.released {
		return nil
	}
	l.released = true
	_, err := l.conn.ExecContext(ctx,
		"EXEC sp_releaseapplock @Resource = @p1, @LockOwner = 'Session'", l.name,
	)
	closeErr := l.conn.Close()
	if err != nil {
		return fmt.Errorf("mssql migration lock: sp_releaseapplock: %w", err)
	}
	return closeErr
}

// --- Oracle ------------------------------------------------------------------

// dbmsLockMaxWait is DBMS_LOCK's "wait forever" sentinel (the documented
// MAXWAIT constant, 32767 seconds). Used when the caller passes a
// non-positive timeout.
const dbmsLockMaxWait = 32767

// AcquireMigrationLock uses Oracle's `DBMS_LOCK` package — the session-
// scoped advisory lock that mirrors PG's `pg_advisory_lock` and MySQL's
// `GET_LOCK`. `ALLOCATE_UNIQUE(name)` maps the lock name to a handle
// (deterministic per name), and `REQUEST(handle, X_MODE,
// release_on_commit => FALSE)` takes the exclusive lock. Because the
// lock is session-scoped and explicitly NOT released on commit, it
// survives Oracle's implicit DDL commits and needs no open transaction
// — which is what lets it fit the connection-only DBConn interface (a
// lock-table `SELECT … FOR UPDATE` would need a held transaction the
// interface doesn't expose). See ADR-0018.
//
// REQUEST returns 0 (acquired), 1 (timeout), 2 (deadlock), 3 (parameter
// error), 4 (already own this lock), 5 (illegal handle). 0 and 4 are
// success; 1 maps to ErrLockTimeout; the rest are errors.
//
// Requires `GRANT EXECUTE ON DBMS_LOCK TO <user>` — DBMS_LOCK is not
// granted to schema users by default. The timeout is in whole seconds
// (DBMS_LOCK's granularity); sub-second waits round up to 1s.
//
// Prerequisite caveat is documented in website/docs/guides/migrations.mdx.
func (d *OracleDialect) AcquireMigrationLock(ctx context.Context, db DBConnector, name string, timeout time.Duration) (MigrationLock, error) {
	conn, err := db.Conn(ctx)
	if err != nil {
		return nil, fmt.Errorf("oracle migration lock: get conn: %w", err)
	}
	waitSecs := int64(timeout / time.Second)
	switch {
	case timeout <= 0:
		waitSecs = dbmsLockMaxWait
	case waitSecs == 0:
		waitSecs = 1
	}
	var result int
	_, err = conn.ExecContext(ctx,
		`DECLARE
		   h VARCHAR2(128);
		 BEGIN
		   DBMS_LOCK.ALLOCATE_UNIQUE(:lockname, h);
		   :result := DBMS_LOCK.REQUEST(lockhandle => h, lockmode => DBMS_LOCK.X_MODE, timeout => :wait, release_on_commit => FALSE);
		 END;`,
		sql.Named("lockname", name),
		sql.Named("result", sql.Out{Dest: &result}),
		sql.Named("wait", waitSecs),
	)
	if err != nil {
		_ = conn.Close()
		return nil, fmt.Errorf("oracle migration lock: DBMS_LOCK.REQUEST: %w", err)
	}
	switch result {
	case 0, 4: // 0 = acquired, 4 = this session already owns it
		return &oracleMigrationLock{conn: conn, name: name}, nil
	case 1:
		_ = conn.Close()
		return nil, ErrLockTimeout
	default:
		_ = conn.Close()
		return nil, fmt.Errorf("oracle migration lock: DBMS_LOCK.REQUEST returned %d", result)
	}
}

type oracleMigrationLock struct {
	conn     DBConn
	name     string
	released bool
	mu       sync.Mutex
}

// Release re-derives the handle on the same (still-held) session via
// ALLOCATE_UNIQUE — deterministic for the name — and calls
// DBMS_LOCK.RELEASE, then returns the connection to the pool. Idempotent.
func (l *oracleMigrationLock) Release(ctx context.Context) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.released {
		return nil
	}
	l.released = true
	var result int
	_, err := l.conn.ExecContext(ctx,
		`DECLARE
		   h VARCHAR2(128);
		 BEGIN
		   DBMS_LOCK.ALLOCATE_UNIQUE(:lockname, h);
		   :result := DBMS_LOCK.RELEASE(h);
		 END;`,
		sql.Named("lockname", l.name),
		sql.Named("result", sql.Out{Dest: &result}),
	)
	closeErr := l.conn.Close()
	if err != nil {
		return fmt.Errorf("oracle migration lock: DBMS_LOCK.RELEASE: %w", err)
	}
	// RELEASE returns 0 (released) or 4 (didn't own — already gone). Both
	// are acceptable end states; other codes signal a lifecycle bug.
	if result != 0 && result != 4 {
		return fmt.Errorf("oracle migration lock: DBMS_LOCK.RELEASE returned %d", result)
	}
	return closeErr
}

// --- SQLite: intentionally NOT MigrationLocker -------------------------------
//
// SQLite has no distributed-lock primitive (single-writer model;
// transactions block on the WAL). Use `BEGIN IMMEDIATE` inside the
// process for the equivalent semantic. The dialect therefore does not
// implement MigrationLocker — `Client.AcquireMigrationLock` returns
// `ErrUnsupportedFeature`.
