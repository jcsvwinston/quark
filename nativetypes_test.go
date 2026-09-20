// Copyright 2026 jcsvwinston
// SPDX-License-Identifier: Apache-2.0

package quark

import (
	"context"
	"errors"
	"net"
	"reflect"
	"strings"
	"testing"
	"time"
)

// A8 S6 (TYP-04, TYP-06, TYP-07): raw slices and maps, Range[T] and net.IP
// are stored in the type each engine has for them, and the PostgreSQL-only
// operators are known to the builder and refused by engine elsewhere.

func TestRangeLiteralAndJSON(t *testing.T) {
	now := time.Date(2026, 9, 20, 12, 0, 0, 0, time.UTC)
	r := Range[time.Time]{Lower: now, Upper: now.Add(time.Hour)}
	lit, err := r.PGLiteral()
	if err != nil || lit != `["2026-09-20T12:00:00Z","2026-09-20T13:00:00Z")` {
		t.Fatalf("PGLiteral: %q %v", lit, err)
	}
	v, _ := r.Value()
	if !strings.HasPrefix(v.(string), `{"lower":`) {
		t.Fatalf("Value is not JSON: %v", v)
	}
	// Scan both forms, and PostgreSQL's own spelling of the bound.
	for _, src := range []string{v.(string), lit, `["2026-09-20 12:00:00+00","2026-09-20 13:00:00+00")`} {
		var back Range[time.Time]
		if err := back.Scan(src); err != nil || !back.Lower.Equal(now) || !back.Upper.Equal(now.Add(time.Hour)) || back.bounds() != "[)" {
			t.Fatalf("Scan(%q) = %+v, %v", src, back, err)
		}
	}
	var empty Range[int64]
	if err := empty.Scan("empty"); err != nil || !empty.Empty {
		t.Fatalf("empty: %+v %v", empty, err)
	}
	ints := Range[int64]{Lower: 1, Upper: 10, Bounds: "[]"}
	if lit, _ := ints.PGLiteral(); lit != "[1,10]" {
		t.Fatalf("int literal %q", lit)
	}
	var backInts Range[int64]
	if err := backInts.Scan("[1,11)"); err != nil || backInts.Lower != 1 || backInts.Upper != 11 {
		t.Fatalf("int scan: %+v %v", backInts, err)
	}
}

func TestPGArrayLiteralRoundTrip(t *testing.T) {
	in := []string{"go", "", `a,"b"`, `back\slash`}
	lit := pgArrayLiteral(reflect.ValueOf(in))
	if lit != `{"go","","a,\"b\"","back\\slash"}` {
		t.Fatalf("literal %q", lit)
	}
	var out []string
	if err := (sliceScanner{dest: reflect.ValueOf(&out).Elem()}).Scan(lit); err != nil || !reflect.DeepEqual(out, in) {
		t.Fatalf("round trip: %v %v", out, err)
	}
	var ints []int64
	if err := (sliceScanner{dest: reflect.ValueOf(&ints).Elem()}).Scan("{1,2,3}"); err != nil || !reflect.DeepEqual(ints, []int64{1, 2, 3}) {
		t.Fatalf("ints: %v %v", ints, err)
	}
	// And the JSON form the other engines return.
	if err := (sliceScanner{dest: reflect.ValueOf(&ints).Elem()}).Scan(`[4,5]`); err != nil || !reflect.DeepEqual(ints, []int64{4, 5}) {
		t.Fatalf("json: %v %v", ints, err)
	}
	if lit := pgArrayLiteral(reflect.ValueOf([]bool{true, false})); lit != "{t,f}" {
		t.Fatalf("bools %q", lit)
	}
}

func TestIPScannerAcceptsTextAndBytes(t *testing.T) {
	var ip net.IP
	s := ipScanner{dest: &ip}
	if err := s.Scan("10.0.0.1"); err != nil || !ip.Equal(net.ParseIP("10.0.0.1")) {
		t.Fatalf("text: %v %v", ip, err)
	}
	if err := s.Scan("10.0.0.1/32"); err != nil || !ip.Equal(net.ParseIP("10.0.0.1")) {
		t.Fatalf("inet with mask: %v %v", ip, err)
	}
	if err := s.Scan([]byte{10, 0, 0, 2}); err != nil || !ip.Equal(net.IPv4(10, 0, 0, 2)) {
		t.Fatalf("raw bytes: %v %v", ip, err)
	}
	if err := s.Scan([]byte("::1")); err != nil || !ip.Equal(net.ParseIP("::1")) {
		t.Fatalf("text bytes: %v %v", ip, err)
	}
}

type s6Event struct {
	ID     int64            `db:"id" pk:"true"`
	Tags   []string         `db:"tags"`
	Counts []int64          `db:"counts"`
	Meta   map[string]any   `db:"meta"`
	Window Range[time.Time] `db:"window"`
	Span   Range[int64]     `db:"span"`
	Source net.IP           `db:"source"`
}

func (s6Event) TableName() string { return "s6_events" }

func TestNativeTypesRoundTripOnSQLite(t *testing.T) {
	c := constraintsClient(t, "s6_roundtrip")
	ctx := context.Background()
	if err := c.Migrate(ctx, &s6Event{}); err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 9, 20, 12, 0, 0, 0, time.UTC)
	row := &s6Event{
		Tags: []string{"go", "a,b"}, Counts: []int64{1, 2}, Meta: map[string]any{"k": "v"},
		Window: Range[time.Time]{Lower: now, Upper: now.Add(time.Hour)}, Span: Range[int64]{Lower: 1, Upper: 5},
		Source: net.ParseIP("10.0.0.1"),
	}
	if err := For[s6Event](ctx, c).Create(row); err != nil {
		t.Fatal(err)
	}
	got, err := For[s6Event](ctx, c).Find(row.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got.Tags, row.Tags) || !reflect.DeepEqual(got.Counts, row.Counts) || got.Meta["k"] != "v" ||
		!got.Window.Lower.Equal(now) || got.Span.Upper != 5 || !got.Source.Equal(row.Source) {
		t.Fatalf("round trip: %+v", got)
	}
	// The column types on SQLite: JSON-backed text, and text for the address.
	tb := tableNamed(t, c, "s6_events")
	for _, col := range []string{"tags", "counts", "meta", "window", "span", "source"} {
		if typ := columnNamed(t, tb, col).Type; !strings.EqualFold(typ, "TEXT") {
			t.Errorf("%s: %q on sqlite, want TEXT", col, typ)
		}
	}
	// The plan says nothing about the table after Migrate.
	if plan, err := c.PlanMigration(ctx, &s6Event{}); err != nil || !plan.IsEmpty() {
		t.Fatalf("plan: %v %s", err, plan)
	}
	// The PostgreSQL-only operators: known, and refused by ENGINE here.
	for _, op := range []string{"@>", "<@", "&&", ">>", "<<"} {
		_, err := For[s6Event](ctx, c).Where("tags", op, []string{"go"}).List()
		if !errors.Is(err, ErrUnsupportedFeature) || !strings.Contains(err.Error(), "sqlite") {
			t.Errorf("%s on sqlite: %v, want ErrUnsupportedFeature naming the engine", op, err)
		}
	}
	// A DELETE with such an operator is refused the same way, before any SQL.
	if _, err := For[s6Event](ctx, c).Where("tags", "@>", []string{"go"}).DeleteBy(); !errors.Is(err, ErrUnsupportedFeature) {
		t.Errorf("DeleteBy: %v", err)
	}
	// And still refused by the allowlist when it is not an operator at all.
	if _, err := For[s6Event](ctx, c).Where("tags", "@@", 1).List(); !errors.Is(err, ErrInvalidQuery) {
		t.Errorf("@@: %v", err)
	}
}

// On a PostgreSQL-dialect client the WHERE bind of a slice is the array
// literal and of a Range its literal; the types per dialect are PostgreSQL's.
func TestNativeTypesPostgresShapes(t *testing.T) {
	rec := &txStatementRecorder{}
	c, err := New("sqlite", "file:s6_pgshape?mode=memory&cache=shared", WithDialect(PostgreSQL()), WithQueryObserver(rec))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = c.Close() })
	_, _ = For[s6Event](context.Background(), c).Where("tags", "@>", []string{"go", "orm"}).Count()
	if len(rec.args) != 1 || rec.args[0] != `{"go","orm"}` {
		t.Fatalf("bind under postgres: %v", rec.args)
	}
	if !strings.Contains(rec.read(), `"tags" @> $1`) {
		t.Fatalf("statement: %s", rec.read())
	}
	q := &BaseQuery{dialect: PostgreSQL()}
	if got := q.nativeBind(Range[int64]{Lower: 1, Upper: 3}); got != "[1,3)" {
		t.Fatalf("range bind: %v", got)
	}
	if got := q.nativeBind(net.ParseIP("10.0.0.1")); got != "10.0.0.1" {
		t.Fatalf("ip bind: %v", got)
	}
	if got := q.nativeBind(map[string]any{"a": 1}); got != `{"a":1}` {
		t.Fatalf("map bind: %v", got)
	}
	// The DDL a PostgreSQL client would emit.
	for col, want := range map[string]string{"tags": "TEXT[]", "counts": "BIGINT[]", "meta": "JSONB", "window": "TSTZRANGE", "span": "INT8RANGE", "source": "INET"} {
		f, _ := reflect.TypeOf(s6Event{}).FieldByName(map[string]string{"tags": "Tags", "counts": "Counts", "meta": "Meta", "window": "Window", "span": "Span", "source": "Source"}[col])
		if got := c.mapColumnType(migrateSQLType("postgres", f.Type)); got != want {
			t.Errorf("%s: %q, want %q", col, got, want)
		}
	}
}
