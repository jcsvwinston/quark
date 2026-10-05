// Copyright 2026 jcsvwinston
// SPDX-License-Identifier: Apache-2.0

package quarkdriver

import (
	"errors"
	"sync"
)

// This file is the dialect contract: the interface a dialect implements, the
// types its methods name, the errors it returns and the registry Quark finds
// it in. Per ADR-0026 it lives here, in the leaf, so a driver module writes
// and registers a dialect without importing package quark. Package quark
// keeps every name — quark.Dialect, quark.LockOptions, quark.RegisterDialect
// — as an alias of the same type, or a function that calls the one here, so
// a dialect written against either set of names is the same dialect.

// Dialect defines the interface for database-specific SQL generation.
// Each supported database (PostgreSQL, MySQL, SQLite, etc.) implements this interface.
//
// Package quark names the same type quark.Dialect, and its six built-in
// dialects (quark.PostgreSQL(), quark.SQLite(), …) implement it.
type Dialect interface {
	// Name returns the dialect name (e.g., "postgres", "mysql", "sqlite").
	Name() string

	// Placeholder returns the placeholder for the given parameter index.
	// PostgreSQL: $1, $2, etc.
	// MySQL/SQLite: ?
	// MSSQL: @p1, @p2, etc.
	// Oracle: :1, :2, etc.
	Placeholder(index int) string

	// Quote returns a quoted identifier (table/column name).
	// PostgreSQL: "identifier"
	// MySQL: `identifier`
	// MSSQL: [identifier]
	// SQLite/Oracle: "identifier"
	Quote(identifier string) string

	// Placeholders returns a slice of placeholders for n parameters.
	Placeholders(n int) []string

	// LimitOffset returns the LIMIT/OFFSET clause for the given parameters.
	LimitOffset(limit, offset int) string

	// SupportsReturning indicates if the dialect supports RETURNING clause.
	SupportsReturning() bool

	// Returning returns the RETURNING clause for the given columns.
	// Returns empty string if not supported.
	Returning(columns ...string) string

	// SupportsLastInsertID indicates if the dialect supports LastInsertId().
	SupportsLastInsertID() bool

	// LastInsertIDQuery returns the query to get the last insert ID.
	// Used for dialects that don't support RETURNING.
	LastInsertIDQuery(table, pkColumn string) string

	// CurrentTimestamp returns the SQL function for current timestamp.
	CurrentTimestamp() string

	// BuildRoutineQuery returns the SQL for a table-valued function or routine returning rows.
	// E.g., Postgres: SELECT * FROM func($1, $2)
	BuildRoutineQuery(routine string, argCount int) string

	// BuildProcedureCall returns the SQL for calling a procedure (pure logic / OUT params).
	// E.g., MySQL: CALL proc(?, ?)
	BuildProcedureCall(procedure string, argCount int) string

	// JSONExtract returns the SQL expression to extract a value from a JSON column,
	// the bind args required by that expression, or an error if the path is
	// malformed.
	//
	// The returned SQL fragment uses literal '?' as a neutral bind marker; the
	// caller (typically buildWhereClause) substitutes each '?' for the dialect's
	// placeholder syntax (`$N`, `?`, `@pN`, `:N`) at the appropriate arg index.
	//
	// The path arrives as the caller wrote it: the dialect validates it
	// before it reaches the SQL. The built-in dialects call Quark's
	// guard.ValidateJSONPath, which accepts a dotted chain of identifiers
	// ([A-Za-z0-9_.]) and nothing else; a dialect outside the repository
	// cannot import that internal package and validates the path itself.
	// Every dialect that can bind the path does so — never interpolating
	// it — which closes the SQL-injection vector that existed while the path
	// was concatenated with fmt.Sprintf. Oracle is the one exception: its
	// JSON_VALUE rejects a bound path (ORA-40454), so the validated path is
	// inlined as a literal, made safe by the same [A-Za-z0-9_.] restriction
	// that makes Quote(validatedIdentifier) safe.
	//
	// Example outputs (with column "data" and path "user.name"):
	//   Postgres: jsonb_extract_path_text(("data")::jsonb, ?, ?) / args=["user","name"]
	//   MySQL:    JSON_EXTRACT(`data`, ?) / args=["$.user.name"]
	//   SQLite:   JSON_EXTRACT("data", ?) / args=["$.user.name"]
	//   MSSQL:    JSON_VALUE([data], ?) / args=["$.user.name"]
	//   Oracle:   JSON_VALUE("DATA", '$.user.name') / args=nil (path inlined, see above)
	JSONExtract(column, path string) (sql string, args []any, err error)

	// AlterTableAddColumn returns SQL to add a column to a table.
	// E.g., PostgreSQL: ALTER TABLE "users" ADD COLUMN "email" VARCHAR(255)
	AlterTableAddColumn(table, column, dataType string) string

	// AlterTableDropColumn returns SQL to drop a column from a table.
	// E.g., PostgreSQL: ALTER TABLE "users" DROP COLUMN "email"
	AlterTableDropColumn(table, column string) string

	// AlterTableAlterColumn returns SQL to alter a column's type.
	// E.g., PostgreSQL: ALTER TABLE "users" ALTER COLUMN "email" TYPE VARCHAR(255)
	AlterTableAlterColumn(table, column, newDataType string) string

	// RenameColumn returns SQL to rename a column.
	// E.g., PostgreSQL: ALTER TABLE "users" RENAME COLUMN "old_name" TO "new_name"
	RenameColumn(table, oldName, newName string) string

	// RenameTable returns SQL to rename a table.
	// E.g., PostgreSQL: ALTER TABLE "users" RENAME TO "accounts"
	RenameTable(oldName, newName string) string

	// SupportsTransactionalDDL indicates if the dialect supports DDL in
	// transactions: whether a ROLLBACK undoes a CREATE, ALTER or DROP run
	// inside the transaction. ApplyPlan wraps a plan in one transaction when
	// it is true and takes its resumable, checkpointed path when it is false;
	// Sync wraps its column changes in a transaction when it is true.
	SupportsTransactionalDDL() bool

	// LockSuffix returns the SQL fragments needed to attach a pessimistic
	// lock to a SELECT.
	//
	//   - tableHint is appended after the FROM clause's table name. MSSQL
	//     uses this slot for `WITH (UPDLOCK, ROWLOCK)`-style hints; the
	//     row-level locking dialects return "" here.
	//   - suffix is appended at the very end of the SELECT (after ORDER BY
	//     and LIMIT/OFFSET) — `FOR UPDATE [SKIP LOCKED|NOWAIT]` for the
	//     PG/MySQL/Oracle/MariaDB family.
	//
	// Returning ErrUnsupportedFeature signals "this dialect doesn't speak
	// pessimistic locks at this level" — SQLite is the canonical case.
	// LockOptions.IsZero() input must always return ("", "", nil).
	LockSuffix(opts LockOptions) (tableHint, suffix string, err error)

	// UpsertSQL returns the dialect-specific upsert (INSERT … ON CONFLICT … DO UPDATE)
	// fragment that is appended after the VALUES clause.
	// conflictCols: columns that define the conflict target (e.g. primary key or unique index).
	// updateCols:   columns to update on conflict; if empty defaults to all non-conflict columns.
	// argOffset:    current placeholder index (1-based) so positional dialects stay in sync.
	// Returns the SQL fragment and the additional argument list (for the SET clause values).
	UpsertSQL(conflictCols, updateCols []string, argOffset int) string
}

// LockMode is the kind of pessimistic lock requested for a SELECT.
// Package quark names the same type quark.LockMode.
type LockMode int

const (
	// LockNone means no lock clause is emitted (the default).
	LockNone LockMode = iota
	// LockForUpdate locks the rows for update; other transactions cannot
	// read-with-lock or write the matching rows until the current
	// transaction commits or rolls back. Most engines support it.
	LockForUpdate
	// LockForShare takes a shared read lock — other transactions can also
	// read-with-lock but not write. Supported on PG / MySQL 8+ / MariaDB;
	// not on SQLite. MSSQL approximates with HOLDLOCK.
	LockForShare
)

// LockOptions describes the pessimistic-lock behaviour for a SELECT.
// The zero value (LockMode == LockNone) emits nothing — callers opt in
// via ForUpdate / ForShare on quark's Query[T]. Package quark names the
// same type quark.LockOptions.
type LockOptions struct {
	Mode       LockMode
	SkipLocked bool
	NoWait     bool
}

// IsZero reports whether the options request no lock at all. Used by
// dialects to short-circuit their LockSuffix implementations.
func (o LockOptions) IsZero() bool {
	return o.Mode == LockNone && !o.SkipLocked && !o.NoWait
}

// SavepointDialect is an optional [Dialect] extension for engines whose
// savepoint statements diverge from the ANSI form (SAVEPOINT /
// ROLLBACK TO SAVEPOINT / RELEASE SAVEPOINT). A Dialect that does not
// implement it gets the ANSI statements, which are correct for PostgreSQL,
// MySQL, MariaDB and SQLite. The transaction layer (quark's Tx.Savepoint,
// Tx.RollbackTo and Tx.ReleaseSavepoint) consults this interface via a type
// assertion, so adding it to a dialect is non-breaking for existing custom
// dialects registered through [RegisterDialect].
//
// name arrives already validated by Quark's identifier guard; the dialect
// decides whether to quote it. A ReleaseSavepointStmt returning "" means the
// engine has no explicit savepoint-release statement (the savepoint is
// released at COMMIT) — the Tx layer then skips that Exec. Found by the
// post-v1.0 bug-bash (BB-9, phase F8): SQL Server uses SAVE TRANSACTION /
// ROLLBACK TRANSACTION and has no release; Oracle has SAVEPOINT and
// ROLLBACK TO SAVEPOINT but no RELEASE SAVEPOINT.
type SavepointDialect interface {
	SavepointStmt(name string) string
	RollbackToSavepointStmt(name string) string
	ReleaseSavepointStmt(name string) string
}

// ColumnTypeMapper is the optional Dialect interface for translating a
// neutral/foreign column-type string into the dialect's native form
// before it reaches DDL. Kept as a stand-alone interface (same pattern
// as SchemaIntrospector / MigrationLocker) so custom dialects don't have
// to grow the method to keep compiling — dialects that don't implement
// it leave the type string untouched.
//
// It exists because a hand-built migration plan (quark.Plan) can carry a
// generic type like "TEXT" (every engine except Oracle accepts it);
// Oracle's CLOB is the native equivalent. MapColumnType must be idempotent
// — a type that is already dialect-native (e.g. "NUMBER(19)") passes
// through unchanged.
type ColumnTypeMapper interface {
	MapColumnType(t string) string
}

// ErrUnsupportedFeature indicates that a feature is not supported by the
// active database dialect. Returned by builder methods (e.g. ForUpdate
// on SQLite) so callers can branch by dialect or fall back to a different
// strategy. The error message includes the dialect name and the feature
// being requested.
//
// A dialect returns it, wrapped with %w, for what its engine cannot do:
// LockSuffix on an engine without row locks, AcquireMigrationLock on one
// without a distributed lock. Package quark's quark.ErrUnsupportedFeature
// holds this same value, so errors.Is matches under either name.
var ErrUnsupportedFeature = errors.New("feature not supported by dialect")

// ErrLockTimeout is returned by AcquireMigrationLock when the lock
// cannot be acquired within the given timeout. Distinct from
// ErrUnsupportedFeature (which means the dialect doesn't model
// distributed locks at all). Distinct from generic driver errors.
// Package quark's quark.ErrLockTimeout holds this same value.
var ErrLockTimeout = errors.New("migration lock acquisition timed out")

// dialectMu guards dialects. A registration can happen after start-up — a
// test registering its own dialect, a module that registers late — while
// another goroutine resolves a dialect (quark.New calls quark.DetectDialect,
// which calls LookupDialect). Without the lock that was a data race on a
// plain map (A11 bench, control DRV-01), and a concurrent write could end
// the process with "concurrent map writes".
var (
	dialectMu sync.RWMutex
	dialects  = map[string]Dialect{}
)

// RegisterDialect records d under name, so that quark.New(name, dsn) and
// quark.DetectDialect(name) resolve it. name is the database/sql driver
// name a client is opened with — a driver module registers both under the
// same name:
//
//	func init() {
//		sql.Register("extsql", &myDriver{})
//		quarkdriver.RegisterDialect("extsql", myDialect{})
//	}
//
// A pool opened under another driver name takes the dialect explicitly, with
// quark.WithDialect.
//
// Registering a name again replaces the dialect registered under it. A name
// registered here is consulted before the built-in names, so a module can
// replace a built-in dialect for a driver name. RegisterDialect is safe to
// call concurrently with itself and with [LookupDialect]. quark.RegisterDialect
// calls it: the two names write the same registry.
func RegisterDialect(name string, d Dialect) {
	dialectMu.Lock()
	defer dialectMu.Unlock()
	dialects[name] = d
}

// LookupDialect returns the dialect registered under name with
// [RegisterDialect], and whether there is one. It knows only registered
// dialects: the built-in names ("postgres", "pgx", "sqlite", …) are resolved
// by quark.DetectDialect, which asks LookupDialect first.
func LookupDialect(name string) (Dialect, bool) {
	dialectMu.RLock()
	defer dialectMu.RUnlock()
	d, ok := dialects[name]
	return d, ok
}
