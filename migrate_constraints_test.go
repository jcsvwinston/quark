// Copyright 2026 jcsvwinston
// SPDX-License-Identifier: Apache-2.0

package quark

import (
	"context"
	"io"
	"log/slog"
	"strings"
	"testing"

	_ "modernc.org/sqlite"
)

// QK-27 (A8 S3). OpCreateTable carried the whole table and the executor read
// only its columns: an index or a foreign key in the desired schema applied
// with a nil error and never arrived, so the same desired schema re-proposed
// them after every apply. These tests pin the three halves of the fix — the
// executor emits everything the op carries, Diff orders and matches so the
// loop converges, and a model can declare an index the plan reads.

func constraintsClient(t *testing.T, name string) *Client {
	t.Helper()
	c, err := New("sqlite", "file:"+name+"?mode=memory&cache=shared",
		WithLogger(slog.New(slog.NewTextHandler(io.Discard, nil))))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { _ = c.Close() })
	return c
}

var constraintsDesired = Schema{Tables: []Table{
	{
		Name:    "qk27_parent",
		Columns: []Column{{Name: "id", Type: "INTEGER", PrimaryKey: true}},
	},
	{
		Name: "qk27_child",
		Columns: []Column{
			{Name: "id", Type: "INTEGER", PrimaryKey: true},
			{Name: "parent_id", Type: "INTEGER", Nullable: true},
			{Name: "email", Type: "TEXT", Nullable: true},
			{Name: "qty", Type: "INTEGER", Nullable: true},
		},
		Indexes:     []Index{{Name: "idx_qk27_child_email", Columns: []string{"email"}, Unique: true}},
		ForeignKeys: []ForeignKey{{Name: "fk_qk27_child_parent", Columns: []string{"parent_id"}, RefTable: "qk27_parent", RefColumns: []string{"id"}}},
		Checks:      []Check{{Name: "ck_qk27_child_qty", Expression: "qty >= 0"}},
	},
}}

// The executor emits the whole table, and the loop converges: re-diffing the
// desired schema against what was applied proposes nothing.
func TestApplyPlanCreatesTheWholeTable(t *testing.T) {
	c := constraintsClient(t, "qk27_whole")
	ctx := context.Background()
	current, err := c.IntrospectSchema(ctx)
	if err != nil {
		t.Fatal(err)
	}
	plan := Plan{Ops: Diff(constraintsDesired, current)}
	if err := c.ApplyPlan(ctx, plan); err != nil {
		t.Fatalf("apply: %v", err)
	}
	live, err := c.IntrospectSchema(ctx)
	if err != nil {
		t.Fatal(err)
	}
	var child Table
	for _, tb := range live.Tables {
		if tb.Name == "qk27_child" {
			child = tb
		}
	}
	if len(child.Indexes) != 1 || child.Indexes[0].Name != "idx_qk27_child_email" || !child.Indexes[0].Unique {
		t.Fatalf("the index the op carried did not arrive: %+v", child.Indexes)
	}
	if len(child.ForeignKeys) != 1 || child.ForeignKeys[0].RefTable != "qk27_parent" {
		t.Fatalf("the foreign key the op carried did not arrive: %+v", child.ForeignKeys)
	}
	// SQLite does not introspect checks, so the constraint is proven by the
	// row it refuses.
	if _, err := c.db.ExecContext(ctx, "INSERT INTO qk27_child (id, qty) VALUES (1, -1)"); err == nil || !strings.Contains(strings.ToUpper(err.Error()), "CHECK") {
		t.Fatalf("the check the op carried did not arrive: insert of a negative qty returned %v", err)
	}
	if residual := Diff(constraintsDesired, live); len(residual) != 0 {
		t.Fatalf("the loop does not converge; the applied schema re-proposes: %v", residual)
	}
}

// Diff creates a referenced table before the table that references it, name
// order otherwise, and a cycle falls back to name order instead of hanging.
func TestDiffOrdersNewTablesParentsFirst(t *testing.T) {
	current := Schema{}
	// Names chosen so that name order is the WRONG order.
	desired := Schema{Tables: []Table{
		{Name: "a_child", Columns: []Column{{Name: "id", Type: "INTEGER", PrimaryKey: true}, {Name: "p", Type: "INTEGER"}},
			ForeignKeys: []ForeignKey{{Columns: []string{"p"}, RefTable: "z_parent", RefColumns: []string{"id"}}}},
		{Name: "m_grandchild", Columns: []Column{{Name: "id", Type: "INTEGER", PrimaryKey: true}, {Name: "c", Type: "INTEGER"}},
			ForeignKeys: []ForeignKey{{Columns: []string{"c"}, RefTable: "a_child", RefColumns: []string{"id"}}}},
		{Name: "z_parent", Columns: []Column{{Name: "id", Type: "INTEGER", PrimaryKey: true}}},
		{Name: "b_free", Columns: []Column{{Name: "id", Type: "INTEGER", PrimaryKey: true}}},
	}}
	var got []string
	for _, op := range Diff(desired, current) {
		got = append(got, op.(OpCreateTable).Table.Name)
	}
	if want := "b_free z_parent a_child m_grandchild"; strings.Join(got, " ") != want {
		t.Fatalf("create order %v, want %s (parents first, name order among the free)", got, want)
	}

	cycle := Schema{Tables: []Table{
		{Name: "x", Columns: []Column{{Name: "id", Type: "INTEGER", PrimaryKey: true}, {Name: "y_id", Type: "INTEGER"}},
			ForeignKeys: []ForeignKey{{Columns: []string{"y_id"}, RefTable: "y", RefColumns: []string{"id"}}}},
		{Name: "y", Columns: []Column{{Name: "id", Type: "INTEGER", PrimaryKey: true}, {Name: "x_id", Type: "INTEGER"}},
			ForeignKeys: []ForeignKey{{Columns: []string{"x_id"}, RefTable: "x", RefColumns: []string{"id"}}}},
	}}
	if ops := Diff(cycle, current); len(ops) != 2 {
		t.Fatalf("a reference cycle produced %d ops, want 2 in name order", len(ops))
	}
}

// A foreign key is matched by what it is, never by a name SQLite does not
// keep; and an unspecified action is the engine default, NO ACTION.
func TestDiffMatchesForeignKeysByComposition(t *testing.T) {
	desired := Table{Name: "t", ForeignKeys: []ForeignKey{{Name: "fk_named", Columns: []string{"p"}, RefTable: "u", RefColumns: []string{"id"}}}}
	live := Table{Name: "t", ForeignKeys: []ForeignKey{{Name: "", Columns: []string{"p"}, RefTable: "u", RefColumns: []string{"id"}, OnDelete: "NO ACTION", OnUpdate: "NO ACTION"}}}
	if ops := diffTable("t", live, desired); len(ops) != 0 {
		t.Fatalf("the same key with a name on one side and NO ACTION spelled out on the other is not drift: %v", ops)
	}
	changed := live
	changed.ForeignKeys[0].OnDelete = "CASCADE"
	if ops := diffTable("t", changed, desired); len(ops) != 2 {
		t.Fatalf("a different ON DELETE is drift (DROP + ADD), got %v", ops)
	}
}

// A desired index is satisfied by a live index of the same shape whatever
// its name — the engine-named backing index of a quark:"unique" column.
func TestDiffMatchesIndexesByShape(t *testing.T) {
	desired := Table{Name: "t", Indexes: []Index{{Name: "uq_t_email", Columns: []string{"email"}, Unique: true}}}
	live := Table{Name: "t", Indexes: []Index{{Name: "sqlite_autoindex_t_1", Columns: []string{"email"}, Unique: true}}}
	if ops := diffTable("t", live, desired); len(ops) != 0 {
		t.Fatalf("a live index of the same shape under another name is not drift: %v", ops)
	}
	live.Indexes[0].Unique = false
	ops := diffTable("t", live, desired)
	if len(ops) != 2 {
		t.Fatalf("a different shape is drift (CREATE the declared, DROP the stray), got %v", ops)
	}
}

// The model declares an index with quark:"index"; Migrate creates it,
// PlanMigration proposes it when missing and leaves undeclared ones alone.
type qk27Doc struct {
	ID    int64  `db:"id" pk:"true"`
	Slug  string `db:"slug" quark:"index"`
	Email string `db:"email" quark:"index=ix_docs_mail,unique"`
}

func (qk27Doc) TableName() string { return "qk27_docs" }

func TestModelDeclaredIndexes(t *testing.T) {
	meta := GetModelMeta[qk27Doc]()
	got := modelIndexes(meta)
	if len(got) != 2 || got[0].Name != "idx_qk27_docs_slug" || got[1].Name != "ix_docs_mail" {
		t.Fatalf("modelIndexes = %+v", got)
	}

	c := constraintsClient(t, "qk27_model_idx")
	ctx := context.Background()
	if err := c.Migrate(ctx, &qk27Doc{}); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	plan, err := c.PlanMigration(ctx, &qk27Doc{})
	if err != nil {
		t.Fatal(err)
	}
	if !plan.IsEmpty() {
		t.Fatalf("a freshly migrated model plans to something: %s", plan)
	}
	if err := c.CreateIndex(ctx, "qk27_docs", "idx_manual", []string{"id"}, false); err != nil {
		t.Fatal(err)
	}
	if plan, _ = c.PlanMigration(ctx, &qk27Doc{}); !plan.IsEmpty() {
		t.Fatalf("an undeclared live index is proposed for something: %s", plan)
	}
	if _, err := c.db.ExecContext(ctx, "DROP INDEX idx_qk27_docs_slug"); err != nil {
		t.Fatal(err)
	}
	plan, _ = c.PlanMigration(ctx, &qk27Doc{})
	if len(plan.Ops) != 1 {
		t.Fatalf("the missing declared index should be the only proposal: %s", plan)
	}
	if ci, ok := plan.Ops[0].(OpCreateIndex); !ok || ci.Index.Name != "idx_qk27_docs_slug" {
		t.Fatalf("proposed %v, want CREATE INDEX idx_qk27_docs_slug", plan.Ops[0])
	}
	if err := c.ApplyPlan(ctx, plan); err != nil {
		t.Fatalf("apply the proposal: %v", err)
	}
	if plan, _ = c.PlanMigration(ctx, &qk27Doc{}); !plan.IsEmpty() {
		t.Fatalf("after applying its own proposal the plan is not empty: %s", plan)
	}
}

// The tag vocabulary: an empty index= is a tag error, like any other typo.
type qk27BadTag struct {
	ID   int64  `db:"id" pk:"true"`
	Slug string `db:"slug" quark:"index="`
}

func TestEmptyIndexNameIsATagError(t *testing.T) {
	meta := GetModelMeta[qk27BadTag]()
	if meta.TagError == nil || !strings.Contains(meta.TagError.Error(), "index=") {
		t.Fatalf("quark:\"index=\" with no name was accepted: %v", meta.TagError)
	}
}
