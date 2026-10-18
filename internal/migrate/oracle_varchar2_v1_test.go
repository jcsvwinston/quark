// Copyright 2026 jcsvwinston
// SPDX-License-Identifier: Apache-2.0

package migrate_test

import (
	"reflect"
	"strings"
	"testing"

	"github.com/jcsvwinston/quark/internal/migrate"
	"github.com/jcsvwinston/quark/quarkdriver"
)

// DEP-2026-010 (QK-50): a sized string on Oracle is rendered with explicit
// character semantics, VARCHAR2(n CHAR), so that a length means characters
// on the six engines. A bare VARCHAR2(n) counts bytes under Oracle's default
// NLS_LENGTH_SEMANTICS, and a value of n non-ASCII characters that fits on
// the other five was ORA-12899 there. The string primary key and the text
// forms of a uuid and an inet carry the qualifier too.
//
// The file keeps the path the notice cites under Validation, which the
// internal-docs drift guard checks; it is renamed when the notice is closed.
func TestOracleSizedStringsCountCharacters(t *testing.T) {
	str := reflect.TypeOf("")
	cases := []struct {
		name, got, want string
	}{
		{"string without size", migrate.SQLType("oracle", str, false), "VARCHAR2(255 CHAR)"},
		{"string primary key", migrate.SQLType("oracle", str, true), "VARCHAR2(36 CHAR) PRIMARY KEY"},
		{"string with size", migrate.SQLTypeWithOpts("oracle", str, migrate.TypeOptions{Size: 40}), "VARCHAR2(40 CHAR)"},
		{"ColumnSpec with size", migrate.EngineColumnType("oracle", quarkdriver.ColumnSpec{Kind: quarkdriver.KindString, Size: 45}), "VARCHAR2(45 CHAR)"},
		{"ColumnSpec without size", migrate.EngineColumnType("oracle", quarkdriver.ColumnSpec{Kind: quarkdriver.KindString}), "VARCHAR2(255 CHAR)"},
		{"uuid as text", migrate.EngineColumnType("oracle", quarkdriver.ColumnSpec{Kind: quarkdriver.KindUUID}), "VARCHAR2(36 CHAR)"},
		{"inet as text", migrate.EngineColumnType("oracle", quarkdriver.ColumnSpec{Kind: quarkdriver.KindIP}), "VARCHAR2(45 CHAR)"},
		// Long text has no length and no semantics to declare.
		{"text", migrate.EngineColumnType("oracle", quarkdriver.ColumnSpec{Kind: quarkdriver.KindText}), "CLOB"},
	}
	for _, tc := range cases {
		if tc.got != tc.want {
			t.Errorf("%s: %q, want %q", tc.name, tc.got, tc.want)
		}
		if strings.HasPrefix(tc.got, "VARCHAR2(") && !strings.Contains(tc.got, " CHAR)") {
			t.Errorf("%s: %q declares no length semantics, and Oracle would count bytes (DEP-2026-010)", tc.name, tc.got)
		}
	}
}
