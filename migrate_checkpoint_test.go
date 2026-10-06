// Copyright 2026 jcsvwinston
// SPDX-License-Identifier: Apache-2.0

package quark

import (
	"context"
	"io"
	"log/slog"
	"strings"
	"testing"
)

// noTxSQLite is SQLite's dialect answering that its DDL is not
// transactional, so ApplyPlan takes the checkpointed path MySQL, MariaDB
// and Oracle take, on an engine the unit lane has. Every optional
// interface of the dialect is kept: the pointer is embedded, not the
// Dialect interface.
type noTxSQLite struct{ *SQLiteDialect }

func (noTxSQLite) SupportsTransactionalDDL() bool { return false }

func newNoTxClient(t *testing.T, name string) *Client {
	t.Helper()
	c, err := New("sqlite", "file:"+name+"?mode=memory&cache=shared",
		WithDialect(noTxSQLite{SQLite().(*SQLiteDialect)}),
		WithLogger(slog.New(slog.NewTextHandler(io.Discard, nil))))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { _ = c.Close() })
	return c
}

func mustExec(t *testing.T, c *Client, stmts ...string) {
	t.Helper()
	for _, s := range stmts {
		if _, err := c.Raw().ExecContext(context.Background(), s); err != nil {
			t.Fatalf("%s: %v", s, err)
		}
	}
}

// liveTable reads one table back through the dialect's introspector.
func liveTable(t *testing.T, c *Client, name string) (Table, bool) {
	t.Helper()
	s, err := c.IntrospectSchema(context.Background())
	if err != nil {
		t.Fatalf("introspect: %v", err)
	}
	for _, tb := range s.Tables {
		if strings.EqualFold(tb.Name, name) {
			return tb, true
		}
	}
	return Table{}, false
}

func tableHasColumn(tb Table, name string) bool {
	for _, col := range tb.Columns {
		if strings.EqualFold(col.Name, name) {
			return true
		}
	}
	return false
}

func checkpointRows(t *testing.T, c *Client, plan Plan) int {
	t.Helper()
	var n int
	if err := c.Raw().QueryRowContext(context.Background(),
		"SELECT COUNT(*) FROM quark_migration_state WHERE plan_hash = ?", plan.Hash()).Scan(&n); err != nil {
		t.Fatalf("count checkpoint rows: %v", err)
	}
	return n
}

// QK-51: on an engine without transactional DDL the checkpoint recorded every
// op of a plan, and the same plan applied again after the schema was put back
// as it was found every op "already applied" and changed nothing. The
// checkpoint now answers only for what the schema still holds.
func TestApplyPlanCheckpointReappliesAfterSchemaReset(t *testing.T) {
	ctx := context.Background()
	c := newNoTxClient(t, "qk51_reset")
	if c.Dialect().SupportsTransactionalDDL() {
		t.Fatal("the test dialect must take the checkpointed path")
	}
	seed := []string{
		`DROP TABLE IF EXISTS ck_new`,
		`DROP TABLE IF EXISTS ck_base`,
		`CREATE TABLE ck_base (id INTEGER PRIMARY KEY)`,
	}
	mustExec(t, c, seed...)

	plan := Plan{Ops: []Operation{
		OpCreateTable{Table: Table{Name: "ck_new", Columns: []Column{
			{Name: "id", Type: "INTEGER", PrimaryKey: true},
			{Name: "name", Type: "TEXT", Nullable: true},
		}}},
		OpAddColumn{Table: "ck_base", Column: Column{Name: "note", Type: "TEXT", Nullable: true}},
		OpCreateIndex{Table: "ck_new", Index: Index{Name: "ck_new_name_ix", Columns: []string{"name"}}},
	}}
	applied := func(when string) {
		t.Helper()
		nt, ok := liveTable(t, c, "ck_new")
		if !ok {
			t.Fatalf("%s: table ck_new does not exist", when)
		}
		if len(nt.Indexes) != 1 || nt.Indexes[0].Name != "ck_new_name_ix" {
			t.Errorf("%s: ck_new has indexes %+v, want ck_new_name_ix", when, nt.Indexes)
		}
		bt, _ := liveTable(t, c, "ck_base")
		if !tableHasColumn(bt, "note") {
			t.Errorf("%s: column ck_base.note does not exist", when)
		}
	}

	if err := c.ApplyPlan(ctx, plan); err != nil {
		t.Fatalf("first apply: %v", err)
	}
	applied("after the first apply")

	// The schema goes back to what the plan was made against.
	mustExec(t, c, seed...)
	if err := c.ApplyPlan(ctx, plan); err != nil {
		t.Fatalf("apply after the reset: %v", err)
	}
	applied("after the reset and the second apply")
	if n := checkpointRows(t, c, plan); n != len(plan.Ops) {
		t.Errorf("the checkpoint holds %d rows for the plan, want %d: the discarded rows were not replaced one for one", n, len(plan.Ops))
	}

	// Applied again over a schema that holds it, the plan is still a no-op:
	// the checkpoint answers for what is there.
	if err := c.ApplyPlan(ctx, plan); err != nil {
		t.Fatalf("apply over a schema that already holds the plan: %v", err)
	}
	applied("after the third apply")
}

// The resume the checkpoint exists for is untouched: a plan that failed
// half-way, its cause fixed by hand, picks up at the op that failed, and the
// op before it — whose table is there — is not run again.
func TestApplyPlanCheckpointStillResumesAfterAFix(t *testing.T) {
	ctx := context.Background()
	c := newNoTxClient(t, "qk51_resume")
	mustExec(t, c,
		`DROP TABLE IF EXISTS ck_a`, `DROP TABLE IF EXISTS ck_b`, `DROP TABLE IF EXISTS ck_missing`)

	plan := Plan{Ops: []Operation{
		OpCreateTable{Table: Table{Name: "ck_a", Columns: []Column{{Name: "id", Type: "INTEGER", PrimaryKey: true}}}},
		OpDropTable{Table: "ck_missing"},
		OpCreateTable{Table: Table{Name: "ck_b", Columns: []Column{{Name: "id", Type: "INTEGER", PrimaryKey: true}}}},
	}}
	if err := c.ApplyPlan(ctx, plan); err == nil {
		t.Fatal("a plan dropping a table that does not exist applied")
	}
	if _, ok := liveTable(t, c, "ck_a"); !ok {
		t.Fatal("op 0 should have stayed applied")
	}
	// The fix: the table op 1 drops now exists. It is not a table op 0
	// touches, so the checkpoint of op 0 still holds.
	mustExec(t, c, `CREATE TABLE ck_missing (id INTEGER PRIMARY KEY)`)
	if err := c.ApplyPlan(ctx, plan); err != nil {
		t.Fatalf("resume: %v — op 0 was run again, or the fix was not seen", err)
	}
	if _, ok := liveTable(t, c, "ck_b"); !ok {
		t.Error("after the resume ck_b should exist")
	}
	if _, ok := liveTable(t, c, "ck_missing"); ok {
		t.Error("after the resume ck_missing should be gone")
	}
}

// checkpointMismatch decides from positive evidence only, and lets a later op
// of the same prefix answer for an earlier one on the same object.
func TestCheckpointMismatch(t *testing.T) {
	def := "'x'"
	narrow := Column{Name: "note", Type: "VARCHAR(20)", Nullable: true}
	wide := Column{Name: "note", Type: "VARCHAR(60)", Nullable: true}
	fk := ForeignKey{Name: "fk_child_parent", Columns: []string{"parent_id"}, RefTable: "parent", RefColumns: []string{"id"}}
	child := func(mod func(*Table)) Schema {
		tb := Table{
			Name:    "child",
			Columns: []Column{{Name: "id", Type: "INTEGER", PrimaryKey: true}, {Name: "parent_id", Type: "INTEGER"}, narrow},
		}
		if mod != nil {
			mod(&tb)
		}
		return Schema{Tables: []Table{{Name: "parent", Columns: []Column{{Name: "id", Type: "INTEGER", PrimaryKey: true}}}, tb}}
	}
	cases := []struct {
		name    string
		live    Schema
		applied []Operation
		missing bool
	}{
		{"created table is there", child(nil), []Operation{OpCreateTable{Table: Table{Name: "CHILD"}}}, false},
		{"created table is gone", child(nil), []Operation{OpCreateTable{Table: Table{Name: "other"}}}, true},
		{"dropped table is back", child(nil), []Operation{OpDropTable{Table: "child"}}, true},
		{"dropped table is gone", child(nil), []Operation{OpDropTable{Table: "other"}}, false},
		{"added column is there", child(nil), []Operation{OpAddColumn{Table: "child", Column: Column{Name: "NOTE"}}}, false},
		{"added column is gone", child(nil), []Operation{OpAddColumn{Table: "child", Column: Column{Name: "extra"}}}, true},
		{"added column on a missing table", child(nil), []Operation{OpAddColumn{Table: "other", Column: Column{Name: "x"}}}, true},
		{"dropped column is back", child(nil), []Operation{OpDropColumn{Table: "child", Column: "note"}}, true},
		{"dropped column is gone", child(nil), []Operation{OpDropColumn{Table: "child", Column: "gone"}}, false},
		{"altered column still reads as Old", child(nil), []Operation{OpAlterColumn{Table: "child", Old: narrow, New: wide}}, true},
		{"altered column reads as New", child(nil), []Operation{OpAlterColumn{Table: "child", Old: wide, New: narrow}}, false},
		{"altered column reads as neither", child(nil), []Operation{OpAlterColumn{Table: "child",
			Old: Column{Name: "note", Type: "TEXT"}, New: Column{Name: "note", Type: "CLOB"}}}, false},
		{"altered default still the Old one", child(func(tb *Table) { tb.Columns[2].Default = nil }),
			[]Operation{OpAlterColumn{Table: "child", Old: narrow, New: Column{Name: "note", Type: "VARCHAR(20)", Nullable: true, Default: &def}}}, true},
		{"created index is gone", child(nil), []Operation{OpCreateIndex{Table: "child", Index: Index{Name: "ix_note"}}}, true},
		{"created index is there", child(func(tb *Table) { tb.Indexes = []Index{{Name: "IX_NOTE", Columns: []string{"note"}}} }),
			[]Operation{OpCreateIndex{Table: "child", Index: Index{Name: "ix_note"}}}, false},
		{"index hidden as a foreign key's backing index", child(func(tb *Table) { tb.ForeignKeys = []ForeignKey{fk} }),
			[]Operation{OpCreateIndex{Table: "child", Index: Index{Name: "fk_child_parent"}}}, false},
		{"dropped index is back", child(func(tb *Table) { tb.Indexes = []Index{{Name: "ix_note"}} }),
			[]Operation{OpDropIndex{Table: "child", Index: "ix_note"}}, true},
		{"dropped then recreated index", child(func(tb *Table) { tb.Indexes = []Index{{Name: "ix_note"}} }),
			[]Operation{OpDropIndex{Table: "child", Index: "ix_note"}, OpCreateIndex{Table: "child", Index: Index{Name: "ix_note"}}}, false},
		{"added foreign key is gone", child(nil), []Operation{OpAddForeignKey{Table: "child", ForeignKey: fk}}, true},
		{"added foreign key is there by name", child(func(tb *Table) { tb.ForeignKeys = []ForeignKey{fk} }),
			[]Operation{OpAddForeignKey{Table: "child", ForeignKey: fk}}, false},
		{"added foreign key is there under the engine's name", child(func(tb *Table) {
			named := fk
			named.Name = "child_ibfk_1"
			tb.ForeignKeys = []ForeignKey{named}
		}), []Operation{OpAddForeignKey{Table: "child", ForeignKey: ForeignKey{Columns: []string{"parent_id"}, RefTable: "PARENT", RefColumns: []string{"id"}}}}, false},
		{"dropped foreign key is back", child(func(tb *Table) { tb.ForeignKeys = []ForeignKey{fk} }),
			[]Operation{OpDropForeignKey{Table: "child", ForeignKey: "fk_child_parent"}}, true},
		{"foreign key dropped without a name settles nothing", child(func(tb *Table) { tb.ForeignKeys = []ForeignKey{fk} }),
			[]Operation{OpDropForeignKey{Table: "child"}}, false},
		{"added check is gone", child(nil), []Operation{OpAddCheck{Table: "child", Check: Check{Name: "ck_qty"}}}, true},
		{"added check is there", child(func(tb *Table) { tb.Checks = []Check{{Name: "CK_QTY"}} }),
			[]Operation{OpAddCheck{Table: "child", Check: Check{Name: "ck_qty"}}}, false},
		{"dropped check is back", child(func(tb *Table) { tb.Checks = []Check{{Name: "ck_qty"}} }),
			[]Operation{OpDropCheck{Table: "child", Check: "ck_qty"}}, true},
		{"a later drop of the table answers for the ops before it", child(nil),
			[]Operation{OpAddColumn{Table: "other", Column: Column{Name: "x"}}, OpCreateIndex{Table: "other", Index: Index{Name: "ix"}}, OpDropTable{Table: "other"}}, false},
		{"an earlier op is asked when no later op touches it", child(nil),
			[]Operation{OpCreateTable{Table: Table{Name: "gone"}}, OpAddColumn{Table: "child", Column: Column{Name: "note"}}}, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			reason, missing := checkpointMismatch(tc.live, tc.applied)
			if missing != tc.missing {
				t.Fatalf("checkpointMismatch = %v (%q), want %v", missing, reason, tc.missing)
			}
			if missing && reason == "" {
				t.Error("a mismatch has to say which op and why")
			}
		})
	}
}

// A dialect that cannot read its schema keeps the checkpoint as recorded.
func TestCheckpointStillHoldsWithoutIntrospector(t *testing.T) {
	c := newNoTxClient(t, "qk51_nointrospect")
	c.dialect = noIntrospector{c.dialect}
	holds, _, err := c.checkpointStillHolds(context.Background(), []Operation{OpCreateTable{Table: Table{Name: "nowhere"}}})
	if err != nil || !holds {
		t.Fatalf("checkpointStillHolds = %v, %v; want true, nil for a dialect that cannot be asked", holds, err)
	}
}

// noIntrospector hides every optional interface of the dialect it wraps,
// SchemaIntrospector among them.
type noIntrospector struct{ Dialect }
