// Copyright 2026 jcsvwinston
// SPDX-License-Identifier: Apache-2.0

package quark

import (
	"context"
	"database/sql/driver"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/go-playground/validator/v10"
	_ "modernc.org/sqlite"
)

// A12 Q2 took work off the paths the engine bench measures. Each test below
// pins that what the path answers did not change with it.

// --- QK-35: the key filter of a preload --------------------------------------

type pkInt32 int32

type pkName string

type pkValuer int64

func (v pkValuer) Value() (driver.Value, error) { return int64(v) * 10, nil }

func TestPGArrayKeys(t *testing.T) {
	cases := []struct {
		name string
		keys []any
		want string
		ok   bool
	}{
		{"int64", []any{int64(1), int64(22), int64(333)}, "{1,22,333}", true},
		{"int", []any{1, 2}, "{1,2}", true},
		{"int32", []any{int32(-5), int32(7)}, "{-5,7}", true},
		{"named int", []any{pkInt32(3), pkInt32(4)}, "{3,4}", true},
		{"uint64 past int64", []any{uint64(18446744073709551615)}, "{18446744073709551615}", true},
		{"uint8", []any{uint8(9)}, "{9}", true},
		{"strings", []any{"a", "b c"}, `{"a","b c"}`, true},
		{"strings needing quotes", []any{`a"b`, `c\d`, "e,f", "{g}", "NULL"}, `{"a\"b","c\\d","e,f","{g}","NULL"}`, true},
		{"named string", []any{pkName("x")}, `{"x"}`, true},
		{"a Valuer keeps the IN list", []any{pkValuer(1)}, "", false},
		{"a Valuer among ints", []any{int64(1), pkValuer(2)}, "", false},
		{"a time keeps the IN list", []any{time.Unix(0, 0)}, "", false},
		{"a byte array keeps the IN list", []any{[16]byte{1}}, "", false},
		{"a float keeps the IN list", []any{1.5}, "", false},
		{"ints and strings mixed", []any{int64(1), "2"}, "", false},
		{"strings and ints mixed", []any{"1", int64(2)}, "", false},
		{"no keys", nil, "", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, ok := pgArrayKeys(tc.keys)
			if ok != tc.ok || got != tc.want {
				t.Errorf("pgArrayKeys(%v) = %q, %v; want %q, %v", tc.keys, got, ok, tc.want, tc.ok)
			}
		})
	}
}

func TestPreloadKeyFilter(t *testing.T) {
	ints := []any{int64(1), int64(2), int64(3)}
	valuers := []any{pkValuer(1), pkValuer(2)}
	cases := []struct {
		name     string
		dialect  Dialect
		keys     []any
		first    int
		wantSQL  string
		wantArgs []any
	}{
		{"postgres, integers", PostgreSQL(), ints, 1, `"user_id" = ANY($1)`, []any{"{1,2,3}"}},
		{"postgres, after one parameter", PostgreSQL(), ints, 2, `"user_id" = ANY($2)`, []any{"{1,2,3}"}},
		{"postgres, Valuer keys", PostgreSQL(), valuers, 2, `"user_id" IN ($2, $3)`, valuers},
		{"mysql", MySQL(), ints, 1, "`user_id` IN (?, ?, ?)", ints},
		{"mariadb", MariaDB(), ints, 1, "`user_id` IN (?, ?, ?)", ints},
		{"sqlite", SQLite(), ints, 1, `"user_id" IN (?, ?, ?)`, ints},
		{"mssql", MSSQL(), ints, 1, "[user_id] IN (@p1, @p2, @p3)", ints},
		{"oracle", Oracle(), ints, 2, `"USER_ID" IN (:2, :3, :4)`, ints},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			q := &BaseQuery{dialect: tc.dialect}
			sqlStr, args := q.preloadKeyFilter("user_id", tc.keys, tc.first)
			if sqlStr != tc.wantSQL {
				t.Errorf("SQL = %s, want %s", sqlStr, tc.wantSQL)
			}
			if !reflect.DeepEqual(args, tc.wantArgs) {
				t.Errorf("args = %#v, want %#v", args, tc.wantArgs)
			}
		})
	}
}

// --- QK-36: placeholders ------------------------------------------------------

// wrappedPG overrides nothing: writePlaceholder must take its Placeholder,
// not assume the built-in format from the embedded type.
type wrappedPG struct{ *PostgresDialect }

func (wrappedPG) Placeholder(i int) string { return fmt.Sprintf("$%d::text", i) }

func TestWritePlaceholderIsPlaceholder(t *testing.T) {
	dialects := []Dialect{PostgreSQL(), MySQL(), MariaDB(), SQLite(), MSSQL(), Oracle(), wrappedPG{&PostgresDialect{}}}
	for _, d := range dialects {
		for _, i := range []int{0, 1, 9, 10, 99, 100, 4000, 65535, -1} {
			var b strings.Builder
			writePlaceholder(&b, d, i)
			if got, want := b.String(), d.Placeholder(i); got != want {
				t.Errorf("%T: writePlaceholder(%d) = %q, Placeholder = %q", d, i, got, want)
			}
		}
	}
	for d, want := range map[Dialect]string{PostgreSQL(): "$4000", MSSQL(): "@p4000", Oracle(): ":4000", MySQL(): "?"} {
		if got := d.Placeholder(4000); got != want {
			t.Errorf("%T.Placeholder(4000) = %q, want %q", d, got, want)
		}
	}
}

func TestWritePlaceholderDoesNotAllocate(t *testing.T) {
	var b strings.Builder
	b.Grow(1 << 16)
	d := PostgreSQL()
	i := 0
	allocs := testing.AllocsPerRun(1000, func() {
		i++
		writePlaceholder(&b, d, 1000+i)
	})
	if allocs != 0 {
		t.Errorf("writePlaceholder allocates %.1f times per call into a grown builder, want 0", allocs)
	}
}

// --- QK-36: Validate on a model without rules ---------------------------------

type vNoTags struct {
	ID    int64  `db:"id"`
	Name  string `db:"name"`
	At    time.Time
	Next  *vNoTags
	Kids  []vNoTags
	Attrs map[string]int
}

type vTagged struct {
	Name string `validate:"required"`
}

type vNested struct {
	ID    int64
	Inner vTagged
}

type vNestedPtr struct {
	Inner *vTagged
}

type vInSlice struct {
	Items []vTagged `validate:"dive"`
}

type vUntaggedSliceOfTagged struct {
	Items []vTagged
}

type vInterface struct {
	Any any
}

type vUnexportedTag struct {
	name string `validate:"required"` //nolint:unused // read by the validator's walk, not by code
}

type vEmbedded struct {
	vTagged
}

type vSelfValidating struct {
	ID int64
}

var errSelf = errors.New("self says no")

func (vSelfValidating) Validate(context.Context) error { return errSelf }

func TestMayHaveTagRules(t *testing.T) {
	cases := []struct {
		name  string
		model any
		want  bool
	}{
		{"no tags anywhere", &vNoTags{}, false},
		{"no tags, by value", vNoTags{}, false},
		{"a tag", &vTagged{}, true},
		{"a tag in a nested struct", &vNested{}, true},
		{"a tag behind a pointer", &vNestedPtr{}, true},
		{"a dive tag", &vInSlice{}, true},
		{"a tag in a slice element", &vUntaggedSliceOfTagged{}, true},
		{"an interface field", &vInterface{}, true},
		{"a tag on an unexported field", &vUnexportedTag{}, true},
		{"a tag in an embedded struct", &vEmbedded{}, true},
		{"nil", nil, true},
		{"a nil pointer", (*vNoTags)(nil), true},
		{"not a struct", 42, true},
		{"a pointer to a pointer", func() any { p := &vNoTags{}; return &p }(), true},
		{"time.Time", time.Now(), true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := mayHaveTagRules(tc.model); got != tc.want {
				t.Errorf("mayHaveTagRules(%T) = %v, want %v", tc.model, got, tc.want)
			}
		})
	}
}

// TestValidateAnswersAsTheValidatorDid compares Client.Validate with what it
// returned before it learned to skip: the model's own Validate, then the
// validator's walk, for every shape mayHaveTagRules distinguishes.
func TestValidateAnswersAsTheValidatorDid(t *testing.T) {
	ctx := context.Background()
	c := &Client{}
	before := func(model any) error {
		if v, ok := model.(interface{ Validate(context.Context) error }); ok {
			if err := v.Validate(ctx); err != nil {
				return err
			}
		}
		return validator.New().Struct(model)
	}
	models := []any{
		&vNoTags{}, vNoTags{Name: "x"}, &vTagged{}, &vTagged{Name: "ok"}, &vNested{}, &vNestedPtr{},
		&vNestedPtr{Inner: &vTagged{}}, &vInSlice{Items: []vTagged{{}}}, &vUntaggedSliceOfTagged{Items: []vTagged{{}}},
		&vInterface{}, &vInterface{Any: &vTagged{}}, &vUnexportedTag{}, &vEmbedded{}, &vSelfValidating{},
		nil, (*vNoTags)(nil), 42, time.Now(),
	}
	for _, m := range models {
		got, want := c.Validate(ctx, m), before(m)
		if (got == nil) != (want == nil) || (got != nil && got.Error() != want.Error()) {
			t.Errorf("Validate(%T %+v) = %v, before = %v", m, m, got, want)
		}
	}
}

// --- QK-35: assembling has_many rows onto their parents -------------------------

// stickyText is a Scanner that leaves its value alone on NULL, the worst case
// for a scan target reused across rows: only zeroing the row between scans
// keeps one child's value out of the next.
type stickyText struct{ V string }

func (s *stickyText) Scan(src any) error {
	switch v := src.(type) {
	case nil:
	case string:
		s.V = v
	case []byte:
		s.V = string(v)
	default:
		return fmt.Errorf("stickyText: %T", src)
	}
	return nil
}

type smParent struct {
	ID   int64     `db:"id" pk:"true"`
	Name string    `db:"name"`
	Kids []smChild `rel:"has_many" join:"parent_id"`
}

func (smParent) TableName() string { return "sm_parents" }

type smChild struct {
	ID       int64      `db:"id" pk:"true"`
	ParentID int64      `db:"parent_id"`
	Note     *string    `db:"note"`
	Sticky   stickyText `db:"sticky"`
	Tags     []string   `db:"tags"`
}

func (smChild) TableName() string { return "sm_children" }

func TestHasManyAssembly(t *testing.T) {
	ctx := context.Background()
	c, err := New("sqlite", "file:hasmanyassembly?mode=memory&cache=shared",
		WithLogger(slog.New(slog.NewTextHandler(io.Discard, nil))))
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	for _, s := range []string{
		`CREATE TABLE sm_parents (id INTEGER PRIMARY KEY, name TEXT NOT NULL)`,
		`CREATE TABLE sm_children (id INTEGER PRIMARY KEY, parent_id INTEGER NOT NULL, note TEXT, sticky TEXT, tags TEXT)`,
		`INSERT INTO sm_parents (id, name) VALUES (1, 'one'), (2, 'two'), (3, 'none')`,
		`INSERT INTO sm_children (id, parent_id, note, sticky, tags) VALUES
		  (10, 1, 'n10', 's10', '["a","b"]'),
		  (11, 2, NULL, NULL, NULL),
		  (12, 1, NULL, NULL, '[]'),
		  (13, 2, 'n13', 's13', '["c"]')`,
	} {
		if _, err := c.Raw().Exec(s); err != nil {
			t.Fatalf("%s: %v", s, err)
		}
	}

	got, err := For[smParent](ctx, c).OrderBy("id", "ASC").Limit(10).Preload("Kids").List()
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 3 {
		t.Fatalf("parents = %+v", got)
	}
	ids := func(ks []smChild) []int64 {
		out := []int64{}
		for _, k := range ks {
			out = append(out, k.ID)
		}
		return out
	}
	if g := ids(got[0].Kids); !reflect.DeepEqual(g, []int64{10, 12}) {
		t.Errorf("parent 1 kids = %v, want [10 12] in row order", g)
	}
	if g := ids(got[1].Kids); !reflect.DeepEqual(g, []int64{11, 13}) {
		t.Errorf("parent 2 kids = %v, want [11 13] in row order", g)
	}
	if got[2].Kids != nil {
		t.Errorf("parent 3 has no kids and received %+v", got[2].Kids)
	}
	// Row 12 follows row 10 and row 11 is the first scanned with NULLs: none
	// of them may keep a value of the row before.
	k10, k12 := got[0].Kids[0], got[0].Kids[1]
	k11, k13 := got[1].Kids[0], got[1].Kids[1]
	if k10.Note == nil || *k10.Note != "n10" || k10.Sticky.V != "s10" || !reflect.DeepEqual(k10.Tags, []string{"a", "b"}) {
		t.Errorf("kid 10 = %+v", k10)
	}
	if k11.Note != nil || k11.Sticky.V != "" || k11.Tags != nil {
		t.Errorf("kid 11 carries values of another row: note %v, sticky %q, tags %v", k11.Note, k11.Sticky.V, k11.Tags)
	}
	if k12.Note != nil || k12.Sticky.V != "" || len(k12.Tags) != 0 {
		t.Errorf("kid 12 carries values of another row: note %v, sticky %q, tags %v", k12.Note, k12.Sticky.V, k12.Tags)
	}
	if k13.Note == nil || *k13.Note != "n13" || k13.Sticky.V != "s13" {
		t.Errorf("kid 13 = %+v", k13)
	}
	// Two kids never share a slice or a pointer.
	if k10.Note == k13.Note {
		t.Errorf("kids 10 and 13 share their note pointer")
	}

	// The same parent twice in the slice, and a parent that already holds a
	// kid: every copy receives the rows, appended after what it held.
	q := For[smParent](ctx, c).Preload("Kids")
	held := smChild{ID: 99, ParentID: 1}
	parents := []smParent{{ID: 1}, {ID: 2}, {ID: 1, Kids: []smChild{held}}}
	if err := q.loadRelations(parents); err != nil {
		t.Fatal(err)
	}
	if g := ids(parents[0].Kids); !reflect.DeepEqual(g, []int64{10, 12}) {
		t.Errorf("first copy of parent 1 = %v, want [10 12]", g)
	}
	if g := ids(parents[1].Kids); !reflect.DeepEqual(g, []int64{11, 13}) {
		t.Errorf("parent 2 = %v, want [11 13]", g)
	}
	if g := ids(parents[2].Kids); !reflect.DeepEqual(g, []int64{99, 10, 12}) {
		t.Errorf("second copy of parent 1 = %v, want [99 10 12]", g)
	}
}
