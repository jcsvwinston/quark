// Copyright 2026 jcsvwinston
// SPDX-License-Identifier: Apache-2.0

package guard_test

import (
	"strings"
	"testing"

	"github.com/jcsvwinston/quark/internal/guard"
)

// jsonTableCharset is the byte set the JSON_TABLE grammar may use. Anything
// outside it — a quote, a semicolon, whitespace, a comment marker — would end
// up inside the path literal MariaDB receives.
func jsonTableCharset(s string) bool {
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case c >= 'a' && c <= 'z', c >= 'A' && c <= 'Z', c >= '0' && c <= '9':
		case c == '_', c == '.', c == '$', c == '[', c == ']', c == '*':
		default:
			return false
		}
	}
	return true
}

// FuzzValidateJSONPath covers both JSON path grammars the dialect layer holds.
//
// The invariant that matters is P0-2's: on Oracle the validated path is not
// bound but inlined into a string literal, because JSON_VALUE raises ORA-40454
// on a bound path. So an accepted path has to survive being pasted between two
// single quotes — the target renders exactly that literal and asserts it still
// has two quotes and nothing that could close it early. On the other dialects
// the path is bound, and the same restriction is what lets the MySQL/SQLite/
// MSSQL forms concatenate it into "$.<path>" before binding.
func FuzzValidateJSONPath(f *testing.F) {
	for _, seed := range []string{
		// Accepted (TestValidateJSONPath_Valid).
		"name", "user_id", "user.name", "user.profile.email", "a.b.c.d",
		"_private.field", "x1.y2.z3", strings.Repeat("a", 256),
		// Rejected (TestValidateJSONPath_Invalid), payloads included.
		"", ".x", "x.", "x..y", "1user", "$.user", "user-name", "user name",
		"x'; DROP TABLE users--", "x; SELECT 1", "x/*y*/z", "x\"y", "x'y",
		"x\\y", "x\ny", "x\ty", strings.Repeat("a", 257),
		// The JSON_TABLE grammar, which is rooted at $ and admits indexes.
		"$", "$[*]", "$[0]", "$.items", "$.items[0]", "$.items[*].name",
		"$.a.b.c", "$['a']", "$.items[*]'; DROP TABLE x--", "$..a", "$.",
	} {
		f.Add(seed)
	}

	g := guard.New()
	f.Fuzz(func(t *testing.T, path string) {
		err := guard.ValidateJSONPath(path)

		// The bound method is documented as the same logic; a divergence would
		// mean two call sites disagree about what a path is.
		if bound := g.ValidateJSONPath(path); (bound == nil) != (err == nil) {
			t.Fatalf("ValidateJSONPath(%q) and the bound method disagree: %v vs %v", path, err, bound)
		}
		if err != nil && !strings.HasPrefix(err.Error(), "ErrInvalidJSONPath:") {
			t.Fatalf("ValidateJSONPath(%q) error %q does not carry the prefix package quark wraps on", path, err)
		}

		if err == nil {
			if path == "" || len(path) > 256 {
				t.Fatalf("ValidateJSONPath accepted a path of %d bytes", len(path))
			}
			for _, seg := range strings.Split(path, ".") {
				if seg == "" {
					t.Fatalf("ValidateJSONPath accepted %q, which has an empty segment", path)
				}
				if !identifierCharset(seg) || startsWithDigit(seg) {
					t.Fatalf("ValidateJSONPath accepted %q, whose segment %q is not identifier-shaped", path, seg)
				}
			}
			// P0-2 / Oracle: the accepted path is inlined into a literal.
			if inlined := "'$." + path + "'"; strings.Count(inlined, "'") != 2 {
				t.Fatalf("ValidateJSONPath accepted %q: inlined as %q it closes its own literal", path, inlined)
			}
			// The other dialects concatenate it into a bound "$.<path>" arg.
			if strings.ContainsAny(path, "\"';$[]*(){}") {
				t.Fatalf("ValidateJSONPath accepted %q, which carries a JSONPath or SQL metacharacter", path)
			}
		}

		// The JSON_TABLE root path is a different, wider grammar. It must stay
		// disjoint from the one above: a "$"-rooted path is exactly what
		// WhereJSON rejects, and the widening must not leak back.
		if tErr := guard.ValidateJSONTablePath(path); tErr == nil {
			if !strings.HasPrefix(path, "$") {
				t.Fatalf("ValidateJSONTablePath accepted %q, which is not rooted at $", path)
			}
			if len(path) > 256 {
				t.Fatalf("ValidateJSONTablePath accepted a path of %d bytes", len(path))
			}
			if !jsonTableCharset(path) {
				t.Fatalf("ValidateJSONTablePath accepted %q, which carries a byte outside the JSONPath charset", path)
			}
			if strings.Count("'"+path+"'", "'") != 2 {
				t.Fatalf("ValidateJSONTablePath accepted %q: it closes its own literal", path)
			}
			if err == nil {
				t.Fatalf("%q passes BOTH JSON path grammars; WhereJSON must keep rejecting $-rooted paths", path)
			}
		} else if !strings.HasPrefix(tErr.Error(), "ErrInvalidJSONPath:") {
			t.Fatalf("ValidateJSONTablePath(%q) error %q does not carry the prefix package quark wraps on", path, tErr)
		}
	})
}
