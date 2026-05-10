// Copyright 2026 jcsvwinston
// SPDX-License-Identifier: Apache-2.0

package quark

import (
	"errors"
	"strings"
	"testing"
)

// astCtx returns the dialect + guard pair used by the expr render tests.
// Postgres dialect is the canonical choice because Quote uses double quotes
// and Placeholder is $N — both make the assertions readable.
func astCtx() (Dialect, *SQLGuard) {
	return &PostgresDialect{}, NewSQLGuard()
}

// TestAST_LeavesAndCmp pins the rendering of the smallest legal expressions:
// a leaf column, a leaf literal, and the comparison combinator that ties
// them together. The fragment must be emitted with '?' as the bind marker —
// dialect substitution happens at buildWhereClause time, not here.
func TestAST_LeavesAndCmp(t *testing.T) {
	d, g := astCtx()

	sql, args, err := Eq(Col("name"), Lit("alice")).ToSQL(d, g)
	if err != nil {
		t.Fatalf("ToSQL: %v", err)
	}
	if sql != `"name" = ?` {
		t.Errorf("sql = %q, want %q", sql, `"name" = ?`)
	}
	if len(args) != 1 || args[0] != "alice" {
		t.Errorf("args = %v, want [alice]", args)
	}
}

func TestAST_AndOrNot(t *testing.T) {
	d, g := astCtx()

	expr := And(
		Eq(Col("active"), Lit(true)),
		Or(
			Eq(Col("role"), Lit("admin")),
			Eq(Col("role"), Lit("super")),
		),
	)
	sql, args, err := expr.ToSQL(d, g)
	if err != nil {
		t.Fatalf("ToSQL: %v", err)
	}
	want := `("active" = ? AND ("role" = ? OR "role" = ?))`
	if sql != want {
		t.Errorf("sql = %q, want %q", sql, want)
	}
	if len(args) != 3 {
		t.Errorf("args = %v, want 3 elements", args)
	}

	notSQL, _, err := Not(Eq(Col("flag"), Lit(false))).ToSQL(d, g)
	if err != nil {
		t.Fatalf("Not.ToSQL: %v", err)
	}
	if notSQL != `NOT ("flag" = ?)` {
		t.Errorf("not sql = %q", notSQL)
	}
}

// TestAST_AndSingleAndEmpty captures the parenthesisation contract: an empty
// And renders to "" (a no-op), a single-element And renders without parens
// (so combining with the outer query stays tidy), two or more get wrapped.
func TestAST_AndSingleAndEmpty(t *testing.T) {
	d, g := astCtx()

	emptySQL, _, err := And().ToSQL(d, g)
	if err != nil {
		t.Fatalf("ToSQL: %v", err)
	}
	if emptySQL != "" {
		t.Errorf("empty And sql = %q, want \"\"", emptySQL)
	}

	soloSQL, _, err := And(Eq(Col("x"), Lit(1))).ToSQL(d, g)
	if err != nil {
		t.Fatalf("ToSQL: %v", err)
	}
	if soloSQL != `"x" = ?` {
		t.Errorf("solo And sql = %q (should not wrap parens)", soloSQL)
	}
}

func TestAST_InAndNotIn(t *testing.T) {
	d, g := astCtx()

	sql, args, err := In(Col("status"), Lit("active"), Lit("pending")).ToSQL(d, g)
	if err != nil {
		t.Fatalf("ToSQL: %v", err)
	}
	if sql != `"status" IN (?, ?)` {
		t.Errorf("sql = %q", sql)
	}
	if len(args) != 2 || args[0] != "active" || args[1] != "pending" {
		t.Errorf("args = %v", args)
	}

	notSQL, _, err := NotIn(Col("status"), Lit("archived")).ToSQL(d, g)
	if err != nil {
		t.Fatalf("ToSQL: %v", err)
	}
	if notSQL != `"status" NOT IN (?)` {
		t.Errorf("not sql = %q", notSQL)
	}

	if _, _, err := In(Col("status")).ToSQL(d, g); !errors.Is(err, ErrInvalidQuery) {
		t.Errorf("empty In should error with ErrInvalidQuery, got %v", err)
	}
}

func TestAST_FuncWhitelist(t *testing.T) {
	d, g := astCtx()

	sql, _, err := Func("count", Col("*")).ToSQL(d, g)
	if err != nil {
		t.Fatalf("ToSQL: %v", err)
	}
	if sql != "COUNT(*)" {
		t.Errorf("sql = %q, want COUNT(*)", sql)
	}

	sql, args, err := Func("COALESCE", Col("name"), Lit("anon")).ToSQL(d, g)
	if err != nil {
		t.Fatalf("ToSQL: %v", err)
	}
	if sql != `COALESCE("name", ?)` {
		t.Errorf("sql = %q", sql)
	}
	if len(args) != 1 || args[0] != "anon" {
		t.Errorf("args = %v", args)
	}

	if _, _, err := Func("EVIL_FUNC", Col("x")).ToSQL(d, g); !errors.Is(err, ErrInvalidQuery) {
		t.Errorf("unknown func should error with ErrInvalidQuery, got %v", err)
	}
}

// TestAST_IdentifierGuardedThroughLeaves makes sure the AST inherits the
// builder's identifier-validation contract. Reaching for a junk column name
// must fail through the same SQLGuard the rest of Where uses, regardless of
// nesting depth.
func TestAST_IdentifierGuardedThroughLeaves(t *testing.T) {
	d, g := astCtx()

	_, _, err := And(
		Eq(Col("active"), Lit(true)),
		Eq(Col("name; DROP TABLE users;--"), Lit("x")),
	).ToSQL(d, g)
	if err == nil {
		t.Fatalf("expected identifier validation error, got nil")
	}
	if !strings.Contains(err.Error(), "identifier") &&
		!strings.Contains(err.Error(), "Identifier") &&
		!strings.Contains(err.Error(), "invalid") {
		t.Errorf("error %q should mention identifier/invalid", err)
	}
}

func TestAST_OperatorGuarded(t *testing.T) {
	d, g := astCtx()

	if _, _, err := Cmp(Col("x"), "BOGUS", Lit(1)).ToSQL(d, g); err == nil {
		t.Fatalf("expected operator validation error, got nil")
	}
}
