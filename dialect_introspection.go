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
		idx, err := sqliteListIndexes(ctx, exec, name)
		if err != nil {
			return Schema{}, fmt.Errorf("sqlite introspect: list indexes for %q: %w", name, err)
		}
		fks, err := sqliteListForeignKeys(ctx, exec, name)
		if err != nil {
			return Schema{}, fmt.Errorf("sqlite introspect: list foreign keys for %q: %w", name, err)
		}
		tables = append(tables, Table{Name: name, Columns: cols, Indexes: idx, ForeignKeys: fks})
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

// sqliteListIndexes uses PRAGMA index_list / index_info for the index
// surface. PRAGMA index_list returns one row per index with an `origin`
// column we filter on:
//
//	c  → user-created via CREATE INDEX        (surfaced)
//	u  → implicit, from a UNIQUE constraint    (surfaced; the diff layer
//	     decides whether it round-trips as a CREATE INDEX or as a column
//	     UNIQUE)
//	pk → implicit, backing the PRIMARY KEY     (NOT surfaced — PK is a
//	     constraint in the diff model, not an index)
//
// PRAGMA index_info(<name>) then returns the columns in `seqno` order,
// which is the column order in the index — significant for B-trees.
//
// Identifier note: SQLite PRAGMA syntax doesn't accept parameterised
// arguments — the table and index name are spliced into the SQL surface.
// We validate via SQLGuard's `ValidateIdentifier` before each splice,
// which is a strict ASCII rule (`^[a-zA-Z_][a-zA-Z0-9_]*$`, len ≤ 64).
// Tables/indexes whose names came in via Quark APIs always pass that
// rule; tables/indexes created externally with hyphens, spaces, or
// non-ASCII characters will surface as an error from this function
// rather than being silently skipped. That's intentional: a migrations
// tool that hides indexes it can't read is a foot-gun (the diff would
// emit DROP INDEX for missing entries it never saw). If you hit this
// in production, rename the affected index to ASCII-only or open an
// issue requesting per-dialect quoting that bypasses the guard.
func sqliteListIndexes(ctx context.Context, exec Executor, table string) ([]Index, error) {
	if err := NewSQLGuard().ValidateIdentifier(table); err != nil {
		return nil, fmt.Errorf("sqlite introspect: bad table name %q: %w", table, err)
	}
	listQ := fmt.Sprintf(`PRAGMA index_list("%s")`, table)
	rows, err := exec.QueryContext(ctx, listQ)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	type meta struct {
		name   string
		unique bool
	}
	var pending []meta
	for rows.Next() {
		var (
			seq     int
			name    string
			uniq    int
			origin  string
			partial int
		)
		if err := rows.Scan(&seq, &name, &uniq, &origin, &partial); err != nil {
			return nil, err
		}
		if origin == "pk" {
			continue
		}
		pending = append(pending, meta{name: name, unique: uniq == 1})
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	rows.Close()

	out := make([]Index, 0, len(pending))
	for _, m := range pending {
		if err := NewSQLGuard().ValidateIdentifier(m.name); err != nil {
			return nil, fmt.Errorf("sqlite introspect: bad index name %q: %w", m.name, err)
		}
		infoQ := fmt.Sprintf(`PRAGMA index_info("%s")`, m.name)
		infoRows, err := exec.QueryContext(ctx, infoQ)
		if err != nil {
			return nil, err
		}
		var cols []string
		for infoRows.Next() {
			var (
				seqno   int
				cid     int
				colname sql.NullString
			)
			if err := infoRows.Scan(&seqno, &cid, &colname); err != nil {
				infoRows.Close()
				return nil, err
			}
			// `cid = -1` and `colname IS NULL` indicate an expression
			// index (CREATE INDEX … ON t(lower(x))). We surface the
			// raw "" column name so the diff layer can decide whether
			// to treat it as opaque; expression indexes are out of
			// scope for the F3-3 column-equality diff.
			if colname.Valid {
				cols = append(cols, colname.String)
			} else {
				cols = append(cols, "")
			}
		}
		if err := infoRows.Err(); err != nil {
			infoRows.Close()
			return nil, err
		}
		infoRows.Close()
		out = append(out, Index{Name: m.name, Columns: cols, Unique: m.unique})
	}
	return out, nil
}

// sqliteListForeignKeys uses `PRAGMA foreign_key_list(<table>)`. The
// PRAGMA does NOT preserve constraint names from the CREATE TABLE DDL
// — it returns a synthetic `id` per FK constraint. We surface
// `Name = ""` so the diff layer's symmetric-matching path (which
// keys on column-tuple + ref_table rather than name) is exercised
// rather than relying on a spurious synthetic name.
//
// Rows are grouped by `id` (FK constraint identifier) preserving
// seq order. PRAGMA columns are:
//
//	id INTEGER, seq INTEGER, table TEXT, from TEXT, to TEXT,
//	on_update TEXT, on_delete TEXT, match TEXT
//
// `match` is one of NONE/PARTIAL/FULL — SQL-standard but rarely used
// (SQLite only enforces NONE). Not surfaced; F3-2 doesn't diff on
// MATCH semantics.
func sqliteListForeignKeys(ctx context.Context, exec Executor, table string) ([]ForeignKey, error) {
	if err := NewSQLGuard().ValidateIdentifier(table); err != nil {
		return nil, fmt.Errorf("sqlite introspect: bad table name %q: %w", table, err)
	}
	q := fmt.Sprintf(`PRAGMA foreign_key_list("%s")`, table)
	rows, err := exec.QueryContext(ctx, q)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	type accum struct {
		refTable string
		cols     []string
		refCols  []string
		onUpdate string
		onDelete string
	}
	byID := map[int]*accum{}
	var order []int
	for rows.Next() {
		var (
			id       int
			seq      int
			refTab   string
			fromCol  sql.NullString
			toCol    sql.NullString
			onUpdate string
			onDelete string
			matchVal string
		)
		if err := rows.Scan(&id, &seq, &refTab, &fromCol, &toCol, &onUpdate, &onDelete, &matchVal); err != nil {
			return nil, err
		}
		a, ok := byID[id]
		if !ok {
			a = &accum{refTable: refTab, onUpdate: sqliteFKAction(onUpdate), onDelete: sqliteFKAction(onDelete)}
			byID[id] = a
			order = append(order, id)
		}
		if fromCol.Valid {
			a.cols = append(a.cols, fromCol.String)
		} else {
			a.cols = append(a.cols, "")
		}
		if toCol.Valid {
			a.refCols = append(a.refCols, toCol.String)
		} else {
			a.refCols = append(a.refCols, "")
		}
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	out := make([]ForeignKey, 0, len(order))
	for _, id := range order {
		a := byID[id]
		out = append(out, ForeignKey{
			Name:       "",
			Columns:    a.cols,
			RefTable:   a.refTable,
			RefColumns: a.refCols,
			OnDelete:   a.onDelete,
			OnUpdate:   a.onUpdate,
		})
	}
	return out, nil
}

// sqliteFKAction normalises SQLite's PRAGMA action strings to the
// SQL-standard verbose form. SQLite returns `NO ACTION`, `RESTRICT`,
// `SET NULL`, `SET DEFAULT`, `CASCADE` — already verbose — so this
// is mostly a passthrough. SQLite never returns the empty string
// (NO ACTION is its default for unspecified clauses).
func sqliteFKAction(s string) string {
	// PRAGMA returns "NONE" for no-action FK actions in some SQLite
	// versions; map that to NO ACTION for cross-dialect consistency.
	if s == "NONE" {
		return "NO ACTION"
	}
	return s
}

// --- MySQL / MariaDB ---------------------------------------------------------

// IntrospectSchema reads the MySQL/MariaDB schema using
// `INFORMATION_SCHEMA.TABLES` and `INFORMATION_SCHEMA.COLUMNS`. Both
// engines share the same catalog structure for the column-level
// surface, so a single implementation covers them (the two Dialect
// types just delegate here).
//
// MySQL caveats handled here:
//   - Scope: `TABLE_SCHEMA = DATABASE()` — the current database, which
//     is MySQL's analogue of PG's `current_schema()`. Cross-database
//     introspection is out of scope (caller would need to switch DBs
//     explicitly).
//   - Type representation: we use `COLUMN_TYPE` (full type string with
//     parameters and modifiers — `int(11) unsigned`, `varchar(255)`,
//     `decimal(10,2)`) instead of reassembling from `DATA_TYPE`. MySQL
//     returns this verbatim, which means the round-trip vs the Go
//     migrate-side DDL is comparable without per-type switches.
//   - System tables: MySQL exposes `mysql`, `information_schema`,
//     `performance_schema`, `sys` as system databases. Our scope is
//     the user's current DB, so those don't surface; we additionally
//     filter `quark_%` for our internal tables.
func (d *MySQLDialect) IntrospectSchema(ctx context.Context, exec Executor) (Schema, error) {
	return mysqlLikeIntrospect(ctx, exec, "mysql")
}

func (d *MariaDBDialect) IntrospectSchema(ctx context.Context, exec Executor) (Schema, error) {
	return mysqlLikeIntrospect(ctx, exec, "mariadb")
}

func mysqlLikeIntrospect(ctx context.Context, exec Executor, dialectName string) (Schema, error) {
	tableNames, err := mysqlListTables(ctx, exec)
	if err != nil {
		return Schema{}, fmt.Errorf("%s introspect: list tables: %w", dialectName, err)
	}
	tables := make([]Table, 0, len(tableNames))
	for _, name := range tableNames {
		cols, err := mysqlListColumns(ctx, exec, name)
		if err != nil {
			return Schema{}, fmt.Errorf("%s introspect: list columns for %q: %w", dialectName, name, err)
		}
		idx, err := mysqlListIndexes(ctx, exec, name)
		if err != nil {
			return Schema{}, fmt.Errorf("%s introspect: list indexes for %q: %w", dialectName, name, err)
		}
		fks, err := mysqlListForeignKeys(ctx, exec, name)
		if err != nil {
			return Schema{}, fmt.Errorf("%s introspect: list foreign keys for %q: %w", dialectName, name, err)
		}
		tables = append(tables, Table{Name: name, Columns: cols, Indexes: idx, ForeignKeys: fks})
	}
	return Schema{Tables: tables}, nil
}

func mysqlListTables(ctx context.Context, exec Executor) ([]string, error) {
	rows, err := exec.QueryContext(ctx, `
		SELECT TABLE_NAME
		  FROM INFORMATION_SCHEMA.TABLES
		 WHERE TABLE_SCHEMA = DATABASE()
		   AND TABLE_TYPE = 'BASE TABLE'
		   AND TABLE_NAME NOT LIKE 'quark_%'
		 ORDER BY TABLE_NAME`)
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

func mysqlListColumns(ctx context.Context, exec Executor, table string) ([]Column, error) {
	rows, err := exec.QueryContext(ctx, `
		SELECT COLUMN_NAME,
		       COLUMN_TYPE,
		       IS_NULLABLE,
		       COLUMN_DEFAULT
		  FROM INFORMATION_SCHEMA.COLUMNS
		 WHERE TABLE_SCHEMA = DATABASE()
		   AND TABLE_NAME = ?
		 ORDER BY ORDINAL_POSITION`, table)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Column
	for rows.Next() {
		var (
			name     string
			colType  string
			nullable string
			dflt     sql.NullString
		)
		if err := rows.Scan(&name, &colType, &nullable, &dflt); err != nil {
			return nil, err
		}
		col := Column{
			Name:     name,
			Type:     colType,
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

// mysqlListIndexes reads `INFORMATION_SCHEMA.STATISTICS`, the
// documented catalog for index metadata in MySQL/MariaDB. The
// catalog returns one row per (index, column) — we group those rows
// in Go using `SEQ_IN_INDEX` for column order. PRIMARY KEY backing
// indexes are filtered (the PK is a constraint, not an index in our
// diff model). `NON_UNIQUE = 0` is the unique flag.
//
// MariaDB shares this catalog with MySQL, so the same query works
// for both — no dialect branching needed.
func mysqlListIndexes(ctx context.Context, exec Executor, table string) ([]Index, error) {
	rows, err := exec.QueryContext(ctx, `
		SELECT INDEX_NAME, NON_UNIQUE, COLUMN_NAME, SEQ_IN_INDEX
		  FROM INFORMATION_SCHEMA.STATISTICS
		 WHERE TABLE_SCHEMA = DATABASE()
		   AND TABLE_NAME = ?
		   AND INDEX_NAME != 'PRIMARY'
		 ORDER BY INDEX_NAME, SEQ_IN_INDEX`, table)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	// Group rows by index name preserving the first-seen order.
	type accum struct {
		unique bool
		cols   []string
	}
	byName := map[string]*accum{}
	var order []string
	for rows.Next() {
		var (
			indexName string
			nonUnique int
			colName   sql.NullString
			seqInIdx  int
		)
		if err := rows.Scan(&indexName, &nonUnique, &colName, &seqInIdx); err != nil {
			return nil, err
		}
		a, ok := byName[indexName]
		if !ok {
			a = &accum{unique: nonUnique == 0}
			byName[indexName] = a
			order = append(order, indexName)
		}
		// COLUMN_NAME is NULL for functional/expression indexes
		// (MySQL 8.0+: `CREATE INDEX … ON t((lower(x)))`). Surface ""
		// so the diff layer can spot the expression-index case rather
		// than silently dropping the column slot.
		if colName.Valid {
			a.cols = append(a.cols, colName.String)
		} else {
			a.cols = append(a.cols, "")
		}
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	out := make([]Index, 0, len(order))
	for _, name := range order {
		a := byName[name]
		out = append(out, Index{Name: name, Columns: a.cols, Unique: a.unique})
	}
	return out, nil
}

// mysqlListForeignKeys reads `INFORMATION_SCHEMA.KEY_COLUMN_USAGE` joined
// with `INFORMATION_SCHEMA.REFERENTIAL_CONSTRAINTS`. The catalog
// returns one row per (constraint, column); we group by
// CONSTRAINT_NAME in Go, ordered by ORDINAL_POSITION for stable
// column matching across composite FKs.
//
// MySQL/MariaDB share this catalog so a single implementation covers
// both. The dialect-shared `mysqlLikeIntrospect` already delegates
// here.
//
// REFERENTIAL_CONSTRAINTS returns UPDATE_RULE / DELETE_RULE as
// verbose strings (`CASCADE`, `SET NULL`, `NO ACTION`, `RESTRICT`,
// `SET DEFAULT`) — no normalisation needed.
//
// Asymmetry to know about: when a FK is declared without an explicit
// ON DELETE/UPDATE clause, MariaDB stores the SQL-standard default
// as `RESTRICT` while MySQL stores it as `NO ACTION`. Both are
// semantically equivalent in immediate-check mode (which is the only
// mode either engine supports), but the catalog labelling diverges.
// We pass either label through verbatim rather than collapsing them
// — the diff layer (F3-3) treats them as equivalent.
func mysqlListForeignKeys(ctx context.Context, exec Executor, table string) ([]ForeignKey, error) {
	rows, err := exec.QueryContext(ctx, `
		SELECT kcu.CONSTRAINT_NAME,
		       kcu.COLUMN_NAME,
		       kcu.REFERENCED_TABLE_NAME,
		       kcu.REFERENCED_COLUMN_NAME,
		       rc.UPDATE_RULE,
		       rc.DELETE_RULE,
		       kcu.ORDINAL_POSITION
		  FROM INFORMATION_SCHEMA.KEY_COLUMN_USAGE kcu
		  JOIN INFORMATION_SCHEMA.REFERENTIAL_CONSTRAINTS rc
		    ON kcu.CONSTRAINT_SCHEMA = rc.CONSTRAINT_SCHEMA
		   AND kcu.CONSTRAINT_NAME   = rc.CONSTRAINT_NAME
		 WHERE kcu.TABLE_SCHEMA      = DATABASE()
		   AND kcu.TABLE_NAME        = ?
		   AND kcu.REFERENCED_TABLE_NAME IS NOT NULL
		 ORDER BY kcu.CONSTRAINT_NAME, kcu.ORDINAL_POSITION`, table)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	type accum struct {
		refTable string
		cols     []string
		refCols  []string
		onUpdate string
		onDelete string
	}
	byName := map[string]*accum{}
	var order []string
	for rows.Next() {
		var (
			constraintName string
			columnName     string
			refTable       string
			refColumn      string
			updateRule     string
			deleteRule     string
			pos            int
		)
		if err := rows.Scan(&constraintName, &columnName, &refTable, &refColumn, &updateRule, &deleteRule, &pos); err != nil {
			return nil, err
		}
		a, ok := byName[constraintName]
		if !ok {
			a = &accum{refTable: refTable, onUpdate: updateRule, onDelete: deleteRule}
			byName[constraintName] = a
			order = append(order, constraintName)
		}
		a.cols = append(a.cols, columnName)
		a.refCols = append(a.refCols, refColumn)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	out := make([]ForeignKey, 0, len(order))
	for _, name := range order {
		a := byName[name]
		out = append(out, ForeignKey{
			Name:       name,
			Columns:    a.cols,
			RefTable:   a.refTable,
			RefColumns: a.refCols,
			OnDelete:   a.onDelete,
			OnUpdate:   a.onUpdate,
		})
	}
	return out, nil
}

// --- SQL Server --------------------------------------------------------------

// IntrospectSchema reads the MSSQL schema via `sys.tables`,
// `sys.columns`, `sys.types`, and `sys.default_constraints`. MSSQL
// stores defaults in a separate catalog joined on parent object_id
// + column_id, so default-extraction needs a LEFT JOIN; everything
// else is a straight catalog read.
//
// MSSQL caveats handled here:
//   - System-shipped tables are filtered via `is_ms_shipped = 0` plus
//     `name NOT LIKE 'quark[_]%' ESCAPE`-style char class (the `[_]`
//     bracket prevents `_` from being interpreted as the wildcard).
//   - Type reassembly: `sys.types` returns the bare type name
//     (`varchar`, `decimal`); we glue `(N)` / `(P,S)` / `(MAX)`
//     onto it from the adjacent columns. `max_length = -1` is the
//     MSSQL convention for VARCHAR(MAX) / NVARCHAR(MAX). For
//     nvarchar, `max_length` is bytes (chars * 2), so we divide by
//     2 when emitting the displayed type — matches what a user would
//     write in DDL.
//   - Default values: MSSQL wraps them in parens (`(0)`,
//     `(getdate())`, `('draft')`). We pass that through raw — the
//     diff layer (F3-3) is responsible for unwrapping if it needs
//     to compare against the Go-side DDL.
func (d *MSSQLDialect) IntrospectSchema(ctx context.Context, exec Executor) (Schema, error) {
	tableNames, err := mssqlListTables(ctx, exec)
	if err != nil {
		return Schema{}, fmt.Errorf("mssql introspect: list tables: %w", err)
	}
	tables := make([]Table, 0, len(tableNames))
	for _, name := range tableNames {
		cols, err := mssqlListColumns(ctx, exec, name)
		if err != nil {
			return Schema{}, fmt.Errorf("mssql introspect: list columns for %q: %w", name, err)
		}
		idx, err := mssqlListIndexes(ctx, exec, name)
		if err != nil {
			return Schema{}, fmt.Errorf("mssql introspect: list indexes for %q: %w", name, err)
		}
		fks, err := mssqlListForeignKeys(ctx, exec, name)
		if err != nil {
			return Schema{}, fmt.Errorf("mssql introspect: list foreign keys for %q: %w", name, err)
		}
		tables = append(tables, Table{Name: name, Columns: cols, Indexes: idx, ForeignKeys: fks})
	}
	return Schema{Tables: tables}, nil
}

func mssqlListTables(ctx context.Context, exec Executor) ([]string, error) {
	rows, err := exec.QueryContext(ctx, `
		SELECT t.name
		  FROM sys.tables t
		 WHERE t.is_ms_shipped = 0
		   AND t.name NOT LIKE 'quark[_]%'
		 ORDER BY t.name`)
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

func mssqlListColumns(ctx context.Context, exec Executor, table string) ([]Column, error) {
	// Placeholder via Dialect rather than hardcoded `@p1`. The query
	// body is MSSQL-specific (sys.* catalog), but using the dialect's
	// Placeholder() keeps the call site honest — a future driver change
	// would only need to touch the dialect, not every catalog query.
	d := &MSSQLDialect{}
	q := fmt.Sprintf(`
		SELECT c.name,
		       ty.name AS type_name,
		       c.max_length,
		       c.precision,
		       c.scale,
		       c.is_nullable,
		       dc.definition AS default_value
		  FROM sys.columns c
		  JOIN sys.types ty ON c.user_type_id = ty.user_type_id
		  LEFT JOIN sys.default_constraints dc
		    ON dc.parent_object_id = c.object_id
		   AND dc.parent_column_id = c.column_id
		 WHERE c.object_id = OBJECT_ID(%s)
		 ORDER BY c.column_id`, d.Placeholder(1))
	rows, err := exec.QueryContext(ctx, q, table)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Column
	for rows.Next() {
		var (
			name       string
			typeName   string
			maxLength  int16
			precision  uint8
			scale      uint8
			isNullable bool
			dflt       sql.NullString
		)
		if err := rows.Scan(&name, &typeName, &maxLength, &precision, &scale, &isNullable, &dflt); err != nil {
			return nil, err
		}
		col := Column{
			Name:     name,
			Type:     mssqlReassembleType(typeName, maxLength, precision, scale),
			Nullable: isNullable,
		}
		if dflt.Valid {
			s := dflt.String
			col.Default = &s
		}
		out = append(out, col)
	}
	return out, rows.Err()
}

// mssqlListIndexes reads `sys.indexes` joined with `sys.index_columns`
// and `sys.columns` to get the (index, column) tuples. Three filters
// apply at the catalog level:
//
//   - `is_primary_key = 0` — PK is a constraint, not an index here.
//   - `type > 0` — `type = 0` is HEAP (no real index storage), `1` is
//     CLUSTERED, `2` is NONCLUSTERED. We want 1 and 2 (and 5/6 for
//     XML / SPATIAL aren't going to round-trip anyway).
//   - `is_unique_constraint = 0` is NOT applied — a UNIQUE constraint
//     creates a backing index that we DO want to surface, mirroring
//     what other dialects do.
//
// `key_ordinal` is the column position in the index key (1-based).
// We group rows by index name in Go preserving the catalog order.
func mssqlListIndexes(ctx context.Context, exec Executor, table string) ([]Index, error) {
	d := &MSSQLDialect{}
	q := fmt.Sprintf(`
		SELECT i.name AS index_name,
		       i.is_unique,
		       c.name AS column_name,
		       ic.key_ordinal
		  FROM sys.indexes i
		  JOIN sys.index_columns ic
		    ON i.object_id = ic.object_id
		   AND i.index_id  = ic.index_id
		  JOIN sys.columns c
		    ON ic.object_id = c.object_id
		   AND ic.column_id = c.column_id
		 WHERE i.object_id = OBJECT_ID(%s)
		   AND i.is_primary_key = 0
		   AND i.type > 0
		   AND ic.is_included_column = 0
		 ORDER BY i.name, ic.key_ordinal`, d.Placeholder(1))
	rows, err := exec.QueryContext(ctx, q, table)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	type accum struct {
		unique bool
		cols   []string
	}
	byName := map[string]*accum{}
	var order []string
	for rows.Next() {
		var (
			indexName  string
			isUnique   bool
			columnName string
			keyOrdinal uint8
		)
		if err := rows.Scan(&indexName, &isUnique, &columnName, &keyOrdinal); err != nil {
			return nil, err
		}
		a, ok := byName[indexName]
		if !ok {
			a = &accum{unique: isUnique}
			byName[indexName] = a
			order = append(order, indexName)
		}
		a.cols = append(a.cols, columnName)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	out := make([]Index, 0, len(order))
	for _, name := range order {
		a := byName[name]
		out = append(out, Index{Name: name, Columns: a.cols, Unique: a.unique})
	}
	return out, nil
}

// mssqlListForeignKeys reads `sys.foreign_keys` joined with
// `sys.foreign_key_columns`, `sys.tables` (referenced table name),
// and `sys.columns` (twice — once for the parent/local column, once
// for the referenced column).
//
// MSSQL exposes the FK actions as descriptive verbose strings in
// `delete_referential_action_desc` / `update_referential_action_desc`,
// but with underscores (`NO_ACTION`, `SET_NULL`, `SET_DEFAULT`) —
// the helper [mssqlFKAction] strips them to the SQL-standard form.
func mssqlListForeignKeys(ctx context.Context, exec Executor, table string) ([]ForeignKey, error) {
	d := &MSSQLDialect{}
	q := fmt.Sprintf(`
		SELECT fk.name AS fk_name,
		       pc.name AS column_name,
		       rt.name AS ref_table,
		       rc.name AS ref_column,
		       fk.delete_referential_action_desc AS on_delete,
		       fk.update_referential_action_desc AS on_update,
		       fkc.constraint_column_id
		  FROM sys.foreign_keys fk
		  JOIN sys.foreign_key_columns fkc
		    ON fk.object_id = fkc.constraint_object_id
		  JOIN sys.tables rt
		    ON rt.object_id = fkc.referenced_object_id
		  JOIN sys.columns pc
		    ON pc.object_id = fkc.parent_object_id
		   AND pc.column_id = fkc.parent_column_id
		  JOIN sys.columns rc
		    ON rc.object_id = fkc.referenced_object_id
		   AND rc.column_id = fkc.referenced_column_id
		 WHERE fk.parent_object_id = OBJECT_ID(%s)
		 ORDER BY fk.name, fkc.constraint_column_id`, d.Placeholder(1))
	rows, err := exec.QueryContext(ctx, q, table)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	type accum struct {
		refTable string
		cols     []string
		refCols  []string
		onDelete string
		onUpdate string
	}
	byName := map[string]*accum{}
	var order []string
	for rows.Next() {
		var (
			fkName       string
			columnName   string
			refTable     string
			refColumn    string
			onDelete     string
			onUpdate     string
			constraintID int
		)
		if err := rows.Scan(&fkName, &columnName, &refTable, &refColumn, &onDelete, &onUpdate, &constraintID); err != nil {
			return nil, err
		}
		a, ok := byName[fkName]
		if !ok {
			a = &accum{
				refTable: refTable,
				onDelete: mssqlFKAction(onDelete),
				onUpdate: mssqlFKAction(onUpdate),
			}
			byName[fkName] = a
			order = append(order, fkName)
		}
		a.cols = append(a.cols, columnName)
		a.refCols = append(a.refCols, refColumn)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	out := make([]ForeignKey, 0, len(order))
	for _, name := range order {
		a := byName[name]
		out = append(out, ForeignKey{
			Name:       name,
			Columns:    a.cols,
			RefTable:   a.refTable,
			RefColumns: a.refCols,
			OnDelete:   a.onDelete,
			OnUpdate:   a.onUpdate,
		})
	}
	return out, nil
}

// mssqlFKAction normalises MSSQL's `*_referential_action_desc`
// strings (`NO_ACTION`, `SET_NULL`, `SET_DEFAULT`, `CASCADE`) to the
// SQL-standard verbose form used in `ForeignKey.OnDelete` /
// `OnUpdate` — i.e. underscores → spaces.
func mssqlFKAction(s string) string {
	switch s {
	case "NO_ACTION":
		return "NO ACTION"
	case "SET_NULL":
		return "SET NULL"
	case "SET_DEFAULT":
		return "SET DEFAULT"
	default:
		// CASCADE comes through as-is. NOTE: MSSQL does NOT support
		// RESTRICT as a referential action — T-SQL has no such code
		// in `*_referential_action_desc`. Don't add a "RESTRICT" case
		// here on autopilot from PG/MySQL/SQLite habit.
		return s
	}
}

// mssqlReassembleType glues parameters onto MSSQL's bare type name from
// the catalog: `varchar` + `max_length=255` → `varchar(255)`;
// `decimal` + `precision=10,scale=2` → `decimal(10,2)`; `nvarchar` +
// `max_length=-1` → `nvarchar(MAX)`. For `nvarchar`/`nchar` the
// catalog's max_length is bytes (chars × 2) so we halve it when
// emitting the displayed length. `ntext` is intentionally NOT in the
// switch — it has no length parameter (always MAX-sized), so the
// default branch returning the bare name is correct.
func mssqlReassembleType(typeName string, maxLength int16, precision, scale uint8) string {
	switch typeName {
	case "varchar", "char", "varbinary", "binary":
		if maxLength == -1 {
			return fmt.Sprintf("%s(MAX)", typeName)
		}
		if maxLength > 0 {
			return fmt.Sprintf("%s(%d)", typeName, maxLength)
		}
	case "nvarchar", "nchar":
		if maxLength == -1 {
			return fmt.Sprintf("%s(MAX)", typeName)
		}
		if maxLength > 0 {
			// nvarchar/nchar store 2 bytes per char.
			return fmt.Sprintf("%s(%d)", typeName, maxLength/2)
		}
	case "decimal", "numeric":
		return fmt.Sprintf("%s(%d,%d)", typeName, precision, scale)
	}
	return typeName
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
		idx, err := pgListIndexes(ctx, exec, name)
		if err != nil {
			return Schema{}, fmt.Errorf("pg introspect: list indexes for %q: %w", name, err)
		}
		fks, err := pgListForeignKeys(ctx, exec, name)
		if err != nil {
			return Schema{}, fmt.Errorf("pg introspect: list foreign keys for %q: %w", name, err)
		}
		tables = append(tables, Table{Name: name, Columns: cols, Indexes: idx, ForeignKeys: fks})
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

// pgListIndexes reads index metadata via `pg_index` joined with
// `pg_class` (for the index name) and `pg_attribute` (for the column
// name). `unnest(indkey) WITH ORDINALITY` lets us preserve column
// order — `indkey` is a `smallint[]` array of attribute numbers in
// key order, so the ordinality column is the key position.
//
// PG caveats:
//   - `indisprimary` filters PK-backing indexes — PK is a constraint,
//     not an index in our diff model.
//   - `indisunique` is the unique flag (covers both UNIQUE INDEX and
//     UNIQUE-constraint-backing indexes, which is what we want — the
//     diff layer treats them uniformly).
//   - `current_schema()` scopes to the public-ish schema in the same
//     way `pgListTables` does.
//   - Expression indexes have `attnum = 0` for the expression slot;
//     the LEFT JOIN to pg_attribute returns NULL for those, which we
//     surface as `""` to match the SQLite/MySQL convention.
func pgListIndexes(ctx context.Context, exec Executor, table string) ([]Index, error) {
	// Placeholder via Dialect rather than hardcoded `$1`. The query
	// body uses pg_catalog (PG-specific), but going through
	// Placeholder() keeps the call site consistent with mssql's
	// catalog query and prevents the "copy-paste $1" anti-pattern
	// from spreading. (pgListColumns above still hardcodes $1 —
	// that's pre-existing deuda; this PR refuses to add a new
	// instance.)
	d := &PostgresDialect{}
	q := fmt.Sprintf(`
		SELECT i.relname        AS index_name,
		       ix.indisunique   AS is_unique,
		       a.attname        AS column_name,
		       k.ord            AS column_position
		  FROM pg_class t
		  JOIN pg_namespace n ON n.oid = t.relnamespace
		  JOIN pg_index    ix ON ix.indrelid = t.oid
		  JOIN pg_class    i  ON i.oid = ix.indexrelid
		  JOIN LATERAL unnest(ix.indkey) WITH ORDINALITY AS k(attnum, ord) ON true
		  LEFT JOIN pg_attribute a ON a.attrelid = t.oid AND a.attnum = k.attnum
		 WHERE t.relname = %s
		   AND n.nspname = current_schema()
		   AND NOT ix.indisprimary
		 ORDER BY i.relname, k.ord`, d.Placeholder(1))
	rows, err := exec.QueryContext(ctx, q, table)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	type accum struct {
		unique bool
		cols   []string
	}
	byName := map[string]*accum{}
	var order []string
	for rows.Next() {
		var (
			indexName string
			isUnique  bool
			colName   sql.NullString
			position  int
		)
		if err := rows.Scan(&indexName, &isUnique, &colName, &position); err != nil {
			return nil, err
		}
		a, ok := byName[indexName]
		if !ok {
			a = &accum{unique: isUnique}
			byName[indexName] = a
			order = append(order, indexName)
		}
		if colName.Valid {
			a.cols = append(a.cols, colName.String)
		} else {
			a.cols = append(a.cols, "")
		}
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	out := make([]Index, 0, len(order))
	for _, name := range order {
		a := byName[name]
		out = append(out, Index{Name: name, Columns: a.cols, Unique: a.unique})
	}
	return out, nil
}

// pgListForeignKeys reads `pg_constraint` (contype='f') directly,
// joining `pg_attribute` twice (once for the parent/local columns
// via `conkey`, once for the referenced columns via `confkey`).
//
// `pg_constraint.confdeltype` / `confupdtype` are single-character
// codes documented in the PG catalog reference:
//
//	a = NO ACTION  (SQL default)
//	r = RESTRICT
//	c = CASCADE
//	n = SET NULL
//	d = SET DEFAULT
//
// [pgFKAction] translates them to the SQL-standard verbose form.
//
// The `unnest` + `WITH ORDINALITY` pattern preserves column order
// across composite FKs — `conkey[i]` matches `confkey[i]` by
// position, and we group by FK name in Go.
func pgListForeignKeys(ctx context.Context, exec Executor, table string) ([]ForeignKey, error) {
	d := &PostgresDialect{}
	// Column order in the SELECT mirrors mssqlListForeignKeys and
	// mysqlListForeignKeys — local column first, ref column second —
	// so the Scan call in this file reads symmetrically across the
	// three dialect implementations. Don't reorder without auditing
	// the Scan below.
	q := fmt.Sprintf(`
		SELECT c.conname AS fk_name,
		       a.attname  AS column_name,
		       ra.attname AS ref_column,
		       c.confdeltype,
		       c.confupdtype,
		       rt.relname AS ref_table,
		       k.ord
		  FROM pg_constraint c
		  JOIN pg_class      pt ON pt.oid = c.conrelid
		  JOIN pg_namespace  pn ON pn.oid = pt.relnamespace
		  JOIN pg_class      rt ON rt.oid = c.confrelid
		  JOIN LATERAL unnest(c.conkey)  WITH ORDINALITY AS k(attnum, ord) ON true
		  JOIN LATERAL unnest(c.confkey) WITH ORDINALITY AS rk(attnum, ord) ON rk.ord = k.ord
		  JOIN pg_attribute a  ON a.attrelid  = c.conrelid  AND a.attnum  = k.attnum
		  JOIN pg_attribute ra ON ra.attrelid = c.confrelid AND ra.attnum = rk.attnum
		 WHERE c.contype = 'f'
		   AND pn.nspname = current_schema()
		   AND pt.relname = %s
		 ORDER BY c.conname, k.ord`, d.Placeholder(1))
	rows, err := exec.QueryContext(ctx, q, table)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	type accum struct {
		refTable string
		cols     []string
		refCols  []string
		onDelete string
		onUpdate string
	}
	byName := map[string]*accum{}
	var order []string
	for rows.Next() {
		var (
			fkName      string
			columnName  string
			refColumn   string
			confdeltype string
			confupdtype string
			refTable    string
			position    int
		)
		if err := rows.Scan(&fkName, &columnName, &refColumn, &confdeltype, &confupdtype, &refTable, &position); err != nil {
			return nil, err
		}
		a, ok := byName[fkName]
		if !ok {
			a = &accum{
				refTable: refTable,
				onDelete: pgFKAction(confdeltype),
				onUpdate: pgFKAction(confupdtype),
			}
			byName[fkName] = a
			order = append(order, fkName)
		}
		a.cols = append(a.cols, columnName)
		a.refCols = append(a.refCols, refColumn)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	out := make([]ForeignKey, 0, len(order))
	for _, name := range order {
		a := byName[name]
		out = append(out, ForeignKey{
			Name:       name,
			Columns:    a.cols,
			RefTable:   a.refTable,
			RefColumns: a.refCols,
			OnDelete:   a.onDelete,
			OnUpdate:   a.onUpdate,
		})
	}
	return out, nil
}

// pgFKAction maps PG's `pg_constraint.confdeltype` / `confupdtype`
// single-character codes to the SQL-standard verbose form.
func pgFKAction(c string) string {
	switch c {
	case "a":
		return "NO ACTION"
	case "r":
		return "RESTRICT"
	case "c":
		return "CASCADE"
	case "n":
		return "SET NULL"
	case "d":
		return "SET DEFAULT"
	default:
		// Unknown codes pass through — keeps the introspector
		// forward-compatible if PG adds a code.
		return c
	}
}
