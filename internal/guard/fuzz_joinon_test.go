// Copyright 2026 jcsvwinston
// SPDX-License-Identifier: Apache-2.0

package guard_test

import (
	"regexp"
	"strings"
	"testing"

	"github.com/jcsvwinston/quark/internal/guard"
)

// joinOperand is the only shape an ON-clause operand may take: an identifier,
// optionally qualified by one table name.
var joinOperand = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*(\.[A-Za-z_][A-Za-z0-9_]*)?$`)

// joinOperators is the closed set of comparisons the grammar admits.
var joinOperators = map[string]bool{
	"=": true, "!=": true, "<>": true, "<": true, "<=": true, ">": true, ">=": true,
}

type joinToken struct {
	text string
	op   bool
}

// tokenizeJoinOn re-reads an ON clause with a hand-written scanner instead of
// the guard's regexp, so the two have to agree independently. It returns false
// as soon as it meets a byte the clause is not allowed to contain — which is
// the point: the expression is pasted into the SQL verbatim, so its byte set
// IS its security boundary.
func tokenizeJoinOn(expr string) ([]joinToken, bool) {
	var toks []joinToken
	for i := 0; i < len(expr); {
		c := expr[i]
		switch {
		case c == ' ' || c == '\t' || c == '\n' || c == '\f' || c == '\r':
			i++
		case c >= 'a' && c <= 'z', c >= 'A' && c <= 'Z', c >= '0' && c <= '9', c == '_', c == '.':
			j := i
			for j < len(expr) {
				d := expr[j]
				if (d >= 'a' && d <= 'z') || (d >= 'A' && d <= 'Z') || (d >= '0' && d <= '9') || d == '_' || d == '.' {
					j++
					continue
				}
				break
			}
			toks = append(toks, joinToken{text: expr[i:j]})
			i = j
		case c == '=' || c == '<' || c == '>' || c == '!':
			j := i
			for j < len(expr) && (expr[j] == '=' || expr[j] == '<' || expr[j] == '>' || expr[j] == '!') {
				j++
			}
			toks = append(toks, joinToken{text: expr[i:j], op: true})
			i = j
		default:
			return nil, false
		}
	}
	return toks, true
}

// FuzzValidateJoinOn asserts that an accepted ON clause can be re-parsed, by a
// scanner that shares no code with the guard, into nothing but identifier-to-
// identifier comparisons joined by AND/OR. ValidateJoinOn is the one guard
// whose input is concatenated into the statement as written — there is no
// quoting step downstream to catch a leak — so "what did it accept" is the
// whole security property.
func FuzzValidateJoinOn(f *testing.F) {
	for _, seed := range []string{
		// Accepted (TestValidateJoinOn_Valid).
		"users.id = orders.user_id", "a = b", "users.id=orders.user_id",
		"users.id != orders.user_id", "users.id <> orders.user_id",
		"users.id <= orders.user_id", "users.id >= orders.user_id",
		"users.id <  orders.user_id",
		"users.id = orders.user_id AND users.tenant_id = orders.tenant_id",
		"users.id = orders.user_id and users.tenant_id = orders.tenant_id",
		"a.x = b.y OR c.z = d.w", "a = b AND c = d AND e = f",
		// Rejected (TestValidateJoinOn_Invalid), payloads included.
		"", "users.id = orders.user_id; DROP TABLE orders",
		"users.id = orders.user_id -- comment",
		"users.id = orders.user_id /* x */",
		"users.id = 1", "users.id = 'alice'", "users.id = LOWER(orders.user_id)",
		"users.id = orders.user_id UNION SELECT 1",
		"(users.id = orders.user_id)", "users.id = orders.user_id OR 1=1",
		"users.id =", "= orders.user_id", "users.id orders.user_id",
		"users.id $$$ orders.user_id", "users-id = orders.user_id",
		"$.user.id = orders.user_id", "a.b.c = d.e", "a..b = c.d",
		"a = b; DROP", strings.Repeat("a", 513) + " = b",
	} {
		f.Add(seed)
	}

	f.Fuzz(func(t *testing.T, expr string) {
		err := guard.ValidateJoinOn(expr)
		if err != nil {
			if !strings.HasPrefix(err.Error(), "ErrInvalidJoin:") {
				t.Fatalf("ValidateJoinOn(%q) error %q does not carry the prefix package quark wraps on", expr, err)
			}
			return
		}

		if expr == "" || len(expr) > 512 {
			t.Fatalf("ValidateJoinOn accepted a clause of %d bytes", len(expr))
		}

		toks, ok := tokenizeJoinOn(expr)
		if !ok {
			t.Fatalf("ValidateJoinOn accepted %q, which carries a byte outside [A-Za-z0-9_. =<>!] and whitespace", expr)
		}

		// condition ( (AND|OR) condition )*, and nothing else. Three tokens
		// per condition plus one joiner between each pair.
		if len(toks)%4 != 3 {
			t.Fatalf("ValidateJoinOn accepted %q, which does not re-parse as identifier comparisons joined by AND/OR (%d tokens)", expr, len(toks))
		}
		for i := 0; i < len(toks); i += 4 {
			lhs, op, rhs := toks[i], toks[i+1], toks[i+2]
			if lhs.op || rhs.op || !op.op {
				t.Fatalf("ValidateJoinOn accepted %q: condition %d is not <operand> <op> <operand>", expr, i/4)
			}
			if !joinOperand.MatchString(lhs.text) || !joinOperand.MatchString(rhs.text) {
				t.Fatalf("ValidateJoinOn accepted %q: %q or %q is not an identifier, so a literal reached the ON clause", expr, lhs.text, rhs.text)
			}
			if !joinOperators[op.text] {
				t.Fatalf("ValidateJoinOn accepted %q with the operator %q, which is outside the whitelist", expr, op.text)
			}
			if i+3 < len(toks) {
				j := toks[i+3]
				if j.op || (!strings.EqualFold(j.text, "AND") && !strings.EqualFold(j.text, "OR")) {
					t.Fatalf("ValidateJoinOn accepted %q, which joins conditions with %q instead of AND/OR", expr, j.text)
				}
			}
		}

		// The grammar is anchored: nothing may ride along behind a clause that
		// parsed cleanly. This is the shape every reported payload took.
		for _, tail := range []string{"; DROP TABLE orders", " -- x", " UNION SELECT 1", " OR 1=1"} {
			if guard.ValidateJoinOn(expr+tail) == nil {
				t.Fatalf("ValidateJoinOn accepted %q with %q appended", expr, tail)
			}
		}
	})
}
