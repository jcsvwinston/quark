// Copyright 2026 jcsvwinston
// SPDX-License-Identifier: Apache-2.0

package db

import (
	"database/sql"
	"slices"
	"testing"
)

func TestDriverName(t *testing.T) {
	tests := []struct {
		in, want string
	}{
		// The names `quark init` writes must all resolve to registered drivers (H-Q1).
		{"postgresql", "pgx"},
		{"postgres", "pgx"},
		{"mariadb", "mysql"},
		{"mysql", "mysql"},
		{"sqlite", "sqlite"},
		{"mssql", "mssql"},
		{"sqlserver", "sqlserver"},
		{"oracle", "oracle"},
		// Unknown names pass through so custom drivers keep working.
		{"cockroach", "cockroach"},
	}
	for _, tt := range tests {
		if got := DriverName(tt.in); got != tt.want {
			t.Errorf("DriverName(%q) = %q, want %q", tt.in, got, tt.want)
		}
	}
}

// Every driver name DriverName can return for a dialect that `quark init`
// offers must actually be registered in this binary — otherwise sql.Open
// fails at runtime with `unknown driver` (the original H-Q1 failure).
func TestInitDialectsResolveToRegisteredDrivers(t *testing.T) {
	registered := sql.Drivers()
	for _, dialect := range []string{"postgresql", "mysql", "sqlite", "mssql", "oracle"} {
		driver := DriverName(dialect)
		if !slices.Contains(registered, driver) {
			t.Errorf("dialect %q maps to driver %q, which is not registered (have %v)", dialect, driver, registered)
		}
	}
}
