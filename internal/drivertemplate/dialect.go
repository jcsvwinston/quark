// Copyright 2026 jcsvwinston
// SPDX-License-Identifier: Apache-2.0

package drivertemplate

import (
	"fmt"
	"regexp"
	"strings"

	"github.com/jcsvwinston/quark/quarkdriver"
)

// Dialect writes the engine's SQL. Every statement Quark sends goes through
// it: the methods of quarkdriver.Dialect below, and the optional interfaces
// it implements in schema.go, which Quark finds by a type assertion.
//
// None of it comes from Quark's built-in SQLite dialect: a driver reaches a
// built-in only by importing package quark, and this one is written against
// quarkdriver alone.
type Dialect struct{}

var _ quarkdriver.Dialect = Dialect{}

// Name is the name the dialect is registered and found under. What the
// engine can do Quark asks through the methods and the optional interfaces;
// the few places where it still reads a built-in engine's name take their
// default path for any other name.
func (Dialect) Name() string { return Name }

// Placeholder is the bind marker of the index-th argument, from 1. SQLite
// takes "?" for every one; an engine with numbered markers writes "$1",
// "@p1" or ":1" here.
func (Dialect) Placeholder(int) string { return "?" }

func (d Dialect) Placeholders(n int) []string {
	out := make([]string, n)
	for i := range out {
		out[i] = d.Placeholder(i + 1)
	}
	return out
}

// Quote quotes an identifier, doubling the quote character inside it, so a
// name can never close its quotes and continue as SQL.
func (Dialect) Quote(identifier string) string {
	return `"` + strings.ReplaceAll(identifier, `"`, `""`) + `"`
}

// LimitOffset writes the row-limiting clause. SQLite has no OFFSET without a
// LIMIT; a negative LIMIT means no limit.
func (Dialect) LimitOffset(limit, offset int) string {
	switch {
	case limit > 0 && offset > 0:
		return fmt.Sprintf("LIMIT %d OFFSET %d", limit, offset)
	case limit > 0:
		return fmt.Sprintf("LIMIT %d", limit)
	case offset > 0:
		return fmt.Sprintf("LIMIT -1 OFFSET %d", offset)
	}
	return ""
}

// SupportsReturning and Returning: SQLite 3.35 and later return the
// generated key from the INSERT itself. An engine without RETURNING answers
// false, returns "" from Returning, and gives Quark the key through
// SupportsLastInsertID and LastInsertIDQuery.
func (Dialect) SupportsReturning() bool { return true }

func (d Dialect) Returning(columns ...string) string {
	if len(columns) == 0 {
		return ""
	}
	return "RETURNING " + d.quoteAll(columns)
}

func (Dialect) SupportsLastInsertID() bool { return true }

func (Dialect) LastInsertIDQuery(string, string) string { return "SELECT last_insert_rowid()" }

func (Dialect) CurrentTimestamp() string { return "CURRENT_TIMESTAMP" }

// SupportsTransactionalDDL reports whether a ROLLBACK undoes a CREATE, ALTER
// or DROP. It decides how ApplyPlan runs a plan: in one transaction when
// true, step by step with a checkpoint table when false. SQLite's DDL is
// transactional.
func (Dialect) SupportsTransactionalDDL() bool { return true }

// UpsertSQL is the conflict clause Quark appends to an INSERT for Upsert.
func (d Dialect) UpsertSQL(conflictCols, updateCols []string, _ int) string {
	if len(conflictCols) == 0 {
		return " ON CONFLICT DO NOTHING"
	}
	conflict := d.quoteAll(conflictCols)
	if len(updateCols) == 0 {
		return fmt.Sprintf(" ON CONFLICT (%s) DO NOTHING", conflict)
	}
	sets := make([]string, len(updateCols))
	for i, c := range updateCols {
		sets[i] = fmt.Sprintf("%s = excluded.%s", d.Quote(c), d.Quote(c))
	}
	return fmt.Sprintf(" ON CONFLICT (%s) DO UPDATE SET %s", conflict, strings.Join(sets, ", "))
}

// LockSuffix attaches a row lock to a SELECT. The zero options must write
// nothing. SQLite has no row locks, so any lock is refused with the
// contract's sentinel, which an application matches as
// quark.ErrUnsupportedFeature: the same value.
func (Dialect) LockSuffix(opts quarkdriver.LockOptions) (tableHint, suffix string, err error) {
	if opts.IsZero() {
		return "", "", nil
	}
	return "", "", fmt.Errorf("%w: %s has no row-level locks", quarkdriver.ErrUnsupportedFeature, Name)
}

// jsonPath is the grammar of a JSON path the dialect accepts: a dotted chain
// of identifiers. Quark hands JSONExtract the path as the caller wrote it,
// and its own validator is internal, so a dialect validates the path itself.
var jsonPath = regexp.MustCompile(`^[A-Za-z0-9_]+(\.[A-Za-z0-9_]+)*$`)

// JSONExtract writes the expression that reads a value out of a JSON column.
// The "?" in the fragment is not the engine's placeholder: it is a neutral
// marker that Quark replaces with Placeholder(n) for the argument's
// position. The path travels as that bound argument, never inside the SQL.
func (d Dialect) JSONExtract(column, path string) (string, []any, error) {
	if len(path) > 256 || !jsonPath.MatchString(path) {
		return "", nil, fmt.Errorf("%s: JSON path %q must be a dotted chain of identifiers", Name, path)
	}
	return fmt.Sprintf("JSON_EXTRACT(%s, ?)", d.Quote(column)), []any{"$." + path}, nil
}

// BuildRoutineQuery and BuildProcedureCall write calls to a table-valued
// function and to a procedure. SQLite has neither as SQL objects; these are
// the shapes an engine with them would accept, and the kit checks them only
// when the driver's test hands it a routine to call.
func (d Dialect) BuildRoutineQuery(routine string, argCount int) string {
	return fmt.Sprintf("SELECT * FROM %s(%s)", d.Quote(routine), strings.Join(d.Placeholders(argCount), ", "))
}

func (d Dialect) BuildProcedureCall(procedure string, argCount int) string {
	return fmt.Sprintf("SELECT %s(%s)", d.Quote(procedure), strings.Join(d.Placeholders(argCount), ", "))
}

func (d Dialect) AlterTableAddColumn(table, column, dataType string) string {
	return fmt.Sprintf("ALTER TABLE %s ADD COLUMN %s %s", d.Quote(table), d.Quote(column), dataType)
}

func (d Dialect) AlterTableDropColumn(table, column string) string {
	return fmt.Sprintf("ALTER TABLE %s DROP COLUMN %s", d.Quote(table), d.Quote(column))
}

// AlterTableAlterColumn: SQLite has no ALTER COLUMN, and ApplyPlan never asks
// for it here, because the dialect rebuilds tables instead (TableRebuilder,
// in schema.go). The standard statement fails at the engine rather than
// change something else.
func (d Dialect) AlterTableAlterColumn(table, column, newDataType string) string {
	return fmt.Sprintf("ALTER TABLE %s ALTER COLUMN %s TYPE %s", d.Quote(table), d.Quote(column), newDataType)
}

func (d Dialect) RenameColumn(table, oldName, newName string) string {
	return fmt.Sprintf("ALTER TABLE %s RENAME COLUMN %s TO %s", d.Quote(table), d.Quote(oldName), d.Quote(newName))
}

func (d Dialect) RenameTable(oldName, newName string) string {
	return fmt.Sprintf("ALTER TABLE %s RENAME TO %s", d.Quote(oldName), d.Quote(newName))
}

func (d Dialect) quoteAll(columns []string) string {
	quoted := make([]string, len(columns))
	for i, c := range columns {
		quoted[i] = d.Quote(c)
	}
	return strings.Join(quoted, ", ")
}
