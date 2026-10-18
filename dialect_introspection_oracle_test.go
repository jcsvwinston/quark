// Copyright 2026 jcsvwinston
// SPDX-License-Identifier: Apache-2.0

package quark

import (
	"database/sql"
	"testing"
)

// TestOracleReassembleTypeCarriesLengthSemantics pins how the Oracle
// introspection spells a sized string from USER_TAB_COLUMNS (DEP-2026-010):
// CHAR_LENGTH with the CHAR_USED the catalog records, so that a byte column
// under a model that declares a length — rendered VARCHAR2(n CHAR) — is a
// type change Diff can see, and converges once it is applied.
func TestOracleReassembleTypeCarriesLengthSemantics(t *testing.T) {
	n := func(v int64) sql.NullInt64 { return sql.NullInt64{Int64: v, Valid: true} }
	s := func(v string) sql.NullString { return sql.NullString{String: v, Valid: true} }
	none := sql.NullInt64{}
	for _, tc := range []struct {
		name     string
		dataType string
		charLen  sql.NullInt64
		charUsed sql.NullString
		want     string
	}{
		{"varchar2 in characters", "VARCHAR2", n(40), s("C"), "VARCHAR2(40 CHAR)"},
		{"varchar2 in bytes", "VARCHAR2", n(40), s("B"), "VARCHAR2(40 BYTE)"},
		{"varchar2 without a recorded unit", "VARCHAR2", n(40), sql.NullString{}, "VARCHAR2(40)"},
		{"char in bytes", "CHAR", n(8), s("B"), "CHAR(8 BYTE)"},
		{"char in characters", "CHAR", n(8), s("C"), "CHAR(8 CHAR)"},
		{"nvarchar2 takes no qualifier", "NVARCHAR2", n(40), s("C"), "NVARCHAR2(40)"},
		{"nchar takes no qualifier", "NCHAR", n(4), s("C"), "NCHAR(4)"},
		{"clob has no length", "CLOB", none, sql.NullString{}, "CLOB"},
	} {
		if got := oracleReassembleType(tc.dataType, tc.charLen, tc.charUsed, none, none); got != tc.want {
			t.Errorf("%s: %q, want %q", tc.name, got, tc.want)
		}
	}
	// NUMBER is untouched by the unit.
	if got := oracleReassembleType("NUMBER", none, sql.NullString{}, n(19), n(0)); got != "NUMBER(19)" {
		t.Errorf("NUMBER(19): %q", got)
	}
	if got := oracleReassembleType("NUMBER", none, sql.NullString{}, n(10), n(2)); got != "NUMBER(10,2)" {
		t.Errorf("NUMBER(10,2): %q", got)
	}
}
