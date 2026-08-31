// Copyright 2026 jcsvwinston
// SPDX-License-Identifier: Apache-2.0

// Package driverclassify holds the per-driver error predicates.
//
// It exists to have exactly one copy of each. Three places need them and they
// cannot all reach the same one otherwise: the driver modules under drivers/,
// the `quark` CLI (which links every engine, being a tool people point at
// whatever database they have), and Quark's own test binary. Written out
// three times, they would drift — and a classifier that drifts does not fail,
// it answers false, which is a wrong answer nothing reports.
//
// Nothing in the Quark library imports this package, and that is the whole
// point: importing it means importing the driver error types, which is the
// weight the drivers/ split removed.
package driverclassify

import (
	"errors"

	gomysql "github.com/go-sql-driver/mysql"
	mssqldb "github.com/microsoft/go-mssqldb"
	goora "github.com/sijms/go-ora/v2/network"
	moderncsqlite "modernc.org/sqlite"
)

// --- MySQL / MariaDB -------------------------------------------------------

func mysqlNumber(err error) (uint16, bool) {
	var e *gomysql.MySQLError
	if errors.As(err, &e) {
		return e.Number, true
	}
	return 0, false
}

// MySQLUniqueViolation matches 1062 (ER_DUP_ENTRY) on the CODE, not on the
// message: a server running with another lc_messages answers the same
// rejection in that language.
func MySQLUniqueViolation(err error) bool {
	n, ok := mysqlNumber(err)
	return ok && n == 1062
}

// MySQLDeadlock matches 1213 (ER_LOCK_DEADLOCK): this transaction was chosen
// as the victim, so re-running the closure is the correct response.
func MySQLDeadlock(err error) bool {
	n, ok := mysqlNumber(err)
	return ok && n == 1213
}

// MySQLTransientConn matches 2002/2003 (cannot connect), 2006 (server gone
// away) and 2013 (lost connection during query) — all of them the connection
// rather than the statement.
func MySQLTransientConn(err error) bool {
	n, ok := mysqlNumber(err)
	if !ok {
		return false
	}
	switch n {
	case 2002, 2003, 2006, 2013:
		return true
	}
	return false
}

// --- SQL Server ------------------------------------------------------------

// mssqlNumber targets the VALUE type: mssqldb.Error has a value receiver on
// Error(), so a pointer target never matches.
func mssqlNumber(err error) (int32, bool) {
	var e mssqldb.Error
	if errors.As(err, &e) {
		return e.Number, true
	}
	return 0, false
}

// MSSQLUniqueViolation matches 2627 (unique/primary-key CONSTRAINT) and 2601
// (duplicate row in a unique INDEX). The engine picks between them by how
// uniqueness was declared, which the caller never sees.
func MSSQLUniqueViolation(err error) bool {
	n, ok := mssqlNumber(err)
	return ok && (n == 2627 || n == 2601)
}

// MSSQLDeadlock matches 1205: chosen as the deadlock victim.
func MSSQLDeadlock(err error) bool {
	n, ok := mssqlNumber(err)
	return ok && n == 1205
}

// MSSQLTransientConn matches the transport-level failures.
func MSSQLTransientConn(err error) bool {
	n, ok := mssqlNumber(err)
	if !ok {
		return false
	}
	switch n {
	case 233, 10053, 10054, 10060:
		return true
	}
	return false
}

// --- Oracle ----------------------------------------------------------------

// oracleCode walks the Unwrap chain: go-ora/v2 may return *network.OracleError
// directly or wrapped inside a *network.SessionError, and errors.As covers
// both shapes — do not "simplify" this into a type switch.
func oracleCode(err error) (int, bool) {
	var e *goora.OracleError
	if errors.As(err, &e) {
		return e.ErrCode, true
	}
	return 0, false
}

// OracleUniqueViolation matches ORA-00001.
func OracleUniqueViolation(err error) bool {
	c, ok := oracleCode(err)
	return ok && c == 1
}

// OracleDeadlock matches ORA-00060.
func OracleDeadlock(err error) bool {
	c, ok := oracleCode(err)
	return ok && c == 60
}

// OracleTransientConn always reports false. The Oracle driver surfaces
// connection loss through the network layer, which Quark classifies via
// net.Error before consulting any driver. Reporting false is the honest
// answer, not a gap: claiming an error this driver cannot identify would
// answer for another engine.
func OracleTransientConn(error) bool { return false }

// --- SQLite (modernc, pure Go) ---------------------------------------------

// SQLiteUniqueViolation matches the extended result codes 2067
// (SQLITE_CONSTRAINT_UNIQUE) and 1555 (SQLITE_CONSTRAINT_PRIMARYKEY). Both
// mean "already taken"; the primary-key code is separate and would be missed
// by a check that looked only for the unique one.
func SQLiteUniqueViolation(err error) bool {
	var e *moderncsqlite.Error
	if errors.As(err, &e) {
		code := e.Code()
		return code == 2067 || code == 1555
	}
	return false
}

// SQLiteDeadlock always reports false. SQLite is single-writer and never
// raises a true deadlock. SQLITE_BUSY looks like one and is deliberately NOT
// reported: it is lock contention, and a caller hitting it should serialise
// its writes rather than retry a transaction that was never a victim.
func SQLiteDeadlock(error) bool { return false }

// SQLiteTransientConn always reports false: SQLite has no network layer, so a
// "down" database is a closed handle, which Quark recognises unaided.
func SQLiteTransientConn(error) bool { return false }
