// Copyright 2026 jcsvwinston
// SPDX-License-Identifier: Apache-2.0

package domain

import (
	"fmt"
	"reflect"

	"github.com/google/uuid"
	"github.com/shopspring/decimal"

	"github.com/jcsvwinston/quark"
)

// init registers the per-dialect SQL type for the two custom column types
// the domain uses. Quark pre-wires time.Duration (BIGINT) but not these.
// Importing this package is enough — RegisterTypeMapper mutates a global
// registry keyed by reflect.Type.
func init() {
	quark.RegisterTypeMapper(reflect.TypeOf(uuid.UUID{}), func(dialect string, _ quark.TypeOptions) string {
		switch dialect {
		case "postgres":
			return "UUID"
		case "oracle":
			return "VARCHAR2(36)"
		default: // mysql, mariadb, sqlite, mssql
			// NOT mssql UNIQUEIDENTIFIER: SQL Server stores a GUID's first
			// three groups little-endian while google/uuid is big-endian, so
			// a uuid.UUID round-trips byte-swapped (silent corruption). Text
			// storage sidesteps it — this matches Quark's own type_mapper.go
			// example. The UNIQUEIDENTIFIER footgun is filed under TASKS.md
			// § "Bug-bash hallazgos" (F1, BB-1).
			return "VARCHAR(36)"
		}
	})

	quark.RegisterTypeMapper(reflect.TypeOf(decimal.Decimal{}), func(dialect string, opts quark.TypeOptions) string {
		precision, scale := opts.Precision, opts.Scale
		if precision == 0 {
			// Money-grade default when the field omits precision/scale.
			precision, scale = 18, 4
		}
		switch dialect {
		case "oracle":
			return fmt.Sprintf("NUMBER(%d,%d)", precision, scale)
		case "sqlite":
			// SQLite is dynamically typed; NUMERIC keeps the affinity
			// without enforcing precision it cannot honour anyway.
			return "NUMERIC"
		default: // postgres, mysql, mariadb, mssql
			return fmt.Sprintf("DECIMAL(%d,%d)", precision, scale)
		}
	})
}
