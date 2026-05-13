// Copyright 2026 jcsvwinston
// SPDX-License-Identifier: Apache-2.0

package quark

import (
	"context"
	"errors"
	"fmt"
	"time"
)

// MigrationLock is the handle returned by Client.AcquireMigrationLock.
// The caller must invoke Release before the *Client is closed; the
// lock is held by a dedicated connection for its entire lifetime so a
// process panic / Client.Close releases it automatically through the
// underlying driver's session teardown.
//
// The lock guarantees mutual exclusion across processes sharing the
// same database. Concurrent acquirers of the same `name` block up to
// the requested timeout; the first one wins, the rest receive
// `ErrLockTimeout` if the timeout elapses.
type MigrationLock interface {
	// Release relinquishes the lock and returns the underlying
	// connection to the pool. Safe to call multiple times; subsequent
	// calls are no-ops. Returns an error only if the release RPC fails
	// — not if the lock was already released.
	Release(ctx context.Context) error
}

// MigrationLocker is the optional interface a Dialect implements to
// support distributed migration locks. PG / MySQL / MariaDB / MSSQL
// implement it; SQLite and (currently) Oracle do not.
//
// Kept as an optional interface — not a required method on Dialect —
// so custom Dialect implementations downstream don't have to grow
// this method to keep compiling. They opt in if and when they need
// distributed-lock support.
type MigrationLocker interface {
	AcquireMigrationLock(ctx context.Context, db DBConnector, name string, timeout time.Duration) (MigrationLock, error)
}

// DBConnector is the narrow subset of *sql.DB the lock implementations
// need. It exists so the optional-interface contract doesn't drag the
// full Executor surface into MigrationLocker. The only implementation
// of this in practice is *sql.DB itself; the alias keeps tests honest
// without re-exporting database/sql.
type DBConnector interface {
	Conn(ctx context.Context) (DBConn, error)
}

// DBConn is the per-connection subset the lock implementations consume.
// Wraps *sql.Conn so the locks can ExecContext and Close on a single
// connection without coupling to database/sql package types directly.
type DBConn interface {
	ExecContext(ctx context.Context, query string, args ...any) (Result, error)
	QueryRowContext(ctx context.Context, query string, args ...any) Row
	Close() error
}

// Result mirrors database/sql.Result for the lock implementations.
type Result interface {
	LastInsertId() (int64, error)
	RowsAffected() (int64, error)
}

// Row mirrors *database/sql.Row for the lock implementations (Scan only).
type Row interface {
	Scan(dest ...any) error
}

// ErrLockTimeout is returned by AcquireMigrationLock when the lock
// cannot be acquired within the given timeout. Distinct from
// ErrUnsupportedFeature (which means the dialect doesn't model
// distributed locks at all). Distinct from generic driver errors.
var ErrLockTimeout = errors.New("migration lock acquisition timed out")

// AcquireMigrationLock attempts to acquire a cluster-wide advisory
// lock named `name` for migration operations. The first concurrent
// caller wins; subsequent callers block up to `timeout` (or receive
// ErrLockTimeout if the timeout elapses).
//
// Typical use:
//
//	lock, err := client.AcquireMigrationLock(ctx, "schema-migrations", 30*time.Second)
//	if err != nil {
//	    return err
//	}
//	defer lock.Release(ctx)
//
//	if err := client.Migrate(ctx, &User{}, &Order{}); err != nil {
//	    return err
//	}
//
// Behaviour per dialect (see TASKS § F3-1):
//   - PostgreSQL: `pg_advisory_lock(hashtext(name))` on a dedicated
//     connection. Released by `pg_advisory_unlock` on Release.
//   - MySQL / MariaDB: `GET_LOCK(name, timeout_seconds)` + `RELEASE_LOCK`.
//   - MSSQL: `sp_getapplock @LockMode='Exclusive', @LockOwner='Session'`
//   - `sp_releaseapplock`.
//   - SQLite: returns `ErrUnsupportedFeature` — no distributed-lock
//     primitive; use a `BEGIN IMMEDIATE` transaction inside the
//     process for single-writer semantics.
//   - Oracle: returns `ErrUnsupportedFeature` — the DBMS_LOCK API
//     requires PL/SQL blocks and per-lock allocation handles; deferred
//     to a follow-up PR.
func (c *Client) AcquireMigrationLock(ctx context.Context, name string, timeout time.Duration) (MigrationLock, error) {
	locker, ok := c.dialect.(MigrationLocker)
	if !ok {
		return nil, fmt.Errorf("%w: dialect %s does not support distributed migration locks", ErrUnsupportedFeature, c.dialect.Name())
	}
	return locker.AcquireMigrationLock(ctx, sqlDBAdapter{c.db}, name, timeout)
}
