// Copyright 2026 jcsvwinston
// SPDX-License-Identifier: Apache-2.0

package quark

import (
	"testing"
)

// TestSchema_DialectInterfaceConformance pins which dialects implement
// SchemaIntrospector. All six — SQLite, PG, MySQL, MariaDB, MSSQL, and
// Oracle (F3-2-oracle) — opt in.
//
// The test is the lever that locks the expectation: if a future PR
// accidentally regresses a dialect's introspector (e.g. removes the
// method during a refactor), this test fires with "<dialect> must
// implement SchemaIntrospector", reminding the author.
func TestSchema_DialectInterfaceConformance(t *testing.T) {
	cases := []struct {
		dialect any
		want    bool
		reason  string
	}{
		{&SQLiteDialect{}, true, "SQLite must implement SchemaIntrospector (F3-2 core)"},
		{&PostgresDialect{}, true, "Postgres must implement SchemaIntrospector (F3-2 core)"},
		{&MySQLDialect{}, true, "MySQL must implement SchemaIntrospector (F3-2-mysql)"},
		{&MariaDBDialect{}, true, "MariaDB must implement SchemaIntrospector (F3-2-mysql — shares impl with MySQL)"},
		{&MSSQLDialect{}, true, "MSSQL must implement SchemaIntrospector (F3-2-mssql)"},
		{&OracleDialect{}, true, "Oracle must implement SchemaIntrospector (F3-2-oracle)"},
	}
	for _, tc := range cases {
		_, ok := tc.dialect.(SchemaIntrospector)
		if ok != tc.want {
			if tc.want {
				t.Errorf("%T must implement SchemaIntrospector — %s", tc.dialect, tc.reason)
			} else {
				t.Errorf("%T must NOT YET implement SchemaIntrospector — %s. If you just added the impl, flip the expectation here.", tc.dialect, tc.reason)
			}
		}
	}
}

// TestSchema_StringDefaultRoundTrip pins the column-default
// representation contract: a Column with no default has Default==nil;
// a Column with an empty-string default has Default==&"". The
// distinction matters for the diff layer (F3-3) — "no default" vs
// "default is the empty string" produce different DDL.
func TestSchema_StringDefaultRoundTrip(t *testing.T) {
	noDefault := Column{Name: "x", Type: "TEXT", Nullable: true}
	if noDefault.Default != nil {
		t.Errorf("zero-value Column.Default should be nil, got %v", *noDefault.Default)
	}

	emptyDefault := Column{Name: "x", Type: "TEXT", Nullable: true, Default: ptrString("")}
	if emptyDefault.Default == nil {
		t.Errorf("explicit empty-string Default should not be nil")
	}
	if *emptyDefault.Default != "" {
		t.Errorf("explicit empty-string Default has value %q", *emptyDefault.Default)
	}
}

// ptrString is the convenience used by table-driven tests below; keeps
// the call sites readable without sprinkling `&s := …` patterns
// everywhere.
func ptrString(s string) *string { return &s }
