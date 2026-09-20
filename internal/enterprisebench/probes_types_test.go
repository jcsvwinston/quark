// Copyright 2026 jcsvwinston
// SPDX-License-Identifier: Apache-2.0

package enterprisebench

import (
	"context"
	"database/sql/driver"
	"encoding/hex"
	"errors"
	"fmt"
	"net"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/jcsvwinston/quark"
)

// ---------------------------------------------------------------------------
// Reading what the migrator emitted.
//
// Most of this family is about a COLUMN TYPE, which is a clause, not a row:
// TEXT and JSONB both return the same string to Find. The bench reads clauses
// through e.rec, but Migrate execs its DDL straight on the pool and emits no
// QueryEvent, so the recorder never sees a CREATE TABLE. SQLite stores the
// statement it received verbatim in sqlite_master, which is the same evidence
// from the other end of the wire: what Quark actually asked the engine for.
// ---------------------------------------------------------------------------

func tiposDDL(t *testing.T, c *quark.Client, table string) string {
	t.Helper()
	var ddl string
	err := c.Raw().QueryRowContext(context.Background(),
		"SELECT sql FROM sqlite_master WHERE type = 'table' AND name = ?", table).Scan(&ddl)
	if err != nil {
		t.Fatalf("read the DDL of %q from sqlite_master: %v", table, err)
	}
	return ddl
}

// tiposColumnType returns everything the DDL wrote after a column's name —
// the type AND any inline constraint, because "did the PRIMARY KEY suffix
// survive" is one of the things this family measures. The scan is
// parenthesis-aware so DECIMAL(10,2) does not get cut at its own comma.
func tiposColumnType(t *testing.T, ddl, column string) string {
	t.Helper()
	marker := `"` + column + `" `
	i := strings.Index(ddl, marker)
	if i < 0 {
		t.Fatalf("column %q is not in the DDL:\n%s", column, ddl)
	}
	rest := ddl[i+len(marker):]
	depth := 0
	for j := 0; j < len(rest); j++ {
		switch rest[j] {
		case '(':
			depth++
		case ')':
			if depth == 0 {
				return strings.TrimSpace(rest[:j])
			}
			depth--
		case ',', '\n':
			if depth == 0 {
				return strings.TrimSpace(rest[:j])
			}
		}
	}
	return strings.TrimSpace(rest)
}

// tiposUnsupportedType reports whether err is database/sql's refusal to bind a
// Go value it has no conversion for. That refusal is the end of the line for a
// type Quark does not know: the column exists, the INSERT is built, and the
// value never reaches the driver. Matching on the message is deliberate — the
// converter returns a plain error with no sentinel to test against, and the
// bench would rather say so than pretend the write succeeded.
func tiposUnsupportedType(err error) bool {
	return err != nil && strings.Contains(err.Error(), "unsupported type")
}

// tiposRawString reads one column back exactly as the engine stored it.
//
// "JSON-backed" is a claim about the VALUE in the column, and Find cannot
// settle it: the wrapper's own Scan would decode whatever its Value side
// wrote, so a round-trip proves the pair agrees with itself and nothing more.
// The probe asks the database for the raw text instead.
func tiposRawString(t *testing.T, c *quark.Client, query string, args ...any) string {
	t.Helper()
	var s string
	if err := c.Raw().QueryRowContext(context.Background(), query, args...).Scan(&s); err != nil {
		t.Fatalf("read a raw value (%s): %v", query, err)
	}
	return s
}

// ---------------------------------------------------------------------------
// Models. They live at package scope so each can name its own table: the
// probes read that table out of the catalog, and guessing the pluralisation
// of a type name is not a measurement.
// ---------------------------------------------------------------------------

// tiposUUID is a UUID in the shape an application actually declares: 16 bytes
// with a Valuer/Scanner pair — the shape github.com/google/uuid ships, and the
// one RegisterTypeMapper's own godoc uses as its example.
//
// The probe used to declare a bare [16]byte with no Valuer. What that measured
// was database/sql refusing to bind a Go array, which is the standard
// library's rule and would hold against any driver and any ORM: it said
// nothing about whether Quark has a uuid type. With the Valuer in place the
// question the title asks — what COLUMN TYPE does a UUID-shaped value get —
// is the only one left.
type tiposUUID [16]byte

func (u tiposUUID) Value() (driver.Value, error) {
	return fmt.Sprintf("%x-%x-%x-%x-%x", u[0:4], u[4:6], u[6:8], u[8:10], u[10:16]), nil
}

func (u *tiposUUID) Scan(src any) error {
	var s string
	switch v := src.(type) {
	case string:
		s = v
	case []byte:
		s = string(v)
	default:
		return fmt.Errorf("tiposUUID: cannot scan %T", src)
	}
	b, err := hex.DecodeString(strings.ReplaceAll(s, "-", ""))
	if err != nil {
		return fmt.Errorf("tiposUUID: %w", err)
	}
	if len(b) != len(u) {
		return fmt.Errorf("tiposUUID: got %d bytes", len(b))
	}
	copy(u[:], b)
	return nil
}

type tiposUUIDRow struct {
	ID  int64     `db:"id" pk:"true"`
	Ref tiposUUID `db:"ref"`
}

func (tiposUUIDRow) TableName() string { return "tipos_uuid" }

// tiposStringKeyRow is the UUID-shaped thing that does work, and that TYP-01's
// note points an application at: a string primary key. The note names the type
// the PK builder renders for it, so the probe reads it instead of asserting it
// from memory.
type tiposStringKeyRow struct {
	ID   string `db:"id" pk:"true"`
	Name string `db:"name"`
}

func (tiposStringKeyRow) TableName() string { return "tipos_uuid_key" }

type tiposMoney string

type tiposMappedColRow struct {
	ID    int64      `db:"id" pk:"true"`
	Price tiposMoney `db:"price"`
}

func (tiposMappedColRow) TableName() string { return "tipos_mapper_col" }

type tiposKey string

type tiposMappedKeyRow struct {
	ID   tiposKey `db:"id" pk:"true"`
	Name string   `db:"name"`
}

func (tiposMappedKeyRow) TableName() string { return "tipos_mapper_key" }

// The two tag spellings an application reaches for when it wants an enum
// constrained by the database.
type tiposEnumQuarkTagRow struct {
	ID     int64  `db:"id" pk:"true"`
	Status string `db:"status" quark:"check=status IN ('draft','live')"`
}

func (tiposEnumQuarkTagRow) TableName() string { return "tipos_enum_quark" }

type tiposEnumDBTagRow struct {
	ID     int64  `db:"id" pk:"true"`
	Status string `db:"status,enum=draft|live"`
}

func (tiposEnumDBTagRow) TableName() string { return "tipos_enum_db" }

type tiposEnumPlainRow struct {
	ID     int64  `db:"id" pk:"true"`
	Status string `db:"status"`
}

func (tiposEnumPlainRow) TableName() string { return "tipos_enum_plain" }

type tiposNativeArrayRow struct {
	ID   int64    `db:"id" pk:"true"`
	Tags []string `db:"tags"`
}

func (tiposNativeArrayRow) TableName() string { return "tipos_native_array" }

type tiposArrayRow struct {
	ID   int64               `db:"id" pk:"true"`
	Tags quark.Array[string] `db:"tags"`
}

func (tiposArrayRow) TableName() string { return "tipos_array" }

// tiposTSRange is the shape of a timestamp range: a lower and an upper bound
// carried as one value, which is what tstzrange is for.
type tiposTSRange struct {
	Lower time.Time
	Upper time.Time
}

type tiposRangeRow struct {
	ID     int64                  `db:"id" pk:"true"`
	Window quark.Range[time.Time] `db:"window"`
}

func (tiposRangeRow) TableName() string { return "tipos_range" }

// tiposTSRangeMapped is the same shape under a name of its own, so the probe
// can register a type mapper for it — RegisterTypeMapper's registry is global
// and keyed by reflect.Type — without deciding the DDL of the unmapped half.
// It exists because TYP-06's note used to ASSERT what a mapper buys here while
// the neighbouring inet control measured the same door.
type tiposTSRangeMapped tiposTSRange

type tiposRangeMappedRow struct {
	ID     int64              `db:"id" pk:"true"`
	Window tiposTSRangeMapped `db:"window"`
}

func (tiposRangeMappedRow) TableName() string { return "tipos_range_mapped" }

// The IP an application declares is net.IP, which is a named []byte and
// therefore something database/sql binds on its own.
//
// The probe used to declare `struct{ Octets [4]byte }`, and what that measured
// was the converter refusing a Go struct — again the standard library's rule,
// not Quark's. With net.IP the value crosses, which is exactly what makes the
// remaining absence readable: what is missing is the native column type and
// the network semantics that come with it, not the ability to store an
// address.
type tiposInetPlainRow struct {
	ID   int64  `db:"id" pk:"true"`
	Addr net.IP `db:"addr"`
}

func (tiposInetPlainRow) TableName() string { return "tipos_inet_plain" }

// tiposInetMapped is the same net.IP under a name of its own: the mapped half
// of the probe registers a mapper for it, and the registry is global and keyed
// by reflect.Type, so it must not be net.IP itself.
type tiposInetMapped net.IP

type tiposInetMappedRow struct {
	ID   int64           `db:"id" pk:"true"`
	Addr tiposInetMapped `db:"addr"`
}

func (tiposInetMappedRow) TableName() string { return "tipos_inet_mapped" }

type tiposJSONRow struct {
	ID   int64                      `db:"id" pk:"true"`
	Data quark.JSON[map[string]any] `db:"data"`
}

func (tiposJSONRow) TableName() string { return "tipos_json" }

type tiposNullableRow struct {
	ID   int64                                      `db:"id" pk:"true"`
	Bio  quark.Nullable[string]                     `db:"bio"`
	Tags quark.Nullable[quark.Array[string]]        `db:"tags"`
	Doc  quark.Nullable[quark.JSON[map[string]int]] `db:"doc"`
}

func (tiposNullableRow) TableName() string { return "tipos_nullable" }

type tiposBuiltinRow struct {
	ID   int64         `db:"id" pk:"true"`
	TTL  time.Duration `db:"ttl"`
	Blob []byte        `db:"blob"`
}

func (tiposBuiltinRow) TableName() string { return "tipos_builtins" }

// Price is the case precision/scale exists for: a decimal field asking to be
// sized. Name and Flag are the same hints on fields that are not decimal at
// all, which is where a sizing hint stops refining and starts being a type.
// Both halves are needed: without Price the probe could only see the defect,
// and would publish "present" the day someone fixed it without ever having
// checked that the hint sizes a decimal column.
type tiposPrecisionRow struct {
	ID    int64   `db:"id" pk:"true"`
	Price float64 `db:"price,precision=10,scale=2"`
	Name  string  `db:"name,precision=10,scale=2"`
	Flag  bool    `db:"flag,precision=3"`
}

func (tiposPrecisionRow) TableName() string { return "tipos_precision" }

// ---------------------------------------------------------------------------
// Probes.
// ---------------------------------------------------------------------------

// TYP-01. Whether Quark has a UUID type is not answered by looking for one:
// it is answered by declaring the UUID an application declares — 16 bytes with
// a Valuer/Scanner — and reading the column type the migrator chose for it.
//
// Only that column type decides the verdict, because it is the only thing the
// title claims. The write is measured too, but as the note's evidence, not as
// the verdict's: a UUID that round-trips as text IS the absence of a native
// uuid column, so letting the write move the number would make the control say
// "present" about a capability nobody built.
func probeTiposUUIDNative(t *testing.T, e *env) verdict {
	ctx := context.Background()
	c, _ := e.fresh(t, "tipos_uuid")
	if err := c.Migrate(ctx, &tiposUUIDRow{}, &tiposStringKeyRow{}); err != nil {
		t.Fatalf("migrate the uuid-shaped models: %v", err)
	}

	col := tiposColumnType(t, tiposDDL(t, c, "tipos_uuid"), "ref")
	row := &tiposUUIDRow{Ref: tiposUUID{0x01, 0x02, 0x03}}
	writeErr := quark.For[tiposUUIDRow](ctx, c).Create(row)
	roundTrip := writeErr == nil
	if roundTrip {
		got, err := quark.For[tiposUUIDRow](ctx, c).Find(row.ID)
		roundTrip = err == nil && got.Ref == row.Ref
		if !roundTrip {
			t.Logf("the UUID was written but did not come back: err=%v ref=%x", err, got.Ref)
		}
	}

	// Two claims the note makes, both measured here so the note cannot outlive
	// them: that a UUID with a Valuer does go in, through the fallback type,
	// and that the string primary key the modeling guide points at instead is
	// rendered VARCHAR(36).
	if !roundTrip {
		t.Errorf("the note records that a UUID with a Valuer round-trips through "+
			"the %q fallback; it no longer does (write: %v)", col, writeErr)
	}
	if key := tiposColumnType(t, tiposDDL(t, c, "tipos_uuid_key"), "id"); key != "VARCHAR(36) PRIMARY KEY" {
		t.Errorf("the note records VARCHAR(36) for a UUID-shaped string primary key; "+
			"the builder emits %q", key)
	}

	if strings.Contains(strings.ToUpper(col), "UUID") {
		// A uuid branch appeared. Whether the value also survives it decides
		// between present and partial, and the bench wants to be told either way.
		if roundTrip {
			return present
		}
		t.Logf("the column is %q but the value does not round-trip", col)
		return partial
	}
	// Measured: no uuid branch in the per-dialect switch — a UUID-shaped value
	// takes the fallback type, whatever it can do once it is there.
	t.Logf("a UUID-shaped value takes the %q fallback", col)
	return absent
}

// TYP-02. The escape hatch has to be measured in both directions: that what a
// mapper returns is what CREATE TABLE gets, and what the mapper costs when the
// mapped column is the primary key. The second half is measured through
// behaviour, not DDL text — two rows with the same key, both accepted.
func probeTiposTypeMapper(t *testing.T, e *env) verdict {
	ctx := context.Background()
	quark.RegisterTypeMapper(reflect.TypeOf(tiposMoney("")),
		func(string, quark.TypeOptions) string { return "NUMERIC(18,4)" })
	// The mapper for the key is the one the godoc and the modeling guide show
	// for UUID keys, in its shape: a constant width, ignoring opts.IsPK.
	quark.RegisterTypeMapper(reflect.TypeOf(tiposKey("")),
		func(string, quark.TypeOptions) string { return "CHAR(26)" })

	c, _ := e.fresh(t, "tipos_mapper")
	if err := c.Migrate(ctx, &tiposMappedColRow{}, &tiposMappedKeyRow{}); err != nil {
		t.Fatalf("migrate the mapped models: %v", err)
	}

	plain := tiposColumnType(t, tiposDDL(t, c, "tipos_mapper_col"), "price")
	if plain != "NUMERIC(18,4)" {
		t.Logf("a registered mapper did not reach the DDL: column is %q", plain)
		return absent
	}

	// The title claims two facts about the key column — that the mapper's type
	// is what CREATE TABLE got, AND that the PRIMARY KEY is there — so both
	// are measured and `present` needs both. Reading PRIMARY KEY alone as
	// present would publish "custom SQL type, primary key included" over a
	// world where the builder had stopped applying the mapper to key columns
	// and emitted its own default type with its own suffix: that world is the
	// escape hatch MISSING on the key, not the escape hatch working. And the
	// note's own premise — the mapper's type replaces the PRIMARY KEY
	// fragment — has to be asserted, or a key that quietly came back TEXT
	// would still return the recorded verdict through the duplicate insert.
	key := strings.ToUpper(tiposColumnType(t, tiposDDL(t, c, "tipos_mapper_key"), "id"))
	mapped := strings.HasPrefix(key, "CHAR(26)")
	if !mapped {
		t.Errorf("the note records that the mapper's type occupies the key column "+
			"(CHAR(26), in place of the PRIMARY KEY fragment); the column is %q", key)
	}
	if mapped && strings.Contains(key, "PRIMARY KEY") {
		return present
	}
	// The DDL says the key is not a key. Confirm what that means for an
	// application: the same id twice, no error, two rows.
	if err := quark.For[tiposMappedKeyRow](ctx, c).Create(&tiposMappedKeyRow{ID: "01HZY", Name: "first"}); err != nil {
		t.Fatalf("insert the first row: %v", err)
	}
	dup := quark.For[tiposMappedKeyRow](ctx, c).Create(&tiposMappedKeyRow{ID: "01HZY", Name: "second"})
	if dup != nil {
		t.Logf("the column lost PRIMARY KEY (%q) but the duplicate key was still rejected: %v", key, dup)
		return partial
	}
	rows, err := quark.For[tiposMappedKeyRow](ctx, c).List()
	if err != nil {
		t.Fatalf("list the mapped-key table: %v", err)
	}
	if len(rows) != 2 {
		t.Fatalf("expected the two rows the duplicate insert should have left, got %d", len(rows))
	}
	// Measured: the mapper reaches the DDL, and it silently replaces the
	// primary key.
	return partial
}

// TYP-03. An enum constrained by the database needs two things: a way to say
// it on the model, and a CHECK in the emitted DDL. The probe asks for the
// first in both grammars an application would try, and reads the second off a
// table that migrated cleanly.
func probeTiposEnumCheck(t *testing.T, e *env) verdict {
	ctx := context.Background()
	c, _ := e.fresh(t, "tipos_enum")

	quarkTag := c.Migrate(ctx, &tiposEnumQuarkTagRow{})
	dbTag := c.Migrate(ctx, &tiposEnumDBTagRow{})
	// An accepted tag is only half the title, and it cannot decide the verdict
	// on its own. Returning `partial` the moment a grammar stops being refused
	// left this probe unable to reach `present` by any path — the day someone
	// lands the grammar AND the CHECK, the bench would have asked for the
	// whole capability to be recorded as "half" — and it read two opposite
	// worlds the same way: the gap fully closed, and a grammar that is
	// accepted and then dropped on the floor, which is worse than today's
	// ErrInvalidTag because it is silent. So when a grammar is accepted the
	// probe migrates that same model and reads its table for the CHECK.
	for _, grammar := range []struct {
		err   error
		spelt string
		table string
	}{
		{quarkTag, `quark:"check=..."`, "tipos_enum_quark"},
		{dbTag, `db:"...,enum=..."`, "tipos_enum_db"},
	} {
		if grammar.err != nil {
			continue
		}
		ddl := tiposDDL(t, c, grammar.table)
		up := strings.ToUpper(ddl)
		if strings.Contains(up, "CHECK") && strings.Contains(up, "DRAFT") && strings.Contains(up, "LIVE") {
			t.Logf("%s is accepted and the CHECK reached the DDL:\n%s", grammar.spelt, ddl)
			return present
		}
		t.Logf("%s is accepted but no CHECK carrying the declared values reached the "+
			"DDL — the declaration is swallowed in silence:\n%s", grammar.spelt, ddl)
		return partial
	}
	if !errors.Is(quarkTag, quark.ErrInvalidTag) || !errors.Is(dbTag, quark.ErrInvalidTag) {
		t.Fatalf("both tags were refused for a reason other than an unknown token, "+
			"which the probe cannot read as the absence of the grammar: quark=%v db=%v",
			quarkTag, dbTag)
	}

	// Neither grammar exists. The other half: nothing the migrator emits for a
	// plain enum-shaped column carries a CHECK either.
	if err := c.Migrate(ctx, &tiposEnumPlainRow{}); err != nil {
		t.Fatalf("migrate the plain model: %v", err)
	}
	if ddl := tiposDDL(t, c, "tipos_enum_plain"); strings.Contains(strings.ToUpper(ddl), "CHECK") {
		t.Logf("the migrator emitted a CHECK without being asked:\n%s", ddl)
		return partial
	}
	return absent
}

// TYP-04. A native array column is measured the same way as a native uuid:
// declare the Go value an application would declare, then look at the column
// and at the first write. The published type matrix says these columns are
// "serialised as text"; the write is where that claim is settled.
func probeTiposNativeArray(t *testing.T, e *env) verdict {
	ctx := context.Background()
	c, _ := e.fresh(t, "tipos_native_array")
	if err := c.Migrate(ctx, &tiposNativeArrayRow{}); err != nil {
		t.Fatalf("migrate the []string model: %v", err)
	}

	col := tiposColumnType(t, tiposDDL(t, c, "tipos_native_array"), "tags")
	row := &tiposNativeArrayRow{Tags: []string{"go", "orm", "a,b"}}
	err := quark.For[tiposNativeArrayRow](ctx, c).Create(row)
	if err != nil {
		if tiposUnsupportedType(err) {
			// The S0 state: the column exists and database/sql refuses the
			// value above the driver.
			t.Logf("a raw slice is refused on write (column %q): %v", col, err)
			return absent
		}
		t.Fatalf("the write failed for a reason the probe cannot attribute to slice support: %v", err)
	}
	got, err := quark.For[tiposNativeArrayRow](ctx, c).Find(row.ID)
	if err != nil || len(got.Tags) != 3 || got.Tags[2] != "a,b" {
		t.Logf("the slice was written but did not come back whole: err=%v tags=%v", err, got.Tags)
		return partial
	}
	// This bench opens SQLite, where the column is the JSON-backed text
	// column; the native TEXT[] on PostgreSQL is proven in
	// internal/enginesuite. What is measured here is the round trip and
	// that the value in the column is JSON, not fmt's rendering of a slice.
	raw := tiposRawString(t, c, `SELECT tags FROM tipos_native_array WHERE id = ?`, row.ID)
	if !strings.HasPrefix(raw, "[") {
		t.Logf("the slice round-trips but the column holds %q, which is not JSON", raw)
		return partial
	}
	return present
}

// TYP-05. The wrapper that does exist, measured end to end: the column it
// gets, a list written and read back, and the empty case the Value/Scan pair
// is explicitly documented to fold to nil.
func probeTiposArrayWrapper(t *testing.T, e *env) verdict {
	ctx := context.Background()
	c, _ := e.fresh(t, "tipos_array")
	if err := c.Migrate(ctx, &tiposArrayRow{}); err != nil {
		t.Fatalf("migrate the Array[T] model: %v", err)
	}
	if col := tiposColumnType(t, tiposDDL(t, c, "tipos_array"), "tags"); col != "TEXT" {
		// TEXT is SQLite's answer from the same per-dialect switch that answers
		// JSONB on PostgreSQL, and it is the answer the note records. A
		// different one means the switch moved under the bench, so it moves the
		// verdict: as a log it moved nothing, and the number stayed put while
		// the thing it describes changed.
		t.Logf("Array[T] on SQLite is no longer TEXT but %q", col)
		return partial
	}

	row := &tiposArrayRow{Tags: quark.Array[string]{V: []string{"go", "orm", "sql"}}}
	if err := quark.For[tiposArrayRow](ctx, c).Create(row); err != nil {
		t.Logf("writing an Array[string]: %v", err)
		return absent
	}
	got, err := quark.For[tiposArrayRow](ctx, c).Find(row.ID)
	if err != nil {
		t.Logf("reading the Array[string] back: %v", err)
		return absent
	}
	if got.Tags.Len() != 3 || strings.Join(got.Tags.Slice(), ",") != "go,orm,sql" {
		t.Errorf("Array[string] round-trip: wrote %v, read %v", row.Tags.Slice(), got.Tags.Slice())
		return partial
	}
	// "JSON-backed" is the other half of the title, and a round-trip cannot
	// prove it: Value and Scan are one pair and would agree on any encoding.
	// What is in the column is readable here, so the bench reads it.
	if raw := tiposRawString(t, c, `SELECT tags FROM tipos_array WHERE id = ?`, row.ID); raw != `["go","orm","sql"]` {
		t.Logf("the stored value is not the JSON the title claims: %q", raw)
		return partial
	}

	empty := &tiposArrayRow{}
	if err := quark.For[tiposArrayRow](ctx, c).Create(empty); err != nil {
		t.Errorf("writing a zero-value Array[string]: %v", err)
		return partial
	}
	back, err := quark.For[tiposArrayRow](ctx, c).Find(empty.ID)
	if err != nil {
		t.Errorf("reading the zero-value Array[string] back: %v", err)
		return partial
	}
	if back.Tags.Len() != 0 {
		t.Errorf("a zero-value Array[string] came back with %d elements", back.Tags.Len())
		return partial
	}
	if raw := tiposRawString(t, c, `SELECT tags FROM tipos_array WHERE id = ?`, empty.ID); raw != `[]` {
		t.Logf("the empty array is not stored as JSON either: %q", raw)
		return partial
	}
	return present
}

// TYP-06. A range is two things at once: a column type that holds both bounds,
// and an operator that asks whether a point is inside it. The probe measures
// both, and the verdict is their conjunction — present needs a range column AND
// a containment operator, absent needs neither, and anything in between is
// partial. One fact deciding the verdict on its own is how a control ends up
// green over a capability that is half gone.
//
// The mapper door is measured here too, because the note used to assert it:
// "a type mapper could emit tstzrange; nothing would be able to bind a value
// into it". The neighbouring inet control opens that same door and measures
// it, so this one does the same instead of borrowing the conclusion. Quark
// ships Array, JSON and Nullable and nothing range-shaped, so a range value
// has no Valuer to cross database/sql with — which is what the mapped write
// shows.
func probeTiposRanges(t *testing.T, e *env) verdict {
	ctx := context.Background()
	c, rec := e.fresh(t, "tipos_range")
	quark.RegisterTypeMapper(reflect.TypeOf(tiposTSRangeMapped{}),
		func(string, quark.TypeOptions) string { return "TSTZRANGE" })
	if err := c.Migrate(ctx, &tiposRangeRow{}, &tiposRangeMappedRow{}); err != nil {
		t.Fatalf("migrate the range-shaped models: %v", err)
	}

	now := time.Now().UTC().Truncate(time.Second)
	row := &tiposRangeRow{Window: quark.Range[time.Time]{Lower: now, Upper: now.Add(time.Hour)}}
	writeErr := quark.For[tiposRangeRow](ctx, c).Create(row)
	roundTrip := writeErr == nil
	if roundTrip {
		got, err := quark.For[tiposRangeRow](ctx, c).Find(row.ID)
		roundTrip = err == nil && got.Window.Lower.Equal(now) && got.Window.Upper.Equal(now.Add(time.Hour))
		if !roundTrip {
			t.Logf("the range was written but did not come back: err=%v window=%+v", err, got.Window)
		}
	}

	// A plain struct that merely LOOKS like a range is still refused by
	// database/sql, mapper or no mapper: the shape Quark stores is Range[T].
	mappedCol := tiposColumnType(t, tiposDDL(t, c, "tipos_range_mapped"), "window")
	if mappedCol != "TSTZRANGE" {
		t.Fatalf("the mapper did not produce the column type: %q", mappedCol)
	}
	if err := quark.For[tiposRangeMappedRow](ctx, c).Create(&tiposRangeMappedRow{
		Window: tiposTSRangeMapped{Lower: now, Upper: now.Add(time.Hour)},
	}); err == nil {
		t.Errorf("a plain struct binds into a mapped TSTZRANGE column now; the note about the mapper door is stale")
	}

	// Containment: the operator PostgreSQL has for a range. On this bench's
	// engine the honest answer is a refusal BY ENGINE — the builder knows
	// the operator and says which engine has it — with no SQL sent. A
	// refusal by the allowlist (ErrInvalidQuery) is the S0 state: the
	// operator did not exist at all.
	rec.reset()
	_, opErr := quark.For[tiposRangeRow](ctx, c).Where("window", "@>", now).List()
	emitted := rec.sql()
	operatorKnown := opErr == nil || errors.Is(opErr, quark.ErrUnsupportedFeature)
	if len(emitted) != 0 && opErr != nil {
		t.Errorf("the refused operator still emitted SQL: %v", emitted)
	}

	switch {
	case roundTrip && operatorKnown:
		return present
	case roundTrip || operatorKnown:
		t.Logf("half the range surface: round trip=%v, containment known=%v (%v)", roundTrip, operatorKnown, opErr)
		return partial
	}
	if !errors.Is(opErr, quark.ErrInvalidQuery) {
		t.Fatalf("containment failed for a reason other than the operator guard: %v", opErr)
	}
	return absent
}

// TYP-07. The two things that make a native inet column worth asking for are
// the column type itself and the network operators that only exist for it. The
// probe measures both, and the verdict is their conjunction.
//
// The address it declares is net.IP — the type an application actually has —
// so what the probe sees is Quark's answer and not database/sql's: the value
// goes in, as bytes, and comes back. That is the point. What is missing is not
// the ability to store an address; it is the type and its semantics, and the
// containment question is asked against the mapped column, where the DDL
// really does say INET, so that even the best case an application can build
// today is measured rather than assumed.
func probeTiposInet(t *testing.T, e *env) verdict {
	ctx := context.Background()
	c, rec := e.fresh(t, "tipos_inet")
	quark.RegisterTypeMapper(reflect.TypeOf(tiposInetMapped(nil)),
		func(string, quark.TypeOptions) string { return "INET" })
	if err := c.Migrate(ctx, &tiposInetPlainRow{}, &tiposInetMappedRow{}); err != nil {
		t.Fatalf("migrate the IP-shaped models: %v", err)
	}

	addr := net.ParseIP("10.0.0.1")
	plainCol := tiposColumnType(t, tiposDDL(t, c, "tipos_inet_plain"), "addr")
	plain := &tiposInetPlainRow{Addr: addr}
	plainErr := quark.For[tiposInetPlainRow](ctx, c).Create(plain)
	stored := plainErr == nil
	if stored {
		got, err := quark.For[tiposInetPlainRow](ctx, c).Find(plain.ID)
		stored = err == nil && got.Addr.Equal(addr)
		if !stored {
			t.Logf("the address was written but did not come back: err=%v addr=%v", err, got.Addr)
		}
	}
	if !stored {
		t.Errorf("the note records that an IP value is stored, untyped, in the %q "+
			"fallback; it no longer round-trips (write: %v)", plainCol, plainErr)
	}

	// The escape hatch, with the DDL an application would want.
	mappedCol := tiposColumnType(t, tiposDDL(t, c, "tipos_inet_mapped"), "addr")
	if mappedCol != "INET" {
		t.Fatalf("the mapper did not even produce the column type, so the probe "+
			"cannot tell the two doors apart: %q", mappedCol)
	}
	if err := quark.For[tiposInetMappedRow](ctx, c).Create(
		&tiposInetMappedRow{Addr: tiposInetMapped(addr)}); err != nil {
		t.Errorf("the note records that a mapped INET column takes the value; it no "+
			"longer does: %v", err)
	}

	// The semantics, asked on the column that really is INET: containment is
	// the operator an inet column exists for, and the allowlist decides it
	// before any SQL is built.
	rec.reset()
	_, opErr := quark.For[tiposInetMappedRow](ctx, c).Where("addr", ">>", "10.0.0.0/8").List()
	emitted := rec.sql()

	// The address is stored AS text on this engine (INET on PostgreSQL,
	// proven in internal/enginesuite), so the column holds the address the
	// way a person reads it, not four opaque bytes.
	rawAddr := tiposRawString(t, c, `SELECT addr FROM tipos_inet_plain WHERE id = ?`, plain.ID)
	asText := rawAddr == "10.0.0.1"
	// The network operator: known to the builder, refused by ENGINE here
	// with no SQL sent. A refusal by the allowlist is the S0 state.
	operatorKnown := opErr == nil || errors.Is(opErr, quark.ErrUnsupportedFeature)
	if opErr != nil && len(emitted) != 0 {
		t.Errorf("the refused operator still emitted SQL: %v", emitted)
	}
	switch {
	case stored && asText && operatorKnown:
		return present
	case stored && (asText || operatorKnown):
		t.Logf("half the inet surface: stored as text=%v (column holds %q, type %q), network operator known=%v (%v)",
			asText, rawAddr, plainCol, operatorKnown, opErr)
		return partial
	}
	if !errors.Is(opErr, quark.ErrInvalidQuery) {
		t.Fatalf("the network operator failed for a reason other than the operator guard: %v", opErr)
	}
	t.Logf("IP-shaped value mapped to %q and stored as %q; the network operator is refused by the allowlist", plainCol, rawAddr)
	return absent
}

// TYP-08. The JSON surface splits in two, and the probe measures the split:
// the typed round-trip and the dotted-path filter that do work, and the two
// things that make JSONB worth asking for — reaching into an array, and
// containment — which are refused before any SQL is built.
func probeTiposJSON(t *testing.T, e *env) verdict {
	ctx := context.Background()
	c, rec := e.fresh(t, "tipos_json")
	if err := c.Migrate(ctx, &tiposJSONRow{}); err != nil {
		t.Fatalf("migrate the JSON[T] model: %v", err)
	}

	row := &tiposJSONRow{Data: quark.JSON[map[string]any]{V: map[string]any{
		"plan":  "enterprise",
		"items": []any{"a", "b"},
	}}}
	if err := quark.For[tiposJSONRow](ctx, c).Create(row); err != nil {
		t.Logf("writing a JSON[T] payload: %v", err)
		return absent
	}
	got, err := quark.For[tiposJSONRow](ctx, c).Find(row.ID)
	if err != nil || got.Data.V["plan"] != "enterprise" {
		t.Logf("JSON[T] round-trip: err=%v payload=%v", err, got)
		return absent
	}

	// The filter that works, read as a clause: the path must be bound into a
	// JSON function, not pasted into the predicate.
	rec.reset()
	hits, err := quark.For[tiposJSONRow](ctx, c).WhereJSON("data", "plan", "=", "enterprise").List()
	if err != nil || len(hits) != 1 {
		t.Logf("filtering by a dotted path: err=%v rows=%d", err, len(hits))
		return absent
	}
	if sel := rec.last(); !strings.Contains(strings.ToUpper(sel), "JSON_EXTRACT") {
		t.Errorf("the dotted-path filter did not go through a JSON function: %s", sel)
	}

	// The first half of what is missing: a path into an array.
	_, idxErr := quark.For[tiposJSONRow](ctx, c).WhereJSON("data", "items[0]", "=", "a").List()
	// The second: containment, the operator JSONB exists for.
	rec.reset()
	_, containErr := quark.For[tiposJSONRow](ctx, c).Where("data", "@>", `{"plan":"enterprise"}`).List()
	containSQL := rec.sql()

	if idxErr == nil && containErr == nil {
		return present
	}
	if idxErr != nil && !errors.Is(idxErr, quark.ErrInvalidJSONPath) {
		t.Fatalf("the array path failed for a reason other than the path grammar: %v", idxErr)
	}
	if containErr != nil {
		// Since A8 S6 the operator is known to the builder and refused BY
		// ENGINE where the engine lacks it (ErrUnsupportedFeature); the
		// allowlist refusal (ErrInvalidQuery) is the older state. Either is
		// the same fact for this control: no containment on this engine.
		if !errors.Is(containErr, quark.ErrInvalidQuery) && !errors.Is(containErr, quark.ErrUnsupportedFeature) {
			t.Fatalf("containment failed for a reason other than the operator guards: %v", containErr)
		}
		if len(containSQL) != 0 {
			t.Errorf("the rejected operator still emitted SQL: %v", containSQL)
		}
	}
	// Measured: typed JSON in and out, filtering by a dotted path, and no way
	// to index an array or ask for containment.
	return partial
}

// TYP-09. Nullable[T] is measured where it earns its keep — telling NULL apart
// from the zero value — and over the two compositions its own godoc recommends
// for exactly that and which no test in the repository exercises.
func probeTiposNullable(t *testing.T, e *env) verdict {
	ctx := context.Background()
	c, _ := e.fresh(t, "tipos_nullable")
	if err := c.Migrate(ctx, &tiposNullableRow{}); err != nil {
		t.Fatalf("migrate the Nullable[T] model: %v", err)
	}
	// The wrapper must not leak into the column: the storage type is T's.
	if col := tiposColumnType(t, tiposDDL(t, c, "tipos_nullable"), "bio"); col != "TEXT" {
		t.Errorf("Nullable[string] should take string's column type, got %q", col)
		return partial
	}

	set := &tiposNullableRow{
		Bio:  quark.SomeOf("hello"),
		Tags: quark.SomeOf(quark.Array[string]{V: []string{"go"}}),
		Doc:  quark.SomeOf(quark.JSON[map[string]int]{V: map[string]int{"n": 1}}),
	}
	if err := quark.For[tiposNullableRow](ctx, c).Create(set); err != nil {
		t.Logf("writing the set row: %v", err)
		return absent
	}
	null := &tiposNullableRow{}
	if err := quark.For[tiposNullableRow](ctx, c).Create(null); err != nil {
		t.Logf("writing the NULL row: %v", err)
		return absent
	}

	gotSet, err := quark.For[tiposNullableRow](ctx, c).Find(set.ID)
	if err != nil {
		t.Logf("reading the set row: %v", err)
		return absent
	}
	gotNull, err := quark.For[tiposNullableRow](ctx, c).Find(null.ID)
	if err != nil {
		t.Logf("reading the NULL row: %v", err)
		return absent
	}

	if !gotSet.Bio.Valid || gotSet.Bio.V != "hello" {
		t.Errorf("Nullable[string] round-trip: %+v", gotSet.Bio)
		return partial
	}
	if !gotSet.Tags.Valid || gotSet.Tags.V.Len() != 1 || gotSet.Tags.V.Slice()[0] != "go" {
		t.Errorf("Nullable[Array[string]] round-trip: %+v", gotSet.Tags)
		return partial
	}
	if !gotSet.Doc.Valid || gotSet.Doc.V.V["n"] != 1 {
		t.Errorf("Nullable[JSON[map[string]int]] round-trip: %+v", gotSet.Doc)
		return partial
	}
	// The point of the type: an unset column comes back invalid, not empty.
	if gotNull.Bio.Valid || gotNull.Tags.Valid || gotNull.Doc.Valid {
		t.Errorf("an unset row came back valid: bio=%+v tags=%+v doc=%+v",
			gotNull.Bio, gotNull.Tags, gotNull.Doc)
		return partial
	}
	return present
}

// TYP-10. Two built-ins the arc's list never mentions, measured together
// because they are one claim: that Quark maps the Go types an enterprise model
// already uses. Both do, column type and round-trip.
//
// The db tag's precision/scale used to ride along here and now has a control
// of its own (TYP-11). It had to move: a shipped mapper that stops mapping and
// a sizing hint that turns into a type are two different defects, and while
// they shared one number the hint's every failure — inert, hijacking, or both
// — came back as this control's recorded `partial`.
func probeTiposRichBuiltins(t *testing.T, e *env) verdict {
	ctx := context.Background()
	c, _ := e.fresh(t, "tipos_builtins")
	if err := c.Migrate(ctx, &tiposBuiltinRow{}); err != nil {
		t.Fatalf("migrate the built-in model: %v", err)
	}

	builtins := tiposDDL(t, c, "tipos_builtins")
	if col := tiposColumnType(t, builtins, "ttl"); col != "BIGINT" {
		t.Errorf("time.Duration should take the shipped mapper's BIGINT, got %q", col)
		return partial
	}
	if col := tiposColumnType(t, builtins, "blob"); col != "BLOB" {
		t.Errorf("[]byte should take SQLite's BLOB, got %q", col)
		return partial
	}
	row := &tiposBuiltinRow{TTL: 90 * time.Second, Blob: []byte{0xDE, 0xAD, 0xBE, 0xEF}}
	if err := quark.For[tiposBuiltinRow](ctx, c).Create(row); err != nil {
		t.Logf("writing Duration/[]byte: %v", err)
		return absent
	}
	got, err := quark.For[tiposBuiltinRow](ctx, c).Find(row.ID)
	if err != nil || got.TTL != 90*time.Second || string(got.Blob) != string(row.Blob) {
		t.Logf("Duration/[]byte round-trip: err=%v ttl=%v blob=%x", err, got.TTL, got.Blob)
		return absent
	}

	// Measured: both shipped mappers, in the column type and in the value.
	return present
}

// TYP-11. precision/scale, measured from both ends and with a verdict per
// world. The hint has to DO its job on a decimal field, and it has to stay out
// of the way on fields that are not decimal at all — the tag linter accepts it
// on any of them.
//
// Measuring only the second half was a trap: the probe would report present as
// soon as string and bool stopped coming back DECIMAL, which is exactly what a
// guard on applyPrecisionScale (internal/migrate/migrate.go) would produce if
// it also stopped sizing decimals. Measuring both halves into ONE non-present
// verdict was the same trap one step later: "sizes and hijacks" (what ships)
// and "does nothing at all" are opposite facts, and while both answered
// `partial` the hint could go inert under a green bench. Each split gets its
// own verdict here, and the one split no verdict describes fails loudly.
func probeTiposPrecisionScale(t *testing.T, e *env) verdict {
	ctx := context.Background()
	c, _ := e.fresh(t, "tipos_precision")
	if err := c.Migrate(ctx, &tiposPrecisionRow{}); err != nil {
		t.Fatalf("migrate the precision model: %v", err)
	}

	ddl := tiposDDL(t, c, "tipos_precision")
	price := tiposColumnType(t, ddl, "price")
	name := tiposColumnType(t, ddl, "name")
	flag := tiposColumnType(t, ddl, "flag")
	// refines: the hint sizes the field it exists for.
	// hijacks: the same rewrite lands on fields that are not decimal at all.
	refines := price == "DECIMAL(10,2)"
	hijacks := strings.HasPrefix(name, "DECIMAL") || strings.HasPrefix(flag, "DECIMAL")
	switch {
	case refines && !hijacks:
		return present
	case refines && hijacks:
		t.Logf("precision/scale sizes the decimal (float64 → %q) and replaces the base "+
			"type everywhere else: string → %q, bool → %q", price, name, flag)
		return partial
	case !refines && !hijacks:
		// The tag is still accepted by the linter, so the surface is reachable
		// and inert: nothing the model declares reaches the column. That is the
		// capability absent, not a smaller version of it.
		t.Logf("precision/scale sizes nothing at all: float64 → %q, string → %q, bool → %q",
			price, name, flag)
		return absent
	default:
		// Sizing gone AND the rewrite still landing on other types: worse than
		// the recorded world in both directions, and no verdict says that.
		t.Errorf("precision/scale stopped sizing a decimal (float64 → %q) while it still "+
			"replaces other base types (string → %q, bool → %q)", price, name, flag)
		return absent
	}
}
