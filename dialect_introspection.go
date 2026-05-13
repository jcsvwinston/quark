// Copyright 2026 jcsvwinston
// SPDX-License-Identifier: Apache-2.0

package quark

import (
	"context"
	"database/sql"
	"fmt"
)

// --- SQLite ------------------------------------------------------------------

// IntrospectSchema reads the SQLite schema using `sqlite_master` for
// the table list and `PRAGMA table_info(<table>)` for the column
// metadata of each table. This avoids parsing the CREATE TABLE DDL,
// which would be brittle.
//
// SQLite caveats handled here:
//   - System tables (`sqlite_*`) and quark internal tables
//     (`quark_*`) are filtered out. The diff layer doesn't need to
//     reason about them.
//   - SQLite's PRAGMA returns columns in declaration order. We preserve
//     that order (Tables is sorted alphabetically; Columns aren't
//     re-sorted within a table).
//   - The `dflt_value` column from PRAGMA table_info comes back as a
//     literal SQL fragment (`'draft'`, `0`, `CURRENT_TIMESTAMP`); we
//     pass it through unchanged in `Column.Default`.
func (d *SQLiteDialect) IntrospectSchema(ctx context.Context, exec Executor) (Schema, error) {
	tableNames, err := sqliteListTables(ctx, exec)
	if err != nil {
		return Schema{}, fmt.Errorf("sqlite introspect: list tables: %w", err)
	}
	tables := make([]Table, 0, len(tableNames))
	for _, name := range tableNames {
		cols, err := sqliteListColumns(ctx, exec, name)
		if err != nil {
			return Schema{}, fmt.Errorf("sqlite introspect: list columns for %q: %w", name, err)
		}
		tables = append(tables, Table{Name: name, Columns: cols})
	}
	return Schema{Tables: tables}, nil
}

func sqliteListTables(ctx context.Context, exec Executor) ([]string, error) {
	rows, err := exec.QueryContext(ctx,
		`SELECT name FROM sqlite_master
		 WHERE type = 'table'
		   AND name NOT LIKE 'sqlite_%'
		   AND name NOT LIKE 'quark_%'
		 ORDER BY name`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			return nil, err
		}
		out = append(out, name)
	}
	return out, rows.Err()
}

func sqliteListColumns(ctx context.Context, exec Executor, table string) ([]Column, error) {
	// PRAGMA table_info doesn't accept parameterised arguments — the
	// table name is interpolated into the SQL surface. We validate via
	// SQLGuard's identifier rules before we let it through;
	// sqliteListTables already only returns names from sqlite_master
	// (the schema itself is trusted), but the defence-in-depth is
	// cheap.
	//
	// Quoting note: `fmt.Sprintf("%q", …)` would apply Go-style string
	// quoting (with `\"`-escapes for quote chars), which is NOT the
	// same as SQLite's identifier quoting (doubled `""` for quote
	// escape). They coincide for ASCII identifiers without special
	// chars — and `ValidateIdentifier` already rejects anything that
	// could trigger a divergence — but we use the SQL-standard form
	// `"<name>"` (which SQLite accepts as an identifier quote)
	// explicitly so the quoting intent is clear at the call site.
	if err := NewSQLGuard().ValidateIdentifier(table); err != nil {
		return nil, fmt.Errorf("sqlite introspect: bad table name %q: %w", table, err)
	}
	q := fmt.Sprintf(`PRAGMA table_info("%s")`, table)
	rows, err := exec.QueryContext(ctx, q)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Column
	for rows.Next() {
		// PRAGMA table_info columns:
		//   cid INTEGER, name TEXT, type TEXT, notnull INTEGER,
		//   dflt_value (any), pk INTEGER
		var (
			cid     int
			name    string
			typ     string
			notnull int
			dflt    sql.NullString
			pk      int
		)
		if err := rows.Scan(&cid, &name, &typ, &notnull, &dflt, &pk); err != nil {
			return nil, err
		}
		col := Column{
			Name:     name,
			Type:     typ,
			Nullable: notnull == 0,
		}
		if dflt.Valid {
			s := dflt.String
			col.Default = &s
		}
		out = append(out, col)
	}
	return out, rows.Err()
}

// --- PostgreSQL --------------------------------------------------------------

// IntrospectSchema reads the PG schema by querying `information_schema`
// (more portable than `pg_catalog` and sufficient for the column-level
// surface F3-2 needs). The `current_schema()` (typically `public`)
// scopes the lookup so multi-schema setups don't drag in unrelated
// tables.
//
// PG caveats handled here:
//   - The data type returned by `data_type` is the SQL-standard form
//     (`integer`, `bigint`, `character varying`). For native
//     parameter-bearing types (`varchar(255)`, `numeric(10,2)`) we
//     reassemble the precision/scale/length from the adjacent columns
//     so the round-trip vs Go-side schema is comparable.
//   - The `column_default` is preserved as-is — including PG's
//     `nextval('seq')` wrappers around SERIAL/IDENTITY columns. The
//     diff layer is responsible for recognising those.
func (d *PostgresDialect) IntrospectSchema(ctx context.Context, exec Executor) (Schema, error) {
	tableNames, err := pgListTables(ctx, exec)
	if err != nil {
		return Schema{}, fmt.Errorf("pg introspect: list tables: %w", err)
	}
	tables := make([]Table, 0, len(tableNames))
	for _, name := range tableNames {
		cols, err := pgListColumns(ctx, exec, name)
		if err != nil {
			return Schema{}, fmt.Errorf("pg introspect: list columns for %q: %w", name, err)
		}
		tables = append(tables, Table{Name: name, Columns: cols})
	}
	return Schema{Tables: tables}, nil
}

func pgListTables(ctx context.Context, exec Executor) ([]string, error) {
	rows, err := exec.QueryContext(ctx, `
		SELECT table_name
		  FROM information_schema.tables
		 WHERE table_schema = current_schema()
		   AND table_type = 'BASE TABLE'
		   AND table_name NOT LIKE 'quark_%'
		 ORDER BY table_name`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			return nil, err
		}
		out = append(out, name)
	}
	return out, rows.Err()
}

func pgListColumns(ctx context.Context, exec Executor, table string) ([]Column, error) {
	rows, err := exec.QueryContext(ctx, `
		SELECT column_name,
		       data_type,
		       is_nullable,
		       column_default,
		       character_maximum_length,
		       numeric_precision,
		       numeric_scale
		  FROM information_schema.columns
		 WHERE table_schema = current_schema()
		   AND table_name = $1
		 ORDER BY ordinal_position`, table)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Column
	for rows.Next() {
		var (
			name      string
			dataType  string
			nullable  string
			dflt      sql.NullString
			charLen   sql.NullInt64
			numPrec   sql.NullInt64
			numScale  sql.NullInt64
			displayed = ""
		)
		if err := rows.Scan(&name, &dataType, &nullable, &dflt, &charLen, &numPrec, &numScale); err != nil {
			return nil, err
		}
		// Reassemble the parameterised type so the round-trip vs the
		// migrate-side DDL is comparable:
		//   character varying(255), numeric(10,2), etc.
		displayed = dataType
		switch dataType {
		case "character varying", "character", "bit varying", "bit":
			if charLen.Valid {
				displayed = fmt.Sprintf("%s(%d)", dataType, charLen.Int64)
			}
		case "numeric", "decimal":
			if numPrec.Valid && numScale.Valid {
				displayed = fmt.Sprintf("%s(%d,%d)", dataType, numPrec.Int64, numScale.Int64)
			} else if numPrec.Valid {
				displayed = fmt.Sprintf("%s(%d)", dataType, numPrec.Int64)
			}
		}
		col := Column{
			Name:     name,
			Type:     displayed,
			Nullable: nullable == "YES",
		}
		if dflt.Valid {
			s := dflt.String
			col.Default = &s
		}
		out = append(out, col)
	}
	return out, rows.Err()
}
