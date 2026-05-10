// Copyright 2026 jcsvwinston
// SPDX-License-Identifier: Apache-2.0

package quark

import (
	"strings"
	"testing"
)

// TestWindow_OverPartitionByOrderBy pins the rendering of the typical
// `RANK() OVER (PARTITION BY <col> ORDER BY <col> DESC)` shape used in
// reporting queries.
func TestWindow_OverPartitionByOrderBy(t *testing.T) {
	d, g := astCtx()

	expr := Over(Rank(),
		NewWindow().
			PartitionBy(Col("status")).
			OrderBy(Col("amount"), true),
	)
	sql, args, err := expr.ToSQL(d, g)
	if err != nil {
		t.Fatalf("ToSQL: %v", err)
	}
	want := `RANK() OVER (PARTITION BY "status" ORDER BY "amount" DESC)`
	if sql != want {
		t.Errorf("sql = %q, want %q", sql, want)
	}
	if len(args) != 0 {
		t.Errorf("args = %v, want empty", args)
	}
}

func TestWindow_RowNumberAndDenseRank(t *testing.T) {
	d, g := astCtx()

	for _, tc := range []struct {
		expr Expr
		want string
	}{
		{Over(RowNumber(), NewWindow().OrderBy(Col("id"), false)), `ROW_NUMBER() OVER (ORDER BY "id")`},
		{Over(DenseRank(), NewWindow().PartitionBy(Col("region"))), `DENSE_RANK() OVER (PARTITION BY "region")`},
	} {
		sql, _, err := tc.expr.ToSQL(d, g)
		if err != nil {
			t.Fatalf("ToSQL: %v", err)
		}
		if sql != tc.want {
			t.Errorf("sql = %q, want %q", sql, tc.want)
		}
	}
}

// TestWindow_LagBindsOffset confirms the Lag/Lead offset is bound as a
// parameter, not interpolated into the SQL surface.
func TestWindow_LagBindsOffset(t *testing.T) {
	d, g := astCtx()

	expr := Over(Lag(Col("amount"), 3), NewWindow().OrderBy(Col("id"), false))
	sql, args, err := expr.ToSQL(d, g)
	if err != nil {
		t.Fatalf("ToSQL: %v", err)
	}
	want := `LAG("amount", ?) OVER (ORDER BY "id")`
	if sql != want {
		t.Errorf("sql = %q, want %q", sql, want)
	}
	if len(args) != 1 || args[0] != 3 {
		t.Errorf("args = %v, want [3]", args)
	}
}

func TestWindow_SumOverEmptyWindow(t *testing.T) {
	// Aggregate-as-window is the canonical "running total" pattern.
	// Empty Window renders as `OVER ()` which is legal SQL for a single
	// partition over the entire result set.
	d, g := astCtx()
	expr := Over(Func("SUM", Col("amount")), NewWindow())
	sql, _, err := expr.ToSQL(d, g)
	if err != nil {
		t.Fatalf("ToSQL: %v", err)
	}
	if sql != `SUM("amount") OVER ()` {
		t.Errorf("sql = %q", sql)
	}
}

func TestWindow_NilExprAndNilWindowError(t *testing.T) {
	d, g := astCtx()
	if _, _, err := Over(nil, NewWindow()).ToSQL(d, g); err == nil {
		t.Errorf("Over(nil, w) should error")
	}
	if _, _, err := Over(Rank(), nil).ToSQL(d, g); err == nil {
		t.Errorf("Over(e, nil) should error")
	}
}

// TestWindow_ChainImmutability ensures PartitionBy/OrderBy return new
// copies so a single Window definition can be reused safely.
func TestWindow_ChainImmutability(t *testing.T) {
	base := NewWindow().PartitionBy(Col("status"))
	a := base.OrderBy(Col("a"), false)
	b := base.OrderBy(Col("b"), true)

	d, g := astCtx()
	asql, _, _ := Over(Rank(), a).ToSQL(d, g)
	bsql, _, _ := Over(Rank(), b).ToSQL(d, g)
	if !strings.Contains(asql, `ORDER BY "a"`) {
		t.Errorf("expected branch a to keep its own OrderBy: %q", asql)
	}
	if !strings.Contains(bsql, `ORDER BY "b" DESC`) {
		t.Errorf("expected branch b to keep its own OrderBy: %q", bsql)
	}
	if strings.Contains(asql, `ORDER BY "b"`) {
		t.Errorf("branch a leaked branch b's OrderBy: %q", asql)
	}
}
