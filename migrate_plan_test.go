// Copyright 2026 jcsvwinston
// SPDX-License-Identifier: Apache-2.0

package quark_test

import (
	"context"
	"strings"
	"testing"

	"github.com/jcsvwinston/quark"

	_ "modernc.org/sqlite"
)

// planTestFixture is the canonical model for the F3-3-plan tests.
// Three column shapes (PK, NOT NULL text, nullable text) plus an
// explicit default exercise:
//   - PK handling on the desired-Schema build (Nullable=false)
//   - the `quark:"not_null"` tag
//   - the `default:"value"` tag
//   - and that the round-trip (Migrate → PlanMigration) doesn't emit
//     spurious diffs for any of those features.
type planTestFixture struct {
	ID     int64  `db:"id" pk:"true"`
	Name   string `db:"name" quark:"not_null"`
	Status string `db:"status" default:"'draft'"`
	Notes  string `db:"notes"`
}

func newSQLitePlanClient(t *testing.T) *quark.Client {
	t.Helper()
	// In-memory SQLite — each test gets its own DB so the round-trip
	// assertion isn't polluted by state from earlier tests.
	client, err := quark.New("sqlite", "file::memory:?cache=shared")
	if err != nil {
		t.Fatalf("quark.New: %v", err)
	}
	t.Cleanup(func() { _ = client.Close() })
	return client
}

// TestPlanMigration_EmptyDB asserts that against an empty database
// the plan is exactly one OpCreateTable for the fixture model. This
// is the simplest case: no current state, every desired table is new.
func TestPlanMigration_EmptyDB(t *testing.T) {
	ctx := context.Background()
	c := newSQLitePlanClient(t)

	plan, err := c.PlanMigration(ctx, &planTestFixture{})
	if err != nil {
		t.Fatalf("PlanMigration: %v", err)
	}
	if plan.IsEmpty() {
		t.Fatalf("plan should not be empty against an empty DB")
	}
	if len(plan.Ops) != 1 {
		t.Fatalf("want 1 op (create table), got %d:\n%s", len(plan.Ops), plan.String())
	}
	create, ok := plan.Ops[0].(quark.OpCreateTable)
	if !ok {
		t.Fatalf("ops[0]: want OpCreateTable, got %T", plan.Ops[0])
	}
	// The fixture's quark struct tag fixes Table name as "plan_test_fixtures".
	if create.Table.Name == "" {
		t.Errorf("OpCreateTable.Table.Name should not be empty")
	}
	// 4 columns expected (id, name, status, notes).
	if len(create.Table.Columns) != 4 {
		t.Errorf("expected 4 columns, got %d: %+v", len(create.Table.Columns), create.Table.Columns)
	}
}

// TestPlanMigration_RoundTripIsEmpty is the contract that justifies
// F3-3-plan's existence: after Migrate creates the schema from a
// model, PlanMigration against the same model must return an empty
// Plan. If this test fails, every time a user runs `quark migrate
// plan` against a stable schema they'd see noise — the tool would
// be perpetually "dirty" and unusable.
//
// SQLite-specific: the introspector + the migrator emit type
// strings that happen to round-trip cleanly on SQLite (both sides
// use the bare `INTEGER` / `TEXT` form, no parameter-bearing types
// in this fixture). On PG/MySQL/MSSQL the round-trip currently
// produces noisy OpAlterColumn ops because the introspector
// returns `bigint` (lowercase) while the migrator emits `BIGINT`
// (uppercase), etc. — that's the type-normalisation work flagged in
// the PlanMigration godoc. Once that lands, this test will move
// into the shared integration suite and run on all 4 motors.
func TestPlanMigration_RoundTripIsEmpty(t *testing.T) {
	ctx := context.Background()
	c := newSQLitePlanClient(t)

	if err := c.Migrate(ctx, &planTestFixture{}); err != nil {
		t.Fatalf("Migrate: %v", err)
	}
	plan, err := c.PlanMigration(ctx, &planTestFixture{})
	if err != nil {
		t.Fatalf("PlanMigration after Migrate: %v", err)
	}
	if !plan.IsEmpty() {
		t.Fatalf("plan after Migrate should be empty (round-trip identity), got:\n%s", plan.String())
	}
}

// TestPlanMigration_DropsUnknownTable: a table in the DB that no
// model declares should surface as OpDropTable in the plan. This
// is the inverse of the empty-DB case — confirms PlanMigration
// detects drift in both directions.
func TestPlanMigration_DropsUnknownTable(t *testing.T) {
	ctx := context.Background()
	c := newSQLitePlanClient(t)

	if _, err := c.Raw().ExecContext(ctx,
		`CREATE TABLE legacy (id INTEGER PRIMARY KEY)`); err != nil {
		t.Fatalf("seed legacy: %v", err)
	}

	// PlanMigration against an empty model list: every table in
	// the DB is "unknown" and should be dropped.
	plan, err := c.PlanMigration(ctx)
	if err != nil {
		t.Fatalf("PlanMigration: %v", err)
	}
	if len(plan.Ops) != 1 {
		t.Fatalf("want 1 op (drop legacy), got %d:\n%s", len(plan.Ops), plan.String())
	}
	drop, ok := plan.Ops[0].(quark.OpDropTable)
	if !ok || drop.Table != "legacy" {
		t.Errorf("want OpDropTable{legacy}, got %T %+v", plan.Ops[0], plan.Ops[0])
	}
}

// TestPlan_StringEmpty pins the "(no changes)" rendering — the CLI
// (F3-5) will check this exact string when reporting clean schemas.
func TestPlan_StringEmpty(t *testing.T) {
	p := quark.Plan{}
	if got := p.String(); got != "(no changes)" {
		t.Errorf("Plan{}.String(): want %q, got %q", "(no changes)", got)
	}
	if !p.IsEmpty() {
		t.Errorf("Plan{} should be empty")
	}
}

// TestPlan_StringNonEmpty pins the rendering of a non-empty plan:
// one line per op, prefixed `  N. `. The CLI doesn't parse this
// (it renders ops itself), but tests and human review do.
func TestPlan_StringNonEmpty(t *testing.T) {
	p := quark.Plan{Ops: []quark.Operation{
		quark.OpDropTable{Table: "old"},
	}}
	got := p.String()
	if !strings.Contains(got, "1. DROP TABLE old") {
		t.Errorf("Plan.String() should render the op with number prefix, got %q", got)
	}
}

// TestPlanMigration_NonStructErrors is a defensive test: passing
// a non-struct OR nil model must surface a helpful error rather
// than panicking in reflect or producing garbage Schema.
func TestPlanMigration_NonStructErrors(t *testing.T) {
	ctx := context.Background()
	c := newSQLitePlanClient(t)

	t.Run("int", func(t *testing.T) {
		_, err := c.PlanMigration(ctx, 42) // int, not struct
		if err == nil {
			t.Fatalf("want error for non-struct model, got nil")
		}
		if !strings.Contains(err.Error(), "struct") {
			t.Errorf("error should mention 'struct', got %q", err)
		}
	})
	t.Run("untyped_nil", func(t *testing.T) {
		// `c.PlanMigration(ctx, nil)` passes an `any` with both type
		// and value set to nil. `reflect.TypeOf` returns nil — the
		// guard added for this case must turn the panic into an
		// error message mentioning "nil".
		_, err := c.PlanMigration(ctx, nil)
		if err == nil {
			t.Fatalf("want error for nil model, got nil error")
		}
		if !strings.Contains(err.Error(), "nil") {
			t.Errorf("error should mention 'nil', got %q", err)
		}
	})
}
