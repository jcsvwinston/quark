// Copyright 2026 jcsvwinston
// SPDX-License-Identifier: Apache-2.0

package quark

import "testing"

// TestSupportsTransactionalDDL pins the answers behind ApplyPlan's
// BEGIN/COMMIT wrapper, which asks Dialect.SupportsTransactionalDDL (A11 Q2;
// before, a private table keyed on the dialect's name decided). The list is
// empirical, not aspirational: PostgreSQL, SQL Server and SQLite roll DDL
// back; MySQL, MariaDB and Oracle commit implicitly around it, so a
// BEGIN/COMMIT around their DDL would be a no-op that looks like a safety
// net. Oracle's method answered true until the method started to matter.
func TestSupportsTransactionalDDL(t *testing.T) {
	cases := []struct {
		dialect Dialect
		want    bool
	}{
		{PostgreSQL(), true},
		{MSSQL(), true},
		{SQLite(), true},
		{MySQL(), false},
		{MariaDB(), false},
		{Oracle(), false},
	}
	for _, tc := range cases {
		t.Run(tc.dialect.Name(), func(t *testing.T) {
			if got := tc.dialect.SupportsTransactionalDDL(); got != tc.want {
				t.Errorf("%s.SupportsTransactionalDDL() = %v, want %v", tc.dialect.Name(), got, tc.want)
			}
		})
	}
}

// TestWrapExpressionInParens is an internal (package-private) test
// for the CHECK expression wrapper, in the `quark` package rather
// than `quark_test` so the unexported function is callable. The
// table-driven cases pin the contract:
//
//   - Empty strings get wrapped (to `()`) — defensive; the diff
//     layer never produces empty expressions but the wrapper
//     shouldn't panic on them.
//   - Bare predicates without parens get wrapped.
//   - Single fully-enclosing parens stay (no double-wrap).
//   - Multi-term expressions with internal parens get wrapped
//     correctly (the reviewer-found bug: `(a > 0) AND (b < 0)`
//     starts and ends with parens but the opening doesn't pair
//     with the closing — must be wrapped).
//   - Quoted strings with parens inside don't confuse the depth
//     tracker.
func TestWrapExpressionInParens(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want string
	}{
		{"empty", "", "()"},
		{"bare predicate", "a > 0", "(a > 0)"},
		{"already wrapped", "(a > 0)", "(a > 0)"},
		{"already wrapped doubled", "((a > 0))", "((a > 0))"},
		{"multi-term with internal parens (the reviewer-caught bug)",
			"(a > 0) AND (b < 0)", "((a > 0) AND (b < 0))"},
		{"whitespace around already wrapped", "  (a > 0)  ", "(a > 0)"},
		{"quoted close paren inside", "(a = ')')", "(a = ')')"},
		{"quoted with double quotes", `(name = "hi")`, `(name = "hi")`},
		{"no wrap at start", "a > 0 AND b < 0", "(a > 0 AND b < 0)"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := wrapExpressionInParens(tc.in)
			if got != tc.want {
				t.Errorf("wrapExpressionInParens(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}
