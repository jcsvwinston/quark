// Copyright 2026 jcsvwinston
// SPDX-License-Identifier: Apache-2.0

// Package quark provides a modern, type-safe ORM for Go.
// It supports multiple SQL dialects and is designed to be framework-agnostic.
package quark

import (
	"context"
	"errors"
	"github.com/jcsvwinston/quark/quarkdriver"
	"strings"

	"github.com/jcsvwinston/quark/internal/guard"
)

// Common errors returned by quark operations.
//
// Every sentinel's godoc carries a sentence that begins "What to do:" — the
// recipe — and errors_recipe_test.go fails the build when one is missing.
// Match a sentinel with errors.Is; the wrapped message adds the call site
// and, where there is a step to take, names it. A sentinel that is returned
// bare (ErrNotFound, ErrTimeout, ErrConstraintViolation) carries the hint in
// its own text.
var (
	// ErrNotFound indicates that no record was found for the given criteria.
	// Returned bare by First and Find when the filtered result is empty, so
	// its text carries the hint.
	//
	// What to do: use List or Exists when zero rows are a valid outcome;
	// otherwise check the filter, the key and — under a tenant router — the
	// tenant the context resolves.
	ErrNotFound = errors.New("record not found: First/Find matched no row; use List or Exists when zero rows are expected, or check the filter and the tenant")

	// ErrInvalidModel indicates that the provided model is invalid or not
	// registered: it has no metadata, no primary key, or lacks a column the
	// operation needs (deleted_at for Restore, the tenant column under
	// RowLevelSecurityClient).
	//
	// What to do: register the model with Client.RegisterModel, give it a
	// primary key (a field tagged pk:"true" or a column named id) and add
	// the column the wrapped message names.
	ErrInvalidModel = errors.New("invalid model")

	// ErrInvalidQuery indicates that the query is malformed or invalid. Re-exported
	// from internal/guard, where ValidateOperator / ValidateRawQuery wrap it with
	// %w, so errors.Is(err, ErrInvalidQuery) holds for a rejected operator, a raw
	// query missing placeholders, or a suspicious raw pattern — as well as for the
	// query-construction errors raised with it in this package. Nothing reached
	// the database.
	//
	// What to do: the text after the prefix names the method and what it
	// needs — a non-nil operand, a Where clause, a primary key, an option such
	// as Limits.AllowRawQueries — so fix the call as it says.
	ErrInvalidQuery = guard.ErrInvalidQuery

	// ErrInvalidIdentifier indicates that a table or column identifier is invalid.
	// Re-exported from internal/guard, where ValidateIdentifier wraps it with %w,
	// so errors.Is(err, ErrInvalidIdentifier) works for a rejected identifier from
	// any call site (Where/OrderBy/GroupBy columns, table names, migration ops,
	// CTEs, event channels…) — consistent with ErrInvalidJoin / ErrInvalidJSONPath.
	//
	// What to do: use names that match ^[a-zA-Z_][a-zA-Z0-9_]*$, are at most 64
	// characters, carry at most one table. qualifier and are not reserved SQL
	// keywords; rename the column or table, or read a legacy name through
	// RawQuery with the engine's quoting.
	ErrInvalidIdentifier = guard.ErrInvalidIdentifier

	// ErrInvalidJSONPath indicates that a JSON path passed to WhereJSON is malformed
	// or contains characters that could enable SQL injection. Quark accepts dotted
	// paths shaped like "user.name"; see guard.ValidateJSONPath for the grammar.
	// Array indexes and engine-specific syntax are out of scope for WhereJSON.
	//
	// What to do: pass a dotted identifier chain such as "user.name"; use
	// RawQuery for array indexes or engine-specific path syntax.
	ErrInvalidJSONPath = errors.New("invalid JSON path")

	// ErrInvalidJoin indicates that a JOIN ... ON clause passed to Join,
	// LeftJoin, or RightJoin does not match the minimal identifier-only
	// grammar Quark accepts. See guard.ValidateJoinOn for the grammar.
	//
	// What to do: write the ON clause as identifier comparisons joined by
	// AND/OR ("users.id = orders.user_id"), or use OnExpr or RawQuery for a
	// shape outside that grammar.
	ErrInvalidJoin = errors.New("invalid JOIN ON clause")

	// ErrStaleEntity indicates that an optimistic-locking update failed
	// because the row's version column had been bumped by another writer
	// since the entity was loaded. Returned by Update / UpdateFields /
	// Tracked.Save when the model carries a quark:"version" field, and by
	// UpdateBatch under [Query.CheckVersions], joined once per stale row with
	// that row's key (QK-45). UpdateBatch without CheckVersions does not
	// return it; Quark 2.0 makes the check the default (DEP-2026-003).
	//
	// What to do: reload the row, replay the change against the fresh state
	// and retry — or surface the conflict to the user.
	ErrStaleEntity = errors.New("stale entity (optimistic-locking conflict)")

	// ErrUnsupportedFeature indicates that a feature is not supported by the
	// active database dialect. Returned by builder methods (e.g. ForUpdate
	// on SQLite) so callers can branch by dialect or fall back to a different
	// strategy. The error message includes the dialect name and the feature
	// being requested. It holds the value of
	// quarkdriver.ErrUnsupportedFeature, which a dialect returns (ADR-0026),
	// so errors.Is matches under either name.
	//
	// What to do: branch on the dialect, or take the alternative the message
	// names — another lock mode, a distinct set operation, RawQuery.
	ErrUnsupportedFeature = quarkdriver.ErrUnsupportedFeature

	// ErrInvalidTimezone indicates that a model field carries a
	// quark:"tz=..." tag whose value is not a valid IANA timezone name
	// (i.e. time.LoadLocation rejected it). It is surfaced fail-fast by
	// Client.RegisterModel and Client.Migrate so an invalid timezone
	// breaks the application at startup, not on the first query that
	// touches the column. The wrapped error names the field, the column
	// and the offending timezone string.
	//
	// What to do: put an IANA name time.LoadLocation accepts
	// ("Europe/Madrid", "UTC") in the tz= tag of the field the message names.
	ErrInvalidTimezone = errors.New("invalid column timezone")

	// ErrInvalidTag indicates a model struct carries tag tokens quark does
	// not read — a misspelled quark:"..." token, a db option other than
	// size/precision/scale, a pk value other than "true", or a foreign tag
	// key like column:/notnull:. Surfaced by Client.RegisterModel and
	// Client.Migrate so a typo breaks at startup naming every offending
	// field, instead of producing DDL that silently omits NOT NULL, UNIQUE
	// or whole columns (DX-8).
	//
	// Changes in v2.0.0: it also covers a db tag whose column name is a
	// bare Nucleus pkg/model directive (db:"pk", db:"readonly"…), which v1
	// only warns about, unless the field carries quark:"literal_column".
	// See DEP-2026-011.
	//
	// What to do: fix the token the message names on each field it lists;
	// the accepted tokens are the ones the modeling guide documents.
	ErrInvalidTag = errors.New("invalid struct tag")

	// ErrDialectNotSupported indicates that no dialect is registered for the
	// driver name passed to quark.New, or that the active dialect has no
	// implementation of what was asked (a LISTEN/NOTIFY listener outside
	// PostgreSQL).
	//
	// What to do: import the engine's driver module
	// (github.com/jcsvwinston/quark/drivers/<engine>), or pass
	// WithDialect(...) / call RegisterDialect for a custom one; for an
	// engine-specific feature, branch on the dialect name.
	ErrDialectNotSupported = errors.New("dialect not supported")

	// ErrConnection indicates a database connection error: sql.Open or the
	// initial ping failed for the primary or for a replica, the driver is not
	// imported, or NewWithDB received a nil *sql.DB. The wrapped message
	// carries the driver's reason.
	//
	// What to do: check the DSN and that the server accepts connections from
	// this host; when the message names a missing driver import, add it.
	ErrConnection = errors.New("database connection error")

	// ErrTimeout indicates that a query timed out: the context's deadline
	// passed, it was cancelled, or the driver reported a timeout. It is
	// joined (errors.Join) with the original error, so errors.Is matches both
	// it and context.DeadlineExceeded.
	//
	// What to do: give the context more time, or make the query cheaper — an
	// index on the filtered columns, a Limit; for a migration lock that
	// timed out see ErrLockTimeout.
	ErrTimeout = errors.New("query timeout: the context deadline or the driver's timeout elapsed before the statement finished; give the context more time or narrow the query")

	// ErrConstraintViolation indicates a database constraint violation —
	// unique, primary key, foreign key, NOT NULL or CHECK — joined with the
	// driver's error. Unique and primary-key violations are classified by
	// error code; the other kinds by the engine's message text.
	//
	// What to do: call IsUniqueViolation to tell a duplicate key apart and
	// answer it as a conflict; for the other kinds read the engine's message,
	// which names the constraint, and fix the row it rejects.
	ErrConstraintViolation = errors.New("constraint violation: the database rejected the write; use IsUniqueViolation for a duplicate key, otherwise the engine's message names the foreign-key, NOT NULL or CHECK rule")

	// ErrEventEmitFailed indicates that an EventBus.Publish call failed
	// AFTER the originating write was already persisted. The data is
	// committed; only the event emission failed (at-least-once delivery,
	// no outbox; see ADR-0013). Under an explicit transaction the emit
	// runs post-commit via Tx.OnCommit and the failure is logged with
	// the OTel-style event `quark.event.emit_failure` rather than
	// propagated (the commit already returned success).
	//
	// What to do: do NOT retry the write (that would double-write); retry
	// the emit, or rely on the subscriber being idempotent.
	ErrEventEmitFailed = errors.New("event emit failed after commit")

	// ErrListenerClosed indicates an operation was attempted on an
	// EventListener (PostgreSQL LISTEN/NOTIFY) after Close was called.
	// The dedicated connection has been returned to the pool. See ADR-0019.
	//
	// What to do: create a fresh listener with
	// ListenerFactory.CreateListener; a closed one cannot be reopened.
	ErrListenerClosed = quarkdriver.ErrListenerClosed

	// ErrNoSubscription indicates Receive was called on an EventListener
	// that has no active channel subscription. See ADR-0019.
	//
	// What to do: call Listen(channel) at least once before Receive.
	ErrNoSubscription = quarkdriver.ErrNoSubscription
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

	// Driver-reported unique/PK violations are classified by error CODE
	// before any message inspection below. The string matching that follows
	// is locale-dependent — PostgreSQL, MySQL and Oracle all translate their
	// messages when the server is configured for a non-English locale, and a
	// translated message matches none of these substrings — so a code match
	// is both more precise and the only one that holds outside an
	// English-language server. The message patterns stay as the fallback for
	// the constraint kinds not yet covered by code (foreign key, not null,
	// check): removing them would narrow what ErrConstraintViolation catches.
	if isUniqueViolation(err) {
		return errors.Join(ErrConstraintViolation, err)
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

// ErrTenantMismatch is returned, wrapped, by a query built inside a
// transaction whose context resolves a different tenant from the one the
// transaction was opened for. The transaction fixes the tenant (ADR-0025):
// under RowLevelSecurityNative the engine is already filtering by the tenant
// TenantRouter.Tx set on that connection, and under DatabasePerTenant the pool
// was chosen for it, so a query naming another tenant cannot be honoured
// without silently reading or writing across tenants. A context that resolves
// NO tenant inherits the transaction's instead.
//
// What to do: open the transaction for the tenant the query names, or run
// the query with a context that resolves the same tenant (or none, which
// inherits the transaction's).
var ErrTenantMismatch = errors.New("tenant mismatch inside transaction")

// ErrRLSNotEnforced is returned, wrapped, by a RowLevelSecurityNative router
// that cannot confirm the engine enforces row-level security on the table a
// query is about to touch: the table has row security disabled, has no
// policy, or the catalog could not be read. The router refuses to serve
// rather than return every tenant's rows over a database whose policies were
// never installed (A8 S8). quarktenant.ErrRLSNotEnforced is this value.
//
// What to do: install the policies — quarktenant.InstallRLSPolicies, or
// `quark tenant install-rls-policies --from-models` — and check them with
// quarktenant.VerifyRLSPolicies; set TenantConfig.SkipPolicyVerification
// only when the catalog cannot be read and the policies are known to exist.
var ErrRLSNotEnforced = errors.New("row-level security is not enforced")
