// Copyright 2026 jcsvwinston
// SPDX-License-Identifier: Apache-2.0

package quark

import (
	"errors"

	gomysql "github.com/go-sql-driver/mysql"
	"github.com/jackc/pgx/v5/pgconn"
	mattnsqlite "github.com/mattn/go-sqlite3"
	mssql "github.com/microsoft/go-mssqldb"
	goora "github.com/sijms/go-ora/v2/network"
	moderncsqlite "modernc.org/sqlite"
)

// isUniqueViolation reports whether err is a unique-key (or primary-key)
// constraint violation from any of the supported drivers. It uses errors.As
// against driver-specific error types and code constants — no string matching
// — so it stays correct across driver versions and locales.
//
// Used by linkM2M to keep duplicate-link inserts idempotent while still
// propagating any other error (FK violation, missing table, broken
// connection, etc.) instead of silently swallowing it.
func isUniqueViolation(err error) bool {
	if err == nil {
		return false
	}

	// PostgreSQL (pgx). SQLSTATE 23505 = unique_violation.
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) {
		return pgErr.Code == "23505"
	}

	// MySQL / MariaDB. 1062 = ER_DUP_ENTRY.
	var mysqlErr *gomysql.MySQLError
	if errors.As(err, &mysqlErr) {
		return mysqlErr.Number == 1062
	}

	// SQL Server. 2627 = unique constraint violation; 2601 = unique index.
	// mssql.Error has a value receiver Error() and Number is int32; the
	// untyped int literals below assign without overflow.
	var mssqlErr mssql.Error
	if errors.As(err, &mssqlErr) {
		return mssqlErr.Number == 2627 || mssqlErr.Number == 2601
	}

	// Oracle. ORA-00001 = unique constraint violated. go-ora/v2 may return
	// *network.OracleError directly or wrapped inside *network.SessionError;
	// errors.As walks the Unwrap chain, so a single check covers both shapes
	// — do not "optimize" this to a direct type switch.
	var oraErr *goora.OracleError
	if errors.As(err, &oraErr) {
		return oraErr.ErrCode == 1
	}

	// SQLite mattn/go-sqlite3. ExtendedCode 2067 = SQLITE_CONSTRAINT_UNIQUE,
	// 1555 = SQLITE_CONSTRAINT_PRIMARYKEY.
	var mattnErr mattnsqlite.Error
	if errors.As(err, &mattnErr) {
		switch mattnErr.ExtendedCode {
		case mattnsqlite.ErrConstraintUnique, mattnsqlite.ErrConstraintPrimaryKey:
			return true
		}
	}

	// SQLite modernc.org/sqlite. Same numeric extended codes.
	var moderncErr *moderncsqlite.Error
	if errors.As(err, &moderncErr) {
		code := moderncErr.Code()
		return code == 2067 /* SQLITE_CONSTRAINT_UNIQUE */ ||
			code == 1555 /* SQLITE_CONSTRAINT_PRIMARYKEY */
	}

	return false
}

// isDeadlock reports whether err is a deadlock detected by one of the
// supported drivers — the kind of error that aborts the entire current
// transaction and is safe to retry by re-running the transaction
// closure (F4-7). Errors that look like a deadlock but aren't safe to
// blindly retry (e.g. SQLite SQLITE_BUSY, which is lock contention,
// not a deadlock victim) are intentionally NOT classified here: SQLite
// is a single-writer engine and never raises a true deadlock; callers
// hitting BUSY should serialise writes, not retry. The four engines
// below ARE multi-writer with deadlock detection:
//
//   - PostgreSQL: SQLSTATE 40P01 (deadlock_detected).
//   - MySQL / MariaDB: ER_LOCK_DEADLOCK (1213).
//   - SQL Server: error 1205 (chosen as deadlock victim).
//   - Oracle: ORA-00060 (deadlock detected while waiting for resource).
//
// The driver-shape detection mirrors isUniqueViolation: errors.As walks
// the Unwrap chain, so wrapped errors stay correctly classified.
func isDeadlock(err error) bool {
	if err == nil {
		return false
	}

	// PostgreSQL (pgx).
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) {
		return pgErr.Code == "40P01"
	}

	// MySQL / MariaDB.
	var mysqlErr *gomysql.MySQLError
	if errors.As(err, &mysqlErr) {
		return mysqlErr.Number == 1213
	}

	// SQL Server.
	var mssqlErr mssql.Error
	if errors.As(err, &mssqlErr) {
		return mssqlErr.Number == 1205
	}

	// Oracle.
	var oraErr *goora.OracleError
	if errors.As(err, &oraErr) {
		return oraErr.ErrCode == 60
	}

	return false
}
