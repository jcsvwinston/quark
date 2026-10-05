// Copyright 2026 jcsvwinston
// SPDX-License-Identifier: Apache-2.0

// The kit's dialect half is newer than the quark this module pins; the
// standalone lane builds the module against that floor and leaves this file
// out (-tags pinnedquark) until the release train raises it.

//go:build !pinnedquark

package oracle

import (
	"database/sql"
	"os"
	"testing"

	"github.com/jcsvwinston/quark"
	"github.com/jcsvwinston/quark/quarkdriver/drivertest"
)

// TestDialectConformance runs the dialect kit against Quark's Oracle dialect
// through this module's driver. The engine half needs a server, named by
// QUARK_TEST_ORACLE_DSN (CI's oracle lane exports it, and the user needs
// EXECUTE on DBMS_LOCK for the migration lock — ADR-0018); without it the
// contract half runs and the engine half is skipped, saying why.
func TestDialectConformance(t *testing.T) {
	c := drivertest.DialectCase{Dialect: quark.Oracle(), DriverName: "oracle"}
	if dsn := os.Getenv("QUARK_TEST_ORACLE_DSN"); dsn != "" {
		db, err := sql.Open("oracle", dsn)
		if err != nil {
			t.Fatal(err)
		}
		defer db.Close()
		c.DB = db
	}
	drivertest.VerifyDialect(t, c)
}
