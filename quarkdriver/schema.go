// Copyright 2026 jcsvwinston
// SPDX-License-Identifier: Apache-2.0

package quarkdriver

// This file is the schema half of the dialect contract: the questions Quark's
// schema path — Migrate, PlanMigration, Sync, ApplyPlan and the bookkeeping
// tables of the migrator and of Backfill — asks a dialect about the DDL its
// engine writes.
//
// Every interface here is OPTIONAL. Quark asserts it on the dialect it was
// given; a dialect that does not implement one gets the default its
// documentation states — a portable spelling that is right on the engines
// that follow the SQL standard there, or a clear ErrUnsupportedFeature where
// no portable spelling exists. None of them is ever answered from the
// dialect's Name(): the name is how a dialect is registered and found, not
// what it can do. Before A11 Q2 the schema path read the name, so a dialect
// registered under a name Quark did not know — SQLite's own methods under
// another name, in the A11 bench — got a primary key that stayed NULL and an
// ApplyPlan that refused to add a column.
//
// The built-in dialects implement the interfaces where their engine differs
// from the default, and answer exactly what the schema path wrote for them
// before these interfaces existed. A dialect that wraps a built-in one and
// changes its name must forward the optional interfaces too: embedding the
// Dialect interface in a struct promotes the Dialect methods and nothing else.
//
// Per ADR-0026 these interfaces are born here, in the leaf, and name no type
// of package quark: a driver module implements them without compiling the ORM.

// ColumnKind is the portable kind Quark classifies a model field's Go type
// into before it asks the dialect how its engine spells the column. The
// classification is Quark's — it unwraps pointers, sql.Null[T] and the
// quark.JSON[T] / quark.Array[T] wrappers — so a dialect never sees a Go type.
type ColumnKind int

const (
	// KindOther is a Go type with no portable kind: a struct other than
	// time.Time, an array that is not UUID-shaped, an interface. Every
	// built-in dialect writes TEXT for it.
	KindOther ColumnKind = iota
	// KindString is a Go string. ColumnSpec.Size, when set, bounds it.
	KindString
	// KindText is unbounded text. No model field produces it; Quark's own
	// bookkeeping tables use it for values that can outgrow a VARCHAR.
	KindText
	// KindInt16 is int8, int16, uint8 or uint16.
	KindInt16
	// KindInt32 is int32 or uint32.
	KindInt32
	// KindInt64 is int, int64, uint, uint64 and time.Duration (nanoseconds).
	KindInt64
	// KindFloat32 is float32.
	KindFloat32
	// KindFloat64 is float64.
	KindFloat64
	// KindDecimal is a float field with a precision hint: an exact decimal
	// of ColumnSpec.Precision digits, ColumnSpec.Scale of them after the
	// point.
	KindDecimal
	// KindBool is bool.
	KindBool
	// KindTime is time.Time.
	KindTime
	// KindBytes is []byte.
	KindBytes
	// KindJSON is a value stored as JSON text: quark.JSON[T],
	// quark.Array[T], a map, or a slice whose element has no scalar kind.
	KindJSON
	// KindUUID is a 16-byte array, the shape of google/uuid.UUID. Its value
	// travels in text form through the type's own Valuer and Scanner.
	KindUUID
	// KindIP is net.IP, bound and scanned as its textual address.
	KindIP
	// KindArray is a slice of a scalar kind; ColumnSpec.Elem is the element
	// kind. Only PostgreSQL among the built-ins has a native array type; the
	// others store it as JSON.
	KindArray
	// KindRange is quark.Range[T]; ColumnSpec.Elem is the kind of T
	// (KindTime, KindInt32, KindInt64, KindFloat32 or KindFloat64). Only
	// PostgreSQL among the built-ins has range types; the others store it as
	// JSON.
	KindRange
)

var columnKindNames = [...]string{
	KindOther: "other", KindString: "string", KindText: "text",
	KindInt16: "int16", KindInt32: "int32", KindInt64: "int64",
	KindFloat32: "float32", KindFloat64: "float64", KindDecimal: "decimal",
	KindBool: "bool", KindTime: "time", KindBytes: "bytes", KindJSON: "json",
	KindUUID: "uuid", KindIP: "ip", KindArray: "array", KindRange: "range",
}

// String names the kind, for messages and tests.
func (k ColumnKind) String() string {
	if k >= 0 && int(k) < len(columnKindNames) {
		return columnKindNames[k]
	}
	return "unknown"
}

// ColumnSpec is the portable description of a column's type: the kind Quark
// classified the field into, plus the sizing hints of its struct tag.
type ColumnSpec struct {
	Kind ColumnKind
	// Elem is the element kind of a KindArray or a KindRange; zero
	// otherwise.
	Elem ColumnKind
	// Size is the size=N hint of the field's tag; zero when there is none.
	// Quark passes it for every kind and the dialect applies it where its
	// type takes a length. The built-ins apply it to their default string
	// type and nowhere else.
	Size int
	// Precision and Scale are the precision=N,scale=M hints. They arrive
	// with KindDecimal only: a precision on a field that is not a float is
	// ignored (and reported by the tag linter).
	Precision int
	Scale     int
}

// ColumnTyper is the optional interface through which a dialect spells the
// column types of its engine. Quark asks it for every column Migrate, Sync and
// PlanMigration write, and for the columns of its bookkeeping tables.
//
// ColumnType returns the engine's type for spec — with the size, precision
// and scale applied — or "" to take Quark's portable type for that kind.
// BoolLiteral returns the literal a DEFAULT clause writes for a boolean
// column of the type ColumnType gives KindBool ("TRUE" on a BOOLEAN, "1" on a
// BIT), or "" for the portable literal.
//
// A dialect that does not implement ColumnTyper gets the portable types:
// VARCHAR(255) (VARCHAR(N) with a size), TEXT, SMALLINT, INTEGER, BIGINT,
// REAL, DOUBLE PRECISION, DECIMAL(p,s), BOOLEAN, TIMESTAMP, BLOB, TEXT for
// JSON, CHAR(36) for a UUID, VARCHAR(45) for an IP address, and TEXT for an
// array, a range and KindOther — with TRUE and FALSE as the boolean
// literals. The reference page of the dialect API lists the six built-in
// spellings next to these.
//
// A type registered with quark.RegisterTypeMapper is consulted before the
// dialect, and receives the dialect's Name().
type ColumnTyper interface {
	ColumnType(spec ColumnSpec) string
	BoolLiteral(v bool) string
}

// AutoIncrementer is the optional interface through which a dialect says how
// its engine declares a single-column integer primary key that the engine
// numbers itself — what Migrate and ApplyPlan write for `ID int64` with
// pk:"true".
//
// definition is everything after the quoted column name in CREATE TABLE: the
// type, the generation clause and PRIMARY KEY — "INTEGER PRIMARY KEY
// AUTOINCREMENT" on SQLite, "BIGSERIAL PRIMARY KEY" on PostgreSQL.
//
// dataType is the data type the engine's catalog reports for that column,
// which PlanMigration compares against IntrospectSchema; "" when it is the
// type ColumnTyper gives the field's kind. Only SQLite differs among the
// built-ins: its key is INTEGER, because only INTEGER PRIMARY KEY aliases the
// rowid, while an int64 column elsewhere in a table is BIGINT.
//
// A dialect that does not implement AutoIncrementer — or answers "" — gets the
// SQL-standard identity column, "BIGINT GENERATED BY DEFAULT AS IDENTITY
// PRIMARY KEY", whose catalog type is BIGINT. An engine that does not accept
// it fails the CREATE TABLE; it never gets a key column that stays NULL.
type AutoIncrementer interface {
	AutoIncrementColumn() (definition, dataType string)
}

// SchemaObject names what a CREATE statement Quark ran was creating, for
// IdempotentDDL.IsAlreadyExists.
type SchemaObject int

const (
	// ObjectTable is a CREATE TABLE.
	ObjectTable SchemaObject = iota + 1
	// ObjectIndex is a CREATE INDEX.
	ObjectIndex
	// ObjectConstraint is an ALTER TABLE … ADD CONSTRAINT (a foreign key).
	ObjectConstraint
)

// IdempotentDDL is the optional interface through which a dialect says how
// its engine creates a table or an index only when it does not exist yet —
// what makes Migrate, and the tables Quark keeps for itself, safe to run twice.
//
// CreateTableIfNotExists returns the statement that creates table — an
// unquoted name, which the dialect quotes — with body, the comma-separated
// column and constraint list that goes between the parentheses.
// CreateIndexIfNotExists does the same for an index on table's columns, all
// names unquoted. Either may return a plain CREATE when the engine has no
// conditional form, and recognise the error the engine then raises in
// IsAlreadyExists: Quark runs the statement, and an error for which
// IsAlreadyExists(object, err) is true counts as success. Quark also asks
// IsAlreadyExists about the error of an ADD CONSTRAINT … FOREIGN KEY.
// Returning "" from either method takes the default below.
//
// A dialect that does not implement IdempotentDDL gets CREATE TABLE IF NOT
// EXISTS and CREATE [UNIQUE] INDEX IF NOT EXISTS, and no error counts as
// "already exists". PostgreSQL and SQLite take that default; MySQL and
// MariaDB (no IF NOT EXISTS on CREATE INDEX), SQL Server (a guard on
// sys.tables / sys.indexes) and Oracle (ORA-00955, ORA-01408 and ORA-02264)
// implement the interface.
type IdempotentDDL interface {
	CreateTableIfNotExists(table, body string) string
	CreateIndexIfNotExists(table, index string, columns []string, unique bool) string
	IsAlreadyExists(object SchemaObject, err error) bool
}
