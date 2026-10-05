// Copyright 2026 jcsvwinston
// SPDX-License-Identifier: Apache-2.0

package quark

import (
	"context"
	"fmt"

	"github.com/jcsvwinston/quark/quarkdriver"
)

// The schema model — what a dialect's IntrospectSchema returns and what
// PlanMigration and Diff compare — is declared in quarkdriver with the
// introspection contract (ADR-0026), where each type is documented: a
// third-party engine takes part in migrations by answering
// SchemaIntrospector, and it answers in these types. The names below are
// aliases of the same types; nothing that builds or reads a quark.Schema
// changes.

// Schema is the dialect-neutral representation of a database schema.
// Tables are sorted by Name for deterministic ordering.
type Schema = quarkdriver.Schema

// Table represents one table in the schema.
type Table = quarkdriver.Table

// Index is one secondary (non-primary-key) index on a table.
type Index = quarkdriver.Index

// ForeignKey is one FOREIGN KEY constraint declared on a table.
type ForeignKey = quarkdriver.ForeignKey

// Check is one CHECK constraint declared on a table, with its raw catalog
// expression.
type Check = quarkdriver.Check

// Column is one column in a table.
type Column = quarkdriver.Column

// SchemaIntrospector is the optional Dialect interface for retrieving
// the current schema from the database; [Client.IntrospectSchema],
// [Client.PlanMigration] and [Client.Sync] assert it on the dialect.
type SchemaIntrospector = quarkdriver.SchemaIntrospector

// ColumnTypeMapper is the optional Dialect interface for translating a
// neutral/foreign column-type string (the "TEXT" of a hand-built [Plan])
// into the dialect's native form before it reaches DDL.
type ColumnTypeMapper = quarkdriver.ColumnTypeMapper

// mapColumnType translates a column-type string to the dialect's native
// form when the dialect implements [ColumnTypeMapper], and returns it
// unchanged otherwise.
func (c *Client) mapColumnType(t string) string {
	if m, ok := c.dialect.(ColumnTypeMapper); ok {
		return m.MapColumnType(t)
	}
	return t
}

// IntrospectSchema reads the current state of the database's schema and
// returns it as a dialect-neutral [Schema]. It's the first half of the
// F3 migration story: the diff comparator (F3-3) takes the Schema
// produced here plus the Schema derived from the Go models and emits
// the operations needed to bring them into alignment.
//
// Supported dialects: PostgreSQL, SQLite, MySQL, MariaDB, MSSQL, Oracle.
// A dialect that doesn't implement [SchemaIntrospector] returns
// `ErrUnsupportedFeature`.
//
// Surface: tables, columns (primary-key membership included via
// [Column.PrimaryKey]), non-PK indexes, foreign keys, and CHECK
// constraints. SQLite returns `Checks=nil` (the only catalog read it
// doesn't implement; see the [Check] godoc for the rationale).
//
// Code that reads [Schema] should treat the unpopulated slices as
// "not yet introspected" (or, for SQLite Checks, "intentionally not
// surfaced"), not "no constraints exist".
func (c *Client) IntrospectSchema(ctx context.Context) (Schema, error) {
	introspector, ok := c.dialect.(SchemaIntrospector)
	if !ok {
		return Schema{}, fmt.Errorf("%w: dialect %s does not yet support schema introspection (F3-2)", ErrUnsupportedFeature, c.dialect.Name())
	}
	return introspector.IntrospectSchema(ctx, c.db)
}
