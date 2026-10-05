// Copyright 2026 jcsvwinston
// SPDX-License-Identifier: Apache-2.0

package quark

import (
	"context"
	"fmt"
	"time"

	"github.com/jcsvwinston/quark/quarkdriver"
)

// The migration-lock contract a dialect implements is declared in
// quarkdriver (ADR-0026), where each type is documented; the names below are
// aliases of the same types, so a lock written against either is the same
// lock.

// MigrationLock is the handle returned by Client.AcquireMigrationLock; the
// caller must invoke Release before the *Client is closed.
type MigrationLock = quarkdriver.MigrationLock

// MigrationLocker is the optional interface a Dialect implements to
// support distributed migration locks. PG / MySQL / MariaDB / MSSQL /
// Oracle implement it; SQLite does not.
type MigrationLocker = quarkdriver.MigrationLocker

// DBConnector is the narrow subset of *sql.DB the lock implementations
// need.
type DBConnector = quarkdriver.DBConnector

// DBConn is the per-connection subset the lock implementations consume.
type DBConn = quarkdriver.DBConn

// Result mirrors database/sql.Result for the lock implementations.
type Result = quarkdriver.Result

// Row mirrors *database/sql.Row for the lock implementations (Scan only).
type Row = quarkdriver.Row

// ErrLockTimeout is returned by AcquireMigrationLock when the lock
// cannot be acquired within the given timeout. Distinct from
// ErrUnsupportedFeature (which means the dialect doesn't model
// distributed locks at all). Distinct from generic driver errors.
// It holds the value of quarkdriver.ErrLockTimeout, so errors.Is matches
// an error a driver module built from either name.
var ErrLockTimeout = quarkdriver.ErrLockTimeout

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
//   - Oracle: `DBMS_LOCK.ALLOCATE_UNIQUE` + `REQUEST(X_MODE,
//     release_on_commit => FALSE)` — session-scoped, survives the
//     implicit commits of DDL. Requires `GRANT EXECUTE ON DBMS_LOCK`
//     (see ADR-0018).
//   - SQLite: returns `ErrUnsupportedFeature` — no distributed-lock
//     primitive; use a `BEGIN IMMEDIATE` transaction inside the
//     process for single-writer semantics.
func (c *Client) AcquireMigrationLock(ctx context.Context, name string, timeout time.Duration) (MigrationLock, error) {
	locker, ok := c.dialect.(MigrationLocker)
	if !ok {
		return nil, fmt.Errorf("%w: dialect %s does not support distributed migration locks", ErrUnsupportedFeature, c.dialect.Name())
	}
	return locker.AcquireMigrationLock(ctx, sqlDBAdapter{client: c, db: c.db}, name, timeout)
}
