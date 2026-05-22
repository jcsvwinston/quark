// Copyright 2026 jcsvwinston
// SPDX-License-Identifier: Apache-2.0

package quark

import (
	"database/sql/driver"
	"encoding/json"
	"fmt"
)

// JSON[T] wraps a Go value so it round-trips through a SQL JSON column via
// encoding/json. Use it when you want a typed view of a JSON-shaped column
// without writing scan / value code by hand.
//
// JSON implements [database/sql.Scanner] and [database/sql/driver.Valuer], so
// every driver Quark supports handles the round-trip with no extra reflect
// in Quark's hot paths. The migrate layer detects JSON[T] and emits the
// dialect-native JSON column type:
//
//	Postgres → JSONB
//	MySQL / MariaDB → JSON
//	SQLite → TEXT (with json_* functions available at query time)
//	SQL Server → NVARCHAR(MAX)
//	Oracle → CLOB
//
// Example:
//
//	type Settings struct{ Theme string `json:"theme"`; Volume int `json:"volume"` }
//
//	type Profile struct {
//	    ID       int64                  `db:"id" pk:"true"`
//	    Settings quark.JSON[Settings]   `db:"settings"`
//	}
//
//	p := Profile{Settings: quark.JSON[Settings]{V: Settings{Theme: "dark", Volume: 7}}}
//	_ = client.Migrate(ctx, &Profile{})
//	_ = quark.For[Profile](ctx, client).Create(&p)
type JSON[T any] struct {
	V T
}

// Value implements driver.Valuer by JSON-marshalling V into the column.
//
// The marshalled JSON is returned as a string, not []byte: go-mssqldb
// binds a []byte parameter as VARBINARY, and storing that into the
// NVARCHAR(MAX) JSON column triggers an implicit VARBINARY→NVARCHAR
// conversion that reinterprets the UTF-8 bytes as UTF-16 and corrupts
// the payload. Returning a string binds as NVARCHAR (and as the
// equivalent text type on every other driver), so the round-trip is
// clean across all dialects.
func (j JSON[T]) Value() (driver.Value, error) {
	b, err := json.Marshal(j.V)
	if err != nil {
		return nil, fmt.Errorf("JSON.Value: marshal %T: %w", j.V, err)
	}
	return string(b), nil
}

// Scan implements sql.Scanner. Accepts []byte and string sources (the two
// forms drivers return for JSON columns). NULL clears V to its zero value
// rather than erroring; pair with quark.Nullable[JSON[T]] when you want to
// distinguish NULL from "valid but empty" payloads.
func (j *JSON[T]) Scan(src any) error {
	if src == nil {
		var zero T
		j.V = zero
		return nil
	}
	var data []byte
	switch v := src.(type) {
	case []byte:
		data = v
	case string:
		data = []byte(v)
	default:
		return fmt.Errorf("JSON.Scan: unsupported source type %T", src)
	}
	if len(data) == 0 {
		var zero T
		j.V = zero
		return nil
	}
	if err := json.Unmarshal(data, &j.V); err != nil {
		return fmt.Errorf("JSON.Scan: unmarshal into %T: %w", j.V, err)
	}
	return nil
}
