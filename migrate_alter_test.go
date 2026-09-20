// Copyright 2026 jcsvwinston
// SPDX-License-Identifier: Apache-2.0

package quark

import (
	"context"
	"errors"
	"strings"
	"testing"
)

// A8 S4 (MIG-07, MIG-09). SQLite has no ALTER COLUMN and no ADD/DROP
// CONSTRAINT; the executor rebuilt nothing, refused three of the four column
// deltas and rendered the fourth as a SQL comment. These tests pin the table
// rebuild: every delta lands, the rows survive, and what only the CREATE
// TABLE text knows — index DDL, triggers, check and key names — comes back.

func alterClient(t *testing.T, name string) *Client {
	return constraintsClient(t, name)
}

func tableNamed(t *testing.T, c *Client, name string) Table {
	t.Helper()
	live, err := c.IntrospectSchema(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	for _, tb := range live.Tables {
		if tb.Name == name {
			return tb
		}
	}
	t.Fatalf("no table %s", name)
	return Table{}
}

func columnNamed(t *testing.T, tb Table, name string) Column {
	t.Helper()
	for _, col := range tb.Columns {
		if col.Name == name {
			return col
		}
	}
	t.Fatalf("%s has no column %s", tb.Name, name)
	return Column{}
}

func TestSQLiteRebuildAppliesEveryColumnDelta(t *testing.T) {
	c := alterClient(t, "s4_deltas")
	ctx := context.Background()
	for _, stmt := range []string{
		`CREATE TABLE s4_items (id INTEGER PRIMARY KEY, amount INTEGER NOT NULL, label TEXT)`,
		`INSERT INTO s4_items VALUES (1, 10, 'a'), (2, 20, 'b')`,
	} {
		if _, err := c.db.ExecContext(ctx, stmt); err != nil {
			t.Fatal(err)
		}
	}
	cur := columnNamed(t, tableNamed(t, c, "s4_items"), "amount")
	step := func(label string, mutate func(*Column)) Column {
		next := cur
		mutate(&next)
		if err := c.ApplyPlan(ctx, Plan{Ops: []Operation{OpAlterColumn{Table: "s4_items", Old: cur, New: next}}}); err != nil {
			t.Fatalf("%s: %v", label, err)
		}
		got := columnNamed(t, tableNamed(t, c, "s4_items"), "amount")
		cur = got
		return got
	}
	if got := step("type", func(col *Column) { col.Type = "TEXT" }); !strings.EqualFold(got.Type, "TEXT") {
		t.Fatalf("type delta did not land: %+v", got)
	}
	if got := step("nullable", func(col *Column) { col.Nullable = true }); !got.Nullable {
		t.Fatalf("nullable delta did not land: %+v", got)
	}
	zero := "0"
	if got := step("default", func(col *Column) { col.Default = &zero }); got.Default == nil || !strings.Contains(*got.Default, "0") {
		t.Fatalf("default delta did not land: %+v", got)
	}
	if got := step("primary key", func(col *Column) { col.PrimaryKey = true }); !got.PrimaryKey {
		t.Fatalf("primary-key delta did not land: %+v", got)
	}
	// The rows survived four rebuilds.
	var n int
	if err := c.db.QueryRowContext(ctx, "SELECT COUNT(*) FROM s4_items WHERE label IN ('a','b')").Scan(&n); err != nil || n != 2 {
		t.Fatalf("rows after the rebuilds: %d (%v), want 2", n, err)
	}
}

// The rebuild carries what the PRAGMAs do not know: index DDL verbatim, the
// automatic index of a UNIQUE column as a named unique index, triggers, and
// the names of checks and keys written by applyCreateTable.
func TestSQLiteRebuildCarriesIndexesTriggersAndNames(t *testing.T) {
	c := alterClient(t, "s4_carry")
	ctx := context.Background()
	desired := Schema{Tables: []Table{
		{Name: "s4_parent", Columns: []Column{{Name: "id", Type: "INTEGER", PrimaryKey: true}}},
		{Name: "s4_child", Columns: []Column{
			{Name: "id", Type: "INTEGER", PrimaryKey: true},
			{Name: "parent_id", Type: "INTEGER", Nullable: true},
			{Name: "code", Type: "TEXT", Nullable: true},
			{Name: "qty", Type: "INTEGER", Nullable: true},
		},
			Indexes:     []Index{{Name: "idx_s4_child_code", Columns: []string{"code"}}},
			ForeignKeys: []ForeignKey{{Name: "fk_s4_child_parent", Columns: []string{"parent_id"}, RefTable: "s4_parent", RefColumns: []string{"id"}}},
			Checks:      []Check{{Name: "ck_s4_child_qty", Expression: "qty >= 0"}},
		},
	}}
	current, _ := c.IntrospectSchema(ctx)
	if err := c.ApplyPlan(ctx, Plan{Ops: Diff(desired, current)}); err != nil {
		t.Fatal(err)
	}
	for _, stmt := range []string{
		`CREATE UNIQUE INDEX uq_manual ON s4_child(code, qty)`,
		`CREATE TRIGGER trg_s4 AFTER INSERT ON s4_child BEGIN UPDATE s4_child SET qty = 0 WHERE qty IS NULL; END`,
		`INSERT INTO s4_parent VALUES (1)`,
		`INSERT INTO s4_child (id, parent_id, code, qty) VALUES (1, 1, 'x', NULL)`,
	} {
		if _, err := c.db.ExecContext(ctx, stmt); err != nil {
			t.Fatalf("%s: %v", stmt, err)
		}
	}

	// A column delta rebuilds the table; everything else has to be there after.
	old := columnNamed(t, tableNamed(t, c, "s4_child"), "code")
	next := old
	next.Nullable = false
	if err := c.ApplyPlan(ctx, Plan{Ops: []Operation{OpAlterColumn{Table: "s4_child", Old: old, New: next}}}); err != nil {
		t.Fatalf("rebuild: %v", err)
	}
	child := tableNamed(t, c, "s4_child")
	names := map[string]bool{}
	for _, idx := range child.Indexes {
		names[idx.Name] = true
	}
	if !names["idx_s4_child_code"] || !names["uq_manual"] {
		t.Fatalf("indexes after the rebuild: %+v", child.Indexes)
	}
	if len(child.ForeignKeys) != 1 || child.ForeignKeys[0].RefTable != "s4_parent" {
		t.Fatalf("foreign key after the rebuild: %+v", child.ForeignKeys)
	}
	if _, err := c.db.ExecContext(ctx, "INSERT INTO s4_child (id, parent_id, code, qty) VALUES (2, 1, 'y', -1)"); err == nil {
		t.Fatal("the check did not survive the rebuild")
	}
	var qty int
	if _, err := c.db.ExecContext(ctx, "INSERT INTO s4_child (id, parent_id, code, qty) VALUES (3, 1, 'z', NULL)"); err != nil {
		t.Fatal(err)
	}
	if err := c.db.QueryRowContext(ctx, "SELECT qty FROM s4_child WHERE id = 3").Scan(&qty); err != nil || qty != 0 {
		t.Fatalf("the trigger did not survive the rebuild: qty=%d err=%v", qty, err)
	}

	// The key's name survived in the text: the generated rollback finds it.
	up := Plan{Ops: []Operation{OpAddForeignKey{Table: "s4_child", ForeignKey: ForeignKey{Name: "fk_s4_child_self", Columns: []string{"qty"}, RefTable: "s4_child", RefColumns: []string{"id"}}}}}
	down, err := up.Down()
	if err != nil {
		t.Fatal(err)
	}
	if err := c.ApplyPlan(ctx, up); err != nil {
		t.Fatalf("add fk: %v", err)
	}
	if got := tableNamed(t, c, "s4_child"); len(got.ForeignKeys) != 2 {
		t.Fatalf("after add fk: %+v", got.ForeignKeys)
	}
	if err := c.ApplyPlan(ctx, down); err != nil {
		t.Fatalf("drop fk by name: %v", err)
	}
	if got := tableNamed(t, c, "s4_child"); len(got.ForeignKeys) != 1 {
		t.Fatalf("after drop fk: %+v", got.ForeignKeys)
	}
	// And a check comes and goes the same way.
	if err := c.ApplyPlan(ctx, Plan{Ops: []Operation{OpAddCheck{Table: "s4_child", Check: Check{Name: "ck_s4_child_id", Expression: "id > 0"}}}}); err != nil {
		t.Fatalf("add check: %v", err)
	}
	if _, err := c.db.ExecContext(ctx, "INSERT INTO s4_child (id, parent_id, code, qty) VALUES (-1, 1, 'w', 1)"); err == nil {
		t.Fatal("the added check is not enforced")
	}
	if err := c.ApplyPlan(ctx, Plan{Ops: []Operation{OpDropCheck{Table: "s4_child", Check: "ck_s4_child_id"}}}); err != nil {
		t.Fatalf("drop check: %v", err)
	}
	if _, err := c.db.ExecContext(ctx, "INSERT INTO s4_child (id, parent_id, code, qty) VALUES (-1, 1, 'w', 1)"); err != nil {
		t.Fatalf("the dropped check is still enforced: %v", err)
	}
}

// A CHECK the rebuild cannot read back is refused, not lost.
func TestSQLiteRebuildRefusesAnUnreadableCheck(t *testing.T) {
	c := alterClient(t, "s4_unreadable")
	ctx := context.Background()
	if _, err := c.db.ExecContext(ctx, `CREATE TABLE s4_hand (id INTEGER PRIMARY KEY, qty INTEGER CHECK (qty > 0))`); err != nil {
		t.Fatal(err)
	}
	old := columnNamed(t, tableNamed(t, c, "s4_hand"), "qty")
	next := old
	next.Type = "TEXT"
	err := c.ApplyPlan(ctx, Plan{Ops: []Operation{OpAlterColumn{Table: "s4_hand", Old: old, New: next}}})
	if !errors.Is(err, ErrUnsupportedFeature) || !strings.Contains(err.Error(), "CHECK") {
		t.Fatalf("a hand-written inline CHECK should refuse the rebuild loudly, got %v", err)
	}
	if got := columnNamed(t, tableNamed(t, c, "s4_hand"), "qty"); !strings.EqualFold(got.Type, "INTEGER") {
		t.Fatalf("the refused rebuild changed the column: %+v", got)
	}
}

// A NOT NULL delta over rows that hold NULL fails, and the transaction
// leaves the table as it was.
func TestSQLiteRebuildFailsLoudOnDataThatViolatesTheNewShape(t *testing.T) {
	c := alterClient(t, "s4_violates")
	ctx := context.Background()
	for _, stmt := range []string{
		`CREATE TABLE s4_v (id INTEGER PRIMARY KEY, note TEXT)`,
		`INSERT INTO s4_v VALUES (1, NULL)`,
	} {
		if _, err := c.db.ExecContext(ctx, stmt); err != nil {
			t.Fatal(err)
		}
	}
	old := columnNamed(t, tableNamed(t, c, "s4_v"), "note")
	next := old
	next.Nullable = false
	if err := c.ApplyPlan(ctx, Plan{Ops: []Operation{OpAlterColumn{Table: "s4_v", Old: old, New: next}}}); err == nil {
		t.Fatal("a NOT NULL over a NULL row succeeded")
	}
	if got := columnNamed(t, tableNamed(t, c, "s4_v"), "note"); !got.Nullable {
		t.Fatalf("the failed rebuild left the column changed: %+v", got)
	}
	var n int
	if err := c.db.QueryRowContext(ctx, "SELECT COUNT(*) FROM s4_v").Scan(&n); err != nil || n != 1 {
		t.Fatalf("rows after the failed rebuild: %d (%v)", n, err)
	}
}

// The non-SQLite statements, read off a dialect-only client: the shape per
// engine is the contract the engine lanes then prove.
func TestAlterColumnStatementsPerDialect(t *testing.T) {
	zero := "0"
	old := Column{Name: "amount", Type: "INTEGER", Nullable: false}
	cases := []struct {
		d    Dialect
		want []string
	}{
		{PostgreSQL(), []string{`ALTER COLUMN "amount" TYPE`, `DROP NOT NULL`, `SET DEFAULT 0`}},
		{MySQL(), []string{"MODIFY COLUMN `amount`", "NULL DEFAULT 0"}},
		{MSSQL(), []string{`ALTER COLUMN [amount]`, ` NULL`, `ADD CONSTRAINT [DF_t_amount] DEFAULT 0 FOR [amount]`}},
		{Oracle(), []string{`MODIFY ("AMOUNT"`, `DEFAULT 0 NULL`}}, // Oracle folds identifiers to upper case
	}
	for _, tc := range cases {
		rec := &txStatementRecorder{}
		c, err := New("sqlite", "file:s4_stmts_"+tc.d.Name()+"?mode=memory&cache=shared", WithDialect(tc.d), WithQueryObserver(rec))
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = c.Close() })
		next := old
		next.Type = "TEXT"
		next.Nullable = true
		next.Default = &zero
		// The statements fail on SQLite; the executor has no observer, so
		// they are read from the error instead — each is the first thing
		// the executor runs, and the error names it.
		err = c.ApplyPlan(context.Background(), Plan{Ops: []Operation{OpAlterColumn{Table: "t", Old: old, New: next}}})
		if err == nil {
			t.Fatalf("%s: a %s statement ran on SQLite?", tc.d.Name(), tc.d.Name())
		}
		// Build the statements directly for the assertion.
		stmts := alterColumnStatementsForTest(c, OpAlterColumn{Table: "t", Old: old, New: next})
		joined := strings.Join(stmts, "\n")
		for _, w := range tc.want {
			if !strings.Contains(joined, w) {
				t.Errorf("%s: statements lack %q:\n%s", tc.d.Name(), w, joined)
			}
		}
	}
}

// alterColumnStatementsForTest renders what the executor would run, on a
// dialect-only client. The MSSQL catalog lookup only runs when the old
// column had a default, and here it had none.
func alterColumnStatementsForTest(c *Client, o OpAlterColumn) []string {
	stmts, err := c.alterColumnStatements(context.Background(), c.db, o, columnDelta(o.Old, o.New))
	if err != nil {
		return []string{err.Error()}
	}
	return stmts
}
