package quark_test

import (
	"context"
	"testing"

	"github.com/jcsvwinston/quark"
)

// boolDefaultsModel exercises boolean column defaults in their three documented
// forms (1 / 0 / true). Package-level so it can pin its table name. The PK is a
// string (caller-supplied) on purpose: an integer PK becomes an IDENTITY column
// on MSSQL/Oracle, which rejects the explicit-id INSERT this test needs to omit
// the bool columns and let the engine DEFAULT apply.
type boolDefaultsModel struct {
	ID       string `db:"id" pk:"true"`
	Active   bool   `db:"active" default:"1"`
	Archived bool   `db:"archived" default:"0"`
	Verified bool   `db:"verified" default:"true"`
}

func (boolDefaultsModel) TableName() string { return "bool_defaults_t" }

// testBoolDefault is the cross-engine regression for the boolean-default
// portability papercut: a bool field with default:"1" (or "0"/"true") must
// migrate on ALL six engines. PostgreSQL's BOOLEAN rejects DEFAULT 1 (SQLSTATE
// 42804); MSSQL BIT and Oracle NUMBER(1) reject DEFAULT TRUE. The migrator now
// normalizes the literal per dialect on both the direct Migrate path and the
// PlanMigration/ApplyPlan path.
func testBoolDefault(ctx context.Context, t *testing.T, baseClient *quark.Client) {
	t.Helper()

	dropTable(baseClient, "bool_defaults_t")
	defer dropTable(baseClient, "bool_defaults_t")

	// (1) Direct Migrate must succeed on every engine — this was the PG bug.
	if err := baseClient.Migrate(ctx, &boolDefaultsModel{}); err != nil {
		t.Fatalf("migrate with bool defaults: %v", err)
	}

	// (2) Insert OMITTING the bool columns so the engine's DEFAULT applies.
	// Raw *sql.DB bypasses Create (which writes Go zero values). Lowercase
	// unquoted identifiers + a string literal are portable across all six.
	if _, err := baseClient.Raw().ExecContext(ctx, "INSERT INTO bool_defaults_t (id) VALUES ('r1')"); err != nil {
		t.Fatalf("raw insert: %v", err)
	}
	got, err := quark.For[boolDefaultsModel](ctx, baseClient).Find("r1")
	if err != nil {
		t.Fatalf("find: %v", err)
	}
	if !got.Active || got.Archived || !got.Verified {
		t.Errorf("bool defaults applied wrong: active=%v archived=%v verified=%v, want true/false/true",
			got.Active, got.Archived, got.Verified)
	}

	// (3) The plan path (PlanMigration -> ApplyPlan) emits the same DDL and
	// must also create the table on every engine — it has its own DEFAULT
	// emitter (applyCreateTable), now likewise normalized.
	dropTable(baseClient, "bool_defaults_t")
	plan, err := baseClient.PlanMigration(ctx, &boolDefaultsModel{})
	if err != nil {
		t.Fatalf("plan migration: %v", err)
	}
	if err := baseClient.ApplyPlan(ctx, plan); err != nil {
		t.Fatalf("apply plan (bool defaults): %v", err)
	}
}
