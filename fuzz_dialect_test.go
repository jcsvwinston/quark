// Copyright 2026 jcsvwinston
// SPDX-License-Identifier: Apache-2.0

package quark_test

import (
	"fmt"
	"strings"
	"testing"

	"github.com/jcsvwinston/quark"
	"github.com/jcsvwinston/quark/internal/guard"
)

// quotingRule describes how one dialect delimits an identifier, so the fuzz
// target can UNQUOTE what Quote produced and compare. Every dialect Quark ships
// doubles its closing delimiter (H-Q7) — the open one needs no escaping because
// only the closing delimiter can end the identifier.
type quotingRule struct {
	dialect quark.Dialect
	open    string
	close   string
	// upper reports whether the dialect folds the identifier, as Oracle does.
	upper bool
}

func quotingRules() []quotingRule {
	return []quotingRule{
		{quark.PostgreSQL(), `"`, `"`, false},
		{quark.MySQL(), "`", "`", false},
		{quark.MariaDB(), "`", "`", false},
		{quark.SQLite(), `"`, `"`, false},
		{quark.MSSQL(), "[", "]", false},
		{quark.Oracle(), `"`, `"`, true},
	}
}

// unquote is the inverse of Quote: strip the delimiters, then undouble the
// closing one. If Quote ever emits a lone closing delimiter inside the
// identifier, this reconstruction stops matching the input — which is the same
// thing as saying the identifier escaped its quotes.
func unquote(s string, r quotingRule) (string, bool) {
	if len(s) < len(r.open)+len(r.close) {
		return "", false
	}
	if !strings.HasPrefix(s, r.open) || !strings.HasSuffix(s, r.close) {
		return "", false
	}
	inner := s[len(r.open) : len(s)-len(r.close)]
	return strings.ReplaceAll(inner, r.close+r.close, r.close), true
}

// FuzzDialectEscaping drives the two places where the dialect layer puts a
// caller-supplied string into SQL text rather than into a bind parameter:
// Quote (identifiers) and JSONExtract (the JSON path, which Oracle cannot
// bind).
//
// Quote is the defense-in-depth layer under the guard: the main paths validate
// first, but Quote itself must not depend on that. So the target feeds it
// arbitrary bytes and asserts the escaping is REVERSIBLE — an identifier that
// round-trips cannot have terminated its own quoting, and one that does not
// round-trip has.
//
// Seeds are the dialect_quote_test.go table plus the JSON paths from the P0-2
// regression.
func FuzzDialectEscaping(f *testing.F) {
	seeds := []struct{ column, path string }{
		{"users", "user.name"},
		{"data", "name"},
		{`a"b`, "user.profile.email"},
		{"a`b", "a.b.c.d"},
		{"a]b", "_private.field"},
		{`a"; DROP TABLE x; --`, "x'; DROP TABLE users--"},
		{"a]]b", "$.user"},
		{`"`, ""},
		{"]", "x..y"},
		{"``", ".x"},
		{"col'umn", "x."},
		{"", "1user"},
		{"a\x00b", "user-name"},
		{strings.Repeat(`"`, 9), strings.Repeat("a", 257)},
	}
	for _, s := range seeds {
		f.Add(s.column, s.path)
	}

	f.Fuzz(func(t *testing.T, column, path string) {
		// Bound the inputs: the escaping is per-byte, so a mutator-grown
		// kilobyte costs throughput without reaching anything new.
		if len(column) > 512 {
			column = column[:512]
		}
		if len(path) > 512 {
			path = path[:512]
		}

		for _, r := range quotingRules() {
			name := r.dialect.Name()
			quoted := r.dialect.Quote(column)

			want := column
			if r.upper {
				want = strings.ToUpper(column)
			}

			got, ok := unquote(quoted, r)
			if !ok {
				t.Fatalf("%s.Quote(%q) = %q, which is not delimited by %s...%s", name, column, quoted, r.open, r.close)
			}
			if got != want {
				t.Fatalf("%s.Quote(%q) = %q, which unquotes to %q: the identifier escaped its quoting", name, column, quoted, got)
			}
			// Every closing delimiter inside the identifier must be doubled,
			// so an odd run of them can never terminate it early.
			inner := quoted[len(r.open) : len(quoted)-len(r.close)]
			if strings.Count(inner, r.close)%2 != 0 {
				t.Fatalf("%s.Quote(%q) = %q: an unpaired %s survives inside the identifier", name, column, quoted, r.close)
			}

			// JSONExtract: the path is validated first, on every dialect.
			sql, args, err := r.dialect.JSONExtract(column, path)
			pathOK := guard.ValidateJSONPath(path) == nil
			if (err == nil) != pathOK {
				t.Fatalf("%s.JSONExtract(%q, %q) err=%v but guard.ValidateJSONPath says valid=%v", name, column, path, err, pathOK)
			}
			if err != nil {
				if sql != "" || args != nil {
					t.Fatalf("%s.JSONExtract(%q, %q) returned SQL %q / args %v alongside an error", name, column, path, sql, args)
				}
				continue
			}
			if !strings.Contains(sql, quoted) {
				t.Fatalf("%s.JSONExtract(%q, %q) = %q, which does not carry the quoted column", name, column, path, sql)
			}

			// Isolate the path's contribution: whatever is left once the
			// quoted column is removed is what the path put into the SQL.
			rest := strings.Replace(sql, quoted, "", 1)
			switch name {
			case "oracle":
				// ORA-40454 forbids binding the path, so it is inlined into a
				// literal. That literal must be exactly the validated path.
				if args != nil {
					t.Fatalf("oracle.JSONExtract(%q, %q) returned args %v; the path is inlined, not bound", column, path, args)
				}
				if want := fmt.Sprintf("JSON_VALUE(, '$.%s')", path); rest != want {
					t.Fatalf("oracle.JSONExtract(%q, %q) put %q around the column, want %q", column, path, rest, want)
				}
				if strings.Count(rest, "'") != 2 {
					t.Fatalf("oracle.JSONExtract(%q, %q) = %q: the inlined path does not sit in exactly one literal", column, path, sql)
				}
			default:
				// Everywhere else the path is BOUND. Nothing derived from it
				// may appear in the statement text at all.
				if strings.ContainsAny(rest, "'\";") || strings.Contains(rest, "--") {
					t.Fatalf("%s.JSONExtract(%q, %q) = %q: the SQL around the column carries a literal or statement delimiter", name, column, path, sql)
				}
				if len(args) == 0 {
					t.Fatalf("%s.JSONExtract(%q, %q) bound no argument, so the path went into the SQL", name, column, path)
				}
				var carried []string
				for _, a := range args {
					s, isString := a.(string)
					if !isString {
						t.Fatalf("%s.JSONExtract(%q, %q) bound a non-string arg %T", name, column, path, a)
					}
					carried = append(carried, s)
				}
				joined := strings.Join(carried, ".")
				if joined != path && joined != "$."+path {
					t.Fatalf("%s.JSONExtract(%q, %q) bound %v, which does not reconstruct the path", name, column, path, args)
				}
			}
		}
	})
}
