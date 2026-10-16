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

// DEP-2026-010 (QK-50) pins the v1 rendering of a sized string on Oracle:
// VARCHAR2(n) with no length semantics, which Oracle counts in bytes under
// its default NLS_LENGTH_SEMANTICS while the other five engines count
// characters. At v2.0.0 the plan renders VARCHAR2(n CHAR); the 2.0 PR
// changes this test in the same commit as engineBase and applySize.
func TestOracleSizedStringsCountBytesUntilV2(t *testing.T) {
	str := reflect.TypeOf("")
	cases := []struct {
		name, got, want string
	}{
		{"string without size", migrate.SQLType("oracle", str, false), "VARCHAR2(255)"},
		{"string primary key", migrate.SQLType("oracle", str, true), "VARCHAR2(36) PRIMARY KEY"},
		{"string with size", migrate.SQLTypeWithOpts("oracle", str, migrate.TypeOptions{Size: 40}), "VARCHAR2(40)"},
		{"ColumnSpec with size", migrate.EngineColumnType("oracle", quarkdriver.ColumnSpec{Kind: quarkdriver.KindString, Size: 45}), "VARCHAR2(45)"},
		{"ColumnSpec without size", migrate.EngineColumnType("oracle", quarkdriver.ColumnSpec{Kind: quarkdriver.KindString}), "VARCHAR2(255)"},
	}
	for _, tc := range cases {
		if tc.got != tc.want {
			t.Errorf("%s: %q, want %q (v1 renders no length semantics; VARCHAR2(n CHAR) comes with v2.0.0)", tc.name, tc.got, tc.want)
		}
		if strings.Contains(tc.got, " CHAR") {
			t.Errorf("%s: %q already carries CHAR semantics; that is the v2.0.0 change (DEP-2026-010)", tc.name, tc.got)
		}
	}
}
