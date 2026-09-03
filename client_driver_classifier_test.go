// Copyright 2026 jcsvwinston
// SPDX-License-Identifier: Apache-2.0

package quark

import (
	"testing"

	"github.com/jcsvwinston/quark/quarkdriver"
)

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
	// Every engine the mapping names must be one the driver modules
	// actually register — the test binary registers them all.
	for _, dialect := range []string{"mysql", "mariadb", "sqlite", "mssql", "oracle"} {
		engine, _ := classifierEngineFor(dialect)
		if !quarkdriver.HasEngine(engine) {
			t.Errorf("classifierEngineFor(%q) names engine %q, which no driver module registers", dialect, engine)
		}
	}
}
