// Copyright 2026 jcsvwinston
// SPDX-License-Identifier: Apache-2.0

package enginesuite

import (
	"testing"

	"github.com/jcsvwinston/quark"
	"github.com/jcsvwinston/quark/quarkdriver/drivertest"
	"github.com/jcsvwinston/quark/quarkdriver/drivertest/suite"
)

// runConformance runs on this lane's engine what a driver module outside the
// repository runs on its own: the dialect kit (drivertest.VerifyDialect),
// against the built-in dialect the client resolved from driverName, and the
// public engine suite (quarkdriver/drivertest/suite), which holds the
// engine-generic half of what SharedSuite used to run. The in-repo engines
// are held to the same checks a third party's driver is, and a change to
// either that one of the six engines cannot pass turns its lane red here,
// not in a driver author's CI.
func runConformance(t *testing.T, client *quark.Client, driverName string) {
	t.Helper()
	t.Run("DialectKit", func(t *testing.T) {
		drivertest.VerifyDialect(t, drivertest.DialectCase{
			Dialect:    client.Dialect(),
			DB:         client.Raw(),
			DriverName: driverName,
			Routines:   kitRoutines(t, client),
		})
	})
	t.Run("EngineSuite", func(t *testing.T) { suite.Run(t, client) })
}

// kitRoutines creates, on the engines whose DDL for it is short, the
// function and the procedure the kit runs through BuildRoutineQuery and
// BuildProcedureCall, and drops them when the test ends. Oracle (a
// pipelined function needs a collection type, and its rows are named
// COLUMN_VALUE) and SQLite (no stored routines) get nil, and the kit says
// so in the log.
func kitRoutines(t *testing.T, client *quark.Client) *drivertest.Routines {
	t.Helper()
	db := client.Raw()
	var create, drop []string
	switch client.Dialect().Name() {
	case "postgres":
		create = []string{
			`CREATE OR REPLACE FUNCTION qk_kit_series(n integer) RETURNS TABLE(value integer) LANGUAGE sql AS $$ SELECT generate_series(1, n) $$`,
			`CREATE OR REPLACE PROCEDURE qk_kit_proc(n integer) LANGUAGE sql AS $$ SELECT n $$`,
		}
		drop = []string{`DROP FUNCTION IF EXISTS qk_kit_series(integer)`, `DROP PROCEDURE IF EXISTS qk_kit_proc(integer)`}
	case "mysql", "mariadb":
		// BuildRoutineQuery writes CALL on these engines: the "function" is
		// a procedure that returns a result set.
		drop = []string{`DROP PROCEDURE IF EXISTS qk_kit_series`, `DROP PROCEDURE IF EXISTS qk_kit_proc`}
		create = append(drop,
			`CREATE PROCEDURE qk_kit_series(IN n INT) BEGIN WITH RECURSIVE s(value) AS (SELECT 1 UNION ALL SELECT value + 1 FROM s WHERE value < n) SELECT value FROM s; END`,
			`CREATE PROCEDURE qk_kit_proc(IN n INT) BEGIN DECLARE x INT; SET x = n; END`,
		)
	case "mssql":
		drop = []string{
			`IF OBJECT_ID('qk_kit_series') IS NOT NULL DROP FUNCTION qk_kit_series`,
			`IF OBJECT_ID('qk_kit_proc') IS NOT NULL DROP PROCEDURE qk_kit_proc`,
		}
		create = append(drop,
			`CREATE FUNCTION qk_kit_series(@n INT) RETURNS TABLE AS RETURN (WITH s(value) AS (SELECT 1 UNION ALL SELECT value + 1 FROM s WHERE value < @n) SELECT value FROM s)`,
			`CREATE PROCEDURE qk_kit_proc @n INT AS BEGIN SET NOCOUNT ON; END`,
		)
	default:
		return nil
	}
	for _, stmt := range create {
		if _, err := db.Exec(stmt); err != nil {
			t.Fatalf("create the kit's routines: %q: %v", stmt, err)
		}
	}
	t.Cleanup(func() {
		for _, stmt := range drop {
			_, _ = db.Exec(stmt)
		}
	})
	return &drivertest.Routines{Function: "qk_kit_series", Procedure: "qk_kit_proc"}
}
