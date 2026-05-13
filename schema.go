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
	Name    string
	Columns []Column

	// Indexes / ForeignKeys / Checks are intentionally left out of the
	// minimum F3-2 surface. They land in follow-up PRs once the
	// column-level diff is proven against the live engines:
	//   - F3-2-indexes  → reads idx metadata per dialect
	//   - F3-2-fks      → reads foreign key constraints
	//   - F3-2-checks   → reads CHECK constraints
	// The Schema struct will grow these fields in those PRs; tests and
	// downstream code that read this struct should treat zero-values
	// as "not yet introspected" rather than "no constraints".
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
// Supported dialects (this PR): PostgreSQL, SQLite. Other dialects
// (MySQL, MariaDB, MSSQL, Oracle) return `ErrUnsupportedFeature`
// until their follow-up F3-2-* PRs land.
//
// The minimum surface for F3-2 is tables + columns; indexes, foreign
// keys, and check constraints arrive in follow-up PRs. Code that
// reads [Schema] should treat the unpopulated slices as "not yet
// introspected", not "no constraints exist".
func (c *Client) IntrospectSchema(ctx context.Context) (Schema, error) {
	introspector, ok := c.dialect.(SchemaIntrospector)
	if !ok {
		return Schema{}, fmt.Errorf("%w: dialect %s does not yet support schema introspection (F3-2)", ErrUnsupportedFeature, c.dialect.Name())
	}
	return introspector.IntrospectSchema(ctx, c.db)
}
