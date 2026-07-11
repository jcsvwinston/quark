// Copyright 2026 jcsvwinston
// SPDX-License-Identifier: Apache-2.0

package quark_test

import (
	"testing"

	"github.com/jcsvwinston/quark"
)

// Quote must be self-safe (H-Q7): an embedded closing-quote character is
// doubled, never emitted raw, so a hostile identifier cannot break out of
// the quoting even if a call-site skips ValidateIdentifier. The main paths
// do validate first — this pins the defense-in-depth layer underneath them.
func TestQuoteEscapesEmbeddedQuotes(t *testing.T) {
	tests := []struct {
		dialect quark.Dialect
		in      string
		want    string
	}{
		// Plain identifiers stay untouched.
		{quark.PostgreSQL(), "users", `"users"`},
		{quark.MySQL(), "users", "`users`"},
		{quark.MariaDB(), "users", "`users`"},
		{quark.SQLite(), "users", `"users"`},
		{quark.MSSQL(), "users", "[users]"},
		{quark.Oracle(), "users", `"USERS"`},

		// Embedded closing quotes are doubled, not emitted raw.
		{quark.PostgreSQL(), `a"b`, `"a""b"`},
		{quark.PostgreSQL(), `a"; DROP TABLE x; --`, `"a""; DROP TABLE x; --"`},
		{quark.MySQL(), "a`b", "`a``b`"},
		{quark.MariaDB(), "a`b", "`a``b`"},
		{quark.SQLite(), `a"b`, `"a""b"`},
		{quark.MSSQL(), "a]b", "[a]]b]"},
		{quark.Oracle(), `a"b`, `"A""B"`},
	}

	for _, tt := range tests {
		if got := tt.dialect.Quote(tt.in); got != tt.want {
			t.Errorf("%s.Quote(%q) = %s, want %s", tt.dialect.Name(), tt.in, got, tt.want)
		}
	}
}
