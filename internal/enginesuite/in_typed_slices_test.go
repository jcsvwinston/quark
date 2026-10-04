// Copyright 2026 jcsvwinston
// SPDX-License-Identifier: Apache-2.0

package enginesuite

import (
	"context"
	"sort"
	"strings"
	"testing"

	"github.com/jcsvwinston/quark"
)

// inTypedRow carries one column per element type the IN operand is tested
// with: text, an integer, and a UUID-shaped value (suiteUUID, the shape of
// google/uuid.UUID), which Migrate gives the engine's uuid type or a
// 36-character text column.
type inTypedRow struct {
	ID   int64     `db:"id" pk:"true"`
	Name string    `db:"name"`
	Rank int64     `db:"rank"`
	Ref  suiteUUID `db:"ref"`
}

func (inTypedRow) TableName() string { return "in_typed_rows" }

// inTypedNames is a named slice type, the shape a domain package declares.
type inTypedNames []string

// testInTypedSlices proves QK-33 on the engine this lane runs: the operand
// of IN, NOT IN and BETWEEN may be any slice or array, not only []any —
// before, a []string ended the goroutine with an interface-conversion
// panic. Each case is pinned by the rows the engine answers, and the empty
// case by agreeing with what []any{} does on the same engine (SQLite reads
// `IN ()` as matching nothing; the others reject it), because the typed form
// adds no meaning of its own.
func testInTypedSlices(ctx context.Context, t *testing.T, client *quark.Client) {
	engine := client.Dialect().Name()
	dropTable(client, "in_typed_rows")
	if err := client.Migrate(ctx, &inTypedRow{}); err != nil {
		t.Fatalf("migrate on %s: %v", engine, err)
	}
	t.Cleanup(func() { dropTable(client, "in_typed_rows") })

	refs := [4]suiteUUID{}
	for i := range refs {
		refs[i] = suiteUUID{0xca, 0xfe, 0, 0, 0, 0, 0x40, 0, 0x80, 0, 0, 0, 0, 0, 0, byte(i + 1)}
	}
	for i, name := range []string{"alpha", "beta", "gamma", "delta"} {
		row := inTypedRow{Name: name, Rank: int64(i + 1), Ref: refs[i]}
		if err := quark.For[inTypedRow](ctx, client).Create(&row); err != nil {
			t.Fatalf("seed %s on %s: %v", name, engine, err)
		}
	}

	names := func(t *testing.T, q *quark.Query[inTypedRow]) []string {
		t.Helper()
		rows, err := q.List()
		if err != nil {
			t.Fatalf("list on %s: %v", engine, err)
		}
		out := make([]string, 0, len(rows))
		for _, r := range rows {
			out = append(out, r.Name)
		}
		sort.Strings(out)
		return out
	}

	for _, tc := range []struct {
		name    string
		col, op string
		value   any
		want    string
	}{
		{"[]string", "name", "IN", []string{"alpha", "gamma"}, "alpha,gamma"},
		{"[]int64", "rank", "IN", []int64{2, 4}, "beta,delta"},
		{"[]int", "rank", "IN", []int{1, 3}, "alpha,gamma"},
		{"named []string", "name", "IN", inTypedNames{"beta"}, "beta"},
		{"[]uuid-shaped", "ref", "IN", []suiteUUID{refs[1], refs[3]}, "beta,delta"},
		{"[2]int64 array", "rank", "IN", [2]int64{3, 4}, "delta,gamma"},
		{"[]any still", "name", "IN", []any{"delta"}, "delta"},
		{"NOT IN []string", "name", "NOT IN", []string{"alpha", "beta"}, "delta,gamma"},
		{"NOT IN []uuid-shaped", "ref", "NOT IN", []suiteUUID{refs[0]}, "beta,delta,gamma"},
		{"BETWEEN []int64", "rank", "BETWEEN", []int64{2, 3}, "beta,gamma"},
		{"NOT BETWEEN [2]int", "rank", "NOT BETWEEN", [2]int{2, 3}, "alpha,delta"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := strings.Join(names(t, quark.For[inTypedRow](ctx, client).Where(tc.col, tc.op, tc.value)), ",")
			if got != tc.want {
				t.Errorf("%s %s %#v on %s = %q, want %q", tc.col, tc.op, tc.value, engine, got, tc.want)
			}
		})
	}

	t.Run("WhereNot IN []int64", func(t *testing.T) {
		got := strings.Join(names(t, quark.For[inTypedRow](ctx, client).WhereNot("rank", "IN", []int64{1, 2})), ",")
		if got != "delta,gamma" {
			t.Errorf("WhereNot rank IN on %s = %q", engine, got)
		}
	})

	t.Run("empty typed slice agrees with []any{}", func(t *testing.T) {
		for _, op := range []string{"IN", "NOT IN"} {
			nAny, errAny := quark.For[inTypedRow](ctx, client).Where("name", op, []any{}).Count()
			nTyped, errTyped := quark.For[inTypedRow](ctx, client).Where("name", op, []string{}).Count()
			if (errAny == nil) != (errTyped == nil) || nAny != nTyped {
				t.Errorf("%s on %s: []any{} gave (%d, %v) and []string{} gave (%d, %v)", op, engine, nAny, errAny, nTyped, errTyped)
			}
		}
	})
}
