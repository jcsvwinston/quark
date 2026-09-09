// Copyright 2026 jcsvwinston
// SPDX-License-Identifier: Apache-2.0

package quark

import "testing"

// The WARN in newClient looks a classifier up by the engine name the driver
// module registers under, which is not always the dialect name: SQL Server's
// dialect is "mssql" and its module registers "sqlserver"; MariaDB shares
// MySQL's driver and therefore MySQL's classifier. PostgreSQL and custom
// dialects need none.
func TestClassifierEngineForMapsDialectsToRegisteredEngines(t *testing.T) {
	cases := map[string]struct {
		engine string
		needs  bool
	}{
		"mysql":    {"mysql", true},
		"mariadb":  {"mysql", true},
		"sqlite":   {"sqlite", true},
		"mssql":    {"sqlserver", true},
		"oracle":   {"oracle", true},
		"postgres": {"", false},
		"custom":   {"", false},
	}
	for dialect, want := range cases {
		engine, needs := classifierEngineFor(dialect)
		if engine != want.engine || needs != want.needs {
			t.Errorf("classifierEngineFor(%q) = (%q, %v), want (%q, %v)", dialect, engine, needs, want.engine, want.needs)
		}
	}
	// That every engine named here is one a driver module actually registers
	// is asserted from the other side, by the engine-suite module: it links
	// the five driver modules, which the library's own test binary cannot —
	// they import the library (ADR-0023, ADR-0024).
}
