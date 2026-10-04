// Copyright 2026 jcsvwinston
// SPDX-License-Identifier: Apache-2.0

package migrate

import (
	"fmt"
	"strings"

	"github.com/jcsvwinston/quark/quarkdriver"
)

// This file is where the schema path asks a dialect what it writes: each
// function asserts one of quarkdriver's optional schema interfaces on the
// dialect and falls back to the documented portable answer when the dialect
// does not implement it (or answers ""). Package quark and package migrate
// (the versioned migrator's ledger) both ask through here, so the fallbacks
// live in one place.

// Dialect is the part of quark.Dialect the questions here need.
type Dialect interface {
	Name() string
	Quote(identifier string) string
}

// DialectAsker answers the column-type pipeline's questions from d's
// optional interfaces: quarkdriver.ColumnTyper for column types and the
// boolean literal, quarkdriver.AutoIncrementer for the auto-increment key.
func DialectAsker(d Dialect) Asker {
	typer, _ := d.(quarkdriver.ColumnTyper)
	auto, _ := d.(quarkdriver.AutoIncrementer)
	return Asker{
		Name: d.Name(),
		Column: func(s quarkdriver.ColumnSpec) string {
			if typer != nil {
				if t := typer.ColumnType(s); t != "" {
					return t
				}
			}
			return PortableColumnType(s)
		},
		AutoIncrement: func() (string, string) {
			if auto != nil {
				if def, dataType := auto.AutoIncrementColumn(); def != "" {
					return def, dataType
				}
			}
			return PortableAutoIncrement()
		},
		BoolLiteral: func(v bool) string {
			if typer != nil {
				if l := typer.BoolLiteral(v); l != "" {
					return l
				}
			}
			return PortableBoolLiteral(v)
		},
	}
}

// CreateTableIfNotExists is the statement that creates table with body only
// when it does not exist, as d's quarkdriver.IdempotentDDL writes it, or
// CREATE TABLE IF NOT EXISTS.
func CreateTableIfNotExists(d Dialect, table, body string) string {
	if i, ok := d.(quarkdriver.IdempotentDDL); ok {
		if s := i.CreateTableIfNotExists(table, body); s != "" {
			return s
		}
	}
	return PortableCreateTable(d, "IF NOT EXISTS ", table, body)
}

// CreateIndexIfNotExists is the statement that creates an index only when it
// does not exist, as d's quarkdriver.IdempotentDDL writes it, or CREATE
// [UNIQUE] INDEX IF NOT EXISTS.
func CreateIndexIfNotExists(d Dialect, table, index string, columns []string, unique bool) string {
	if i, ok := d.(quarkdriver.IdempotentDDL); ok {
		if s := i.CreateIndexIfNotExists(table, index, columns, unique); s != "" {
			return s
		}
	}
	return PortableCreateIndex(d, "IF NOT EXISTS ", table, index, columns, unique)
}

// IsAlreadyExists reports whether err is the error d's engine raises when the
// object a CREATE (or ADD CONSTRAINT) names exists already. A dialect that
// does not implement quarkdriver.IdempotentDDL recognises none.
func IsAlreadyExists(d Dialect, object quarkdriver.SchemaObject, err error) bool {
	if err == nil {
		return false
	}
	if i, ok := d.(quarkdriver.IdempotentDDL); ok {
		return i.IsAlreadyExists(object, err)
	}
	return false
}

// PortableCreateTable writes CREATE TABLE <ifNotExists><table> (<body>), with
// the table quoted by d. ifNotExists is "IF NOT EXISTS " or "".
func PortableCreateTable(d Dialect, ifNotExists, table, body string) string {
	return fmt.Sprintf("CREATE TABLE %s%s (\n  %s\n)", ifNotExists, d.Quote(table), body)
}

// PortableCreateIndex writes CREATE [UNIQUE] INDEX <ifNotExists><index> ON
// <table> (<columns>), every name quoted by d.
func PortableCreateIndex(d Dialect, ifNotExists, table, index string, columns []string, unique bool) string {
	quoted := make([]string, len(columns))
	for i, col := range columns {
		quoted[i] = d.Quote(col)
	}
	kw := ""
	if unique {
		kw = "UNIQUE "
	}
	return fmt.Sprintf("CREATE %sINDEX %s%s ON %s (%s)", kw, ifNotExists, d.Quote(index), d.Quote(table), strings.Join(quoted, ", "))
}
