// Copyright 2026 jcsvwinston
// SPDX-License-Identifier: Apache-2.0

// Native Go fuzz targets for the SQL guard — the parsing surface that stands
// between caller-supplied strings and the SQL text Quark emits. A hole here is
// an injection, not a crash, so every target asserts an invariant about what an
// ACCEPTED input is allowed to contain rather than merely that the validator
// returns without panicking.
//
// The seed corpora are the inputs the table tests in guard_test.go already
// carry, plus the shapes that broke in the past: the embedded closing quote
// (H-Q7), the literal-masking false positive and its blind spot (H-Q10), the
// qualified identifier (AQ-01) and the JSON path that reached the SQL (P0-2).
// Corpus entries kept on disk live in testdata/fuzz/<target>/.
package guard_test

import (
	"errors"
	"regexp"
	"strings"
	"testing"

	"github.com/jcsvwinston/quark/internal/guard"
)

// identifierCharset reports whether every byte of s is one that SQL text can
// carry with nothing around it: letters, digits, underscore. None of them can
// open a string literal, start a comment or terminate a statement, which is
// the whole reason the guard exists.
func identifierCharset(s string) bool {
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case c >= 'a' && c <= 'z', c >= 'A' && c <= 'Z', c >= '0' && c <= '9', c == '_':
		default:
			return false
		}
	}
	return true
}

func startsWithDigit(s string) bool { return s != "" && s[0] >= '0' && s[0] <= '9' }

// qualifiedRender is the only shape QuoteQualifiedIdentifier may produce with a
// quoter that escapes nothing: each segment inside its own pair of quotes, and
// the single dot as the one structural character living outside them.
var qualifiedRender = regexp.MustCompile(`^"[A-Za-z_][A-Za-z0-9_]*"(\."[A-Za-z_][A-Za-z0-9_]*")?$`)

// FuzzValidateIdentifier pins the contract every builder position depends on:
// an identifier the guard accepts can be pasted into SQL between two quote
// characters and cannot escape them. fakeQuoter (guard_test.go) deliberately
// escapes NOTHING, so the assertion fails the moment the validator lets a
// quote, a semicolon or a comment marker through — the guard alone has to be
// enough, which is exactly what the query/migrate/cte/events call sites assume.
func FuzzValidateIdentifier(f *testing.F) {
	for _, seed := range []string{
		// Accepted shapes from the table tests.
		"users", "user_id", "myTable", "column123", "_private", "a", "z9",
		"users.id", "orders.total", "j_users.created_at", "_t._c",
		strings.Repeat("a", 64),
		// Rejected shapes, payloads included.
		"", "DROP", "select", "user-id", "user id", "user@id", "user$id",
		"1user", "123", "-start", "table;drop", "col; DROP",
		`id; DROP TABLE ident_docs;--`,
		strings.Repeat("a", 65), strings.Repeat("x", 200),
		// AQ-01: the qualified form widened what a column reference can NAME
		// without widening what it can CONTAIN.
		"a.b.c", ".id", "users.", "users..id", "users.id; DROP",
		"users.SELECT", `users."id"`, "users.id ",
		// H-Q7: the characters that terminate a quoted identifier per dialect.
		`a"b`, "a`b", "a]b", "a'b", "a\x00b", "a\nb",
	} {
		f.Add(seed)
	}

	g := guard.New()
	f.Fuzz(func(t *testing.T, name string) {
		err := g.ValidateIdentifier(name)

		// Every rejection stays reachable through the public sentinel: callers
		// classify with errors.Is, not by string matching.
		if err != nil && !errors.Is(err, guard.ErrInvalidIdentifier) {
			t.Fatalf("ValidateIdentifier(%q) = %v, which is not errors.Is(ErrInvalidIdentifier)", name, err)
		}

		switch {
		case err == nil:
			if name == "" {
				t.Fatalf("ValidateIdentifier accepted the empty identifier")
			}
			if len(name) > 64 {
				t.Fatalf("ValidateIdentifier accepted %d bytes, over the 64-byte bound", len(name))
			}
			if !identifierCharset(name) {
				t.Fatalf("ValidateIdentifier accepted %q, which carries a byte outside [A-Za-z0-9_]", name)
			}
			if startsWithDigit(name) {
				t.Fatalf("ValidateIdentifier accepted %q, which starts with a digit", name)
			}
			// The injection invariant, stated where it can actually fail:
			// quoting an accepted identifier with a quoter that escapes
			// nothing still yields ONE well-formed quoted identifier.
			quoted, qerr := g.QuoteIdentifier(fakeQuoter{}, name)
			if qerr != nil {
				t.Fatalf("QuoteIdentifier(%q) rejected what ValidateIdentifier accepted: %v", name, qerr)
			}
			if quoted != `"`+name+`"` || strings.Count(quoted, `"`) != 2 {
				t.Fatalf("QuoteIdentifier(%q) = %q: the identifier broke out of its quotes", name, quoted)
			}

		case identifierCharset(name) && name != "" && len(name) <= 64 && !startsWithDigit(name):
			// The charset, the length and the leading character are all fine,
			// so the reserved-keyword list is the only rule left that may
			// reject it. Any other reason is a rule nobody wrote down.
			if !strings.Contains(err.Error(), "reserved SQL keyword") {
				t.Fatalf("ValidateIdentifier(%q) rejected a charset-safe identifier for an undocumented reason: %v", name, err)
			}
		}

		// The qualified form must widen the NAME and nothing else.
		qErr := g.ValidateQualifiedIdentifier(name)
		if qErr != nil && !errors.Is(qErr, guard.ErrInvalidIdentifier) {
			t.Fatalf("ValidateQualifiedIdentifier(%q) = %v, which is not errors.Is(ErrInvalidIdentifier)", name, qErr)
		}
		if qErr != nil {
			return
		}
		if strings.Count(name, ".") > 1 {
			t.Fatalf("ValidateQualifiedIdentifier accepted %q with more than one qualifier", name)
		}
		for _, seg := range strings.Split(name, ".") {
			if segErr := g.ValidateIdentifier(seg); segErr != nil {
				t.Fatalf("ValidateQualifiedIdentifier accepted %q whose segment %q is not a valid identifier: %v", name, seg, segErr)
			}
		}
		rendered, rErr := g.QuoteQualifiedIdentifier(fakeQuoter{}, name)
		if rErr != nil {
			t.Fatalf("QuoteQualifiedIdentifier(%q) rejected what ValidateQualifiedIdentifier accepted: %v", name, rErr)
		}
		if !qualifiedRender.MatchString(rendered) {
			t.Fatalf("QuoteQualifiedIdentifier(%q) = %q: something other than the qualifying dot escaped the quotes", name, rendered)
		}
	})
}
