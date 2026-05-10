// Copyright 2026 jcsvwinston
// SPDX-License-Identifier: Apache-2.0

package quark

import (
	"reflect"
	"time"

	"github.com/jcsvwinston/quark/internal/migrate"
)

// TypeOptions carries the SQL-type sizing hints parsed from a struct's db
// tag — `size=N`, `precision=N`, `scale=N` — plus a flag indicating whether
// the column is the primary key. A zero value for any field means "use the
// dialect default for the Go type".
type TypeOptions = migrate.TypeOptions

// TypeMapper produces a dialect-specific SQL type for a Go type. The caller
// supplies the dialect name (lower-case: "postgres", "mysql", "mariadb",
// "sqlite", "mssql", "oracle") and the sizing hints from the field's tag.
type TypeMapper = migrate.TypeMapper

// RegisterTypeMapper registers a custom Go-type → SQL-type mapping for the
// migration / sync layer. Pointer types are stripped before registration:
// registering for time.Duration also covers *time.Duration. Re-registering
// the same type overwrites the previous mapper.
//
// Example: storing google/uuid.UUID as native UUID on Postgres and as a
// 36-char string on the rest:
//
//	quark.RegisterTypeMapper(reflect.TypeOf(uuid.UUID{}), func(d string, _ quark.TypeOptions) string {
//	    if d == "postgres" {
//	        return "UUID"
//	    }
//	    return "VARCHAR(36)"
//	})
//
// The mapper is consulted by client.Migrate and client.Sync. database/sql's
// Scanner / driver.Valuer interfaces still apply to the read/write side — a
// type registered here must also implement those interfaces (or be already
// supported by the underlying driver) for round-trip to work.
func RegisterTypeMapper(t reflect.Type, m TypeMapper) {
	migrate.RegisterTypeMapper(t, m)
}

// time.Duration is the canonical example shipped by Quark itself. We map it
// to BIGINT (storing the duration as nanoseconds), which is the format
// time.Duration uses natively when scanned from / written to BIGINT columns
// across drivers. Override per app if you'd rather store as a numeric
// string or a BIGINT in milliseconds.
func init() {
	RegisterTypeMapper(reflect.TypeOf(time.Duration(0)), func(dialect string, _ TypeOptions) string {
		switch dialect {
		case "oracle":
			return "NUMBER(19)"
		case "mssql":
			return "BIGINT"
		default:
			return "BIGINT"
		}
	})
}
