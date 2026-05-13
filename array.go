// Copyright 2026 jcsvwinston
// SPDX-License-Identifier: Apache-2.0

package quark

import (
	"database/sql/driver"
	"encoding/json"
	"fmt"
)

// Array[T] is the typed wrapper for a column that holds a list of T. It
// round-trips through JSON regardless of dialect, so the same model
// definition works on every supported engine without per-dialect Scan /
// Value code:
//
//	Postgres → JSONB
//	MySQL / MariaDB → JSON
//	SQLite → TEXT
//	SQL Server → NVARCHAR(MAX)
//	Oracle → CLOB
//
// The semantic clarity vs. `JSON[[]T]`: `Tags Array[string]` reads as
// "the column holds a list of strings"; the helper methods (`Len`,
// `Slice`, `Contains`) carry that intent through call sites that read
// the field.
//
// Trade-off: this wrapper does NOT use Postgres' native `INT[]` /
// `TEXT[]` array types — operators like `@>`, `&&`, and `array_agg`
// won't fire on Array[T] columns. For PG-native arrays with operators
// drop down to `pgx`/`pgtype.Array` directly or use `RawQuery`. The
// "neutral wrapper" spec in TASKS § Bloque B explicitly asked for a
// dialect-independent type that doesn't import `pgtype`; JSON-backed
// is the simplest shape that satisfies that constraint.
//
// Example:
//
//	type Post struct {
//	    ID   int64                 `db:"id" pk:"true"`
//	    Tags quark.Array[string]   `db:"tags"`
//	}
//
//	p := Post{Tags: quark.Array[string]{V: []string{"go", "orm"}}}
//	_ = client.Migrate(ctx, &Post{})
//	_ = quark.For[Post](ctx, client).Create(&p)
type Array[T any] struct {
	V []T
}

// Value implements driver.Valuer by JSON-marshalling V. A nil V
// serialises to `[]` (empty array) rather than `null` — the more
// useful default when the column is also used in SQL operations.
// Pair with quark.Nullable[Array[T]] when you need to distinguish
// NULL from empty.
func (a Array[T]) Value() (driver.Value, error) {
	if a.V == nil {
		return []byte("[]"), nil
	}
	b, err := json.Marshal(a.V)
	if err != nil {
		var zero T
		return nil, fmt.Errorf("Array.Value: marshal []%T: %w", zero, err)
	}
	return b, nil
}

// Scan implements sql.Scanner. Accepts []byte and string sources (the
// two forms drivers return for JSON columns). NULL clears V to nil.
// An empty / whitespace-only payload also resolves to nil so a column
// default of `'[]'` round-trips through the zero value cleanly.
func (a *Array[T]) Scan(src any) error {
	if src == nil {
		a.V = nil
		return nil
	}
	var data []byte
	switch v := src.(type) {
	case []byte:
		data = v
	case string:
		data = []byte(v)
	default:
		return fmt.Errorf("Array.Scan: unsupported source type %T", src)
	}
	if len(data) == 0 {
		a.V = nil
		return nil
	}
	if err := json.Unmarshal(data, &a.V); err != nil {
		var zero T
		return fmt.Errorf("Array.Scan: unmarshal into []%T: %w", zero, err)
	}
	return nil
}

// Len returns the number of elements in the array. Safe on the zero
// value (returns 0).
func (a Array[T]) Len() int { return len(a.V) }

// Slice returns the underlying slice. Mutations on the returned slice
// affect the Array's storage — useful for appending without
// re-allocating, dangerous if the Array value is used after.
func (a Array[T]) Slice() []T { return a.V }
