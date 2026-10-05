// Copyright 2026 jcsvwinston
// SPDX-License-Identifier: Apache-2.0

package drivertemplate

import (
	"context"

	"github.com/jcsvwinston/quark/quarkdriver"
)

// The optional interfaces. Quark asks a dialect about its schema through
// interfaces it finds by a type assertion; a dialect that does not implement
// one gets a documented default — a portable spelling, or
// ErrUnsupportedFeature where there is none. These three are the ones SQLite
// needs: the defaults it can take (portable column types, CREATE … IF NOT
// EXISTS, the SQL-standard savepoint statements) it takes.
var (
	_ quarkdriver.AutoIncrementer    = Dialect{}
	_ quarkdriver.TableRebuilder     = Dialect{}
	_ quarkdriver.SchemaIntrospector = Dialect{}
)

// AutoIncrementColumn is how the engine declares a single-column integer key
// it numbers itself: what Migrate writes for `ID int64` with pk:"true". Only
// INTEGER PRIMARY KEY aliases SQLite's rowid, and the catalog then reports the
// column as INTEGER. The default — the SQL standard's identity column — is
// one SQLite rejects.
func (Dialect) AutoIncrementColumn() (definition, dataType string) {
	return "INTEGER PRIMARY KEY AUTOINCREMENT", "INTEGER"
}

// RebuildsTables: SQLite has no ALTER COLUMN and no ADD or DROP CONSTRAINT,
// so ApplyPlan changes a column, a foreign key or a check by rebuilding the
// table. The rebuild reads SQLite's own catalog, so only an engine that keeps
// that catalog answers true; any other implements quarkdriver.ColumnAlterer
// and quarkdriver.ObjectDropper, or takes their defaults.
func (Dialect) RebuildsTables() bool { return true }

// IntrospectSchema reads the tables, their columns, indexes and foreign keys
// from the engine's catalog, in the schema model quarkdriver declares.
// PlanMigration diffs models against it and Sync reads a table's columns
// through it; without it both return ErrUnsupportedFeature. Indexes and
// foreign keys are not optional: a plan diffed against a schema that reports
// none proposes, on every run, the index a model declares. Checks are left
// out because SQLite keeps no catalog of them, and the diff skips checks when
// the live side reports none.
func (Dialect) IntrospectSchema(ctx context.Context, exec quarkdriver.Executor) (quarkdriver.Schema, error) {
	names, err := tableNames(ctx, exec)
	if err != nil {
		return quarkdriver.Schema{}, err
	}
	var s quarkdriver.Schema
	for _, name := range names {
		t := quarkdriver.Table{Name: name}
		if t.Columns, err = columns(ctx, exec, name); err != nil {
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

// tableNames lists the user's tables, leaving out SQLite's own.
func tableNames(ctx context.Context, exec quarkdriver.Executor) ([]string, error) {
	rows, err := exec.QueryContext(ctx,
		`SELECT name FROM sqlite_master WHERE type = 'table' AND name NOT LIKE 'sqlite_%' ORDER BY name`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var names []string
	for rows.Next() {
		var n string
		if err := rows.Scan(&n); err != nil {
			return nil, err
		}
		names = append(names, n)
	}
	return names, rows.Err()
}

// columns reads a table's columns in their declared order.
func columns(ctx context.Context, exec quarkdriver.Executor, table string) ([]quarkdriver.Column, error) {
	rows, err := exec.QueryContext(ctx,
		`SELECT name, type, "notnull", dflt_value, pk FROM pragma_table_info(?) ORDER BY cid`, table)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []quarkdriver.Column
	for rows.Next() {
		var c quarkdriver.Column
		var notNull, pk int
		if err := rows.Scan(&c.Name, &c.Type, &notNull, &c.Default, &pk); err != nil {
			return nil, err
		}
		c.Nullable, c.PrimaryKey = notNull == 0 && pk == 0, pk > 0
		out = append(out, c)
	}
	return out, rows.Err()
}

// indexes reads a table's indexes, except the one behind its primary key.
func indexes(ctx context.Context, exec quarkdriver.Executor, table string) ([]quarkdriver.Index, error) {
	rows, err := exec.QueryContext(ctx,
		`SELECT name, "unique" FROM pragma_index_list(?) WHERE origin <> 'pk' ORDER BY name`, table)
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
		if out[i].Columns, err = indexColumns(ctx, exec, out[i].Name); err != nil {
			return nil, err
		}
	}
	return out, nil
}

// indexColumns reads an index's columns in key order.
func indexColumns(ctx context.Context, exec quarkdriver.Executor, index string) ([]string, error) {
	rows, err := exec.QueryContext(ctx, `SELECT name FROM pragma_index_info(?) ORDER BY seqno`, index)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var c string
		if err := rows.Scan(&c); err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

// foreignKeys reads a table's foreign keys. SQLite's catalog keeps no
// constraint names, so Name stays empty and Quark's diff matches them by
// their columns.
func foreignKeys(ctx context.Context, exec quarkdriver.Executor, table string) ([]quarkdriver.ForeignKey, error) {
	rows, err := exec.QueryContext(ctx,
		`SELECT id, "table", "from", "to", on_update, on_delete FROM pragma_foreign_key_list(?) ORDER BY id, seq`, table)
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
