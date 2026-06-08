// Package migrate provides internal utilities for database schema migrations.
package migrate

import (
	"fmt"
	"reflect"
	"strings"
	"sync"
)

// TypeOptions carry the SQL-type sizing hints parsed from a struct's db tag,
// e.g. db:"name,size=512" or db:"price,precision=18,scale=4". A zero value
// means "use the dialect default".
type TypeOptions struct {
	Size      int
	Precision int
	Scale     int
	IsPK      bool
}

// TypeMapper produces a dialect-specific SQL type for a Go type. The caller
// supplies the dialect name (lower-case: "postgres", "mysql", ...) and the
// sizing hints from the field's tag. Implementations should fall back to
// sensible defaults if Size/Precision/Scale are zero.
type TypeMapper func(dialect string, opts TypeOptions) string

// typeMapperRegistry stores the registered mappings. Keyed by reflect.Type
// (the canonical, pointer-stripped form). Using sync.Map keeps reads
// lock-free on the hot path of every CREATE TABLE statement.
var typeMapperRegistry sync.Map // map[reflect.Type]TypeMapper

// RegisterTypeMapper registers a custom Go-type → SQL-type mapping. The
// public API in package quark forwards to this; the registry lives here
// because internal/migrate.SQLType is the only consumer and we want the
// lookup to stay close to the lookup site.
//
// Pointer types are stripped before registration: registering for
// time.Duration also covers *time.Duration. Re-registering the same type
// overwrites the previous mapper.
func RegisterTypeMapper(t reflect.Type, m TypeMapper) {
	if t == nil || m == nil {
		return
	}
	if t.Kind() == reflect.Ptr {
		t = t.Elem()
	}
	typeMapperRegistry.Store(t, m)
}

// LookupTypeMapper returns the registered mapper for t (pointer stripped).
// Returns nil if no mapping is registered.
func LookupTypeMapper(t reflect.Type) TypeMapper {
	if t == nil {
		return nil
	}
	if t.Kind() == reflect.Ptr {
		t = t.Elem()
	}
	if v, ok := typeMapperRegistry.Load(t); ok {
		return v.(TypeMapper)
	}
	return nil
}

// SQLTypeWithOpts is the extended form of SQLType that propagates the field's
// sizing hints to the type mapper. It is the preferred entry point for the
// migrate / sync layers; SQLType remains as a convenience wrapper for
// callers that don't (yet) have TypeOptions.
func SQLTypeWithOpts(dialectName string, t reflect.Type, opts TypeOptions) string {
	// Strip pointer wrapper so *T and T resolve to the same SQL type.
	if t != nil && t.Kind() == reflect.Ptr {
		t = t.Elem()
	}

	// Custom mappers take precedence over the built-in switch — except for
	// PK columns, which need the dialect-specific PRIMARY KEY suffix the
	// SQLType builder appends. Custom mappers can opt in to PK handling by
	// reading opts.IsPK and emitting the suffix themselves.
	if mapper := LookupTypeMapper(t); mapper != nil {
		return mapper(dialectName, opts)
	}

	// sql.Null[T] (re-exported as quark.Nullable[T]): unwrap and recurse
	// into T so the column gets T's SQL type. The wrapper itself is just a
	// (V T, Valid bool) pair plus Scanner+Valuer; the storage type is T's.
	if isSQLNull(t) {
		if vf, ok := t.FieldByName("V"); ok {
			return SQLTypeWithOpts(dialectName, vf.Type, opts)
		}
	}

	// quark.JSON[T]: emit the dialect-native JSON column type. Detection
	// is by package + name prefix; T's identity is irrelevant since the
	// column always stores serialised bytes.
	if isQuarkJSON(t) {
		return jsonColumnType(dialectName)
	}

	// quark.Array[T]: same JSON wire format as JSON[T], so same column
	// type per dialect. The semantic split (Array for lists, JSON for
	// arbitrary documents) is purely on the Go side; on the SQL side
	// both serialise to the same JSON-shaped column.
	if isQuarkArray(t) {
		return jsonColumnType(dialectName)
	}

	// Apply size/precision overrides where they apply naturally to the
	// built-in switch. This wraps SQLType so the existing big switch stays
	// intact.
	base := SQLType(dialectName, t, opts.IsPK)
	if opts.Size > 0 {
		base = applySize(base, dialectName, opts.Size)
	}
	if opts.Precision > 0 {
		base = applyPrecisionScale(base, dialectName, opts.Precision, opts.Scale)
	}
	return base
}

// IsBoolColumn reports whether t maps to a boolean column. It unwraps a pointer
// (*bool) and the sql.Null[bool] / quark.Nullable[bool] wrapper the same way
// SQLTypeWithOpts resolves the column's SQL type, so the default-normalization
// decision stays consistent with the emitted column type. Callers use it to
// gate NormalizeBoolDefault.
func IsBoolColumn(t reflect.Type) bool {
	if t == nil {
		return false
	}
	if t.Kind() == reflect.Ptr {
		t = t.Elem()
	}
	if isSQLNull(t) {
		if vf, ok := t.FieldByName("V"); ok {
			t = vf.Type
			if t.Kind() == reflect.Ptr {
				t = t.Elem()
			}
		}
	}
	return t != nil && t.Kind() == reflect.Bool
}

// NormalizeBoolDefault rewrites a boolean column's `default:"..."` literal to the
// form the target dialect accepts in a DDL DEFAULT clause.
//
// Quark passes a column default through to DDL verbatim, but a boolean default
// has NO single literal portable across the six engines: PostgreSQL's BOOLEAN
// requires TRUE/FALSE and rejects 1/0 (SQLSTATE 42804), while MSSQL's BIT and
// Oracle's NUMBER(1) require 1/0 and reject TRUE/FALSE. This recognizes the
// documented bool literals 1/0/true/false (case-insensitive) and emits the
// dialect-appropriate one (TRUE/FALSE for PostgreSQL, 1/0 for the rest).
//
// Input must be one of 1/0/true/false (case-insensitive); any other string —
// a function call, a quoted literal, a non-bool value — is returned UNCHANGED,
// so non-boolean columns and custom expressions are unaffected. Callers gate
// this on IsBoolColumn; validating the default tag itself is the caller's job.
func NormalizeBoolDefault(dialectName, def string) string {
	var truthy bool
	switch strings.ToLower(strings.TrimSpace(def)) {
	case "1", "true":
		truthy = true
	case "0", "false":
		truthy = false
	default:
		return def // not a recognized bool literal; leave verbatim
	}
	switch dialectName {
	case "postgres", "postgresql":
		if truthy {
			return "TRUE"
		}
		return "FALSE"
	default: // mysql, mariadb, sqlite, mssql, oracle
		if truthy {
			return "1"
		}
		return "0"
	}
}

// isSQLNull reports whether t is database/sql's Null[T] generic struct (which
// quark.Nullable[T] aliases). Identification is by package + name prefix
// because the generic instantiation embeds the type parameter in
// reflect.Type.Name() ("Null[string]", "Null[time.Time]", …).
func isSQLNull(t reflect.Type) bool {
	if t == nil || t.Kind() != reflect.Struct {
		return false
	}
	if t.PkgPath() != "database/sql" {
		return false
	}
	name := t.Name()
	return name == "Null" || strings.HasPrefix(name, "Null[")
}

// isQuarkJSON reports whether t is quark.JSON[T]. Same detection strategy
// as isSQLNull — package path + name prefix — because the generic
// instantiation cannot be addressed by reflect.TypeOf at registration time.
func isQuarkJSON(t reflect.Type) bool {
	if t == nil || t.Kind() != reflect.Struct {
		return false
	}
	if t.PkgPath() != "github.com/jcsvwinston/quark" {
		return false
	}
	name := t.Name()
	return name == "JSON" || strings.HasPrefix(name, "JSON[")
}

// isQuarkArray reports whether t is quark.Array[T]. Detection mirrors
// isQuarkJSON — same package, same prefix-on-instantiation pattern.
func isQuarkArray(t reflect.Type) bool {
	if t == nil || t.Kind() != reflect.Struct {
		return false
	}
	if t.PkgPath() != "github.com/jcsvwinston/quark" {
		return false
	}
	name := t.Name()
	return name == "Array" || strings.HasPrefix(name, "Array[")
}

// jsonColumnType returns the dialect-native column type for a JSON payload.
func jsonColumnType(dialectName string) string {
	switch dialectName {
	case "postgres":
		return "JSONB"
	case "mysql", "mariadb":
		return "JSON"
	case "mssql":
		return "NVARCHAR(MAX)"
	case "oracle":
		return "CLOB"
	default:
		return "TEXT"
	}
}

// applySize rewrites a VARCHAR/CHAR/NVARCHAR family default with an explicit
// size. Engines that emit TEXT for the default (postgres/sqlite) get a
// VARCHAR(N) instead, matching what callers usually mean when they ask for
// a sized string.
func applySize(base, dialectName string, size int) string {
	switch dialectName {
	case "postgres", "sqlite":
		if base == "TEXT" {
			return fmt.Sprintf("VARCHAR(%d)", size)
		}
	case "oracle":
		if base == "VARCHAR2(255)" {
			return fmt.Sprintf("VARCHAR2(%d)", size)
		}
	case "mssql":
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

// applyPrecisionScale rewrites the DECIMAL family default. Today the built-in
// switch never emits DECIMAL itself (it is not in the Go-kind switch), so
// this is reachable only when a custom type mapper has produced a DECIMAL
// expression and the field tag adds extra precision hints. Kept here for
// symmetry with applySize.
func applyPrecisionScale(base, dialectName string, precision, scale int) string {
	_ = dialectName
	if scale == 0 {
		return fmt.Sprintf("DECIMAL(%d)", precision)
	}
	return fmt.Sprintf("DECIMAL(%d,%d)", precision, scale)
}

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
	case reflect.Slice:
		// []byte → BLOB / BYTEA / VARBINARY per dialect. Differentiate
		// from generic slices (which fall through to TEXT) by element kind.
		if t.Elem().Kind() == reflect.Uint8 {
			switch dialectName {
			case "postgres":
				return "BYTEA"
			case "mssql":
				return "VARBINARY(MAX)"
			default:
				return "BLOB"
			}
		}
	}

	return "TEXT" // Fallback
}
