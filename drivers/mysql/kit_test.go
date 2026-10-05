// Copyright 2026 jcsvwinston
// SPDX-License-Identifier: Apache-2.0

// The kit's dialect half is newer than the quark this module pins; the
// standalone lane builds the module against that floor and leaves this file
// out (-tags pinnedquark) until the release train raises it.

//go:build !pinnedquark

package mysql

import (
	"database/sql"
	"os"
	"testing"

	"github.com/jcsvwinston/quark"
	"github.com/jcsvwinston/quark/quarkdriver/drivertest"
)

// TestDialectConformance runs the dialect kit against the two dialects this
// module's driver serves: MySQL's and MariaDB's, both opened as "mysql" —
// Quark tells them apart by the server's version, which the kit's
// Registration check holds it to. The engine halves need a server each:
// QUARK_TEST_MYSQL_DSN and QUARK_TEST_MARIADB_DSN (CI's driver lane sets
// both); without one, that dialect's contract half runs and its engine half
// is skipped, saying why.
func TestDialectConformance(t *testing.T) {
	for _, c := range []struct {
		name    string
		dialect quark.Dialect
		env     string
	}{
		{"MySQL", quark.MySQL(), "QUARK_TEST_MYSQL_DSN"},
		{"MariaDB", quark.MariaDB(), "QUARK_TEST_MARIADB_DSN"},
	} {
		t.Run(c.name, func(t *testing.T) {
			kc := drivertest.DialectCase{Dialect: c.dialect, DriverName: "mysql"}
			if dsn := os.Getenv(c.env); dsn != "" {
				db, err := sql.Open("mysql", dsn)
				if err != nil {
					t.Fatal(err)
				}
				defer db.Close()
				kc.DB = db
			}
			drivertest.VerifyDialect(t, kc)
		})
	}
}
