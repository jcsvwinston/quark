// Copyright 2026 jcsvwinston
// SPDX-License-Identifier: Apache-2.0

package quark

import (
	"context"
	"database/sql/driver"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"reflect"
	"sort"
	"strings"
	"testing"

	_ "modernc.org/sqlite"
)

// QK-33. The operand of IN / NOT IN / BETWEEN / NOT BETWEEN was asserted to
// []any with no check, so the slice a handler has at hand — []string,
// []int64, a slice of UUIDs — ended the goroutine with "interface
// conversion: interface {} is []string, not []interface {}". These tests pin
// every element type by the ROWS it answers on SQLite and by the statement
// it emits, and the engine matrix repeats the row half on the six engines
// (internal/enginesuite, testInTypedSlices).

// inUUID has the reflect shape of github.com/google/uuid.UUID — a [16]byte
// array with a Valuer that answers the text form — without making the
// library require that module.
type inUUID [16]byte

func (u inUUID) Value() (driver.Value, error) { return u.String(), nil }

func (u inUUID) String() string {
	h := hex.EncodeToString(u[:])
	return h[0:8] + "-" + h[8:12] + "-" + h[12:16] + "-" + h[16:20] + "-" + h[20:32]
}

func (u *inUUID) Scan(src any) error {
	var s string
	switch v := src.(type) {
	case string:
		s = v
	case []byte:
		s = string(v)
	default:
		return fmt.Errorf("inUUID: cannot scan %T", src)
	}
	b, err := hex.DecodeString(strings.ReplaceAll(s, "-", ""))
	if err != nil || len(b) != 16 {
		return fmt.Errorf("inUUID: bad value %q", s)
	}
	copy(u[:], b)
	return nil
}

type inRow struct {
	ID   int64  `db:"id" pk:"true"`
	Name string `db:"name"`
	Rank int64  `db:"rank"`
	Ref  inUUID `db:"ref"`
}

func (inRow) TableName() string { return "in_rows" }

// rowNames is a named slice type, the shape a domain package declares.
type rowNames []string

// rowRanks is a named slice of a named element type.
type rowRank int64
type rowRanks []rowRank

var inRefs = [4]inUUID{
	{0xde, 0xad, 0xbe, 0xef, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 1},
	{0xde, 0xad, 0xbe, 0xef, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 2},
	{0xde, 0xad, 0xbe, 0xef, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 3},
	{0xde, 0xad, 0xbe, 0xef, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 4},
}

func inClient(t *testing.T, name string) (*Client, *txStatementRecorder) {
	t.Helper()
	rec := &txStatementRecorder{}
	c, err := New("sqlite", "file:"+name+"?mode=memory&cache=shared",
		WithLogger(slog.New(slog.NewTextHandler(io.Discard, nil))),
		WithQueryObserver(rec))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { _ = c.Close() })
	ctx := context.Background()
	if err := c.Migrate(ctx, &inRow{}); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	for i, n := range []string{"alpha", "beta", "gamma", "delta"} {
		row := inRow{Name: n, Rank: int64(i + 1), Ref: inRefs[i]}
		if err := For[inRow](ctx, c).Create(&row); err != nil {
			t.Fatalf("seed %q: %v", n, err)
		}
	}
	rec.reset()
	return c, rec
}

func inNames(t *testing.T, q *Query[inRow]) []string {
	t.Helper()
	rows, err := q.List()
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	names := make([]string, 0, len(rows))
	for _, r := range rows {
		names = append(names, r.Name)
	}
	sort.Strings(names)
	return names
}

func TestWhereInAcceptsAnySlice(t *testing.T) {
	c, rec := inClient(t, "qk33_in_any_slice")
	ctx := context.Background()

	cases := []struct {
		name    string
		col, op string
		value   any
		want    []string
		clause  string
	}{
		{"[]string", "name", "IN", []string{"alpha", "gamma"}, []string{"alpha", "gamma"}, `"name" IN (?, ?)`},
		{"[]int64", "rank", "IN", []int64{2, 4}, []string{"beta", "delta"}, `"rank" IN (?, ?)`},
		{"[]int", "rank", "IN", []int{1}, []string{"alpha"}, `"rank" IN (?)`},
		{"[]uint32", "rank", "IN", []uint32{3, 4}, []string{"delta", "gamma"}, `"rank" IN (?, ?)`},
		{"[]float64", "rank", "IN", []float64{1, 3}, []string{"alpha", "gamma"}, `"rank" IN (?, ?)`},
		{"[]uuid-shaped", "ref", "IN", []inUUID{inRefs[1], inRefs[2]}, []string{"beta", "gamma"}, `"ref" IN (?, ?)`},
		{"named []string", "name", "IN", rowNames{"beta", "delta"}, []string{"beta", "delta"}, `"name" IN (?, ?)`},
		{"named slice of named int", "rank", "IN", rowRanks{1, 2}, []string{"alpha", "beta"}, `"rank" IN (?, ?)`},
		{"[3]int64 array", "rank", "IN", [3]int64{1, 2, 3}, []string{"alpha", "beta", "gamma"}, `"rank" IN (?, ?, ?)`},
		{"[]any still", "name", "IN", []any{"alpha"}, []string{"alpha"}, `"name" IN (?)`},
		{"NOT IN []string", "name", "NOT IN", []string{"alpha", "beta"}, []string{"delta", "gamma"}, `"name" NOT IN (?, ?)`},
		{"NOT IN []uuid-shaped", "ref", "NOT IN", []inUUID{inRefs[0]}, []string{"beta", "delta", "gamma"}, `"ref" NOT IN (?)`},
		{"lower-case in", "name", "in", []string{"delta"}, []string{"delta"}, `"name" IN (?)`},
		{"padded NOT IN", "name", " not in ", []string{"delta"}, []string{"alpha", "beta", "gamma"}, `"name" NOT IN (?)`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rec.reset()
			got := inNames(t, For[inRow](ctx, c).Where(tc.col, tc.op, tc.value))
			if !reflect.DeepEqual(got, tc.want) {
				t.Errorf("rows = %v, want %v", got, tc.want)
			}
			if !rec.any(tc.clause) {
				t.Errorf("no statement carries %s: %v", tc.clause, rec.all)
			}
		})
	}
}

// The other doors to the same condition: WhereNot, an Or group, Having, and
// the typed slice through WhereIn's own helper.
func TestWhereInTypedSliceThroughEveryDoor(t *testing.T) {
	c, _ := inClient(t, "qk33_in_doors")
	ctx := context.Background()

	if got := inNames(t, For[inRow](ctx, c).WhereNot("name", "IN", []string{"alpha", "beta"})); !reflect.DeepEqual(got, []string{"delta", "gamma"}) {
		t.Errorf("WhereNot IN []string = %v", got)
	}
	got := inNames(t, For[inRow](ctx, c).Where("rank", "=", 1).Or(func(q *Query[inRow]) *Query[inRow] {
		return q.Where("name", "IN", []string{"delta"})
	}))
	if !reflect.DeepEqual(got, []string{"alpha", "delta"}) {
		t.Errorf("Or group with IN []string = %v", got)
	}
	n, err := For[inRow](ctx, c).Where("rank", "IN", []int64{1, 2, 3}).Count()
	if err != nil || n != 3 {
		t.Errorf("Count with IN []int64 = %d, %v", n, err)
	}

	having := inNames(t, For[inRow](ctx, c).Select("name").GroupBy("name").
		Having("name", "IN", []string{"beta", "gamma"}))
	if !reflect.DeepEqual(having, []string{"beta", "gamma"}) {
		t.Errorf("Having IN []string = %v", having)
	}
}

// BETWEEN takes its two bounds from any slice or array too.
func TestWhereBetweenAcceptsAnySlice(t *testing.T) {
	c, rec := inClient(t, "qk33_between")
	ctx := context.Background()
	for _, tc := range []struct {
		name  string
		op    string
		value any
		want  []string
	}{
		{"[]int", "BETWEEN", []int{2, 3}, []string{"beta", "gamma"}},
		{"[2]int64", "BETWEEN", [2]int64{3, 4}, []string{"delta", "gamma"}},
		{"named slice", "NOT BETWEEN", rowRanks{2, 3}, []string{"alpha", "delta"}},
		{"[]any still", "BETWEEN", []any{1, 1}, []string{"alpha"}},
		{"lower-case between", "between", []int64{4, 9}, []string{"delta"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rec.reset()
			got := inNames(t, For[inRow](ctx, c).Where("rank", tc.op, tc.value))
			if !reflect.DeepEqual(got, tc.want) {
				t.Errorf("rows = %v, want %v", got, tc.want)
			}
			if !rec.any(" BETWEEN ? AND ?") {
				t.Errorf("no statement carries BETWEEN ? AND ?: %v", rec.all)
			}
		})
	}
}

// An empty slice of any type means what []any{} always meant: `IN ()`, which
// SQLite reads as matching nothing (and NOT IN as matching everything) and
// the other engines reject. The typed form adds no meaning of its own.
func TestWhereInEmptySliceKeepsTheEmptyAnyMeaning(t *testing.T) {
	c, rec := inClient(t, "qk33_in_empty")
	ctx := context.Background()
	for _, tc := range []struct {
		name  string
		op    string
		value any
		want  int64
	}{
		{"[]any{}", "IN", []any{}, 0},
		{"[]string{}", "IN", []string{}, 0},
		{"nil []int64", "IN", []int64(nil), 0},
		{"[0]int array", "IN", [0]int{}, 0},
		{"NOT IN []any{}", "NOT IN", []any{}, 4},
		{"NOT IN []string{}", "NOT IN", []string{}, 4},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rec.reset()
			n, err := For[inRow](ctx, c).Where("name", tc.op, tc.value).Count()
			if err != nil {
				t.Fatalf("count: %v", err)
			}
			if n != tc.want {
				t.Errorf("count = %d, want %d", n, tc.want)
			}
			if !rec.any(`"name" ` + tc.op + ` ()`) {
				t.Errorf("want the empty list as `%s ()`: %v", tc.op, rec.all)
			}
		})
	}
}

// What is not a list is refused with ErrInvalidQuery before any SQL runs —
// it used to panic. A byte string is one value to database/sql, so it is not
// read as a list of bytes; a UUID passed bare is the same case.
func TestWhereInRefusesWhatIsNotAList(t *testing.T) {
	c, rec := inClient(t, "qk33_in_refused")
	ctx := context.Background()
	for _, tc := range []struct {
		name  string
		op    string
		value any
		msg   string
	}{
		{"nil", "IN", nil, "got <nil>"},
		{"scalar", "IN", 7, "got int"},
		{"string", "NOT IN", "alpha", "got string"},
		{"[]byte", "IN", []byte("alpha"), "single value"},
		{"bare uuid-shaped", "IN", inRefs[0], "single value"},
		{"pointer to a slice", "IN", &[]string{"alpha"}, "got *[]string"},
		{"BETWEEN one value", "BETWEEN", []int{1}, "takes two values, got 1"},
		{"BETWEEN no value", "NOT BETWEEN", []int64{}, "takes two values, got 0"},
		{"BETWEEN scalar", "BETWEEN", 3, "got int"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rec.reset()
			var err error
			func() {
				defer func() {
					if r := recover(); r != nil {
						t.Fatalf("panicked: %v", r)
					}
				}()
				_, err = For[inRow](ctx, c).Where("name", tc.op, tc.value).List()
			}()
			if !errors.Is(err, ErrInvalidQuery) {
				t.Fatalf("err = %v, want ErrInvalidQuery", err)
			}
			if !strings.Contains(err.Error(), tc.msg) {
				t.Errorf("err = %q, want it to say %q", err, tc.msg)
			}
			if rec.count() != 0 {
				t.Errorf("a statement reached the engine: %v", rec.all)
			}
		})
	}
}

// BETWEEN with more than two values binds the first two, as it did before
// QK-33: refusing the third would change what a call that works today does,
// which QADR-0010 keeps for the major.
func TestWhereBetweenMoreThanTwoKeepsTheFirstTwo(t *testing.T) {
	c, _ := inClient(t, "qk33_between_three")
	ctx := context.Background()
	for _, v := range []any{[]any{1, 2, 4}, []int{1, 2, 4}} {
		if got := inNames(t, For[inRow](ctx, c).Where("rank", "BETWEEN", v)); !reflect.DeepEqual(got, []string{"alpha", "beta"}) {
			t.Errorf("BETWEEN %#v = %v, want the first two bounds", v, got)
		}
	}
}

func TestListOperand(t *testing.T) {
	for _, tc := range []struct {
		in   any
		want []any
	}{
		{[]any{1, "a"}, []any{1, "a"}},
		{[]string{"a", "b"}, []any{"a", "b"}},
		{[]int64{1}, []any{int64(1)}},
		{[2]bool{true, false}, []any{true, false}},
		{rowRanks{3}, []any{rowRank(3)}},
		{[][]byte{[]byte("x")}, []any{[]byte("x")}},
		{[]inUUID{inRefs[0]}, []any{inRefs[0]}},
		{[]string(nil), []any{}},
	} {
		got, err := listOperand("IN", tc.in)
		if err != nil {
			t.Errorf("listOperand(%#v): %v", tc.in, err)
			continue
		}
		if len(got) != len(tc.want) || (len(got) > 0 && !reflect.DeepEqual(got, tc.want)) {
			t.Errorf("listOperand(%#v) = %#v, want %#v", tc.in, got, tc.want)
		}
	}
}
