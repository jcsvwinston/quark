// Copyright 2026 jcsvwinston
// SPDX-License-Identifier: Apache-2.0

package quark_test

import (
	"context"
	"testing"

	"github.com/jcsvwinston/quark"
)

// planFixture is the dialect-neutral fixture for the F3-3-plan
// integration contracts. Two columns: a PK and a NOT NULL text.
// We keep this minimal to avoid type-string drift territory — the
// `RoundTripIsEmpty` contract that needs PG/MySQL/MSSQL-clean type
// strings doesn't run here (it's SQLite-only in
// migrate_plan_test.go); the dialect-agnostic plan tests below
// only assert op SHAPE, not type-string equality.
type planFixture struct {
	ID   int64  `db:"id" pk:"true"`
	Name string `db:"name" quark:"not_null"`
}

// testPlanMigration runs the dialect-agnostic F3-3-plan contracts
// against every dialect the SharedSuite covers (SQLite, PG, MySQL,
// MariaDB, MSSQL). The contracts asserted here are:
//
//   - PlanMigration against an empty DB emits OpCreateTable for the
//     fixture model.
//   - PlanMigration against a DB with an unknown table emits
//     OpDropTable for that table.
//
// The third contract — round-trip identity (`Migrate(model)` then
// `PlanMigration(model)` returns an empty plan) — is intentionally
// NOT exercised here, because cross-dialect type-string drift
// (migrator's `BIGINT` vs catalog's `bigint`, etc.) generates
// spurious OpAlterColumn ops until the type-normalisation
// follow-up lands. That contract lives in `migrate_plan_test.go`
// as a SQLite-only test where the strings happen to align today.
func testPlanMigration(ctx context.Context, t *testing.T, baseClient *quark.Client) {
	t.Helper()

	t.Run("PlanFromModels_CreatesTable", func(t *testing.T) {
		// Empty DB → expect at least one OpCreateTable for the
		// fixture. We don't assert "exactly one" because the DB
		// may carry leftover tables from prior tests in the same
		// suite (the SharedSuite shares a connection).
		dropTable(baseClient, "plan_fixtures")
		defer dropTable(baseClient, "plan_fixtures")

		plan, err := baseClient.PlanMigration(ctx, &planFixture{})
		if err != nil {
			t.Fatalf("PlanMigration: %v", err)
		}
		if plan.IsEmpty() {
			t.Fatalf("plan should not be empty against an empty DB")
		}
		var sawCreate bool
		for _, op := range plan.Ops {
			if create, ok := op.(quark.OpCreateTable); ok && create.Table.Name == "plan_fixtures" {
				sawCreate = true
				break
			}
		}
		if !sawCreate {
			t.Fatalf("plan should include OpCreateTable{plan_fixtures}, got:\n%s", plan.String())
		}
	})

	t.Run("ApplyPlan_AddColumnRoundTrip", func(t *testing.T) {
		// F3-3-execute contract on real engines: build a one-op
		// plan that adds a column, apply it, then re-introspect
		// and verify the column exists. We use raw DDL for the
		// initial seed and a hand-built Plan for the apply so the
		// test is independent of model-reflection drift.
		dropTable(baseClient, "plan_apply_fixture")
		defer dropTable(baseClient, "plan_apply_fixture")

		if _, err := baseClient.Raw().ExecContext(ctx,
			`CREATE TABLE plan_apply_fixture (id INTEGER PRIMARY KEY)`); err != nil {
			t.Fatalf("seed: %v", err)
		}

		// Different dialects accept different "text" type strings;
		// use TEXT which all 5 engines accept (PG/SQLite/SQL Server
		// via type alias, MySQL/MariaDB via the TEXT BLOB family).
		addCol := quark.Plan{Ops: []quark.Operation{
			quark.OpAddColumn{
				Table:  "plan_apply_fixture",
				Column: quark.Column{Name: "label", Type: "TEXT", Nullable: true},
			},
		}}
		if err := baseClient.ApplyPlan(ctx, addCol); err != nil {
			t.Fatalf("ApplyPlan add: %v", err)
		}

		// Verify via introspection.
		schema, err := baseClient.IntrospectSchema(ctx)
		if err != nil {
			t.Fatalf("introspect: %v", err)
		}
		var sawLabel bool
		for _, table := range schema.Tables {
			if table.Name == "plan_apply_fixture" {
				for _, col := range table.Columns {
					if col.Name == "label" {
						sawLabel = true
					}
				}
			}
		}
		if !sawLabel {
			t.Errorf("after ApplyPlan(AddColumn), 'label' column should be present")
		}
	})

	t.Run("PlanFromModels_DropsUnknown", func(t *testing.T) {
		// Seed a known-extraneous table, then run PlanMigration
		// with zero models. Every table in the DB is "unknown" and
		// should be dropped.
		dropTable(baseClient, "plan_extraneous")
		if _, err := baseClient.Raw().ExecContext(ctx,
			`CREATE TABLE plan_extraneous (id INTEGER PRIMARY KEY)`); err != nil {
			t.Fatalf("seed plan_extraneous: %v", err)
		}
		defer dropTable(baseClient, "plan_extraneous")

		plan, err := baseClient.PlanMigration(ctx)
		if err != nil {
			t.Fatalf("PlanMigration: %v", err)
		}
		var sawDrop bool
		for _, op := range plan.Ops {
			if drop, ok := op.(quark.OpDropTable); ok && drop.Table == "plan_extraneous" {
				sawDrop = true
				break
			}
		}
		if !sawDrop {
			t.Fatalf("plan should include OpDropTable{plan_extraneous}, got:\n%s", plan.String())
		}
	})
}
