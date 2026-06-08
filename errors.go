// Copyright 2026 jcsvwinston
// SPDX-License-Identifier: Apache-2.0

// Package quark provides a modern, type-safe ORM for Go.
// It supports multiple SQL dialects and is designed to be framework-agnostic.
package quark

import (
	"context"
	"errors"
	"strings"

	"github.com/jcsvwinston/quark/internal/guard"
)

// Common errors returned by quark operations.
var (
	// ErrNotFound indicates that no record was found for the given criteria.
	ErrNotFound = errors.New("record not found")

	// ErrInvalidModel indicates that the provided model is invalid or not registered.
	ErrInvalidModel = errors.New("invalid model")

	// ErrInvalidQuery indicates that the query is malformed or invalid.
	ErrInvalidQuery = errors.New("invalid query")

	// ErrInvalidIdentifier indicates that a table or column identifier is invalid.
	// Re-exported from internal/guard, where ValidateIdentifier wraps it with %w,
	// so errors.Is(err, ErrInvalidIdentifier) works for a rejected identifier from
	// any call site (Where/OrderBy/GroupBy columns, table names, migration ops,
	// CTEs, event channels…) — consistent with ErrInvalidJoin / ErrInvalidJSONPath.
	ErrInvalidIdentifier = guard.ErrInvalidIdentifier

	// ErrInvalidJSONPath indicates that a JSON path passed to WhereJSON is malformed
	// or contains characters that could enable SQL injection. Quark accepts dotted
	// paths shaped like "user.name"; see guard.ValidateJSONPath for the grammar.
	// Array indexes and engine-specific syntax are out of scope for WhereJSON;
	// use RawQuery for those.
	ErrInvalidJSONPath = errors.New("invalid JSON path")

	// ErrInvalidJoin indicates that a JOIN ... ON clause passed to Join,
	// LeftJoin, or RightJoin does not match the minimal identifier-only
	// grammar Quark accepts while a structured Join().On() builder is pending
	// (Phase 2 AST). See guard.ValidateJoinOn for the grammar; use a
	// structured Join (when available) or RawQuery for shapes outside it.
	ErrInvalidJoin = errors.New("invalid JOIN ON clause")

	// ErrStaleEntity indicates that an optimistic-locking update failed
	// because the row's version column had been bumped by another writer
	// since the entity was loaded. The caller should reload the row, replay
	// the change against the fresh state, and retry — or surface the
	// conflict to the user. Returned by Update / UpdateFields / Tracked.Save
	// when the model carries a quark:"version" field.
	ErrStaleEntity = errors.New("stale entity (optimistic-locking conflict)")

	// ErrUnsupportedFeature indicates that a feature is not supported by the
	// active database dialect. Returned by builder methods (e.g. ForUpdate
	// on SQLite) so callers can branch by dialect or fall back to a different
	// strategy. The error message includes the dialect name and the feature
	// being requested.
	ErrUnsupportedFeature = errors.New("feature not supported by dialect")

	// ErrInvalidTimezone indicates that a model field carries a
	// quark:"tz=..." tag whose value is not a valid IANA timezone name
	// (i.e. time.LoadLocation rejected it). It is surfaced fail-fast by
	// Client.RegisterModel and Client.Migrate so an invalid timezone
	// breaks the application at startup, not on the first query that
	// touches the column. The wrapped error names the field, the column
	// and the offending timezone string.
	ErrInvalidTimezone = errors.New("invalid column timezone")

	// ErrDialectNotSupported indicates that the database dialect is not supported.
	ErrDialectNotSupported = errors.New("dialect not supported")

	// ErrConnection indicates a database connection error.
	ErrConnection = errors.New("database connection error")

	// ErrTimeout indicates that a query timed out.
	ErrTimeout = errors.New("query timeout")

	// ErrConstraintViolation indicates a database constraint violation.
	ErrConstraintViolation = errors.New("constraint violation")

	// ErrEventEmitFailed indicates that an EventBus.Publish call failed
	// AFTER the originating write was already persisted. The data is
	// committed; only the event emission failed. Callers that see this
	// wrapped on a non-transactional CRUD return must NOT retry the
	// write (that would double-write) — they should retry the emit or
	// rely on the subscriber being idempotent (at-least-once delivery,
	// no outbox; see ADR-0013). Under an explicit transaction the emit
	// runs post-commit via Tx.OnCommit and the failure is logged with
	// the OTel-style event `quark.event.emit_failure` rather than
	// propagated (the commit already returned success).
	ErrEventEmitFailed = errors.New("event emit failed after commit")

	// ErrListenerClosed indicates an operation was attempted on an
	// EventListener (PostgreSQL LISTEN/NOTIFY) after Close was called.
	// The dedicated connection has been returned to the pool; create a
	// fresh listener via ListenerFactory.CreateListener. See ADR-0019.
	ErrListenerClosed = errors.New("event listener closed")

	// ErrNoSubscription indicates Receive was called on an EventListener
	// that has no active channel subscription — Listen must be called at
	// least once before Receive. See ADR-0019.
	ErrNoSubscription = errors.New("event listener has no channel subscribed")
)

// wrapDBError maps low-level database/context errors to quark sentinel errors.
// It checks for timeout conditions and common constraint violation messages
// across dialects (PostgreSQL, MySQL, SQLite, MSSQL, Oracle).
func wrapDBError(err error) error {
	if err == nil {
		return nil
	}

	// Context timeout / deadline exceeded → ErrTimeout
	if errors.Is(err, context.DeadlineExceeded) || errors.Is(err, context.Canceled) {
		return errors.Join(ErrTimeout, err)
	}

	msg := strings.ToLower(err.Error())

	// Timeout messages from various drivers
	if strings.Contains(msg, "timeout") || strings.Contains(msg, "timed out") {
		return errors.Join(ErrTimeout, err)
	}

	// Constraint violation messages across dialects:
	// PostgreSQL: "unique constraint", "foreign key constraint", "check constraint", "not-null constraint"
	// MySQL/MariaDB: "duplicate entry", "foreign key constraint fails", "cannot be null"
	// SQLite: "unique constraint failed", "foreign key constraint failed", "not null constraint failed"
	// MSSQL: "violation of unique key", "foreign key constraint", "cannot insert the value null"
	// Oracle: "unique constraint", "integrity constraint"
	if strings.Contains(msg, "unique constraint") ||
		strings.Contains(msg, "duplicate entry") ||
		strings.Contains(msg, "unique constraint failed") ||
		strings.Contains(msg, "violation of unique key") ||
		strings.Contains(msg, "integrity constraint") ||
		strings.Contains(msg, "foreign key constraint") ||
		strings.Contains(msg, "not null constraint") ||
		strings.Contains(msg, "check constraint") {
		return errors.Join(ErrConstraintViolation, err)
	}

	return err
}
