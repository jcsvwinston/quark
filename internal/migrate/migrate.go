// Package migrate provides internal utilities for database schema migrations.
//
// The column-type pipeline has two halves since A11 Q2. Classify, and the
// wrappers around it, turn a Go type into a portable quarkdriver.ColumnSpec —
// that half knows Go and nothing about engines. An Asker answers the
// per-engine half: how the engine spells a spec, its auto-increment key and
// its boolean literal. Package quark builds its Asker from the dialect's
// optional interfaces (quarkdriver.ColumnTyper, quarkdriver.AutoIncrementer);
// EngineAsker builds one from a built-in engine's name, for the built-in
// dialects' own implementations of those interfaces and for the CLI, which
// plans for the six built-in engines by name.
package migrate

import (
	"net"
	"reflect"
	"strings"
	"sync"
	"time"

	"github.com/jcsvwinston/quark/quarkdriver"
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
// because the column-type pipeline is the only consumer and we want the
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

// Asker answers the per-engine questions of the column-type pipeline.
type Asker struct {
	// Name is what a registered TypeMapper receives: the dialect's Name().
	Name string
	// Column spells a portable spec as the engine's column type, with the
	// size, precision and scale applied. It never returns "".
	Column func(quarkdriver.ColumnSpec) string
	// AutoIncrement is the engine's auto-increment key: the definition that
	// follows the column name, and the catalog's data type for it ("" when
	// it is the field's ordinary type).
	AutoIncrement func() (definition, dataType string)
	// BoolLiteral is the DEFAULT literal for a boolean column.
	BoolLiteral func(bool) string
}

// EngineAsker answers for a built-in engine by its name. A name that is not
// one of the six built-ins gets the portable answers.
func EngineAsker(name string) Asker {
	return Asker{
		Name:          name,
		Column:        func(s quarkdriver.ColumnSpec) string { return EngineColumnType(name, s) },
		AutoIncrement: func() (string, string) { return EngineAutoIncrement(name) },
		BoolLiteral:   func(v bool) string { return EngineBoolLiteral(name, v) },
	}
}

// durationType is time.Duration, stored as its count of nanoseconds. As a
// key it is the caller's value, never an auto-increment key: until A11 Q2 the
// type was registered as a TypeMapper, and a mapped key keeps the mapper's
// type with no generation clause (QK-29).
var durationType = reflect.TypeOf(time.Duration(0))

// ColumnSQL is the column-type pipeline: the SQL type of a model field of Go
// type t with the tag hints in opts, as a.Column spells it. With opts.IsPK it
// is the whole primary-key fragment — the auto-increment key for an integer,
// a 36-character string for a string, the field's own type plus PRIMARY KEY
// otherwise.
//
// Order: a registered TypeMapper wins (and receives a.Name); then
// sql.Null[T] is unwrapped; quark.JSON[T] and quark.Array[T] are JSON; then
// the key rules; then the field's kind.
func ColumnSQL(a Asker, t reflect.Type, opts TypeOptions) string {
	// Strip pointer wrapper so *T and T resolve to the same SQL type.
	if t != nil && t.Kind() == reflect.Ptr {
		t = t.Elem()
	}

	// Custom mappers take precedence over the built-in switch. A mapped key
	// column keeps its key (QK-29): the mapper's type is taken verbatim — no
	// auto-increment is inferred from it — and the PRIMARY KEY suffix is
	// appended unless the mapper wrote one itself.
	if mapper := LookupTypeMapper(t); mapper != nil {
		mapped := mapper(a.Name, opts)
		if opts.IsPK && !strings.Contains(strings.ToUpper(mapped), "PRIMARY KEY") {
			mapped += " PRIMARY KEY"
		}
		return mapped
	}

	// sql.Null[T] (re-exported as quark.Nullable[T]): unwrap and recurse
	// into T so the column gets T's SQL type. The wrapper itself is just a
	// (V T, Valid bool) pair plus Scanner+Valuer; the storage type is T's.
	if isSQLNull(t) {
		if vf, ok := t.FieldByName("V"); ok {
			return ColumnSQL(a, vf.Type, opts)
		}
	}

	// quark.JSON[T] and quark.Array[T]: the JSON column type. The semantic
	// split (Array for lists, JSON for arbitrary documents) is purely on the
	// Go side; on the SQL side both serialise to the same JSON-shaped
	// column, and neither takes a size.
	if isQuarkJSON(t) || isQuarkArray(t) {
		return a.Column(quarkdriver.ColumnSpec{Kind: quarkdriver.KindJSON})
	}

	if opts.IsPK {
		return primaryKeySQL(a, t, opts)
	}
	return a.Column(Classify(t, opts))
}

// primaryKeySQL is the fragment of a single-column primary key.
func primaryKeySQL(a Asker, t reflect.Type, opts TypeOptions) string {
	switch {
	case t == durationType:
		return a.Column(quarkdriver.ColumnSpec{Kind: quarkdriver.KindInt64}) + " PRIMARY KEY"
	case t.Kind() == reflect.String:
		// UUID / ULID / KSUID — the caller supplies the value. The size tag
		// is not consulted: a string key is 36 characters.
		return a.Column(quarkdriver.ColumnSpec{Kind: quarkdriver.KindString, Size: 36}) + " PRIMARY KEY"
	case isIntegerKind(t.Kind()):
		// 64-bit throughout (QK-21), in whatever form the engine numbers a
		// key itself.
		def, _ := a.AutoIncrement()
		return def
	default:
		// bool, float, a UUID-shaped array… — its own type plus PRIMARY KEY.
		// The size hint is not applied to a key column.
		spec := Classify(t, TypeOptions{Precision: opts.Precision, Scale: opts.Scale})
		return a.Column(spec) + " PRIMARY KEY"
	}
}

func isIntegerKind(k reflect.Kind) bool {
	switch k {
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64,
		reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
		return true
	}
	return false
}

// Classify is the portable kind of a column of Go type t, with the size,
// precision and scale hints of opts. It does not consult the TypeMapper
// registry nor unwrap sql.Null[T] or the quark wrappers — ColumnSQL does that
// first — and it ignores opts.IsPK.
func Classify(t reflect.Type, opts TypeOptions) quarkdriver.ColumnSpec {
	if t != nil && t.Kind() == reflect.Ptr {
		t = t.Elem()
	}
	spec := func(k quarkdriver.ColumnKind) quarkdriver.ColumnSpec {
		return quarkdriver.ColumnSpec{Kind: k, Size: opts.Size}
	}
	if t == nil {
		return spec(quarkdriver.KindOther)
	}

	// An IP address (net.IP) — a []byte, so it is checked first. The bind
	// and scan paths carry it as text (A8 S6).
	if t == netIPType {
		return spec(quarkdriver.KindIP)
	}
	// quark.Range[T]: a range of T where T has a range kind, JSON otherwise
	// (A8 S6).
	if isQuarkRange(t) {
		if elem := rangeElem(t); elem != quarkdriver.KindOther {
			s := spec(quarkdriver.KindRange)
			s.Elem = elem
			return s
		}
		return spec(quarkdriver.KindJSON)
	}
	// A raw slice or map: an array of a scalar kind, JSON otherwise (A8 S6).
	// Before that the column was created and the first write failed in
	// database/sql's converter.
	if t.Kind() == reflect.Slice && t.Elem().Kind() != reflect.Uint8 {
		if elem := scalarKind(t.Elem().Kind()); elem != quarkdriver.KindOther {
			s := spec(quarkdriver.KindArray)
			s.Elem = elem
			return s
		}
		return spec(quarkdriver.KindJSON)
	}
	if t.Kind() == reflect.Map {
		return spec(quarkdriver.KindJSON)
	}
	// A UUID-shaped value — 16 bytes, the shape of google/uuid.UUID and of
	// every other UUID type in the ecosystem (A8 S5).
	if IsUUIDShaped(t) {
		return spec(quarkdriver.KindUUID)
	}

	switch t.Kind() {
	case reflect.Float32, reflect.Float64:
		// A precision hint turns a float into an exact decimal (QK-28). On
		// any other kind the hint is ignored, and the tag linter warns.
		if opts.Precision > 0 {
			return quarkdriver.ColumnSpec{Kind: quarkdriver.KindDecimal, Size: opts.Size, Precision: opts.Precision, Scale: opts.Scale}
		}
		return spec(scalarKind(t.Kind()))
	case reflect.String, reflect.Bool,
		reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64,
		reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
		// Integers map by the WIDTH of the Go type (QK-21).
		return spec(scalarKind(t.Kind()))
	case reflect.Struct:
		if t.String() == "time.Time" {
			return spec(quarkdriver.KindTime)
		}
	case reflect.Slice:
		// []byte (non-byte slices were handled above).
		return spec(quarkdriver.KindBytes)
	}
	return spec(quarkdriver.KindOther)
}

// scalarKind is the portable kind of a scalar reflect.Kind; KindOther for a
// kind that is not a scalar.
func scalarKind(k reflect.Kind) quarkdriver.ColumnKind {
	switch k {
	case reflect.String:
		return quarkdriver.KindString
	case reflect.Bool:
		return quarkdriver.KindBool
	case reflect.Int8, reflect.Int16, reflect.Uint8, reflect.Uint16:
		return quarkdriver.KindInt16
	case reflect.Int32, reflect.Uint32:
		return quarkdriver.KindInt32
	case reflect.Int, reflect.Int64, reflect.Uint, reflect.Uint64:
		return quarkdriver.KindInt64
	case reflect.Float32:
		return quarkdriver.KindFloat32
	case reflect.Float64:
		return quarkdriver.KindFloat64
	}
	return quarkdriver.KindOther
}

// rangeElem is the kind of a Range[T]'s bound, read off its Lower field;
// KindOther when T has no range kind.
func rangeElem(t reflect.Type) quarkdriver.ColumnKind {
	lower, ok := t.FieldByName("Lower")
	if !ok {
		return quarkdriver.KindOther
	}
	switch lt := lower.Type; {
	case lt == reflect.TypeOf(time.Time{}):
		return quarkdriver.KindTime
	case lt.Kind() == reflect.Int64 || lt.Kind() == reflect.Int:
		return quarkdriver.KindInt64
	case lt.Kind() == reflect.Int32:
		return quarkdriver.KindInt32
	case lt.Kind() == reflect.Float64:
		return quarkdriver.KindFloat64
	case lt.Kind() == reflect.Float32:
		return quarkdriver.KindFloat32
	}
	return quarkdriver.KindOther
}

// SQLTypeWithOpts is the column type a built-in engine, named by
// dialectName, gets for a field of Go type t with the tag hints in opts — the
// pipeline of ColumnSQL answered by EngineAsker. The CLI uses it to plan for
// the six built-in engines from source; the library asks the dialect instead.
func SQLTypeWithOpts(dialectName string, t reflect.Type, opts TypeOptions) string {
	return ColumnSQL(EngineAsker(dialectName), t, opts)
}

// SQLType is the raw form of SQLTypeWithOpts: no TypeMapper, no unwrapping of
// sql.Null[T] or the quark wrappers, no tag hints. With isPK the result is the
// primary-key fragment.
//
// When isPK is true the exact type depends on the Go field kind:
//
//   - int / int64 → dialect-native auto-increment (SERIAL, AUTO_INCREMENT, IDENTITY…)
//   - string      → VARCHAR(36) PRIMARY KEY — UUID-friendly; no auto-increment
//   - anything else → its natural SQL type + PRIMARY KEY (no auto-increment)
func SQLType(dialectName string, t reflect.Type, isPK bool) string {
	a := EngineAsker(dialectName)
	if t != nil && t.Kind() == reflect.Ptr {
		t = t.Elem()
	}
	if isPK {
		return primaryKeySQL(a, t, TypeOptions{})
	}
	return a.Column(Classify(t, TypeOptions{}))
}

// IsBoolColumn reports whether t maps to a boolean column. It unwraps a pointer
// (*bool) and the sql.Null[bool] / quark.Nullable[bool] wrapper the same way
// ColumnSQL resolves the column's SQL type, so the default-normalization
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

// NormalizeBoolDefaultWith rewrites a boolean column's `default:"..."` literal
// to the form the engine accepts in a DDL DEFAULT clause, as literal spells it.
//
// Quark passes a column default through to DDL verbatim, but a boolean default
// has NO single literal portable across the six engines: PostgreSQL's BOOLEAN
// requires TRUE/FALSE and rejects 1/0 (SQLSTATE 42804), while MSSQL's BIT and
// Oracle's NUMBER(1) require 1/0 and reject TRUE/FALSE. This recognizes the
// documented bool literals 1/0/true/false (case-insensitive) and emits the
// engine's.
//
// Any other string — a function call, a quoted literal, a non-bool value — is
// returned UNCHANGED, so non-boolean columns and custom expressions are
// unaffected. Callers gate this on IsBoolColumn.
func NormalizeBoolDefaultWith(literal func(bool) string, def string) string {
	switch strings.ToLower(strings.TrimSpace(def)) {
	case "1", "true":
		return literal(true)
	case "0", "false":
		return literal(false)
	default:
		return def // not a recognized bool literal; leave verbatim
	}
}

// NormalizeBoolDefault is NormalizeBoolDefaultWith for a built-in engine
// named by dialectName: TRUE/FALSE on PostgreSQL, 1/0 on the other five.
func NormalizeBoolDefault(dialectName, def string) string {
	return NormalizeBoolDefaultWith(func(v bool) string { return EngineBoolLiteral(dialectName, v) }, def)
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

// isQuarkRange reports whether t is quark.Range[T], by the same package-path
// and name-prefix detection as isQuarkArray.
func isQuarkRange(t reflect.Type) bool {
	if t == nil || t.Kind() != reflect.Struct || t.PkgPath() != "github.com/jcsvwinston/quark" {
		return false
	}
	return t.Name() == "Range" || strings.HasPrefix(t.Name(), "Range[")
}

var netIPType = reflect.TypeOf(net.IP{})

// IsUUIDShaped reports whether t is a 16-byte array — the shape of
// google/uuid.UUID and of the other UUID types — after stripping a pointer.
func IsUUIDShaped(t reflect.Type) bool {
	if t == nil {
		return false
	}
	if t.Kind() == reflect.Ptr {
		t = t.Elem()
	}
	return t.Kind() == reflect.Array && t.Len() == 16 && t.Elem().Kind() == reflect.Uint8
}

// PKClass classifies a single-column primary key for DDL rendering.
// Two callers funnel into the same fragments so the PRIMARY KEY rendering
// lives in one place: the migrator (which classifies from the model's
// reflect.Kind inside ColumnSQL) and ApplyPlan's CREATE TABLE executor
// (which classifies from the neutral type STRING via [ClassifyPKType] — the
// Go type is gone by then).
type PKClass int

const (
	// PKOther appends a plain PRIMARY KEY to the column's own type
	// (composite-key members never get here — they render as a
	// table-level constraint).
	PKOther PKClass = iota
	// PKInteger renders the dialect's auto-increment integer PK.
	PKInteger
	// PKString renders the dialect's fixed-width string PK (UUID/ULID).
	PKString
)

// PKColumnSQLWith returns the full column-type fragment for a single-column
// primary key of the given class, as a answers for its engine. `fallback` is
// the bare type to use for PKOther; it is ignored for the other classes.
func PKColumnSQLWith(a Asker, class PKClass, fallback string) string {
	switch class {
	case PKString:
		return a.Column(quarkdriver.ColumnSpec{Kind: quarkdriver.KindString, Size: 36}) + " PRIMARY KEY"
	case PKInteger:
		def, _ := a.AutoIncrement()
		return def
	default:
		return fallback + " PRIMARY KEY"
	}
}

// PKColumnSQL is PKColumnSQLWith for a built-in engine named by dialectName.
func PKColumnSQL(dialectName string, class PKClass, fallback string) string {
	return PKColumnSQLWith(EngineAsker(dialectName), class, fallback)
}

// ClassifyPKType maps a bare column-type string (catalog- or
// migrator-emitted) to its [PKClass]. Integer-family types get the
// auto-increment treatment — mirroring the reflect-side rule "integer
// PK → auto-increment" so a table created from a Plan matches the one
// [ColumnSQL] would emit for the same model. Everything else renders as
// `<own type> PRIMARY KEY`; notably a string column keeps its declared
// type (TEXT/VARCHAR(n)) instead of being coerced to the reflect-side
// VARCHAR(36) — PKString is reachable only from the reflect path.
//
// Bare `NUMBER` (no precision) is classified as integer on purpose:
// Oracle's catalog reports identity PK columns as precision-less
// NUMBER (the same asymmetry typesEqual's oracleBareNumberMatch
// handles on the diff side), so a Plan built from introspection of a
// Migrate-created table re-renders the identity clause instead of
// silently dropping it. A hand-made non-identity bare-NUMBER PK would
// be coerced — accepted trade-off, mirroring the diff layer's "the
// catalog only emits bare NUMBER for identity columns" assumption.
func ClassifyPKType(bareType string) PKClass {
	t := strings.ToLower(strings.TrimSpace(bareType))
	switch t {
	case "integer", "int", "bigint", "smallint", "mediumint", "tinyint", "number", "number(19)":
		return PKInteger
	}
	// MySQL catalog display-width forms: int(11), bigint(20), …
	for _, p := range []string{"int(", "bigint(", "smallint(", "mediumint(", "tinyint("} {
		if strings.HasPrefix(t, p) {
			return PKInteger
		}
	}
	return PKOther
}

// PKBareColumnTypeWith returns the DATA type an auto-increment integer
// primary key column actually ends up with, when that differs from what
// ColumnSQL reports for the same Go type outside a key.
//
// It exists for the diff and sync paths, which compare a bare type against
// what the catalog reports and therefore ask for the type with IsPK false.
// That answer is right for every built-in engine but SQLite: there, only
// `INTEGER PRIMARY KEY` aliases the rowid — `BIGINT PRIMARY KEY` is an
// ordinary indexed column with different behaviour — so the migrator emits
// INTEGER for the key while a plain int64 column is BIGINT. Without this,
// widening integers to BIGINT (QK-21) made every SQLite table report drift
// against itself immediately after being created.
//
// It applies to a SINGLE-column key only. Only a lone INTEGER PRIMARY KEY
// aliases the rowid; the columns of a composite key are ordinary integers,
// which is why callers check for a composite key before asking.
//
// The second return is false when the column takes the ordinary type.
func PKBareColumnTypeWith(a Asker, t reflect.Type) (string, bool) {
	if t != nil && t.Kind() == reflect.Ptr {
		t = t.Elem()
	}
	if t == nil || !isIntegerKind(t.Kind()) {
		return "", false
	}
	_, dataType := a.AutoIncrement()
	if dataType == "" {
		return "", false
	}
	return dataType, true
}

// PKBareColumnType is PKBareColumnTypeWith for a built-in engine named by
// dialectName.
func PKBareColumnType(dialectName string, t reflect.Type) (string, bool) {
	return PKBareColumnTypeWith(EngineAsker(dialectName), t)
}
