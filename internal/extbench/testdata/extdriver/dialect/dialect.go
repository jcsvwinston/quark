// Copyright 2026 jcsvwinston
// SPDX-License-Identifier: Apache-2.0

// Package dialect is the half of the fixture driver that writes SQL. It is
// written against quarkdriver alone — the Dialect interface, LockOptions,
// ErrUnsupportedFeature, SchemaIntrospector and the schema model all live
// there since ADR-0026 — so the bench can ask the build graph whether a
// dialect from outside the repository needs package quark (control DRV-02).
//
// The engine is SQLite, so the SQL is SQLite's; none of it comes from
// Quark's built-in SQLite dialect, which a driver can only reach by
// importing package quark. The arm that reuses SQLite's own methods under
// another name is the bench's in-process extlite (DRV-04).
package dialect

import (
	"context"
	"fmt"
	"regexp"
	"strings"

	"github.com/jcsvwinston/quark/quarkdriver"
)

// Dialect writes SQLite's SQL under the engine name it is given.
type Dialect struct{ name string }

// New returns the dialect, answering name from Name().
func New(name string) Dialect { return Dialect{name: name} }

var (
	_ quarkdriver.Dialect            = Dialect{}
	_ quarkdriver.SchemaIntrospector = Dialect{}
	_ quarkdriver.AutoIncrementer    = Dialect{}
	_ quarkdriver.TableRebuilder     = Dialect{}
)

func (d Dialect) Name() string             { return d.name }
func (Dialect) Placeholder(int) string     { return "?" }
func (Dialect) SupportsReturning() bool    { return true }
func (Dialect) SupportsLastInsertID() bool { return true }
func (Dialect) CurrentTimestamp() string   { return "CURRENT_TIMESTAMP" }

// SupportsTransactionalDDL: a ROLLBACK undoes SQLite's DDL.
func (Dialect) SupportsTransactionalDDL() bool { return true }

func (Dialect) LastInsertIDQuery(string, string) string { return "SELECT last_insert_rowid()" }

func (Dialect) Quote(identifier string) string {
	return `"` + strings.ReplaceAll(identifier, `"`, `""`) + `"`
}

func (d Dialect) Placeholders(n int) []string {
	out := make([]string, n)
	for i := range out {
		out[i] = d.Placeholder(i + 1)
	}
	return out
}

func (Dialect) LimitOffset(limit, offset int) string {
	switch {
	case limit > 0 && offset > 0:
		return fmt.Sprintf("LIMIT %d OFFSET %d", limit, offset)
	case limit > 0:
		return fmt.Sprintf("LIMIT %d", limit)
	case offset > 0:
		// SQLite has no bare OFFSET; a negative LIMIT means no limit.
		return fmt.Sprintf("LIMIT -1 OFFSET %d", offset)
	}
	return ""
}

func (d Dialect) quoteAll(columns []string) string {
	quoted := make([]string, len(columns))
	for i, c := range columns {
		quoted[i] = d.Quote(c)
	}
	return strings.Join(quoted, ", ")
}

func (d Dialect) Returning(columns ...string) string {
	if len(columns) == 0 {
		return ""
	}
	return "RETURNING " + d.quoteAll(columns)
}

func (d Dialect) BuildRoutineQuery(routine string, argCount int) string {
	return fmt.Sprintf("SELECT * FROM %s(%s)", d.Quote(routine), strings.Join(d.Placeholders(argCount), ", "))
}

func (d Dialect) BuildProcedureCall(procedure string, argCount int) string {
	return fmt.Sprintf("SELECT %s(%s)", d.Quote(procedure), strings.Join(d.Placeholders(argCount), ", "))
}

// jsonPath is the grammar the dialect binds: a dotted chain of identifiers.
// Quark hands JSONExtract the path as the caller wrote it, and its own
// validator is internal, so a dialect from outside validates the path itself.
var jsonPath = regexp.MustCompile(`^[A-Za-z0-9_]+(\.[A-Za-z0-9_]+)*$`)

func (d Dialect) JSONExtract(column, path string) (string, []any, error) {
	if len(path) > 256 || !jsonPath.MatchString(path) {
		return "", nil, fmt.Errorf("%s: JSON path %q must be a dotted identifier chain", d.name, path)
	}
	return fmt.Sprintf("JSON_EXTRACT(%s, ?)", d.Quote(column)), []any{"$." + path}, nil
}

func (d Dialect) AlterTableAddColumn(table, column, dataType string) string {
	return fmt.Sprintf("ALTER TABLE %s ADD COLUMN %s %s", d.Quote(table), d.Quote(column), dataType)
}

func (d Dialect) AlterTableDropColumn(table, column string) string {
	return fmt.Sprintf("ALTER TABLE %s DROP COLUMN %s", d.Quote(table), d.Quote(column))
}

// AlterTableAlterColumn: SQLite has no ALTER COLUMN. The statement fails at
// the engine instead of changing something else.
func (d Dialect) AlterTableAlterColumn(table, column, newDataType string) string {
	return fmt.Sprintf("ALTER TABLE %s ALTER COLUMN %s TYPE %s", d.Quote(table), d.Quote(column), newDataType)
}

func (d Dialect) RenameColumn(table, oldName, newName string) string {
	return fmt.Sprintf("ALTER TABLE %s RENAME COLUMN %s TO %s", d.Quote(table), d.Quote(oldName), d.Quote(newName))
}

func (d Dialect) RenameTable(oldName, newName string) string {
	return fmt.Sprintf("ALTER TABLE %s RENAME TO %s", d.Quote(oldName), d.Quote(newName))
}

// LockSuffix: SQLite has no row locks. The zero options must answer nothing,
// and any lock is refused with the contract's sentinel, which an application
// matches as quark.ErrUnsupportedFeature — the same value.
func (d Dialect) LockSuffix(opts quarkdriver.LockOptions) (string, string, error) {
	if opts.IsZero() {
		return "", "", nil
	}
	return "", "", fmt.Errorf("%w: %s has no row-level locks", quarkdriver.ErrUnsupportedFeature, d.name)
}

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

// AutoIncrementColumn: only INTEGER PRIMARY KEY aliases SQLite's rowid, and
// the catalog reports that column as INTEGER. Without it Quark writes the
// SQL standard's identity column, which SQLite rejects.
func (Dialect) AutoIncrementColumn() (definition, dataType string) {
	return "INTEGER PRIMARY KEY AUTOINCREMENT", "INTEGER"
}

// RebuildsTables: SQLite has no ALTER COLUMN and no ADD or DROP CONSTRAINT,
// so ApplyPlan changes a column, a foreign key or a check by rebuilding the
// table — which reads SQLite's own catalog, and this engine keeps it.
func (Dialect) RebuildsTables() bool { return true }

// IntrospectSchema reads the tables, their columns, indexes and foreign keys
// from SQLite's catalog, in the schema model quarkdriver declares. It reads
// no checks: SQLite keeps no catalog of them, and Quark's diff skips checks
// when the live side reports none. Indexes and foreign keys it must read —
// without them PlanMigration proposes, on every run, the index a model
// declares (the dialect kit's SchemaIntrospector check).
func (d Dialect) IntrospectSchema(ctx context.Context, exec quarkdriver.Executor) (quarkdriver.Schema, error) {
	rows, err := exec.QueryContext(ctx,
		`SELECT name FROM sqlite_master WHERE type = 'table' AND name NOT LIKE 'sqlite_%' ORDER BY name`)
	if err != nil {
		return quarkdriver.Schema{}, err
	}
	var names []string
	for rows.Next() {
		var n string
		if err := rows.Scan(&n); err != nil {
			rows.Close()
			return quarkdriver.Schema{}, err
		}
		names = append(names, n)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return quarkdriver.Schema{}, err
	}

	var s quarkdriver.Schema
	for _, name := range names {
		t := quarkdriver.Table{Name: name}
		cols, err := exec.QueryContext(ctx,
			`SELECT name, type, "notnull", dflt_value, pk FROM pragma_table_info(?) ORDER BY cid`, name)
		if err != nil {
			return quarkdriver.Schema{}, err
		}
		for cols.Next() {
			var c quarkdriver.Column
			var notNull, pk int
			var def *string
			if err := cols.Scan(&c.Name, &c.Type, &notNull, &def, &pk); err != nil {
				cols.Close()
				return quarkdriver.Schema{}, err
			}
			c.Nullable, c.Default, c.PrimaryKey = notNull == 0 && pk == 0, def, pk > 0
			t.Columns = append(t.Columns, c)
		}
		cols.Close()
		if err := cols.Err(); err != nil {
			return quarkdriver.Schema{}, err
		}
		if t.Indexes, err = indexes(ctx, exec, name); err != nil {
			return quarkdriver.Schema{}, err
		}
		if t.ForeignKeys, err = foreignKeys(ctx, exec, name); err != nil {
			return quarkdriver.Schema{}, err
		}
		s.Tables = append(s.Tables, t)
	}
	return s, nil
}

// indexes reads a table's indexes but the one backing its primary key.
func indexes(ctx context.Context, exec quarkdriver.Executor, table string) ([]quarkdriver.Index, error) {
	rows, err := exec.QueryContext(ctx, `SELECT name, "unique" FROM pragma_index_list(?) WHERE origin <> 'pk' ORDER BY name`, table)
	if err != nil {
		return nil, err
	}
	var out []quarkdriver.Index
	for rows.Next() {
		var ix quarkdriver.Index
		var unique int
		if err := rows.Scan(&ix.Name, &unique); err != nil {
			rows.Close()
			return nil, err
		}
		ix.Unique = unique == 1
		out = append(out, ix)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}
	for i := range out {
		cols, err := exec.QueryContext(ctx, `SELECT name FROM pragma_index_info(?) ORDER BY seqno`, out[i].Name)
		if err != nil {
			return nil, err
		}
		for cols.Next() {
			var c string
			if err := cols.Scan(&c); err != nil {
				cols.Close()
				return nil, err
			}
			out[i].Columns = append(out[i].Columns, c)
		}
		cols.Close()
		if err := cols.Err(); err != nil {
			return nil, err
		}
	}
	return out, nil
}

// foreignKeys reads a table's foreign keys. SQLite's catalog keeps no
// constraint names, so Name is empty and Quark's diff matches them by their
// columns.
func foreignKeys(ctx context.Context, exec quarkdriver.Executor, table string) ([]quarkdriver.ForeignKey, error) {
	rows, err := exec.QueryContext(ctx, `SELECT id, "table", "from", "to", on_update, on_delete FROM pragma_foreign_key_list(?) ORDER BY id, seq`, table)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []quarkdriver.ForeignKey
	last := -1
	for rows.Next() {
		var id int
		var ref, from, to, onUpdate, onDelete string
		if err := rows.Scan(&id, &ref, &from, &to, &onUpdate, &onDelete); err != nil {
			return nil, err
		}
		if id != last {
			out = append(out, quarkdriver.ForeignKey{RefTable: ref, OnUpdate: onUpdate, OnDelete: onDelete})
			last = id
		}
		fk := &out[len(out)-1]
		fk.Columns = append(fk.Columns, from)
		fk.RefColumns = append(fk.RefColumns, to)
	}
	return out, rows.Err()
}
