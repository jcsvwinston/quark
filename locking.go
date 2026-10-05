// Copyright 2026 jcsvwinston
// SPDX-License-Identifier: Apache-2.0

package quark

import "github.com/jcsvwinston/quark/quarkdriver"

// LockMode is the kind of pessimistic lock requested for a SELECT. It is
// declared in quarkdriver with the dialect contract whose LockSuffix takes it
// (ADR-0026); this name is an alias of the same type.
type LockMode = quarkdriver.LockMode

const (
	// LockNone means no lock clause is emitted (the default).
	LockNone = quarkdriver.LockNone
	// LockForUpdate locks the rows for update; other transactions cannot
	// read-with-lock or write the matching rows until the current
	// transaction commits or rolls back. Most engines support it.
	LockForUpdate = quarkdriver.LockForUpdate
	// LockForShare takes a shared read lock — other transactions can also
	// read-with-lock but not write. Supported on PG / MySQL 8+ / MariaDB;
	// not on SQLite. MSSQL approximates with HOLDLOCK.
	LockForShare = quarkdriver.LockForShare
)

// LockOptions describes the pessimistic-lock behaviour for a SELECT.
// The zero value (LockMode == LockNone) emits nothing — callers opt in
// via ForUpdate / ForShare on Query[T]. It is declared in quarkdriver, where
// Dialect.LockSuffix names it (ADR-0026); this name is an alias of the same
// type, IsZero method included.
type LockOptions = quarkdriver.LockOptions

// ForUpdate marks the query so the emitted SELECT acquires a row-level
// FOR UPDATE lock. Composes with SkipLocked / NoWait. Returns the receiver
// (no error) so it chains naturally; SQL surface failures (unsupported
// dialect, invalid combination) surface at execution time.
//
//	rows, err := quark.For[Order](ctx, client).
//	    Where("status", "=", "pending").
//	    ForUpdate().
//	    Limit(50).
//	    List()
func (q *Query[T]) ForUpdate() *Query[T] {
	c := q.clone()
	c.lock.Mode = LockForUpdate
	return c
}

// ForShare marks the query so the emitted SELECT acquires a shared
// read lock. Composes with SkipLocked / NoWait. Not supported on SQLite;
// MSSQL approximates with HOLDLOCK; PG / MySQL 8+ / MariaDB / Oracle
// support it natively.
func (q *Query[T]) ForShare() *Query[T] {
	c := q.clone()
	c.lock.Mode = LockForShare
	return c
}

// SkipLocked tells the database to skip rows that are already locked by
// another transaction instead of blocking on them. Combine with
// ForUpdate / ForShare. Implementation-defined per dialect.
func (q *Query[T]) SkipLocked() *Query[T] {
	c := q.clone()
	c.lock.SkipLocked = true
	return c
}

// NoWait tells the database to fail immediately if any matching row is
// already locked by another transaction. Combine with ForUpdate / ForShare.
// Implementation-defined per dialect.
func (q *Query[T]) NoWait() *Query[T] {
	c := q.clone()
	c.lock.NoWait = true
	return c
}
