// Copyright 2026 jcsvwinston
// SPDX-License-Identifier: Apache-2.0

package quark

import "testing"

// TestNormalizeType pins the canonicalisation rules. Two type
// strings should normalize to the same form when they represent
// the same SQL type across dialects + casing variations. A failure
// here indicates either a real round-trip bug or a deliberate
// change in dialect support — both should be reviewed.
func TestNormalizeType(t *testing.T) {
	cases := []struct {
		name      string
		a, b      string
		wantEqual bool
	}{
		{"identity", "bigint", "bigint", true},
		{"case fold", "BIGINT", "bigint", true},
		{"trim whitespace", "  bigint  ", "bigint", true},
		{"PG character varying alias", "character varying(255)", "VARCHAR(255)", true},
		{"PG character alias", "character(36)", "CHAR(36)", true},
		{"MySQL strip int display width", "int(11)", "INT", true},
		{"MySQL strip bigint display width", "bigint(20)", "BIGINT", true},
		{"MySQL strip smallint display width", "smallint(6)", "smallint", true},
		{"MySQL strip tinyint display width", "tinyint(4)", "tinyint", true},
		{"MySQL strip mediumint display width", "mediumint(9)", "mediumint", true},
		{"different types stay different", "bigint", "integer", false},
		{"varchar size matters", "varchar(255)", "varchar(100)", false},
		{"point is preserved (identity)", "point(2,2)", "point(2,2)", true},
		{"point is not equal to int(2)", "point(2,2)", "int(2)", false},
		{"decimal width is preserved", "decimal(10,2)", "decimal(10,2)", true},
		{"different decimal widths stay different", "decimal(10,2)", "decimal(12,4)", false},
		{"PG numeric reassembly vs migrator NUMERIC",
			"numeric(10,2)", "NUMERIC(10,2)", true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			a := normalizeType(tc.a)
			b := normalizeType(tc.b)
			got := a == b
			if got != tc.wantEqual {
				t.Errorf("normalizeType(%q) = %q; normalizeType(%q) = %q; equal=%v, want %v",
					tc.a, a, tc.b, b, got, tc.wantEqual)
			}
		})
	}
}

// TestStripMySQLDisplayWidth is a focused test for the helper —
// the cases TestNormalizeType already covers indirectly are
// re-asserted here at the helper level so future refactors of
// normalizeType don't accidentally break the helper's contract.
func TestStripMySQLDisplayWidth(t *testing.T) {
	cases := []struct {
		in, want string
	}{
		{"int(11)", "int"},
		{"bigint(20)", "bigint"},
		{"smallint(6) unsigned", "smallint unsigned"},
		{"int", "int"},                   // already stripped
		{"point(2,2)", "point(2,2)"},     // substring `int(` shouldn't match
		{"varchar(255)", "varchar(255)"}, // non-integer family preserved
		{"decimal(10,2)", "decimal(10,2)"},
		{"int(0)", "int"}, // zero-width edge case
	}
	for _, tc := range cases {
		t.Run(tc.in, func(t *testing.T) {
			if got := stripMySQLDisplayWidth(tc.in); got != tc.want {
				t.Errorf("stripMySQLDisplayWidth(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}
