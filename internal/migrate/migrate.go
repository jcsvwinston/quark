// Package migrate provides internal utilities for database schema migrations.
package migrate

import (
	"reflect"
)

// SQLType maps Go types to SQL types for the given dialect name.
//
// When isPK is true the column DDL includes the PRIMARY KEY constraint.
// The exact type depends on the Go field kind:
//
//   - int / int64 → dialect-native auto-increment (SERIAL, AUTO_INCREMENT, IDENTITY…)
//   - string      → VARCHAR(36) PRIMARY KEY — UUID-friendly; no auto-increment
//   - anything else → its natural SQL type + PRIMARY KEY (no auto-increment)
func SQLType(dialectName string, t reflect.Type, isPK bool) string {
	if isPK {
		// Unwrap pointer (e.g. *string, *int64)
		base := t
		if base.Kind() == reflect.Ptr {
			base = base.Elem()
		}

		switch base.Kind() {
		case reflect.String:
			// UUID / ULID / KSUID — caller supplies the value, no auto-generation.
			switch dialectName {
			case "oracle":
				return "VARCHAR2(36) PRIMARY KEY"
			case "mssql":
				return "NVARCHAR(36) PRIMARY KEY"
			default:
				return "VARCHAR(36) PRIMARY KEY"
			}
		case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64,
			reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
			// Integer PK → auto-increment
			switch dialectName {
			case "sqlite":
				return "INTEGER PRIMARY KEY AUTOINCREMENT"
			case "postgres":
				return "SERIAL PRIMARY KEY"
			case "mysql", "mariadb":
				return "INT AUTO_INCREMENT PRIMARY KEY"
			case "mssql":
				return "INT IDENTITY(1,1) PRIMARY KEY"
			case "oracle":
				return "NUMBER GENERATED ALWAYS AS IDENTITY PRIMARY KEY"
			default:
				return "INTEGER PRIMARY KEY"
			}
		default:
			// Composite-PK columns, bool, float, etc. — just append PRIMARY KEY.
			return SQLType(dialectName, t, false) + " PRIMARY KEY"
		}
	}

	// Handle pointers (e.g. *time.Time, *string)
	if t.Kind() == reflect.Ptr {
		t = t.Elem()
	}

	switch t.Kind() {
	case reflect.String:
		switch dialectName {
		case "postgres", "sqlite":
			return "TEXT"
		case "oracle":
			return "VARCHAR2(255)"
		case "mssql":
			return "NVARCHAR(255)"
		default:
			return "VARCHAR(255)"
		}
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64,
		reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
		if dialectName == "oracle" {
			return "NUMBER(19)"
		}
		return "INTEGER"
	case reflect.Float32, reflect.Float64:
		switch dialectName {
		case "sqlite", "postgres":
			return "REAL"
		case "mysql", "mariadb":
			return "DOUBLE"
		case "oracle", "mssql":
			return "FLOAT"
		default:
			return "REAL"
		}
	case reflect.Bool:
		switch dialectName {
		case "oracle":
			return "NUMBER(1)"
		case "mssql":
			return "BIT"
		default:
			return "BOOLEAN"
		}
	case reflect.Struct:
		if t.String() == "time.Time" {
			switch dialectName {
			case "sqlite", "mysql", "mariadb":
				return "DATETIME"
			case "postgres", "oracle":
				return "TIMESTAMP"
			case "mssql":
				return "DATETIME2"
			default:
				return "TIMESTAMP"
			}
		}
	}

	return "TEXT" // Fallback
}
