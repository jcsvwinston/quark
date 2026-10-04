// Copyright 2026 jcsvwinston
// SPDX-License-Identifier: Apache-2.0

package migrate

import (
	"fmt"

	"github.com/jcsvwinston/quark/quarkdriver"
)

// This file holds the per-engine answers of the six built-in dialects to the
// schema questions of quarkdriver — their column types, their auto-increment
// key and their boolean literal — and the portable answers a dialect that
// does not implement those interfaces gets.
//
// The tables are keyed by the built-in ENGINE, not by a dialect's Name(): a
// built-in dialect passes its own engine constant when it answers, so a
// dialect that wraps one under another name gets the same answer as long as
// it forwards the interface. A name that is not one of the six gets the
// portable answer.

// Engine names of the built-in dialects, as their methods pass them.
const (
	EnginePostgres = "postgres"
	EngineMySQL    = "mysql"
	EngineMariaDB  = "mariadb"
	EngineSQLite   = "sqlite"
	EngineMSSQL    = "mssql"
	EngineOracle   = "oracle"
)

// EngineColumnType spells spec as engine's column type, with the size hint
// applied the way every engine always applied it: to its default string type
// (and, on PostgreSQL and SQLite, to TEXT) and nowhere else. An engine that
// is not a built-in gets PortableColumnType.
func EngineColumnType(engine string, spec quarkdriver.ColumnSpec) string {
	base := engineBase(engine, spec)
	if spec.Size > 0 && spec.Kind != quarkdriver.KindText {
		base = applySize(base, engine, spec.Size)
	}
	return base
}

// PortableColumnType is the type a dialect that does not implement
// quarkdriver.ColumnTyper gets — or one whose ColumnType answers "".
func PortableColumnType(spec quarkdriver.ColumnSpec) string {
	return EngineColumnType("", spec)
}

// engineBase is the engine's type for a kind, before the size hint.
func engineBase(engine string, spec quarkdriver.ColumnSpec) string {
	switch spec.Kind {
	case quarkdriver.KindString:
		switch engine {
		case EnginePostgres, EngineSQLite:
			return "TEXT"
		case EngineOracle:
			return "VARCHAR2(255)"
		case EngineMSSQL:
			return "NVARCHAR(255)"
		default:
			return "VARCHAR(255)"
		}
	case quarkdriver.KindText:
		switch engine {
		case EngineMSSQL:
			return "NVARCHAR(MAX)"
		case EngineOracle:
			return "CLOB"
		default:
			return "TEXT"
		}
	case quarkdriver.KindInt16:
		if engine == EngineOracle {
			return "NUMBER(5)"
		}
		return "SMALLINT"
	case quarkdriver.KindInt32:
		if engine == EngineOracle {
			return "NUMBER(10)"
		}
		return "INTEGER"
	case quarkdriver.KindInt64:
		// Go's int is 64-bit on every platform Quark supports, so it takes
		// the wide type rather than gambling on the target.
		if engine == EngineOracle {
			return "NUMBER(19)"
		}
		return "BIGINT"
	case quarkdriver.KindFloat32:
		if engine == EngineOracle {
			return "BINARY_FLOAT"
		}
		return "REAL"
	case quarkdriver.KindFloat64:
		// REAL is single precision on PostgreSQL — about seven significant
		// digits, where a float64 carries fifteen. SQLite's REAL is an
		// 8-byte IEEE double, so it was never wrong there.
		switch engine {
		case EngineSQLite:
			return "REAL"
		case EngineMySQL, EngineMariaDB:
			return "DOUBLE"
		case EngineMSSQL:
			return "FLOAT(53)"
		case EngineOracle:
			return "BINARY_DOUBLE"
		default:
			return "DOUBLE PRECISION"
		}
	case quarkdriver.KindDecimal:
		// The engine's fixed-point decimal at the declared precision and
		// scale: NUMBER(p,s) on Oracle, DECIMAL(p,s) everywhere else.
		family := "DECIMAL"
		if engine == EngineOracle {
			family = "NUMBER"
		}
		if spec.Scale == 0 {
			return fmt.Sprintf("%s(%d)", family, spec.Precision)
		}
		return fmt.Sprintf("%s(%d,%d)", family, spec.Precision, spec.Scale)
	case quarkdriver.KindBool:
		switch engine {
		case EngineOracle:
			return "NUMBER(1)"
		case EngineMSSQL:
			return "BIT"
		default:
			return "BOOLEAN"
		}
	case quarkdriver.KindTime:
		switch engine {
		case EngineSQLite, EngineMySQL, EngineMariaDB:
			return "DATETIME"
		case EngineMSSQL:
			return "DATETIME2"
		default:
			return "TIMESTAMP"
		}
	case quarkdriver.KindBytes:
		switch engine {
		case EnginePostgres:
			return "BYTEA"
		case EngineMSSQL:
			return "VARBINARY(MAX)"
		default:
			return "BLOB"
		}
	case quarkdriver.KindJSON:
		return jsonColumnType(engine)
	case quarkdriver.KindUUID:
		// The engine's uuid type where one exists and a 36-character text
		// column where it does not (A8 S5). SQL Server's UNIQUEIDENTIFIER is
		// NOT used: its driver scans the value as sixteen bytes in the
		// engine's mixed-endian order, which a Scanner expecting the RFC
		// order reads as a different UUID.
		switch engine {
		case EnginePostgres:
			return "UUID"
		case EngineOracle:
			return "VARCHAR2(36)"
		case EngineMSSQL:
			return "NCHAR(36)"
		case EngineSQLite:
			// Any type name is legal; UUID names the intent, and text
			// stores as text under its NUMERIC affinity because a UUID is
			// never a well-formed number.
			return "UUID"
		default:
			return "CHAR(36)"
		}
	case quarkdriver.KindIP:
		// PostgreSQL's inet, text elsewhere (A8 S6).
		switch engine {
		case EnginePostgres:
			return "INET"
		case EngineOracle:
			return "VARCHAR2(45)"
		case EngineMSSQL:
			return "NVARCHAR(45)"
		case EngineSQLite:
			return "TEXT"
		default:
			return "VARCHAR(45)"
		}
	case quarkdriver.KindArray:
		// PostgreSQL's array of the element type; JSON on the others.
		if engine == EnginePostgres {
			if elem := engineBase(engine, quarkdriver.ColumnSpec{Kind: spec.Elem}); spec.Elem != quarkdriver.KindOther && elem != "" {
				return elem + "[]"
			}
		}
		return jsonColumnType(engine)
	case quarkdriver.KindRange:
		// PostgreSQL's range type for the bound; JSON on the others.
		if engine == EnginePostgres {
			switch spec.Elem {
			case quarkdriver.KindTime:
				return "TSTZRANGE"
			case quarkdriver.KindInt64:
				return "INT8RANGE"
			case quarkdriver.KindInt32:
				return "INT4RANGE"
			case quarkdriver.KindFloat32, quarkdriver.KindFloat64:
				return "NUMRANGE"
			}
		}
		return jsonColumnType(engine)
	}
	// KindOther: a value Quark has no portable kind for.
	return "TEXT"
}

// jsonColumnType returns the engine-native column type for a JSON payload.
func jsonColumnType(engine string) string {
	switch engine {
	case EnginePostgres:
		return "JSONB"
	case EngineMySQL, EngineMariaDB:
		return "JSON"
	case EngineMSSQL:
		return "NVARCHAR(MAX)"
	case EngineOracle:
		return "CLOB"
	default:
		return "TEXT"
	}
}

// applySize rewrites a VARCHAR/CHAR/NVARCHAR family default with an explicit
// size. Engines that emit TEXT for the default (postgres/sqlite) get a
// VARCHAR(N) instead, matching what callers usually mean when they ask for
// a sized string.
func applySize(base, engine string, size int) string {
	switch engine {
	case EnginePostgres, EngineSQLite:
		if base == "TEXT" {
			return fmt.Sprintf("VARCHAR(%d)", size)
		}
	case EngineOracle:
		if base == "VARCHAR2(255)" {
			return fmt.Sprintf("VARCHAR2(%d)", size)
		}
	case EngineMSSQL:
		if base == "NVARCHAR(255)" {
			return fmt.Sprintf("NVARCHAR(%d)", size)
		}
	default:
		if base == "VARCHAR(255)" {
			return fmt.Sprintf("VARCHAR(%d)", size)
		}
	}
	return base
}

// EngineAutoIncrement is engine's auto-increment key — the fragment after the
// column name, and the catalog's data type for it when that is not the
// field's ordinary type. 64-bit throughout (QK-21): SERIAL and INT are four
// bytes, so an auto-increment key ran out at 2,147,483,647 rows — and a model
// assigning its own int64 key was rejected outright by MySQL and PostgreSQL.
// SQLite's INTEGER PRIMARY KEY is the 64-bit rowid already, and Oracle's
// NUMBER has no width problem. An engine that is not a built-in gets
// PortableAutoIncrement.
func EngineAutoIncrement(engine string) (definition, dataType string) {
	switch engine {
	case EngineSQLite:
		return "INTEGER PRIMARY KEY AUTOINCREMENT", "INTEGER"
	case EnginePostgres:
		return "BIGSERIAL PRIMARY KEY", ""
	case EngineMySQL, EngineMariaDB:
		return "BIGINT AUTO_INCREMENT PRIMARY KEY", ""
	case EngineMSSQL:
		return "BIGINT IDENTITY(1,1) PRIMARY KEY", ""
	case EngineOracle:
		return "NUMBER GENERATED ALWAYS AS IDENTITY PRIMARY KEY", ""
	default:
		return PortableAutoIncrement()
	}
}

// PortableAutoIncrement is the key a dialect that does not implement
// quarkdriver.AutoIncrementer gets: the SQL-standard identity column. An
// engine that does not accept it fails the CREATE TABLE instead of creating a
// key that stays NULL — which is what a dialect under an unknown name got
// before A11 Q2 ("BIGINT PRIMARY KEY").
func PortableAutoIncrement() (definition, dataType string) {
	return "BIGINT GENERATED BY DEFAULT AS IDENTITY PRIMARY KEY", "BIGINT"
}

// EngineBoolLiteral is engine's literal for a boolean DEFAULT: TRUE/FALSE on
// PostgreSQL, whose BOOLEAN rejects 1/0; 1/0 on the other five, where
// SQL Server's BIT and Oracle's NUMBER(1) reject TRUE/FALSE. An engine that
// is not a built-in gets PortableBoolLiteral.
func EngineBoolLiteral(engine string, v bool) string {
	switch engine {
	case EnginePostgres, "postgresql":
		return PortableBoolLiteral(v)
	case EngineMySQL, EngineMariaDB, EngineSQLite, EngineMSSQL, EngineOracle:
		if v {
			return "1"
		}
		return "0"
	default:
		return PortableBoolLiteral(v)
	}
}

// PortableBoolLiteral is the SQL-standard boolean literal, the one that goes
// with the portable BOOLEAN type.
func PortableBoolLiteral(v bool) string {
	if v {
		return "TRUE"
	}
	return "FALSE"
}
