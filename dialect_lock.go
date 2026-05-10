// Copyright 2026 jcsvwinston
// SPDX-License-Identifier: Apache-2.0

package quark

import "fmt"

// LockSuffix implementations live here, separate from the main dialect.go
// catalogue so the matrix is easy to inspect at a glance. Phase 2 F2.

// PostgreSQL pessimistic locking (PG 9.5+ for SKIP LOCKED).
//
//	FOR UPDATE [SKIP LOCKED|NOWAIT]
//	FOR SHARE  [SKIP LOCKED|NOWAIT]
func (p *PostgresDialect) LockSuffix(opts LockOptions) (string, string, error) {
	if opts.IsZero() {
		return "", "", nil
	}
	return "", forUpdateSuffix(opts), nil
}

// MySQL 8.0+ supports SKIP LOCKED and NOWAIT.
// Older 5.x ignores those modifiers (driver does not error; lock still
// taken).
func (m *MySQLDialect) LockSuffix(opts LockOptions) (string, string, error) {
	if opts.IsZero() {
		return "", "", nil
	}
	return "", forUpdateSuffix(opts), nil
}

// MariaDB 10.6+ supports SKIP LOCKED and NOWAIT in the same shape as MySQL.
func (m *MariaDBDialect) LockSuffix(opts LockOptions) (string, string, error) {
	if opts.IsZero() {
		return "", "", nil
	}
	return "", forUpdateSuffix(opts), nil
}

// Oracle pessimistic locking. SKIP LOCKED supported on 12c+.
//
//	FOR UPDATE [NOWAIT|SKIP LOCKED]
//
// Oracle does not have a FOR SHARE; map LockForShare → ErrUnsupportedFeature
// rather than emitting an unsafe approximation.
func (o *OracleDialect) LockSuffix(opts LockOptions) (string, string, error) {
	if opts.IsZero() {
		return "", "", nil
	}
	if opts.Mode == LockForShare {
		return "", "", fmt.Errorf("%w: oracle does not support FOR SHARE; use FOR UPDATE or RawQuery", ErrUnsupportedFeature)
	}
	return "", forUpdateSuffix(opts), nil
}

// MSSQL uses table hints attached to the FROM clause (not a SELECT
// suffix). Single-row pessimistic-style locks come from
// (UPDLOCK, ROWLOCK) for ForUpdate and (HOLDLOCK, ROWLOCK) for ForShare.
// SkipLocked maps to READPAST; NoWait has no direct hint, so it errors out
// rather than silently blocking.
func (m *MSSQLDialect) LockSuffix(opts LockOptions) (string, string, error) {
	if opts.IsZero() {
		return "", "", nil
	}
	if opts.NoWait {
		return "", "", fmt.Errorf("%w: mssql has no NOWAIT for table hints; use SET LOCK_TIMEOUT 0 in your transaction or RawQuery", ErrUnsupportedFeature)
	}
	hints := make([]string, 0, 3)
	switch opts.Mode {
	case LockForUpdate:
		hints = append(hints, "UPDLOCK", "ROWLOCK")
	case LockForShare:
		hints = append(hints, "HOLDLOCK", "ROWLOCK")
	}
	if opts.SkipLocked {
		hints = append(hints, "READPAST")
	}
	if len(hints) == 0 {
		return "", "", nil
	}
	return " WITH (" + joinComma(hints) + ")", "", nil
}

// SQLite has no row-level pessimistic-lock primitive — locking is
// transaction-scoped via BEGIN IMMEDIATE / EXCLUSIVE. Return
// ErrUnsupportedFeature so callers can branch by dialect or fall back.
func (s *SQLiteDialect) LockSuffix(opts LockOptions) (string, string, error) {
	if opts.IsZero() {
		return "", "", nil
	}
	return "", "", fmt.Errorf("%w: sqlite has no row-level FOR UPDATE; use BEGIN IMMEDIATE in your transaction", ErrUnsupportedFeature)
}

// forUpdateSuffix builds the trailing "FOR UPDATE [SKIP LOCKED|NOWAIT]"
// clause shared by PG, MySQL, MariaDB, and Oracle.
func forUpdateSuffix(opts LockOptions) string {
	prefix := " FOR UPDATE"
	if opts.Mode == LockForShare {
		prefix = " FOR SHARE"
	}
	switch {
	case opts.SkipLocked:
		return prefix + " SKIP LOCKED"
	case opts.NoWait:
		return prefix + " NOWAIT"
	default:
		return prefix
	}
}

// joinComma is a tiny helper kept local so this file doesn't pull in a
// strings import for the only use case.
func joinComma(parts []string) string {
	out := ""
	for i, p := range parts {
		if i > 0 {
			out += ", "
		}
		out += p
	}
	return out
}
