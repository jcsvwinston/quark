// Copyright 2026 jcsvwinston
// SPDX-License-Identifier: Apache-2.0

package enginesuite

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"testing"

	"github.com/jcsvwinston/quark"
)

// A4/S5: a generated migration has to be reversible, and "reversible" is only
// worth claiming if something checks that the schema comes BACK. This applies
// a plan and its Down on every engine and compares the catalog before and
// after — not the SQL, the schema the engine actually ended up with.

type revArticle struct {
	ID       int64   `db:"id" pk:"true"`
	Title    string  `db:"title,size=200" quark:"not_null"`
	Score    float64 `db:"score"`
	Views    int64   `db:"views"`
	AuthorID int64   `db:"author_id"`
}

func testMigrationIsReversible(ctx context.Context, t *testing.T, client *quark.Client) {
	t.Helper()
	table := quark.GetModelMeta[revArticle]().Table
	dropTable(client, table)
	defer dropTable(client, table)

	before, err := schemaFingerprint(ctx, client, table)
	if err != nil {
		t.Fatalf("fingerprint before: %v", err)
	}

	full, err := client.PlanMigration(ctx, &revArticle{})
	if err != nil {
		t.Fatalf("plan: %v", err)
	}
	// PlanMigration compares the models it is given against the WHOLE
	// catalog, so in a shared suite database it also proposes dropping the
	// tables the other tests left behind. Those drops are not reversible —
	// nothing records their shape — and they are not what this measures, so
	// the plan is narrowed to the table under test.
	plan := onlyTable(full, table)
	if plan.IsEmpty() {
		t.Fatal("plan is empty; the table should not exist yet")
	}

	// Down is captured BEFORE applying: a plan built from a schema already
	// changed describes a different starting point.
	down, err := plan.Down()
	if err != nil {
		t.Fatalf("Down: %v", err)
	}

	if err := client.ApplyPlan(ctx, plan); err != nil {
		t.Fatalf("apply: %v", err)
	}
	applied, err := schemaFingerprint(ctx, client, table)
	if err != nil {
		t.Fatalf("fingerprint after apply: %v", err)
	}
	if applied == before {
		t.Fatal("the schema did not change; the plan did nothing")
	}

	if err := client.ApplyPlan(ctx, down); err != nil {
		t.Fatalf("apply down: %v", err)
	}
	after, err := schemaFingerprint(ctx, client, table)
	if err != nil {
		t.Fatalf("fingerprint after down: %v", err)
	}
	if after != before {
		t.Errorf("the schema did not come back.\nbefore: %s\nafter:  %s", before, after)
	}
}

// testDownRefusesWhatItCannotRebuild pins the other half of the contract: an
// operation whose inverse is not in the plan errors instead of producing a
// rollback that reports success and leaves the schema subtly different.
func testDownRefusesWhatItCannotRebuild(ctx context.Context, t *testing.T, client *quark.Client) {
	t.Helper()
	table := quark.GetModelMeta[revArticle]().Table
	dropTable(client, table)
	if err := client.Migrate(ctx, &revArticle{}); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	defer dropTable(client, table)

	// A plan that drops the table: nothing in it records the shape needed to
	// build it again.
	dropPlan := quark.Plan{Ops: []quark.Operation{quark.OpDropTable{Table: table}}}
	if _, err := dropPlan.Down(); !errors.Is(err, quark.ErrIrreversibleOperation) {
		t.Errorf("Down of a DROP TABLE: err = %v, want ErrIrreversibleOperation", err)
	}
}

// onlyTable keeps the operations that touch one table. See the call site.
func onlyTable(p quark.Plan, table string) quark.Plan {
	var ops []quark.Operation
	for _, op := range p.Ops {
		switch o := op.(type) {
		case quark.OpCreateTable:
			if strings.EqualFold(o.Table.Name, table) {
				ops = append(ops, op)
			}
		case quark.OpCreateIndex:
			if strings.EqualFold(o.Table, table) {
				ops = append(ops, op)
			}
		case quark.OpAddColumn:
			if strings.EqualFold(o.Table, table) {
				ops = append(ops, op)
			}
		case quark.OpAddForeignKey:
			if strings.EqualFold(o.Table, table) {
				ops = append(ops, op)
			}
		}
	}
	return quark.Plan{Ops: ops}
}

// schemaFingerprint renders the catalog as a stable string: table and column
// names with their types and nullability, sorted. Comparing fingerprints
// catches a rollback that leaves a column or an index behind, which comparing
// table names alone would not.
func schemaFingerprint(ctx context.Context, client *quark.Client, only string) (string, error) {
	s, err := client.IntrospectSchema(ctx)
	if err != nil {
		return "", err
	}
	var lines []string
	for _, tb := range s.Tables {
		// Only the table under test: the suite shares a database, so other
		// tests' tables come and go for reasons that have nothing to do with
		// this one.
		if !strings.EqualFold(tb.Name, only) {
			continue
		}
		for _, c := range tb.Columns {
			lines = append(lines, fmt.Sprintf("%s.%s:%s:null=%v:pk=%v",
				strings.ToLower(tb.Name), strings.ToLower(c.Name),
				strings.ToUpper(c.Type), c.Nullable, c.PrimaryKey))
		}
		for _, idx := range tb.Indexes {
			lines = append(lines, fmt.Sprintf("%s!idx:%s:%v",
				strings.ToLower(tb.Name), strings.ToLower(idx.Name), idx.Unique))
		}
	}
	sort.Strings(lines)
	return strings.Join(lines, "\n"), nil
}
