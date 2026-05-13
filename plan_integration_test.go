// Copyright 2026 jcsvwinston
// SPDX-License-Identifier: Apache-2.0

package quark_test

import (
	"context"
	"strings"
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

// opTouchesTable reports whether an Operation's String() format
// references the given table name. Used by the round-trip test
// to scope assertions to a specific fixture rather than the whole
// (shared, polluted) database.
//
// We tokenize Op.String() on whitespace + `.` + `(` + `,` and check
// for an exact-token match against `table`. A naive
// `strings.Contains` would false-positive on `plan_fixtures` vs
// `plan_fixtures_archive` or similar substring relationships —
// uncommon today but the kind of latent fragility a token check
// eliminates outright.
func opTouchesTable(op quark.Operation, table string) bool {
	sep := func(r rune) bool {
		return r == ' ' || r == '.' || r == '(' || r == ',' || r == ')'
	}
	for _, tok := range strings.FieldsFunc(op.String(), sep) {
		if tok == table {
			return true
		}
	}
	return false
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
	dialect := baseClient.Dialect().Name()

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

	t.Run("PlanMigration_RoundTripScopedToFixture", func(t *testing.T) {
		// F3-3-plan's headline contract — now reachable on all 5
		// motors thanks to F3-3-types (cross-dialect type-string
		// normalisation): after Migrate(model), PlanMigration(model)
		// emits **no ops that touch the fixture's table**.
		//
		// We scope the assertion to operations referencing
		// `plan_fixtures` because the SharedSuite reuses one DB
		// connection across tests, so other tests' tables (which
		// PlanMigration legitimately wants to drop given our
		// single-model input) would otherwise flood the result.
		// The contract we want to pin is "the fixture round-trips
		// clean", not "the entire DB matches the fixture".
		//
		// Before F3-3-types this only worked on SQLite because the
		// migrator emits `BIGINT` / `VARCHAR(255)` while PG returns
		// `bigint` / `character varying(255)` and MySQL returns
		// `bigint(20)` / `varchar(255)`. The normaliser in
		// columnsEqual now collapses all those to the same canonical
		// form. If this test fails on a motor, the normaliser is
		// missing a case for that engine's catalog output.
		dropTable(baseClient, "plan_fixtures")
		defer dropTable(baseClient, "plan_fixtures")

		if err := baseClient.Migrate(ctx, &planFixture{}); err != nil {
			t.Fatalf("Migrate: %v", err)
		}
		plan, err := baseClient.PlanMigration(ctx, &planFixture{})
		if err != nil {
			t.Fatalf("PlanMigration after Migrate: %v", err)
		}
		for _, op := range plan.Ops {
			if opTouchesTable(op, "plan_fixtures") {
				t.Errorf("plan after Migrate should NOT touch plan_fixtures, got: %s", op.String())
			}
		}
	})

	t.Run("ApplyPlan_TransactionalRollback", func(t *testing.T) {
		// F3-4-tx contract: when a plan fails mid-execution on a
		// transactional-DDL engine (PG / MSSQL / SQLite), the whole
		// plan rolls back. We feed a 2-op plan where the first
		// would succeed in isolation and the second is guaranteed
		// to fail; after the call the schema must show NO sign of
		// the first op (transaction rolled back).
		//
		// MySQL / MariaDB / Oracle do NOT support transactional
		// DDL — every statement implicitly commits. On those
		// engines the first op DOES land and the second fails.
		// The test branches per-dialect to assert the right
		// contract for each.
		dropTable(baseClient, "tx_rollback_probe")
		defer dropTable(baseClient, "tx_rollback_probe")

		plan := quark.Plan{Ops: []quark.Operation{
			quark.OpCreateTable{Table: quark.Table{
				Name:    "tx_rollback_probe",
				Columns: []quark.Column{{Name: "id", Type: "INTEGER", Nullable: false}},
			}},
			quark.OpDropTable{Table: "doesnt_exist_xyz_for_rollback_test"},
		}}

		err := baseClient.ApplyPlan(ctx, plan)
		if err == nil {
			t.Fatalf("plan with guaranteed-fail op should error, got nil")
		}

		schema, ierr := baseClient.IntrospectSchema(ctx)
		if ierr != nil {
			t.Fatalf("introspect: %v", ierr)
		}
		var sawProbe bool
		for _, table := range schema.Tables {
			if table.Name == "tx_rollback_probe" {
				sawProbe = true
				break
			}
		}

		switch dialect {
		case "postgres", "mssql", "sqlite":
			// Transactional DDL — rollback should erase the probe.
			if sawProbe {
				t.Errorf("dialect %s supports transactional DDL — tx_rollback_probe should NOT exist after rollback", dialect)
			}
		default:
			// Non-transactional DDL (mysql / mariadb / oracle /
			// anything not in the transactional list) — the first
			// op committed implicitly. The probe SHOULD exist; this
			// pins the behaviour for every engine in the matrix so
			// future improvements (F3-4-resumable checkpoint state)
			// have a clear contract to flip. Using `default:` rather
			// than enumerating each engine means Oracle (currently
			// out of CI) is automatically covered when its container
			// image issue is unblocked — no test maintenance needed.
			if !sawProbe {
				t.Errorf("dialect %s does NOT support transactional DDL — tx_rollback_probe should exist (first op implicitly committed)", dialect)
			}
		}
	})

	t.Run("ApplyPlan_ResumesAfterMidPlanFailure", func(t *testing.T) {
		// F3-4-resumable contract: on engines without transactional
		// DDL (MySQL / MariaDB / Oracle), a mid-plan failure leaves
		// the schema partially applied — that's unavoidable. What
		// F3-4-resumable adds is that re-invoking ApplyPlan with
		// the SAME plan picks up from the first un-applied op
		// instead of re-applying ops that already landed.
		//
		// On transactional engines (PG / MSSQL / SQLite) the
		// resumable path isn't used — rollback handles failure.
		// So this test only runs the assertion on non-tx engines;
		// transactional engines get an early return with a note
		// so the test still passes there (it's a no-op for them).
		if dialect == "postgres" || dialect == "mssql" || dialect == "sqlite" {
			// CLAUDE.md rule 7 — no t.Skip for per-engine gating.
			// Use early return so the subtest doesn't show up as
			// SKIP in CI (which can be silently ignored); it just
			// doesn't exercise the assertion path for engines where
			// the contract doesn't apply.
			return
		}

		dropTable(baseClient, "resume_probe_a")
		dropTable(baseClient, "resume_probe_b")
		defer dropTable(baseClient, "resume_probe_a")
		defer dropTable(baseClient, "resume_probe_b")
		// Also clean the state table after the test so subsequent
		// runs see a fresh slate. Use raw DDL because the state
		// table is internal and not exposed via the Client API.
		defer func() {
			_, _ = baseClient.Raw().ExecContext(ctx,
				"DELETE FROM quark_migration_state")
		}()

		// 3-op plan where op 1 will fail (DROP non-existent),
		// surrounded by ops 0 and 2 which would succeed in
		// isolation. The plan hash will identify this exact
		// sequence; after the failure, we patch the missing
		// table and re-invoke. The contract: only op 2 runs on
		// the second invocation (op 0 was already applied).
		plan := quark.Plan{Ops: []quark.Operation{
			quark.OpCreateTable{Table: quark.Table{
				Name:    "resume_probe_a",
				Columns: []quark.Column{{Name: "id", Type: "INTEGER", Nullable: false}},
			}},
			quark.OpDropTable{Table: "resume_doesnt_exist_xyz"},
			quark.OpCreateTable{Table: quark.Table{
				Name:    "resume_probe_b",
				Columns: []quark.Column{{Name: "id", Type: "INTEGER", Nullable: false}},
			}},
		}}

		// First invocation: ops 0 and 1 hit the engine; op 1
		// fails; op 2 never runs. State table records op 0.
		err := baseClient.ApplyPlan(ctx, plan)
		if err == nil {
			t.Fatalf("plan with guaranteed-fail op should error on first run, got nil")
		}

		// Verify op 0 landed (resume_probe_a exists) and op 2
		// did NOT (resume_probe_b doesn't exist yet).
		schema, ierr := baseClient.IntrospectSchema(ctx)
		if ierr != nil {
			t.Fatalf("introspect: %v", ierr)
		}
		var sawA, sawB bool
		for _, tbl := range schema.Tables {
			if tbl.Name == "resume_probe_a" {
				sawA = true
			}
			if tbl.Name == "resume_probe_b" {
				sawB = true
			}
		}
		if !sawA {
			t.Fatalf("after first run, resume_probe_a should exist (op 0 implicitly committed)")
		}
		if sawB {
			t.Fatalf("after first run, resume_probe_b should NOT exist (op 2 never reached)")
		}

		// "Fix" the failing op by creating the missing table
		// that op 1 wants to drop. Now op 1 will succeed on
		// re-run.
		if _, err := baseClient.Raw().ExecContext(ctx,
			`CREATE TABLE resume_doesnt_exist_xyz (id INTEGER PRIMARY KEY)`); err != nil {
			t.Fatalf("patch: %v", err)
		}
		defer func() {
			_, _ = baseClient.Raw().ExecContext(ctx, "DROP TABLE resume_doesnt_exist_xyz")
		}()

		// Second invocation: the resumable path reads the state
		// table, sees that op 0 was applied, and starts from
		// op 1. Ops 1 and 2 both succeed.
		if err := baseClient.ApplyPlan(ctx, plan); err != nil {
			t.Fatalf("second ApplyPlan (resume): %v", err)
		}
		// Now resume_probe_b should exist.
		schema, ierr = baseClient.IntrospectSchema(ctx)
		if ierr != nil {
			t.Fatalf("introspect after resume: %v", ierr)
		}
		sawA, sawB = false, false
		for _, tbl := range schema.Tables {
			if tbl.Name == "resume_probe_a" {
				sawA = true
			}
			if tbl.Name == "resume_probe_b" {
				sawB = true
			}
		}
		if !sawA {
			t.Errorf("after resume, resume_probe_a should still exist")
		}
		if !sawB {
			t.Errorf("after resume, resume_probe_b should exist (op 2 ran on second invocation)")
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
