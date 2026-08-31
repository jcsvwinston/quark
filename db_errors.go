// Copyright 2026 jcsvwinston
// SPDX-License-Identifier: Apache-2.0

package quark

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"net"
	"strings"

	"github.com/jcsvwinston/quark/quarkdriver"
)

// pgSQLState extracts the five-character SQLSTATE from a PostgreSQL driver
// error, reporting whether err came from PostgreSQL at all.
//
// It matches on the `SQLState() string` METHOD rather than on a concrete
// driver type, because quark supports more than one PostgreSQL driver:
// `dialect.go` accepts the driver names "postgres", "pgx", "pgx/v5" and "pq",
// and the installation guide prescribes `lib/pq` while the events listener
// requires `pgx/v5`. Both expose the code through this method, as do any
// future drivers that follow the same convention, so one assertion covers
// them all — and it costs no import, which is why the pgconn dependency this
// file used to carry is gone.
//
// Matching a concrete type instead is the bug this helper exists to prevent:
// classifying only `*pgconn.PgError` silently skipped every lib/pq error, so
// unique violations, deadlocks and dropped connections went unrecognised
// under the driver the docs recommend. errors.As walks the Unwrap chain, so
// wrapped driver errors classify identically.
func pgSQLState(err error) (string, bool) {
	type sqlStater interface{ SQLState() string }
	var sse sqlStater
	if errors.As(err, &sse) {
		return sse.SQLState(), true
	}
	return "", false
}

// isUniqueViolation reports whether err is a unique-key (or primary-key)
// constraint violation from any of the supported drivers. It uses errors.As
// against driver error codes — through pgSQLState for PostgreSQL, and against
// the driver-specific error type for the rest — and never against message
// text, so it stays correct across driver versions and locales.
//
// Used by linkM2M to keep duplicate-link inserts idempotent while still
// propagating any other error (FK violation, missing table, broken
// connection, etc.) instead of silently swallowing it.
func isUniqueViolation(err error) bool {
	if err == nil {
		return false
	}

	// PostgreSQL. SQLSTATE 23505 = unique_violation.
	if state, ok := pgSQLState(err); ok {
		return state == "23505"
	}

	// Every other engine is classified by the module that supplies its
	// driver's error types. A classifier answers only for its own driver, so
	// consulting them in turn is safe — the first true is the engine the
	// error came from.
	for _, c := range quarkdriver.Classifiers() {
		if c.UniqueViolation(err) {
			return true
		}
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

	// PostgreSQL.
	if state, ok := pgSQLState(err); ok {
		return state == "40P01"
	}

	for _, c := range quarkdriver.Classifiers() {
		if c.Deadlock(err) {
			return true
		}
	}

	return false
}

// isTransientConnErr reports whether err looks like a transient connection
// failure — the server went away, the pooled connection is stale, the host is
// unreachable, or the database handle is closed — as opposed to a query/logic
// error (which a retry would not fix). F6-6 uses it to fail a read over from a
// downed read replica to the primary and mark the replica unhealthy. It mirrors
// isDeadlock's driver-shape detection (errors.As walks the wrap chain).
func isTransientConnErr(err error) bool {
	if err == nil {
		return false
	}

	// Context cancellation / deadline is NOT a connection failure — it is the
	// caller's timeout or cancel, and failing over + marking a healthy replica
	// down for it would be wrong. Filter it BEFORE the net.Error branch:
	// context.DeadlineExceeded implements net.Error (Timeout/Temporary), so it
	// would otherwise be misclassified as transient.
	if errors.Is(err, context.DeadlineExceeded) || errors.Is(err, context.Canceled) {
		return false
	}

	// database/sql sentinels: stale pooled connection, or a connection/DB
	// already closed.
	if errors.Is(err, driver.ErrBadConn) || errors.Is(err, sql.ErrConnDone) {
		return true
	}

	// Network-level failure (connection refused, reset, host unreachable,
	// dial/i-o timeout) — the replica is unreachable. net.OpError and friends
	// implement net.Error.
	var netErr net.Error
	if errors.As(err, &netErr) {
		return true
	}

	// PostgreSQL: SQLSTATE class 08 (connection exception) plus the
	// shutdown / cannot-connect-now codes.
	if state, ok := pgSQLState(err); ok {
		if strings.HasPrefix(state, "08") {
			return true
		}
		switch state {
		case "57P01", "57P02", "57P03": // admin_shutdown, crash_shutdown, cannot_connect_now
			return true
		}
	}

	for _, c := range quarkdriver.Classifiers() {
		if c.TransientConn(err) {
			return true
		}
	}

	// SQLite (modernc / mattn) has no network layer, so a "down" replica is a
	// closed *sql.DB handle. database/sql surfaces that as this message; the
	// sentinel (errDBClosed) is unexported, hence the string match.
	if strings.Contains(err.Error(), "database is closed") {
		return true
	}

	return false
}
