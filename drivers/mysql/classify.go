// Copyright 2026 jcsvwinston
// SPDX-License-Identifier: Apache-2.0

package mysql

// The MySQL/MariaDB error predicates.
//
// They live in this module because this is the module that owns the driver:
// the types they match on come from `github.com/go-sql-driver/mysql`, and a
// predicate is only as current as the driver release it was written against.
// Keeping them here is what lets the Quark library module require no driver
// at all (ADR-0024) — before that they sat in a package shared with the
// library, which is why the library's go.mod listed every engine.
//
// None of them FAILS when it does not recognise an error: it answers false,
// and a false from the deadlock predicate is a transaction that quietly stops
// being retried. That is why each one matches on the driver's error NUMBER
// rather than on message text: a server running with another lc_messages
// answers the same rejection in a different language.

import (
	"errors"

	gomysql "github.com/go-sql-driver/mysql"
)

func number(err error) (uint16, bool) {
	var e *gomysql.MySQLError
	if errors.As(err, &e) {
		return e.Number, true
	}
	return 0, false
}

// uniqueViolation matches 1062 (ER_DUP_ENTRY).
func uniqueViolation(err error) bool {
	n, ok := number(err)
	return ok && n == 1062
}

// deadlock matches 1213 (ER_LOCK_DEADLOCK): this transaction was chosen as
// the victim, so re-running the closure is the correct response.
func deadlock(err error) bool {
	n, ok := number(err)
	return ok && n == 1213
}

// transientConn matches 2002/2003 (cannot connect), 2006 (server gone away)
// and 2013 (lost connection during query) — all of them the connection
// rather than the statement.
func transientConn(err error) bool {
	n, ok := number(err)
	if !ok {
		return false
	}
	switch n {
	case 2002, 2003, 2006, 2013:
		return true
	}
	return false
}
