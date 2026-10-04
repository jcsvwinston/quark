// Copyright 2026 jcsvwinston
// SPDX-License-Identifier: Apache-2.0

package enginesuite

import (
	"context"
	"fmt"
	"reflect"
	"sort"
	"strings"
	"testing"

	"github.com/jcsvwinston/quark"
)

// wwpRow is the fixture of testWriteWhereParity. Rows are told apart by
// name, not by id: the engines assign the ids, and SQL Server refuses an
// explicit value for an identity column.
type wwpRow struct {
	ID     int64   `db:"id" pk:"true"`
	Name   string  `db:"name"`
	Status string  `db:"status"`
	Score  int64   `db:"score"`
	Note   *string `db:"note"`
	Hits   int64   `db:"hits"`
}

func (wwpRow) TableName() string { return "ww_parity_rows" }

func wwpNote(s string) *string { return &s }

func wwpFixture() []wwpRow {
	return []wwpRow{
		{Name: "alpha", Status: "active", Score: 1},
		{Name: "beta", Status: "active", Score: 2, Note: wwpNote("x")},
		{Name: "gamma", Status: "archived", Score: 3},
		{Name: "100% off", Status: "archived", Score: 4, Note: wwpNote("y")},
		{Name: "delta", Status: "pending", Score: 5},
		{Name: "epsilon", Status: "active", Score: 6, Note: wwpNote("z")},
	}
}

type wwpCase struct {
	name  string
	apply func(*quark.Query[wwpRow]) *quark.Query[wwpRow]
	want  func(wwpRow) bool
}

func wwpCases() []wwpCase {
	type q = quark.Query[wwpRow]
	return []wwpCase{
		{"Where =", func(x *q) *q { return x.Where("status", "=", "active") },
			func(r wwpRow) bool { return r.Status == "active" }},
		{"WhereNot =", func(x *q) *q { return x.WhereNot("status", "=", "active") },
			func(r wwpRow) bool { return r.Status != "active" }},
		{"WhereNot after Where", func(x *q) *q { return x.Where("score", ">", 1).WhereNot("status", "=", "archived") },
			func(r wwpRow) bool { return r.Score > 1 && r.Status != "archived" }},
		{"Or group", func(x *q) *q {
			return x.Where("score", ">", 4).Or(func(g *q) *q { return g.Where("name", "=", "alpha") })
		}, func(r wwpRow) bool { return r.Score > 4 || r.Name == "alpha" }},
		{"WhereNot then an Or group of two", func(x *q) *q {
			return x.WhereNot("status", "=", "active").Or(func(g *q) *q {
				return g.Where("status", "=", "active").Where("score", "<", 2)
			})
		}, func(r wwpRow) bool { return r.Status != "active" || r.Score < 2 }},
		{"IN", func(x *q) *q { return x.WhereIn("score", []any{1, 3, 5}) },
			func(r wwpRow) bool { return r.Score == 1 || r.Score == 3 || r.Score == 5 }},
		{"NOT IN", func(x *q) *q { return x.Where("score", "NOT IN", []any{1, 2}) },
			func(r wwpRow) bool { return r.Score != 1 && r.Score != 2 }},
		{"WhereNot IN", func(x *q) *q { return x.WhereNot("status", "IN", []any{"active", "pending"}) },
			func(r wwpRow) bool { return r.Status != "active" && r.Status != "pending" }},
		{"BETWEEN", func(x *q) *q { return x.WhereBetween("score", 2, 4) },
			func(r wwpRow) bool { return r.Score >= 2 && r.Score <= 4 }},
		{"NOT BETWEEN", func(x *q) *q { return x.Where("score", "NOT BETWEEN", []any{2, 4}) },
			func(r wwpRow) bool { return r.Score < 2 || r.Score > 4 }},
		{"IS NULL", func(x *q) *q { return x.Where("note", "IS NULL", nil) },
			func(r wwpRow) bool { return r.Note == nil }},
		{"IS NOT NULL", func(x *q) *q { return x.Where("note", "IS NOT NULL", nil) },
			func(r wwpRow) bool { return r.Note != nil }},
		{"LIKE escaped", func(x *q) *q { return x.WhereContains("name", "%") },
			func(r wwpRow) bool { return strings.Contains(r.Name, "%") }},
		{"WhereExpr", func(x *q) *q {
			return x.WhereExpr(quark.Or(quark.Eq(quark.Col("status"), quark.Lit("pending")), quark.Gt(quark.Col("score"), quark.Lit(5))))
		}, func(r wwpRow) bool { return r.Status == "pending" || r.Score > 5 }},
	}
}

// testWriteWhereParity proves QK-39 on the engine this lane runs: DeleteBy,
// UpdateMap and UpdateFields touch exactly the rows a SELECT with the same
// conditions returns, and those are the rows a Go predicate over the fixture
// names. Before, the write paths rendered each condition as `col OP ?`
// joined by AND: WhereNot lost its NOT — DeleteBy removed the rows the caller
// excluded — and Or groups, IN, BETWEEN, IS NULL and WhereExpr failed. On the
// engines that number placeholders ($N, @pN, :N) the UPDATE cases also prove
// the WHERE's numbering continues after the SET arguments.
func testWriteWhereParity(ctx context.Context, t *testing.T, client *quark.Client) {
	engine := client.Dialect().Name()
	dropTable(client, "ww_parity_rows")
	if err := client.Migrate(ctx, &wwpRow{}); err != nil {
		t.Fatalf("migrate on %s: %v", engine, err)
	}
	t.Cleanup(func() { dropTable(client, "ww_parity_rows") })

	reseed := func(t *testing.T) []wwpRow {
		t.Helper()
		if _, err := client.Raw().Exec("DELETE FROM " + client.Dialect().Quote("ww_parity_rows")); err != nil {
			t.Fatalf("clear on %s: %v", engine, err)
		}
		for _, r := range wwpFixture() {
			r := r
			if err := quark.For[wwpRow](ctx, client).Create(&r); err != nil {
				t.Fatalf("seed %s on %s: %v", r.Name, engine, err)
			}
		}
		return wwpReadAll(t, ctx, client)
	}
	names := func(rows []wwpRow, keep func(wwpRow) bool) []string {
		out := []string{}
		for _, r := range rows {
			if keep(r) {
				out = append(out, r.Name)
			}
		}
		sort.Strings(out)
		return out
	}
	selected := func(t *testing.T, tc wwpCase) []string {
		t.Helper()
		rows, err := tc.apply(quark.For[wwpRow](ctx, client)).Limit(100).List()
		if err != nil {
			t.Fatalf("select on %s: %v", engine, err)
		}
		got := names(rows, func(wwpRow) bool { return true })
		if want := names(wwpFixture(), tc.want); !reflect.DeepEqual(got, want) {
			t.Fatalf("SELECT on %s returned %v, the predicate names %v", engine, got, want)
		}
		return got
	}

	for _, tc := range wwpCases() {
		t.Run("DeleteBy/"+tc.name, func(t *testing.T) {
			reseed(t)
			want := selected(t, tc)
			n, err := tc.apply(quark.For[wwpRow](ctx, client)).DeleteBy()
			if err != nil {
				t.Fatalf("DeleteBy on %s: %v", engine, err)
			}
			left := map[string]bool{}
			for _, r := range wwpReadAll(t, ctx, client) {
				left[r.Name] = true
			}
			gone := names(wwpFixture(), func(r wwpRow) bool { return !left[r.Name] })
			if !reflect.DeepEqual(gone, want) || n != int64(len(want)) {
				t.Errorf("DeleteBy on %s removed %v (n=%d), the SELECT returned %v", engine, gone, n, want)
			}
		})
		t.Run("UpdateMap/"+tc.name, func(t *testing.T) {
			reseed(t)
			want := selected(t, tc)
			n, err := tc.apply(quark.For[wwpRow](ctx, client)).UpdateMap(map[string]any{"hits": 1, "note": "w"})
			if err != nil {
				t.Fatalf("UpdateMap on %s: %v", engine, err)
			}
			written := names(wwpReadAll(t, ctx, client), func(r wwpRow) bool { return r.Hits == 1 })
			if !reflect.DeepEqual(written, want) || n != int64(len(want)) {
				t.Errorf("UpdateMap on %s wrote %v (n=%d), the SELECT returned %v", engine, written, n, want)
			}
		})
		t.Run("UpdateFields/"+tc.name, func(t *testing.T) {
			rows := reseed(t)
			want := selected(t, tc)
			for _, r := range rows {
				e := r
				e.Hits = 1
				if _, err := tc.apply(quark.For[wwpRow](ctx, client)).UpdateFields(&e, "hits"); err != nil {
					t.Fatalf("UpdateFields(%s) on %s: %v", r.Name, engine, err)
				}
			}
			written := names(wwpReadAll(t, ctx, client), func(r wwpRow) bool { return r.Hits == 1 })
			if !reflect.DeepEqual(written, want) {
				t.Errorf("UpdateFields on %s wrote %v, the SELECT returned %v", engine, written, want)
			}
		})
	}

	t.Run("the reported case", func(t *testing.T) {
		reseed(t)
		n, err := quark.For[wwpRow](ctx, client).WhereNot("status", "=", "active").DeleteBy()
		if err != nil {
			t.Fatalf("DeleteBy on %s: %v", engine, err)
		}
		left := names(wwpReadAll(t, ctx, client), func(wwpRow) bool { return true })
		if n != 3 || fmt.Sprint(left) != "[alpha beta epsilon]" {
			t.Errorf("WhereNot(status = active).DeleteBy() on %s removed %d and left %v; want the three active rows left", engine, n, left)
		}
	})
}

func wwpReadAll(t *testing.T, ctx context.Context, client *quark.Client) []wwpRow {
	t.Helper()
	rows, err := quark.For[wwpRow](ctx, client).Limit(100).List()
	if err != nil {
		t.Fatalf("read back: %v", err)
	}
	return rows
}
