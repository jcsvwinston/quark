// Copyright 2026 jcsvwinston
// SPDX-License-Identifier: Apache-2.0

package quarkdriver

import (
	"strings"
	"testing"
)

// The hint's whole value is that it can be followed literally, so every piece
// a person would copy has to be there.
func TestMissingDriverHintCarriesTheWholeRecipe(t *testing.T) {
	for _, name := range []string{"pgx", "postgres", "postgresql", "mariadb", "mssql", "sqlite3", "SQLite"} {
		hint := MissingDriverHint(name)
		if hint == "" {
			t.Errorf("%q produced no hint; a name Quark's dialect layer accepts must resolve", name)
			continue
		}
		for _, want := range []string{"go get github.com/jcsvwinston/quark/drivers/", "import _ \"github.com/jcsvwinston/quark/drivers/"} {
			if !strings.Contains(hint, want) {
				t.Errorf("%q: hint is missing %q:\n%s", name, want, hint)
			}
		}
	}
}

// Inventing a `go get` for a driver nobody publishes would send the reader
// somewhere that does not exist — worse than the plain error it replaces.
func TestMissingDriverHintStaysSilentForForeignDrivers(t *testing.T) {
	for _, name := range []string{"clickhouse", "mongodb", "", "sqlit"} {
		if hint := MissingDriverHint(name); hint != "" {
			t.Errorf("%q must produce no hint, got:\n%s", name, hint)
		}
	}
}

// The aliases exist because nobody types the database/sql name: `quark init`
// writes "postgresql", the docs say "postgres", and the driver is "pgx".
func TestAliasesResolveToTheSameModule(t *testing.T) {
	want := "drivers/postgres"
	for _, name := range []string{"pgx", "postgres", "postgresql", "pq", "pgx/v5"} {
		if !strings.Contains(MissingDriverHint(name), want) {
			t.Errorf("%q did not resolve to %s", name, want)
		}
	}
	if !strings.Contains(MissingDriverHint("mariadb"), "drivers/mysql") {
		t.Error("mariadb speaks the MySQL wire protocol and must resolve to the mysql module")
	}
}
