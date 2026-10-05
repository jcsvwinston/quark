// Copyright 2026 jcsvwinston
// SPDX-License-Identifier: Apache-2.0

package drivertest_test

import (
	"database/sql"
	"testing"

	"github.com/jcsvwinston/quark"
	"github.com/jcsvwinston/quark/quarkdriver/drivertest"

	// The bare pure-Go driver, not the Quark driver module: the module
	// imports Quark, and this test is inside Quark's module (ADR-0023).
	_ "modernc.org/sqlite"
)

// TestVerifyDialectSQLite runs the kit against Quark's own SQLite dialect on
// an in-memory database: the kit's regression test in the library's module,
// so `go test ./...` sees a change that breaks it. The other five engines run
// it from internal/enginesuite and from their driver modules.
func TestVerifyDialectSQLite(t *testing.T) {
	db, err := sql.Open("sqlite", "file:drivertest_kit?mode=memory&cache=shared")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	drivertest.VerifyDialect(t, drivertest.DialectCase{
		Dialect:    quark.SQLite(),
		DB:         db,
		DriverName: "sqlite",
	})
}

// TestVerifyDialectWithoutDB: with no database the contract half runs and
// the engine half is skipped, not failed.
func TestVerifyDialectWithoutDB(t *testing.T) {
	drivertest.VerifyDialect(t, drivertest.DialectCase{Dialect: quark.PostgreSQL()})
}
