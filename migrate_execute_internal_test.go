// Copyright 2026 jcsvwinston
// SPDX-License-Identifier: Apache-2.0

package quark

import "testing"

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
