// Copyright 2026 jcsvwinston
// SPDX-License-Identifier: Apache-2.0

package quark

import "testing"

// TestSupportsTransactionalDDL pins the dialect-classification
// behind ApplyPlan's BEGIN/COMMIT wrapper. The list is empirical,
// not aspirational — see the function godoc for the rationale per
// dialect. A failure here means either a real change in support
// or someone accidentally adding an unsupported engine to the tx
// path (which would silently fail since the BEGIN/COMMIT would be
// no-ops around implicit commits).
func TestSupportsTransactionalDDL(t *testing.T) {
	cases := []struct {
		dialect string
		want    bool
	}{
		{"postgres", true},
		{"mssql", true},
		{"sqlite", true},
		{"mysql", false},
		{"mariadb", false},
		{"oracle", false},
		{"unknown_dialect", false}, // default branch
	}
	for _, tc := range cases {
		t.Run(tc.dialect, func(t *testing.T) {
			if got := supportsTransactionalDDL(tc.dialect); got != tc.want {
				t.Errorf("supportsTransactionalDDL(%q) = %v, want %v", tc.dialect, got, tc.want)
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
