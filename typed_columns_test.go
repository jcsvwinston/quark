// Copyright 2026 jcsvwinston
// SPDX-License-Identifier: Apache-2.0

package quark

import (
	"reflect"
	"testing"
)

// TestWherePLowersToSameConditions proves the F6-4 typed accessors are pure
// sugar: every predicate built through a TypedColumn lowers to exactly the
// internal condition the equivalent string Where(...) call produces, so the
// two APIs are interchangeable and mix freely.
func TestWherePLowersToSameConditions(t *testing.T) {
	age := NewTypedColumn[int]("age")
	email := NewTypedStringColumn("email")
	name := NewTypedColumn[string]("name")

	got := (&Query[struct{}]{}).WhereP(
		age.Gte(18),
		age.Lt(65),
		email.Like("%@example.com"),
		name.In("a", "b"),
		age.Between(20, 30),
		email.IsNotNull(),
		name.Neq("admin"),
	)

	want := (&Query[struct{}]{}).
		Where("age", ">=", 18).
		Where("age", "<", 65).
		Where("email", "LIKE", "%@example.com").
		WhereIn("name", []any{"a", "b"}).
		WhereBetween("age", 20, 30).
		Where("email", "IS NOT NULL", nil).
		Where("name", "!=", "admin")

	if len(got.where) != len(want.where) {
		t.Fatalf("condition count: WhereP=%d string=%d", len(got.where), len(want.where))
	}
	for i := range got.where {
		g, w := got.where[i], want.where[i]
		if g.column != w.column || g.operator != w.operator || g.logic != w.logic {
			t.Errorf("cond %d: got {col:%q op:%q logic:%q} want {col:%q op:%q logic:%q}",
				i, g.column, g.operator, g.logic, w.column, w.operator, w.logic)
		}
		if !reflect.DeepEqual(g.value, w.value) {
			t.Errorf("cond %d (%s): value got %#v want %#v", i, g.column, g.value, w.value)
		}
	}
}

// TestTypedColumnPredicateShapes pins the operator/value each TypedColumn
// method emits, so a future refactor can't silently change the SQL a
// predicate lowers to.
func TestTypedColumnPredicateShapes(t *testing.T) {
	c := NewTypedColumn[int]("n")
	cases := []struct {
		name string
		pred Predicate
		op   string
		val  any
	}{
		{"Eq", c.Eq(1), "=", 1},
		{"Neq", c.Neq(1), "!=", 1},
		{"Gt", c.Gt(1), ">", 1},
		{"Gte", c.Gte(1), ">=", 1},
		{"Lt", c.Lt(1), "<", 1},
		{"Lte", c.Lte(1), "<=", 1},
		{"In", c.In(1, 2), "IN", []any{1, 2}},
		{"NotIn", c.NotIn(1, 2), "NOT IN", []any{1, 2}},
		// Empty In/NotIn lower to the same empty []any the string WhereIn
		// produces (a non-nil zero-length slice), preserving interchangeability
		// in the degenerate case rather than special-casing it.
		{"InEmpty", c.In(), "IN", []any{}},
		{"NotInEmpty", c.NotIn(), "NOT IN", []any{}},
		{"Between", c.Between(1, 9), "BETWEEN", []any{1, 9}},
		{"IsNull", c.IsNull(), "IS NULL", nil},
		{"IsNotNull", c.IsNotNull(), "IS NOT NULL", nil},
	}
	for _, tc := range cases {
		if tc.pred.column != "n" {
			t.Errorf("%s: column = %q, want n", tc.name, tc.pred.column)
		}
		if tc.pred.operator != tc.op {
			t.Errorf("%s: operator = %q, want %q", tc.name, tc.pred.operator, tc.op)
		}
		if !reflect.DeepEqual(tc.pred.value, tc.val) {
			t.Errorf("%s: value = %#v, want %#v", tc.name, tc.pred.value, tc.val)
		}
	}

	s := NewTypedStringColumn("s")
	if p := s.Like("a%"); p.operator != "LIKE" || p.value != "a%" || p.column != "s" {
		t.Errorf("Like: got %+v", p)
	}
	if p := s.NotLike("a%"); p.operator != "NOT LIKE" || p.value != "a%" {
		t.Errorf("NotLike: got %+v", p)
	}
	// StringColumn embeds TypedColumn[string], so Eq is promoted.
	if p := s.Eq("x"); p.operator != "=" || p.value != "x" || p.column != "s" {
		t.Errorf("promoted Eq: got %+v", p)
	}
}
