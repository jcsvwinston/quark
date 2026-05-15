// Copyright 2026 jcsvwinston
// SPDX-License-Identifier: Apache-2.0

package quarktenant

import (
	"errors"
	"strings"
	"testing"
)

// TestNormaliseCast_AcceptsKnownTypes confirms the whitelist allows
// the realistic PostgreSQL type tokens — both bare and with leading
// "::", with and without size parameters. The output always starts
// with "::" so the caller can splice it into the SQL unchanged.
func TestNormaliseCast_AcceptsKnownTypes(t *testing.T) {
	t.Parallel()

	cases := []struct {
		in   string
		want string
	}{
		{"text", "::text"},
		{"::text", "::text"},
		{"uuid", "::uuid"},
		{"::uuid", "::uuid"},
		{"bigint", "::bigint"},
		{"int", "::int"},
		{"int8", "::int8"},
		{"varchar(64)", "::varchar(64)"},
		{"numeric(10,2)", "::numeric(10,2)"},
	}
	for _, c := range cases {
		got, err := normaliseCast(c.in, nil)
		if err != nil {
			t.Errorf("normaliseCast(%q) errored: %v", c.in, err)
			continue
		}
		if got != c.want {
			t.Errorf("normaliseCast(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

// TestNormaliseCast_RejectsInjectionShapes is the F5-3 SQL-injection
// guard. Any input that could splice arbitrary tokens into the policy
// USING/WITH CHECK clauses must be rejected with [ErrInvalidCast]
// before the value reaches the rendered SQL (CLAUDE.md Regla 6).
func TestNormaliseCast_RejectsInjectionShapes(t *testing.T) {
	t.Parallel()

	// Empty string is NOT in this list — it intentionally falls
	// through to the "::text" default (covered by
	// TestNormaliseCast_EmptyDefaultsToText). Every other shape
	// below either chains statements, leaks comments, escapes the
	// expression, or violates the identifier shape.
	cases := []string{
		"text; DROP TABLE foo",
		"text--",
		"text /* comment */",
		"text)) OR 1=1 --",
		"foo bar",
		" text",
		"text ",
		"text\n",
		`text" OR "1"="1`,
		"::",        // bare "::" without a type token.
		"123",       // pure digit token cannot start an identifier.
		"text;text", // chained casts are not allowed.
	}

	for _, in := range cases {
		got, err := normaliseCast(in, nil)
		if err == nil {
			t.Errorf("normaliseCast(%q) should reject; got %q", in, got)
			continue
		}
		if !errors.Is(err, ErrInvalidCast) && !strings.Contains(err.Error(), "invalid SQL cast") {
			t.Errorf("normaliseCast(%q) errored with unexpected message: %v", in, err)
		}
	}
}

// TestNormaliseCast_EmptyDefaultsToText documents the fallback
// behaviour: when no explicit cast is supplied, we emit ::text.
// current_setting() always returns TEXT, so this is the safe default
// for TEXT/VARCHAR tenant columns. UUID/BIGINT users must opt in
// explicitly via TenantColumnSQLCast / --cast.
func TestNormaliseCast_EmptyDefaultsToText(t *testing.T) {
	t.Parallel()

	got, err := normaliseCast("", nil)
	if err != nil {
		t.Fatalf("empty cast should not error: %v", err)
	}
	if got != "::text" {
		t.Errorf("empty cast = %q, want %q", got, "::text")
	}
}
