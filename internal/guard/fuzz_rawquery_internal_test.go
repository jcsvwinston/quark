// Copyright 2026 jcsvwinston
// SPDX-License-Identifier: Apache-2.0

// This file is in package guard (not guard_test) because maskLiterals is the
// piece under test and it is unexported: it is the scanner H-Q10 added so a
// legitimate literal like 'range--max' stops reading as a comment marker. A
// scanner that loses track of the literal state does not crash — it creates a
// blind spot, and the payload after a stray quote stops being scanned at all.
// So the properties here are about the scanner's algebra, and about the pair
// (masker, validator) never disagreeing on where a literal ends.
package guard

import (
	"errors"
	"strings"
	"testing"
)

// clip bounds a fuzz input to n bytes. See the note in FuzzValidateRawQuery.
func clip(s string, n int) string {
	if len(s) > n {
		return s[:n]
	}
	return s
}

// isSubsequence reports whether a is obtained from b by deleting bytes only.
// The masker is allowed to DROP literal contents and nothing else — it may
// never rewrite, reorder or introduce a byte.
func isSubsequence(a, b string) bool {
	i := 0
	for j := 0; j < len(b) && i < len(a); j++ {
		if a[i] == b[j] {
			i++
		}
	}
	return i == len(a)
}

// endsOutsideLiteral reports whether the query's last byte is outside a quoted
// literal. maskLiterals keeps both delimiters of every literal it closes and
// keeps the opening one of a literal it never closes, so an odd number of
// quotes in the masked text means exactly one unterminated literal.
func endsOutsideLiteral(masked string) bool { return strings.Count(masked, "'")%2 == 0 }

// FuzzValidateRawQuery drives the raw-query backstop and the literal masker
// underneath it. ValidateRawQuery is documented as a heuristic, not a complete
// filter, so the target does not assert that it catches every payload; it
// asserts the properties the heuristic must have to be worth having at all.
func FuzzValidateRawQuery(f *testing.F) {
	seeds := []struct{ query, tail string }{
		// The table-test corpus (safe and rejected alike).
		{"SELECT * FROM users WHERE id = $1", ""},
		{"SELECT * FROM users WHERE id = ?", " AND active = 1"},
		{"SELECT * FROM users", ""},
		{"SELECT 1; DROP TABLE users", ""},
		{"SELECT id FROM users UNION SELECT password FROM admins", ""},
		{"SELECT * FROM users WHERE id = 1 OR 1=1", ""},
		{"SELECT 1; DELETE FROM users", ""},
		{"SELECT * FROM users WHERE id = ? -- AND active = 1", ""},
		{"SELECT * FROM users WHERE name = ?--", ""},
		{"SELECT /*+ MAX_EXECUTION_TIME(1000) */ id FROM users WHERE id = ?", ""},
		// H-Q10: structure inside a literal is data...
		{"SELECT * FROM tags WHERE name = 'range--max'", ""},
		{"SELECT * FROM notes WHERE body = '; DROP TABLE x'", ""},
		{"SELECT * FROM posts WHERE title = 'union select in prose'", ""},
		{"SELECT * FROM notes WHERE body = 'it''s a -- dash'", ""},
		// ...and the same markers outside one are still structure.
		{"SELECT * FROM tags WHERE name = 'x' -- AND hidden = 1", ""},
		{"SELECT * FROM tags WHERE name = 'x'; DROP TABLE users", ""},
		{"SELECT id FROM users WHERE note = 'x' UNION SELECT password FROM admins", ""},
		{"SELECT * FROM users WHERE name = ? OR '1'='1'", ""},
		// Unterminated and doubled quotes: where a masker loses its state.
		{"SELECT * FROM t WHERE a = 'unterminated", "; DROP TABLE x"},
		{"SELECT * FROM t WHERE a = ''", "; DROP TABLE x"},
		{"SELECT * FROM t WHERE a = ''''", " -- x"},
		{"'", "'"},
		{"''", "--"},
		{"", ""},
	}
	for _, s := range seeds {
		f.Add(s.query, s.tail)
	}

	g := New()
	f.Fuzz(func(t *testing.T, query, tail string) {
		// Every property below rescans the query several times, so an input
		// the mutator has grown into the kilobytes costs milliseconds per
		// exec and the lane's throughput collapses without finding anything
		// new: the interesting behaviour is all in the first few hundred
		// bytes. Truncate rather than skip, so no exec is wasted and the
		// value printed on failure is the value that was tested.
		query, tail = clip(query, 1024), clip(tail, 256)

		masked := maskLiterals(query)

		// The masker only ever deletes.
		if len(masked) > len(query) || !isSubsequence(masked, query) {
			t.Fatalf("maskLiterals(%q) = %q is not the input with bytes deleted", query, masked)
		}
		// Masked text is already free of literal contents: masking it again
		// must be a no-op, or the state machine is not converging.
		if again := maskLiterals(masked); again != masked {
			t.Fatalf("maskLiterals is not idempotent: %q -> %q -> %q", query, masked, again)
		}
		// A query with no literal at all must be handed to the checks intact.
		if !strings.Contains(query, "'") && masked != query {
			t.Fatalf("maskLiterals rewrote a quote-free query: %q -> %q", query, masked)
		}
		// Everything before the first quote is outside every literal.
		if i := strings.IndexByte(query, '\''); i >= 0 {
			if !strings.HasPrefix(masked, query[:i]) {
				t.Fatalf("maskLiterals dropped bytes before the first quote: %q -> %q", query, masked)
			}
		}
		// The masker's own answer about where a literal ends must compose: if
		// the query ends outside a literal, appending text cannot change how
		// anything before it was read. This is the blind-spot property — a
		// scanner that mis-tracks '' would mask the appended payload away.
		//
		// The one boundary where concatenation legitimately changes the
		// reading is a tail that opens with a quote: SQL itself lexes
		// "'a'" + "'b'" as the single literal 'a''b', so a closing quote and
		// the tail's opening quote merge into one escape. That is the
		// language, not the scanner, so those tails are excluded.
		if endsOutsideLiteral(masked) && !strings.HasPrefix(tail, "'") {
			if got, want := maskLiterals(query+tail), masked+maskLiterals(tail); got != want {
				t.Fatalf("maskLiterals(%q + %q) = %q, want %q: the scanner disagrees with itself about where the literal ended", query, tail, got, want)
			}
		}

		// Rejections stay reachable through the public sentinel.
		err := g.ValidateRawQuery(query, false)
		if err != nil && !errors.Is(err, ErrInvalidQuery) {
			t.Fatalf("ValidateRawQuery(%q) = %v, which is not errors.Is(ErrInvalidQuery)", query, err)
		}
		// The placeholder requirement is unconditional: it is the real
		// boundary the heuristics sit behind.
		if !HasPlaceholders(query) {
			if perr := g.ValidateRawQuery(query, true); perr == nil {
				t.Fatalf("ValidateRawQuery(%q, requirePlaceholders=true) accepted a query with no placeholder", query)
			}
		}

		// Masking must not create a blind spot: a payload appended where no
		// literal is open is structure, and structure is what the heuristic
		// exists to catch.
		if endsOutsideLiteral(masked) {
			for _, payload := range []string{"; DROP TABLE x", " -- x", "; DELETE FROM x"} {
				if g.ValidateRawQuery(query+payload, false) == nil {
					t.Fatalf("ValidateRawQuery accepted %q with %q appended outside every literal", query, payload)
				}
			}
		}
	})
}
