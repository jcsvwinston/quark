// Copyright 2026 jcsvwinston
// SPDX-License-Identifier: Apache-2.0

// The kit's dialect half is newer than the quark this module pins; the
// standalone lane builds the module against that floor and leaves this file
// out (-tags pinnedquark) until the release train raises it.

//go:build !pinnedquark

package postgres_test

import (
	"database/sql"
	"os"
	"testing"

	"github.com/jcsvwinston/quark"
	"github.com/jcsvwinston/quark/quarkdriver/drivertest"
)

// TestDialectConformance runs the dialect kit against Quark's PostgreSQL
// dialect through this module's driver (pgx). This module registers no
// classifier (TestClassificationNeedsNoRegistration), so the dialect kit is
// the conformance check it runs. The engine half needs a server: CI's driver
// lane gives it one in QUARK_TEST_POSTGRES_DSN; without it the contract half
// runs and the engine half is skipped, saying why.
func TestDialectConformance(t *testing.T) {
	c := drivertest.DialectCase{Dialect: quark.PostgreSQL(), DriverName: "pgx"}
	if dsn := os.Getenv("QUARK_TEST_POSTGRES_DSN"); dsn != "" {
		db, err := sql.Open("pgx", dsn)
		if err != nil {
			t.Fatal(err)
		}
		defer db.Close()
		c.DB = db
		c.Routines = routines(t, db)
	}
	drivertest.VerifyDialect(t, c)
}

// routines creates the function and the procedure the kit runs through
// BuildRoutineQuery (SELECT * FROM fn(…)) and BuildProcedureCall (CALL).
func routines(t *testing.T, db *sql.DB) *drivertest.Routines {
	t.Helper()
	for _, stmt := range []string{
		`CREATE OR REPLACE FUNCTION qk_kit_series(n integer) RETURNS TABLE(value integer) LANGUAGE sql AS $$ SELECT generate_series(1, n) $$`,
		`CREATE OR REPLACE PROCEDURE qk_kit_proc(n integer) LANGUAGE sql AS $$ SELECT n $$`,
	} {
		if _, err := db.Exec(stmt); err != nil {
			t.Fatalf("create the kit's routines: %v", err)
		}
	}
	t.Cleanup(func() {
		_, _ = db.Exec(`DROP FUNCTION IF EXISTS qk_kit_series(integer)`)
		_, _ = db.Exec(`DROP PROCEDURE IF EXISTS qk_kit_proc(integer)`)
	})
	return &drivertest.Routines{Function: "qk_kit_series", Procedure: "qk_kit_proc"}
}
