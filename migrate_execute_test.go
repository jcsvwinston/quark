// Copyright 2026 jcsvwinston
// SPDX-License-Identifier: Apache-2.0

package quark_test

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/jcsvwinston/quark"

	_ "modernc.org/sqlite"
)

// TestApplyPlan_EmptyIsNoop locks the obvious-but-important
// contract: an empty Plan is a no-op. Without this pin, a future
// refactor could accidentally call into the dispatch even with
// no ops and trigger a spurious connection / error.
func TestApplyPlan_EmptyIsNoop(t *testing.T) {
	ctx := context.Background()
	c := newSQLitePlanClient(t)
	if err := c.ApplyPlan(ctx, quark.Plan{}); err != nil {
		t.Fatalf("ApplyPlan({}): %v", err)
	}
}

// TestApplyPlan_RoundTrip is the headline contract of F3-3-execute:
// after `PlanMigration → ApplyPlan`, the schema should match the
// model. Validated by introspecting again and asserting the diff
// is empty (the F3-3-plan round-trip identity, now reachable end-
// to-end without manually calling Migrate).
//
// SQLite-only for the same reason as PlanMigration's round-trip:
// type-string drift on PG/MySQL/MSSQL produces noisy OpAlterColumn
// ops that ApplyPlan would try to execute and might fail on
// dialect-specific ALTER syntax. Once the type-normalisation
// follow-up lands, this test moves into the shared suite.
func TestApplyPlan_RoundTrip(t *testing.T) {
	ctx := context.Background()
	c := newSQLitePlanClient(t)

	// Empty DB → plan emits CreateTable for the fixture.
	plan, err := c.PlanMigration(ctx, &planTestFixture{})
	if err != nil {
		t.Fatalf("PlanMigration: %v", err)
	}
	if plan.IsEmpty() {
		t.Fatalf("expected non-empty plan against empty DB")
	}
	if err := c.ApplyPlan(ctx, plan); err != nil {
		t.Fatalf("ApplyPlan: %v\nPlan was:\n%s", err, plan.String())
	}

	// Re-plan — should be empty now.
	plan2, err := c.PlanMigration(ctx, &planTestFixture{})
	if err != nil {
		t.Fatalf("PlanMigration after apply: %v", err)
	}
	if !plan2.IsEmpty() {
		t.Fatalf("plan after apply should be empty (round-trip), got:\n%s", plan2.String())
	}
}

// TestApplyPlan_AddDropColumn covers the bread-and-butter column
// ops independently of the model-reflection path. We use the
// public Diff helpers + raw Op construction so the test doesn't
// depend on model evolution being a separate fixture.
func TestApplyPlan_AddDropColumn(t *testing.T) {
	ctx := context.Background()
	c := newSQLitePlanClient(t)

	// Seed a table with just `id`.
	if _, err := c.Raw().ExecContext(ctx,
		`CREATE TABLE colops (id INTEGER PRIMARY KEY)`); err != nil {
		t.Fatalf("seed: %v", err)
	}
	defer func() {
		_, _ = c.Raw().ExecContext(ctx, "DROP TABLE colops")
	}()

	// Apply ADD COLUMN.
	add := quark.Plan{Ops: []quark.Operation{
		quark.OpAddColumn{Table: "colops", Column: quark.Column{Name: "name", Type: "TEXT", Nullable: true}},
	}}
	if err := c.ApplyPlan(ctx, add); err != nil {
		t.Fatalf("apply add: %v", err)
	}
	// Verify via introspection.
	schema, err := c.IntrospectSchema(ctx)
	if err != nil {
		t.Fatalf("introspect: %v", err)
	}
	if !hasColumn(schema, "colops", "name") {
		t.Errorf("after add, colops should have column 'name'")
	}

	// Apply DROP COLUMN.
	drop := quark.Plan{Ops: []quark.Operation{
		quark.OpDropColumn{Table: "colops", Column: "name"},
	}}
	if err := c.ApplyPlan(ctx, drop); err != nil {
		t.Fatalf("apply drop: %v", err)
	}
	schema, err = c.IntrospectSchema(ctx)
	if err != nil {
		t.Fatalf("introspect after drop: %v", err)
	}
	if hasColumn(schema, "colops", "name") {
		t.Errorf("after drop, colops should NOT have column 'name'")
	}
}

// TestApplyPlan_SQLite_RejectsDropFK pins the SQLite limitation
// explicitly: OpDropForeignKey returns ErrUnsupportedFeature on
// SQLite. The test is the lever that flips when F3-3-execute-
// sqlite-rebuild lands the 12-step procedure.
func TestApplyPlan_SQLite_RejectsDropFK(t *testing.T) {
	ctx := context.Background()
	c := newSQLitePlanClient(t)
	plan := quark.Plan{Ops: []quark.Operation{
		quark.OpDropForeignKey{Table: "t", ForeignKey: "fk_x"},
	}}
	err := c.ApplyPlan(ctx, plan)
	if err == nil {
		t.Fatalf("DropFK on SQLite should error, got nil")
	}
	if !errors.Is(err, quark.ErrUnsupportedFeature) {
		t.Errorf("want ErrUnsupportedFeature, got %v", err)
	}
}

// TestApplyPlan_SQLite_RejectsDropCheck — same as above for CHECK.
func TestApplyPlan_SQLite_RejectsDropCheck(t *testing.T) {
	ctx := context.Background()
	c := newSQLitePlanClient(t)
	plan := quark.Plan{Ops: []quark.Operation{
		quark.OpDropCheck{Table: "t", Check: "chk_x"},
	}}
	err := c.ApplyPlan(ctx, plan)
	if err == nil {
		t.Fatalf("DropCheck on SQLite should error, got nil")
	}
	if !errors.Is(err, quark.ErrUnsupportedFeature) {
		t.Errorf("want ErrUnsupportedFeature, got %v", err)
	}
}

// TestApplyPlan_SQLite_RejectsAddCheck: same SQLite limitation as
// DropFK / DropCheck — symmetric pin.
func TestApplyPlan_SQLite_RejectsAddCheck(t *testing.T) {
	ctx := context.Background()
	c := newSQLitePlanClient(t)
	plan := quark.Plan{Ops: []quark.Operation{
		quark.OpAddCheck{Table: "t", Check: quark.Check{Name: "chk_x", Expression: "a > 0"}},
	}}
	err := c.ApplyPlan(ctx, plan)
	if err == nil {
		t.Fatalf("AddCheck on SQLite should error, got nil")
	}
	if !errors.Is(err, quark.ErrUnsupportedFeature) {
		t.Errorf("want ErrUnsupportedFeature, got %v", err)
	}
}

// TestApplyPlan_AlterColumn_NullableOnlyIsError pins the
// fail-loud contract: when OpAlterColumn carries no Type change
// (only nullable or default deltas), ApplyPlan returns
// ErrUnsupportedFeature pointing at F3-3-execute-alter. The
// alternative — silent noop — was the original implementation;
// the reviewer caught it (B3) and we converted to fail-loud so
// users don't see an unending "dirty" plan.
func TestApplyPlan_AlterColumn_NullableOnlyIsError(t *testing.T) {
	ctx := context.Background()
	c := newSQLitePlanClient(t)
	plan := quark.Plan{Ops: []quark.Operation{
		quark.OpAlterColumn{
			Table: "t",
			Old:   quark.Column{Name: "x", Type: "TEXT", Nullable: true},
			New:   quark.Column{Name: "x", Type: "TEXT", Nullable: false},
		},
	}}
	err := c.ApplyPlan(ctx, plan)
	if err == nil {
		t.Fatalf("nullable-only alter should error, got nil")
	}
	if !errors.Is(err, quark.ErrUnsupportedFeature) {
		t.Errorf("want ErrUnsupportedFeature, got %v", err)
	}
	if !strings.Contains(err.Error(), "F3-3-execute-alter") {
		t.Errorf("error should reference F3-3-execute-alter follow-up, got %q", err)
	}
}

// TestApplyPlan_RejectsBadIdentifier covers the SQLGuard layer
// added per reviewer B1: a Plan with an adversarial identifier
// (e.g. SQL injection attempt) gets rejected at ApplyPlan rather
// than reaching ExecContext. Pin the contract so a future refactor
// that removes the ValidateIdentifier call surfaces immediately.
func TestApplyPlan_RejectsBadIdentifier(t *testing.T) {
	ctx := context.Background()
	c := newSQLitePlanClient(t)
	plan := quark.Plan{Ops: []quark.Operation{
		quark.OpDropTable{Table: "t; DROP TABLE users --"},
	}}
	err := c.ApplyPlan(ctx, plan)
	if err == nil {
		t.Fatalf("adversarial identifier should error, got nil")
	}
}

// TestApplyPlan_ErrorIncludesOpIndex: when a mid-plan op fails,
// the error wraps the failed op's index + String() so the caller
// can identify the failure point in a multi-op plan.
func TestApplyPlan_ErrorIncludesOpIndex(t *testing.T) {
	ctx := context.Background()
	c := newSQLitePlanClient(t)
	plan := quark.Plan{Ops: []quark.Operation{
		quark.OpDropTable{Table: "nonexistent_table_xyz"},
	}}
	err := c.ApplyPlan(ctx, plan)
	if err == nil {
		t.Fatalf("drop nonexistent table should error, got nil")
	}
	// Error message includes the op index and the op's String()
	// rendering for debuggability.
	if !strings.Contains(err.Error(), "op 0") {
		t.Errorf("error should mention op index, got %q", err)
	}
	if !strings.Contains(err.Error(), "DROP TABLE nonexistent_table_xyz") {
		t.Errorf("error should mention the op string, got %q", err)
	}
}

// hasColumn is a small test helper: does the introspected schema
// have a table by `tableName` with a column `colName`?
func hasColumn(schema quark.Schema, tableName, colName string) bool {
	for _, t := range schema.Tables {
		if t.Name != tableName {
			continue
		}
		for _, c := range t.Columns {
			if c.Name == colName {
				return true
			}
		}
	}
	return false
}
