// Copyright 2026 jcsvwinston
// SPDX-License-Identifier: Apache-2.0

package sqlite

// The SQLite error predicates, for the pure-Go driver this module links. They
// live in the module that owns the driver, so that the Quark library module
// requires no engine (ADR-0024).

import (
	"errors"

	moderncsqlite "modernc.org/sqlite"
)

// uniqueViolation matches the extended result codes 2067
// (SQLITE_CONSTRAINT_UNIQUE) and 1555 (SQLITE_CONSTRAINT_PRIMARYKEY). Both
// mean "already taken"; the primary-key code is separate and would be missed
// by a check that looked only for the unique one.
func uniqueViolation(err error) bool {
	var e *moderncsqlite.Error
	if errors.As(err, &e) {
		code := e.Code()
		return code == 2067 || code == 1555
	}
	return false
}

// deadlock always reports false. SQLite is single-writer and never raises a
// true deadlock. SQLITE_BUSY looks like one and is deliberately NOT reported:
// it is lock contention, and a caller hitting it should serialise its writes
// rather than retry a transaction that was never a victim.
func deadlock(error) bool { return false }

// transientConn always reports false: SQLite has no network layer, so a
// "down" database is a closed handle, which Quark recognises unaided.
func transientConn(error) bool { return false }
