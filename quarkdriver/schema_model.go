// Copyright 2026 jcsvwinston
// SPDX-License-Identifier: Apache-2.0

package quarkdriver

import "context"

// This file is the schema model and the introspection half of the dialect
// contract (ADR-0026): what a dialect returns when Quark asks it for the
// schema of the database it is connected to. A third-party engine takes part
// in migrations by answering SchemaIntrospector, so the model it answers in
// lives here with the interface. Package quark names every type here under
// the same name — quark.Schema, quark.Table, … — as an alias of the type here.

// Schema is the dialect-neutral representation of a database schema.
// It's the foundation for F3-3 (schema diff) — the diff comparator
// takes a Schema derived from the Go models and a Schema returned by
// IntrospectSchema, and emits the operations needed to align the two.
//
// Tables are sorted by Name for deterministic ordering; the diff
// comparator relies on this to produce stable plans.
type Schema struct {
	Tables []Table
}

// Table represents one table in the schema. The neutral representation
// stores both the raw dialect-native type strings (`Type`) and (in a
// later phase) a normalised form for cross-dialect comparison.
type Table struct {
	Name        string
	Columns     []Column
	Indexes     []Index
	ForeignKeys []ForeignKey
	Checks      []Check
}

// Index is one secondary (non-primary-key) index on a table. The PK
// is a constraint rather than an index in the diff model and lives on
// the Column (via [Column.PrimaryKey]), not here. The
// introspector deliberately filters PK-backing indexes per dialect so
// `Table.Indexes` only carries what F3-3 diffs need to add/drop.
//
// Columns is the ordered list of column names as the index defines
// them (the order is significant for B-tree indexes — a (a,b) index
// is not the same as (b,a)).
type Index struct {
	Name    string
	Columns []string
	Unique  bool
}

// ForeignKey is one FOREIGN KEY constraint declared on a table. It
// captures the surface that quark's Client.AddForeignKey takes as input so
// the diff comparator (F3-3) can match introspected FKs against
// Go-side declarations symmetrically.
//
// Columns and RefColumns are positionally matched — `Columns[i]`
// references `RefColumns[i]` (FK constraints can span multiple
// columns, e.g. composite FKs).
//
// OnDelete / OnUpdate are normalised to the SQL-standard verbose
// form (`"CASCADE"`, `"SET NULL"`, `"SET DEFAULT"`, `"RESTRICT"`,
// `"NO ACTION"`) regardless of how the underlying catalog encodes
// them (PG single-char `confdeltype`, MSSQL `delete_referential_action_desc`
// with underscores, etc.). The empty string never appears here in
// practice — every catalog returns a verbose label.
//
// Catalog asymmetry to know about: when a foreign key is declared
// without an explicit ON DELETE/ON UPDATE clause, **MariaDB** stores
// the SQL-standard default as `"RESTRICT"` in
// `INFORMATION_SCHEMA.REFERENTIAL_CONSTRAINTS`, while **MySQL**,
// **PostgreSQL**, **MSSQL**, and **SQLite** store it as `"NO ACTION"`.
// In SQL semantics RESTRICT and NO ACTION are equivalent in
// immediate-check mode (the only mode these engines support); the
// difference is purely how each catalog labels the default. The
// introspector reports what the catalog says rather than normalising
// to a single canonical form — the diff layer (F3-3) treats the two
// as equivalent on the MySQL/MariaDB side.
//
// Name comes from the catalog. SQLite returns `""` for inline FKs
// declared without an explicit `CONSTRAINT <name>` clause; the diff
// layer handles unnamed FKs by matching on (columns, ref_table,
// ref_columns) tuples.
type ForeignKey struct {
	Name       string
	Columns    []string
	RefTable   string
	RefColumns []string
	OnDelete   string
	OnUpdate   string
}

// Check is one CHECK constraint declared on a table. Expression is the
// raw catalog text — dialect-specific phrasing, parenthesisation, and
// whitespace. The introspector deliberately does NOT normalise the
// expression because expression-equivalence across dialects is an
// AST-level problem (`(x > 0)` vs `x > 0`, `'a' IN ('a','b')` vs
// `'a' = ANY (ARRAY['a','b'])`, etc.) that belongs to F3-3's diff
// engine, not to the catalog reader.
//
// Name comes from the catalog. Inline anonymous checks (`age INTEGER
// CHECK (age > 0)` without an explicit `CONSTRAINT <name>`) get
// dialect-generated names (`age_check`, `CK__table__age__hash`,
// etc.). F3-3-core's `Diff` matches checks **by name only** — there
// is no fallback to expression equivalence for anonymous ones,
// because expression equivalence is AST-level work that's out of
// scope for the diff layer (each dialect emits its own canonical
// form; comparing them is its own problem). If your checks must
// round-trip cleanly cross-dialect, give them explicit
// `CONSTRAINT <name>` clauses in DDL.
// TODO(F3-3-checks-anon): consider an opt-in pass that matches
// anonymous checks by normalised expression once the AST equivalence
// work lands.
//
// Coverage: PostgreSQL, MySQL, MariaDB, and MSSQL implement the
// introspector. **SQLite** returns `Checks=nil` because SQLite has no
// catalog for CHECK constraints — the only path is parsing
// `sqlite_master.sql` DDL, which is brittle and intentionally out of
// scope for the catalog-reader layer. A future F3-2-checks-sqlite
// follow-up could add DDL parsing if user demand justifies it.
type Check struct {
	Name       string
	Expression string
}

// Column is one column in a table.
//
// `Type` is the raw dialect-native type string as returned by the
// catalog (e.g. `INTEGER`, `bigint`, `character varying(255)`,
// `NVARCHAR(MAX)`). Normalisation to a cross-dialect form is the
// diff layer's responsibility (F3-3), not the introspector's.
//
// `Default` is `nil` when no default is set, and a `*string` when one
// is present — preserving the distinction between "no default" and
// "default is the empty string". The value is the raw dialect-native
// expression: a literal, a function call (`CURRENT_TIMESTAMP`,
// `gen_random_uuid()`), or `NULL`.
//
// `PrimaryKey` reports whether the column is part of the table's
// primary key (true for every member of a composite key). Populated by
// quark's Client.IntrospectSchema on all six engines and by the
// models→Schema pipeline behind Client.PlanMigration, and compared by
// quark.Diff — a PK mismatch surfaces as an OpAlterColumn, which ApplyPlan
// writes through the dialect's [ColumnAlterer] (or rebuilds the table
// under a [TableRebuilder]). `Type` stays the BARE data type even when
// PrimaryKey is set: the constraint rendering (PRIMARY KEY,
// auto-increment) is the executor's job at CREATE TABLE time, mirroring
// Client.Migrate.
type Column struct {
	Name       string
	Type       string
	Nullable   bool
	Default    *string
	PrimaryKey bool
}

// SchemaIntrospector is the optional Dialect interface for retrieving
// the current schema from the database. The same pattern as
// MigrationLocker — kept as a stand-alone interface so custom dialects
// downstream don't have to grow this method to keep compiling.
//
// IntrospectSchema returns the schema of the database the executor is
// connected to (the current schema / database / "user space",
// depending on dialect semantics). It does NOT cross schema or
// database boundaries.
//
// A dialect without it makes Client.IntrospectSchema, PlanMigration and
// Sync return ErrUnsupportedFeature.
type SchemaIntrospector interface {
	IntrospectSchema(ctx context.Context, exec Executor) (Schema, error)
}
