// Copyright 2026 jcsvwinston
// SPDX-License-Identifier: Apache-2.0

package quark

import (
	"errors"
	"strings"
	"testing"
)

// The constructors added for A4/S1 exist so that COUNT(DISTINCT …),
// conditional aggregates and JSON projection stop needing RawQuery WITHOUT
// widening astFunctionWhitelist. These tests pin both halves: the SQL they
// render, and the fact that no caller string reaches the SQL surface.

func TestCountDistinctRendersModifier(t *testing.T) {
	sql, args, err := CountDistinct(Col("user_id")).ToSQL(SQLite(), NewSQLGuard())
	if err != nil {
		t.Fatalf("CountDistinct: %v", err)
	}
	if want := `COUNT(DISTINCT "user_id")`; sql != want {
		t.Errorf("sql = %q, want %q", sql, want)
	}
	if len(args) != 0 {
		t.Errorf("args = %v, want none", args)
	}
}

func TestCountDistinctRejectsNil(t *testing.T) {
	if _, _, err := CountDistinct(nil).ToSQL(SQLite(), NewSQLGuard()); !errors.Is(err, ErrInvalidQuery) {
		t.Errorf("err = %v, want ErrInvalidQuery", err)
	}
}

func TestCaseRendersBranchesInOrder(t *testing.T) {
	e := Case().
		When(Eq(Col("status"), Lit("paid")), Lit(1)).
		When(Eq(Col("status"), Lit("shipped")), Lit(2)).
		Else(Lit(0))
	sql, args, err := e.ToSQL(SQLite(), NewSQLGuard())
	if err != nil {
		t.Fatalf("Case: %v", err)
	}
	want := `CASE WHEN "status" = ? THEN ? WHEN "status" = ? THEN ? ELSE ? END`
	if sql != want {
		t.Errorf("sql = %q, want %q", sql, want)
	}
	// Every literal binds as a parameter: five values, in branch order.
	if len(args) != 5 {
		t.Fatalf("args = %v, want 5", args)
	}
	if args[0] != "paid" || args[1] != 1 || args[2] != "shipped" || args[3] != 2 || args[4] != 0 {
		t.Errorf("args = %v, want [paid 1 shipped 2 0]", args)
	}
}

func TestCaseWithoutElseOmitsIt(t *testing.T) {
	sql, _, err := Case().When(Gt(Col("total"), Lit(100)), Lit(1)).ToSQL(SQLite(), NewSQLGuard())
	if err != nil {
		t.Fatalf("Case: %v", err)
	}
	if strings.Contains(sql, "ELSE") {
		t.Errorf("sql = %q, should not carry an ELSE", sql)
	}
}

func TestCaseWithoutWhenIsRejected(t *testing.T) {
	// A branchless CASE is always a mistake and engines disagree on whether
	// they reject it, so the AST does.
	if _, _, err := Case().Else(Lit(0)).ToSQL(SQLite(), NewSQLGuard()); !errors.Is(err, ErrInvalidQuery) {
		t.Errorf("err = %v, want ErrInvalidQuery", err)
	}
}

func TestConditionalAggregateComposes(t *testing.T) {
	// The case S0 measured as impossible: SUM(CASE WHEN … THEN 1 ELSE 0 END).
	e := Func("SUM", Case().When(Eq(Col("status"), Lit("paid")), Lit(1)).Else(Lit(0)))
	sql, _, err := e.ToSQL(SQLite(), NewSQLGuard())
	if err != nil {
		t.Fatalf("conditional aggregate: %v", err)
	}
	if want := `SUM(CASE WHEN "status" = ? THEN ? ELSE ? END)`; sql != want {
		t.Errorf("sql = %q, want %q", sql, want)
	}
}

func TestJSONExtractIsDialectRendered(t *testing.T) {
	// The accessor is named differently by every engine — that is why the
	// name could never have been a whitelist entry.
	for _, tc := range []struct {
		dialect Dialect
		want    string
	}{
		{SQLite(), "JSON_EXTRACT"},
		{MySQL(), "JSON_EXTRACT"},
		{PostgreSQL(), "jsonb_extract_path_text"},
		{MSSQL(), "JSON_VALUE"},
		{Oracle(), "JSON_VALUE"},
	} {
		sql, _, err := JSONExtract("profile", "tier").ToSQL(tc.dialect, NewSQLGuard())
		if err != nil {
			t.Errorf("%s: %v", tc.dialect.Name(), err)
			continue
		}
		if !strings.Contains(sql, tc.want) {
			t.Errorf("%s: sql = %q, want it to contain %q", tc.dialect.Name(), sql, tc.want)
		}
	}
}

func TestJSONExtractValidatesColumn(t *testing.T) {
	// The column is a caller string, so it goes through the identifier
	// barrier before the dialect interpolates it.
	_, _, err := JSONExtract(`profile"; DROP TABLE users; --`, "tier").ToSQL(SQLite(), NewSQLGuard())
	if err == nil {
		t.Fatal("an invalid identifier must be rejected")
	}
}

func TestJSONExtractRejectsJSONPathSyntax(t *testing.T) {
	// Paths are dotted identifier chains. "$.tier" is the JSONPath spelling
	// and the dialect's validator rejects it — pinned because S0 got this
	// wrong first and the error is the only thing that says so.
	if _, _, err := JSONExtract("profile", "$.tier").ToSQL(SQLite(), NewSQLGuard()); err == nil {
		t.Fatal("JSONPath syntax must be rejected")
	}
}

func TestWindowFunctionLeavesRenderConstantNames(t *testing.T) {
	w := NewWindow().OrderBy(Col("total"), false)
	for _, tc := range []struct {
		expr Expr
		want string
	}{
		{NTile(4), "NTILE(?)"},
		{PercentRank(), "PERCENT_RANK()"},
		{CumeDist(), "CUME_DIST()"},
		{FirstValue(Col("total")), `FIRST_VALUE("total")`},
		{LastValue(Col("total")), `LAST_VALUE("total")`},
		{NthValue(Col("total"), 2), `NTH_VALUE("total", ?)`},
	} {
		sql, _, err := Over(tc.expr, w).ToSQL(SQLite(), NewSQLGuard())
		if err != nil {
			t.Errorf("%s: %v", tc.want, err)
			continue
		}
		if !strings.HasPrefix(sql, tc.want+" OVER (") {
			t.Errorf("sql = %q, want it to start with %q OVER (", sql, tc.want)
		}
	}
}

func TestFuncWhitelistIsUnchanged(t *testing.T) {
	// S1 deliberately did NOT widen the whitelist: the new capabilities are
	// constructors with constant names. If someone widens it later, this
	// test is where the decision gets re-taken rather than drifting.
	if len(astFunctionWhitelist) != 10 {
		t.Errorf("astFunctionWhitelist has %d entries, want 10 — widening it is a decision about a security barrier, see docs/query-bench.md",
			len(astFunctionWhitelist))
	}
	if _, _, err := Func("NTILE", Lit(4)).ToSQL(SQLite(), NewSQLGuard()); !errors.Is(err, ErrInvalidQuery) {
		t.Errorf("an arbitrary name must still be rejected by Func, got %v", err)
	}
}
