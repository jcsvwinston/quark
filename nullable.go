// Copyright 2026 jcsvwinston
// SPDX-License-Identifier: Apache-2.0

package quark

import "database/sql"

// Nullable[T] is a generic wrapper for a column value that may be SQL NULL.
// It is a thin alias of database/sql.Null[T] (Go 1.22+) so a Nullable[T]
// already implements both [database/sql.Scanner] and [database/sql/driver.Valuer]
// — drivers handle the round-trip through their existing fast paths and
// Quark's reflect-based scan / write code does not need to special-case it.
//
// Replace the long-standing pointer-as-nullable idiom (e.g. *time.Time,
// *string) with Nullable[T] when you want explicit "is set" semantics
// without a heap allocation per field. The migrate layer recognises the
// type and emits the SQL type for T (no NOT NULL, since the column is
// nullable by definition).
//
// Example:
//
//	type Profile struct {
//	    ID   int64                `db:"id" pk:"true"`
//	    Bio  quark.Nullable[string]    `db:"bio"`
//	    Born quark.Nullable[time.Time] `db:"born"`
//	}
//
//	p := Profile{
//	    Bio:  quark.SomeOf("hello"),
//	    Born: quark.NullOf[time.Time](),
//	}
type Nullable[T any] = sql.Null[T]

// SomeOf returns a non-null Nullable[T] wrapping v. Equivalent to
// Nullable[T]{V: v, Valid: true} — provided as a constructor so callers
// don't have to spell the struct literal.
func SomeOf[T any](v T) Nullable[T] {
	return Nullable[T]{V: v, Valid: true}
}

// NullOf returns the SQL-NULL value of Nullable[T] — Nullable[T]{} with the
// generic spelt out at the call site for readability.
func NullOf[T any]() Nullable[T] {
	return Nullable[T]{}
}

// nullBytesArg substitutes a typed nil []byte for a SQL-NULL Nullable[[]byte].
// An invalid sql.Null[[]byte] returns an untyped nil from its driver.Valuer,
// which go-mssqldb encodes as an nvarchar NULL — and SQL Server then rejects
// the INSERT into a varbinary column ("implicit conversion from nvarchar to
// varbinary(max) is not allowed"). A typed nil []byte encodes as a binary
// NULL, which is correct on all six engines, so this normalisation is applied
// unconditionally (BB-6).
func nullBytesArg(val any) any {
	if nb, ok := val.(Nullable[[]byte]); ok && !nb.Valid {
		return []byte(nil)
	}
	return val
}
