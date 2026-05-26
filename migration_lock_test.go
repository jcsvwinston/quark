// Copyright 2026 jcsvwinston
// SPDX-License-Identifier: Apache-2.0

package quark

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"testing"
	"time"
)

// TestAcquireMigrationLock_UnsupportedDialects pins the rejection
// contract: SQLite does not implement MigrationLocker, so
// Client.AcquireMigrationLock returns ErrUnsupportedFeature wrapped
// with a descriptive message. The test runs without any database
// connection; the dialect-detection happens before any RPC.
func TestAcquireMigrationLock_UnsupportedDialects(t *testing.T) {
	// SQLite explicitly does NOT implement MigrationLocker; the
	// type assertion in Client.AcquireMigrationLock should fail and
	// return ErrUnsupportedFeature.
	if _, ok := any(&SQLiteDialect{}).(MigrationLocker); ok {
		t.Errorf("SQLiteDialect must NOT implement MigrationLocker (decision in F3-1)")
	}
}

// TestAcquireMigrationLock_SupportedDialects mirrors the unsupported
// check: PG / MySQL / MariaDB / MSSQL / Oracle must implement
// MigrationLocker. A regression where one of them silently drops the
// interface would surface as `ErrUnsupportedFeature` at runtime against
// a real DB — expensive to catch then; cheap to catch here.
func TestAcquireMigrationLock_SupportedDialects(t *testing.T) {
	for _, d := range []any{
		&PostgresDialect{},
		&MySQLDialect{},
		&MariaDBDialect{},
		&MSSQLDialect{},
		&OracleDialect{},
	} {
		if _, ok := d.(MigrationLocker); !ok {
			t.Errorf("dialect %T must implement MigrationLocker", d)
		}
	}
}

// fakeConn is the in-memory DBConn used by the unit tests below. It
// records the SQL exec/query calls so the test can assert the dialect
// emitted the right commands without needing a real database.
type fakeConn struct {
	execs       []string
	rows        []*fakeRow
	closeCalled bool
}

func (c *fakeConn) ExecContext(ctx context.Context, q string, args ...any) (Result, error) {
	c.execs = append(c.execs, q)
	return fakeResult{}, nil
}

func (c *fakeConn) QueryRowContext(ctx context.Context, q string, args ...any) Row {
	c.execs = append(c.execs, q)
	if len(c.rows) == 0 {
		return &fakeRow{scanFn: func(dest ...any) error { return nil }}
	}
	r := c.rows[0]
	c.rows = c.rows[1:]
	return r
}

func (c *fakeConn) Close() error { c.closeCalled = true; return nil }

type fakeResult struct{}

func (fakeResult) LastInsertId() (int64, error) { return 0, nil }
func (fakeResult) RowsAffected() (int64, error) { return 0, nil }

type fakeRow struct {
	scanFn func(dest ...any) error
}

func (r *fakeRow) Scan(dest ...any) error { return r.scanFn(dest...) }

type fakeConnector struct {
	conn *fakeConn
	err  error
}

func (c fakeConnector) Conn(ctx context.Context) (DBConn, error) {
	if c.err != nil {
		return nil, c.err
	}
	return c.conn, nil
}

// TestPostgresMigrationLock_EmitsExpectedSQL exercises the PG locker
// against the fake conn. The dialect must:
//
//  1. `SET lock_timeout = <ms>` for the session.
//  2. `SELECT pg_advisory_lock(hashtext($1))` with the name.
//  3. On Release: `SELECT pg_advisory_unlock(hashtext($1))` then close.
//
// The test pins the SQL shape, not the result — a real DB roundtrip is
// the SharedSuite's job (see testMigrationLock in
// migration_lock_integration_test.go).
func TestPostgresMigrationLock_EmitsExpectedSQL(t *testing.T) {
	conn := &fakeConn{
		rows: []*fakeRow{{scanFn: func(dest ...any) error { return nil }}},
	}
	d := &PostgresDialect{}
	lock, err := d.AcquireMigrationLock(context.Background(), fakeConnector{conn: conn}, "schema-migrate", 2*time.Second)
	if err != nil {
		t.Fatalf("AcquireMigrationLock: %v", err)
	}
	if got := len(conn.execs); got < 2 {
		t.Fatalf("expected ≥2 ExecContext/QueryRow calls, got %d (%v)", got, conn.execs)
	}
	if conn.execs[0] != "SET lock_timeout = 2000" {
		t.Errorf("expected SET lock_timeout first, got %q", conn.execs[0])
	}
	if conn.execs[1] != "SELECT pg_advisory_lock(hashtext($1))" {
		t.Errorf("expected pg_advisory_lock second, got %q", conn.execs[1])
	}

	if err := lock.Release(context.Background()); err != nil {
		t.Fatalf("Release: %v", err)
	}
	// Conn must be closed after Release.
	if !conn.closeCalled {
		t.Errorf("Release should close the underlying conn")
	}
	// Second Release is a no-op (idempotent).
	if err := lock.Release(context.Background()); err != nil {
		t.Errorf("second Release should be no-op, got %v", err)
	}
}

// TestMySQLMigrationLock_TimeoutMapping pins the GET_LOCK return-code
// to ErrLockTimeout mapping. GET_LOCK returns 0 when the timeout
// elapses; the locker must translate that to ErrLockTimeout (not a
// generic error) so callers can `errors.Is(err, ErrLockTimeout)`.
func TestMySQLMigrationLock_TimeoutMapping(t *testing.T) {
	conn := &fakeConn{
		rows: []*fakeRow{
			// GET_LOCK returns 0 → timeout. The locker scans into
			// *sql.NullInt64; the fake-row writes the timeout outcome
			// directly into that destination.
			{scanFn: func(dest ...any) error {
				ptr := dest[0].(*sql.NullInt64)
				ptr.Valid = true
				ptr.Int64 = 0
				return nil
			}},
		},
	}
	d := &MySQLDialect{}
	_, err := d.AcquireMigrationLock(context.Background(), fakeConnector{conn: conn}, "x", time.Second)
	if !errors.Is(err, ErrLockTimeout) {
		t.Errorf("expected ErrLockTimeout, got %v", err)
	}
}

// TestMSSQLMigrationLock_TimeoutMapping pins the sp_getapplock status
// code -1 → ErrLockTimeout mapping. The locker scans into *int.
func TestMSSQLMigrationLock_TimeoutMapping(t *testing.T) {
	conn := &fakeConn{
		rows: []*fakeRow{
			{scanFn: func(dest ...any) error {
				ptr := dest[0].(*int)
				*ptr = -1
				return nil
			}},
		},
	}
	d := &MSSQLDialect{}
	_, err := d.AcquireMigrationLock(context.Background(), fakeConnector{conn: conn}, "x", time.Second)
	if !errors.Is(err, ErrLockTimeout) {
		t.Errorf("expected ErrLockTimeout, got %v", err)
	}
}

// TestOracleMigrationLock_EmitsExpectedSQL pins the Oracle DBMS_LOCK
// shape against the fake conn: Acquire runs an ALLOCATE_UNIQUE + REQUEST
// PL/SQL block, Release runs an ALLOCATE_UNIQUE + RELEASE block and
// closes the connection. The fake doesn't populate the OUT result, so it
// stays 0 (acquired) — the timeout-result mapping is covered by the
// SharedSuite integration test (TimeoutWhenAlreadyHeld).
func TestOracleMigrationLock_EmitsExpectedSQL(t *testing.T) {
	conn := &fakeConn{}
	d := &OracleDialect{}
	lock, err := d.AcquireMigrationLock(context.Background(), fakeConnector{conn: conn}, "schema-migrate", 2*time.Second)
	if err != nil {
		t.Fatalf("AcquireMigrationLock: %v", err)
	}
	if len(conn.execs) != 1 {
		t.Fatalf("expected 1 exec on acquire, got %d (%v)", len(conn.execs), conn.execs)
	}
	if !strings.Contains(conn.execs[0], "DBMS_LOCK.ALLOCATE_UNIQUE") ||
		!strings.Contains(conn.execs[0], "DBMS_LOCK.REQUEST") {
		t.Errorf("acquire SQL should call ALLOCATE_UNIQUE + REQUEST, got %q", conn.execs[0])
	}

	if err := lock.Release(context.Background()); err != nil {
		t.Fatalf("Release: %v", err)
	}
	if len(conn.execs) != 2 || !strings.Contains(conn.execs[1], "DBMS_LOCK.RELEASE") {
		t.Errorf("release SQL should call DBMS_LOCK.RELEASE, got %v", conn.execs)
	}
	if !conn.closeCalled {
		t.Errorf("Release should close the connection")
	}
	// Idempotent — second Release is a no-op (no extra exec).
	if err := lock.Release(context.Background()); err != nil {
		t.Errorf("second Release should be no-op, got %v", err)
	}
	if len(conn.execs) != 2 {
		t.Errorf("second Release should not emit SQL, got %v", conn.execs)
	}
}
