// Copyright 2026 jcsvwinston
// SPDX-License-Identifier: Apache-2.0

package quark

import (
	"context"
	"fmt"
)

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

	// Checks is intentionally left out of the F3-2 minimum surface.
	// It lands in F3-2-checks once the column / index / FK paths are
	// proven across the engines. Tests and downstream code that read
	// this struct should treat zero-values as "not yet introspected"
	// rather than "no constraints".
}

// Index is one secondary (non-primary-key) index on a table. The PK
// is a constraint rather than an index in the diff model and lives on
// the Column (via Default / future PrimaryKey field), not here. The
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
// captures the surface that [Client.AddForeignKey] takes as input so
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
// with underscores, etc.). All four implemented dialects emit
// `"NO ACTION"` explicitly for the SQL-standard default — the empty
// string never appears here in practice. A future Oracle introspector
// (F3-2-oracle) is expected to follow the same convention.
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
type Column struct {
	Name     string
	Type     string
	Nullable bool
	Default  *string
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
type SchemaIntrospector interface {
	IntrospectSchema(ctx context.Context, exec Executor) (Schema, error)
}

// IntrospectSchema reads the current state of the database's schema and
// returns it as a dialect-neutral [Schema]. It's the first half of the
// F3 migration story: the diff comparator (F3-3) takes the Schema
// produced here plus the Schema derived from the Go models and emits
// the operations needed to bring them into alignment.
//
// Supported dialects: PostgreSQL, SQLite, MySQL, MariaDB, MSSQL.
// Oracle returns `ErrUnsupportedFeature` until F3-2-oracle lands
// (deferred while the container image situation is resolved).
//
// Surface: tables, columns, non-PK indexes, and foreign keys. Check
// constraints arrive in F3-2-checks. Code that reads [Schema] should
// treat the unpopulated slices as "not yet introspected", not "no
// constraints exist".
func (c *Client) IntrospectSchema(ctx context.Context) (Schema, error) {
	introspector, ok := c.dialect.(SchemaIntrospector)
	if !ok {
		return Schema{}, fmt.Errorf("%w: dialect %s does not yet support schema introspection (F3-2)", ErrUnsupportedFeature, c.dialect.Name())
	}
	return introspector.IntrospectSchema(ctx, c.db)
}
