// Copyright 2026 jcsvwinston
// SPDX-License-Identifier: Apache-2.0

package quarkdriver

import (
	"context"
	"time"
)

// This file is the migration-lock half of the dialect contract (ADR-0026).
// Package quark names every type here under the same name — quark.MigrationLock,
// quark.MigrationLocker, quark.DBConnector, … — as an alias of the type here.

// MigrationLock is the handle returned by quark's Client.AcquireMigrationLock.
// The caller must invoke Release before the client is closed; the
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
// support distributed migration locks. PG / MySQL / MariaDB / MSSQL /
// Oracle implement it; SQLite does not.
//
// Kept as an optional interface — not a required method on Dialect —
// so custom Dialect implementations downstream don't have to grow
// this method to keep compiling. They opt in if and when they need
// distributed-lock support. A dialect without it makes
// Client.AcquireMigrationLock return ErrUnsupportedFeature; one that
// cannot take the lock within timeout returns ErrLockTimeout.
type MigrationLocker interface {
	AcquireMigrationLock(ctx context.Context, db DBConnector, name string, timeout time.Duration) (MigrationLock, error)
}

// DBConnector is the narrow subset of *sql.DB the lock implementations
// need. It exists so the optional-interface contract doesn't drag the
// full Executor surface into MigrationLocker. Quark hands a lock
// implementation an adapter over the client's *sql.DB; the interfaces keep
// tests honest without re-exporting database/sql.
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
