// Copyright 2026 jcsvwinston
// SPDX-License-Identifier: Apache-2.0

package drivertest

import (
	"errors"
	"fmt"
	"net"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jcsvwinston/quark"
	"github.com/jcsvwinston/quark/quarkdriver"
)

// The engine checks of the schema half of the contract: what Migrate,
// PlanMigration, ApplyPlan, Sync, IntrospectSchema and the migration lock
// ask of the dialect.

// roundTrip migrates model, writes want, reads it back by key and compares
// with eq. It is how ColumnTyper is judged: by the value the column gives
// back, not by the type's name.
func roundTrip[T any](t *testing.T, k *kit, want T, eq func(got, want T) bool) {
	t.Helper()
	var zero T
	k.fresh(t, &zero)
	row := want
	if err := quark.For[T](k.ctx, k.client).Create(&row); err != nil {
		t.Fatalf("insert: %v", err)
	}
	id := reflect.ValueOf(row).FieldByName("ID").Int()
	got, err := quark.For[T](k.ctx, k.client).Find(id)
	if err != nil {
		t.Fatalf("read back: %v", err)
	}
	if !eq(got, row) {
		t.Errorf("wrote %+v, read back %+v", row, got)
	}
}

// checkColumnTyper: every portable kind round-trips a value through the
// type the dialect gives it. A type the engine refuses fails Migrate; a type
// that cannot hold the value changes it.
func checkColumnTyper(t *testing.T, k *kit) {
	if _, ok := k.d.(quarkdriver.ColumnTyper); !ok {
		t.Log("the dialect does not implement quarkdriver.ColumnTyper: Quark's portable types are what is checked")
	}
	at := time.Date(2026, 10, 5, 12, 34, 56, 0, time.UTC)
	note := "set"
	cases := []struct {
		kind string
		run  func(t *testing.T)
	}{
		{"KindString", func(t *testing.T) {
			roundTrip(t, k, kitString{V: "naïve, with ' and \" and a \\"}, func(g, w kitString) bool { return g.V == w.V })
		}},
		{"KindInt16", func(t *testing.T) {
			roundTrip(t, k, kitInt16{V: -32768}, func(g, w kitInt16) bool { return g.V == w.V })
		}},
		{"KindInt32", func(t *testing.T) {
			roundTrip(t, k, kitInt32{V: 2147483647}, func(g, w kitInt32) bool { return g.V == w.V })
		}},
		{"KindInt64", func(t *testing.T) {
			roundTrip(t, k, kitInt64{V: 9223372036854775807}, func(g, w kitInt64) bool { return g.V == w.V })
		}},
		{"KindFloat32", func(t *testing.T) {
			roundTrip(t, k, kitFloat32{V: 0.25}, func(g, w kitFloat32) bool { return g.V == w.V })
		}},
		{"KindFloat64", func(t *testing.T) {
			roundTrip(t, k, kitFloat64{V: 1234567.125}, func(g, w kitFloat64) bool { return g.V == w.V })
		}},
		{"KindDecimal", func(t *testing.T) {
			roundTrip(t, k, kitDecimal{V: 1234567890.12}, func(g, w kitDecimal) bool { return g.V == w.V })
		}},
		{"KindBool", func(t *testing.T) {
			roundTrip(t, k, kitBool{V: true}, func(g, w kitBool) bool { return g.V == w.V })
			roundTrip(t, k, kitBool{V: false}, func(g, w kitBool) bool { return g.V == w.V })
		}},
		{"BoolLiteral", func(t *testing.T) {
			// A DEFAULT clause writes the dialect's boolean literal; a row
			// inserted without the column takes it.
			k.fresh(t, &kitBoolDefault{})
			if err := k.exec("INSERT INTO "+k.q(kitBoolDefault{}.TableName())+" ("+k.q("name")+") VALUES ("+k.d.Placeholder(1)+")", "d"); err != nil {
				t.Fatalf("insert without the defaulted column: %v", err)
			}
			got, err := quark.For[kitBoolDefault](k.ctx, k.client).Where("name", "=", "d").First()
			if err != nil {
				t.Fatalf("read back: %v", err)
			}
			if !got.V {
				t.Error("ColumnTyper.BoolLiteral: a column declared default:\"true\" read false for a row that did not set it")
			}
		}},
		{"KindTime", func(t *testing.T) {
			roundTrip(t, k, kitTime{V: at}, func(g, w kitTime) bool { return g.V.Equal(w.V) })
		}},
		{"KindBytes", func(t *testing.T) {
			roundTrip(t, k, kitBytes{V: []byte{0, 1, 2, 0xfe, 0xff}}, func(g, w kitBytes) bool { return string(g.V) == string(w.V) })
		}},
		{"KindJSON", func(t *testing.T) {
			roundTrip(t, k, kitJSON{V: quark.JSON[map[string]string]{V: map[string]string{"k": "v"}}}, func(g, w kitJSON) bool { return g.V.V["k"] == "v" })
		}},
		{"KindUUID", func(t *testing.T) {
			roundTrip(t, k, kitUUIDRow{V: kitUUID{0xde, 0xad, 0xbe, 0xef, 1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12}}, func(g, w kitUUIDRow) bool { return g.V == w.V })
		}},
		{"KindIP", func(t *testing.T) {
			roundTrip(t, k, kitIP{V: net.ParseIP("10.0.0.1")}, func(g, w kitIP) bool { return g.V.Equal(w.V) })
		}},
		{"KindArray", func(t *testing.T) {
			roundTrip(t, k, kitArray{V: []int64{1, 2, 3}}, func(g, w kitArray) bool { return reflect.DeepEqual(g.V, w.V) })
		}},
		{"KindRange", func(t *testing.T) {
			roundTrip(t, k, kitRange{V: quark.Range[int64]{Lower: 1, Upper: 5}}, func(g, w kitRange) bool { return g.V.Lower == 1 && g.V.Upper == 5 })
		}},
		{"nullable", func(t *testing.T) {
			roundTrip(t, k, kitNullable{V: nil}, func(g, w kitNullable) bool { return g.V == nil })
			roundTrip(t, k, kitNullable{V: &note}, func(g, w kitNullable) bool { return g.V != nil && *g.V == note })
		}},
	}
	for _, c := range cases {
		t.Run(c.kind, c.run)
	}
}

// checkAutoIncrementer: the engine numbers the key, and the catalog reads it
// back as the type the dialect said it would.
func checkAutoIncrementer(t *testing.T, k *kit) {
	who := "AutoIncrementer.AutoIncrementColumn"
	if _, ok := k.d.(quarkdriver.AutoIncrementer); !ok {
		who = "the portable identity column (the dialect does not implement quarkdriver.AutoIncrementer)"
		t.Logf("%s: BIGINT GENERATED BY DEFAULT AS IDENTITY PRIMARY KEY", who)
	}
	k.fresh(t, &kitAuto{})
	var last int64
	for _, name := range []string{"a", "b", "c"} {
		row := kitAuto{Name: name}
		if err := quark.For[kitAuto](k.ctx, k.client).Create(&row); err != nil {
			t.Fatalf("%s: insert without a key: %v", who, err)
		}
		got, err := quark.For[kitAuto](k.ctx, k.client).Where("name", "=", name).First()
		if err != nil {
			t.Fatalf("read back: %v", err)
		}
		if got.ID == 0 || got.ID <= last {
			t.Errorf("%s: the engine gave %q the key %d after %d: the key must be numbered by the engine, increasing", who, name, got.ID, last)
		}
		last = got.ID
	}
	if !k.introspects() {
		return
	}
	for _, op := range k.planFor(t, &kitAuto{}).Ops {
		if a, ok := op.(quark.OpAlterColumn); ok && strings.EqualFold(a.New.Name, "id") {
			t.Errorf("%s: PlanMigration proposes %s for a table Migrate just wrote — the key's data type does not match what IntrospectSchema reads back (AutoIncrementColumn's second result)", who, op)
		}
	}
}

// checkIdempotentDDL: Migrate twice, CreateIndex twice.
func checkIdempotentDDL(t *testing.T, k *kit) {
	who := "IdempotentDDL"
	if _, ok := k.d.(quarkdriver.IdempotentDDL); !ok {
		t.Log("the dialect does not implement quarkdriver.IdempotentDDL: CREATE TABLE IF NOT EXISTS and CREATE INDEX IF NOT EXISTS are what is checked")
		who = "the default IF NOT EXISTS"
	}
	k.fresh(t, &kitIdem{})
	if err := k.client.Migrate(k.ctx, &kitIdem{}); err != nil {
		t.Errorf("%s: a second Migrate of a table with a unique column and an index: %v", who, err)
	}
	table := kitIdem{}.TableName()
	for i := 0; i < 2; i++ {
		if err := k.client.CreateIndex(k.ctx, table, "qk_kit_idem_pair", []string{"code", "rank"}, false); err != nil {
			t.Errorf("%s.CreateIndexIfNotExists: CreateIndex, call %d: %v", who, i+1, err)
		}
	}
	if err := quark.For[kitIdem](k.ctx, k.client).Create(&kitIdem{Code: "x", Rank: 1}); err != nil {
		t.Fatalf("insert: %v", err)
	}
	if err := quark.For[kitIdem](k.ctx, k.client).Create(&kitIdem{Code: "x", Rank: 2}); err == nil {
		t.Errorf("%s: after two Migrates the unique column accepts a duplicate", who)
	}
}

// checkSchemaIntrospector: the catalog read back is the table Migrate wrote,
// so PlanMigration for the same model proposes nothing.
func checkSchemaIntrospector(t *testing.T, k *kit) {
	if !k.introspects() {
		_, err := k.client.IntrospectSchema(k.ctx)
		if !isUnsupported(err) {
			t.Errorf("IntrospectSchema on a dialect without quarkdriver.SchemaIntrospector returned %v, want ErrUnsupportedFeature", err)
		}
		t.Skip("the dialect does not implement quarkdriver.SchemaIntrospector: IntrospectSchema, PlanMigration and Sync are unsupported")
	}
	k.fresh(t, &kitSchema{})
	tb, ok := k.introspected(t, kitSchema{}.TableName())
	if !ok {
		t.Fatalf("SchemaIntrospector.IntrospectSchema does not list the table %s that Migrate created", kitSchema{}.TableName())
	}
	want := map[string]struct{ pk, nullable bool }{
		"id": {true, false}, "name": {false, false}, "note": {false, true}, "code": {false, true}, "rank": {false, true},
	}
	for name, w := range want {
		c, ok := column(tb, name)
		if !ok {
			t.Errorf("SchemaIntrospector: column %s is missing from %s (read %d columns)", name, tb.Name, len(tb.Columns))
			continue
		}
		if c.PrimaryKey != w.pk {
			t.Errorf("SchemaIntrospector: column %s PrimaryKey = %v, want %v", name, c.PrimaryKey, w.pk)
		}
		if !w.pk && c.Nullable != w.nullable {
			t.Errorf("SchemaIntrospector: column %s Nullable = %v, want %v", name, c.Nullable, w.nullable)
		}
		if c.Type == "" {
			t.Errorf("SchemaIntrospector: column %s has no type", name)
		}
	}
	found := false
	for _, ix := range tb.Indexes {
		if len(ix.Columns) == 1 && strings.EqualFold(ix.Columns[0], "rank") {
			found = true
			if ix.Unique {
				t.Errorf("SchemaIntrospector: the index on rank reads as unique")
			}
		}
	}
	if !found {
		t.Errorf("SchemaIntrospector: the index Migrate created on rank is not among %+v — PlanMigration would propose creating it on every run", tb.Indexes)
	}
	if plan := k.planFor(t, &kitSchema{}); !plan.IsEmpty() {
		t.Errorf("SchemaIntrospector: PlanMigration for the model Migrate just wrote proposes:\n%s\nthe types, nullability, keys or indexes IntrospectSchema reads differ from what the dialect writes", plan)
	}
}

// checkSupportsTransactionalDDL: a dialect that says ROLLBACK undoes DDL is
// believed by ApplyPlan and Sync, so the claim is tested.
func checkSupportsTransactionalDDL(t *testing.T, k *kit) {
	if !k.d.SupportsTransactionalDDL() {
		t.Skip("SupportsTransactionalDDL() is false: ApplyPlan takes its resumable path, which the ApplyPlan check exercises")
	}
	table := "qk_kit_txddl"
	k.drop(table)
	t.Cleanup(func() { k.drop(table) })
	tx, err := k.db.BeginTx(k.ctx, nil)
	if err != nil {
		t.Fatalf("begin: %v", err)
	}
	if _, err := tx.ExecContext(k.ctx, "CREATE TABLE "+k.q(table)+" ("+k.q("id")+" "+k.columnType(quarkdriver.ColumnSpec{Kind: quarkdriver.KindInt64})+")"); err != nil {
		_ = tx.Rollback()
		t.Fatalf("CREATE TABLE inside a transaction: %v", err)
	}
	if err := tx.Rollback(); err != nil {
		t.Fatalf("rollback: %v", err)
	}
	if k.tableExists(table) {
		t.Error("Dialect.SupportsTransactionalDDL() is true, but a CREATE TABLE survived the ROLLBACK of its transaction: ApplyPlan would leave a failed plan half-applied")
	}
}

// checkAlterTable runs the five ALTER methods of Dialect on a table with a
// row, and asks the engine what each one changed.
func checkAlterTable(t *testing.T, k *kit) {
	k.fresh(t, &kitAlter{})
	table, renamed := kitAlter{}.TableName(), "qk_kit_alter2"
	k.drop(renamed)
	t.Cleanup(func() { k.drop(renamed) })
	if err := quark.For[kitAlter](k.ctx, k.client).Create(&kitAlter{Name: "kept"}); err != nil {
		t.Fatalf("setup: %v", err)
	}
	step := func(method, stmt string) bool {
		t.Helper()
		if err := k.exec(stmt); err != nil {
			t.Errorf("Dialect.%s wrote a statement the engine rejects: %q: %v", method, stmt, err)
			return false
		}
		return true
	}

	narrow := k.columnType(quarkdriver.ColumnSpec{Kind: quarkdriver.KindString, Size: 10})
	if step("AlterTableAddColumn", k.d.AlterTableAddColumn(table, "extra", narrow)) && !k.columnExists(table, "extra") {
		t.Error("Dialect.AlterTableAddColumn: the column is not there afterwards")
	}
	if step("RenameColumn", k.d.RenameColumn(table, "extra", "extra2")) {
		if !k.columnExists(table, "extra2") || k.columnExists(table, "extra") {
			t.Error("Dialect.RenameColumn: extra was not renamed to extra2")
		}
	}

	if r, ok := k.d.(quarkdriver.TableRebuilder); ok && r.RebuildsTables() {
		t.Log("AlterTableAlterColumn: not run — the dialect rebuilds tables (quarkdriver.TableRebuilder), so ApplyPlan never writes it; the TableRebuilder check covers column changes")
	} else {
		wide := k.columnType(quarkdriver.ColumnSpec{Kind: quarkdriver.KindString, Size: 60})
		if step("AlterTableAlterColumn", k.d.AlterTableAlterColumn(table, "extra2", wide)) {
			long := strings.Repeat("w", 50)
			if err := k.exec("UPDATE "+k.q(table)+" SET "+k.q("extra2")+" = "+k.d.Placeholder(1), long); err != nil {
				t.Errorf("Dialect.AlterTableAlterColumn widened extra2 to %s, but a 50-character value does not fit: %v", wide, err)
			} else {
				var got string
				if err := k.db.QueryRowContext(k.ctx, "SELECT "+k.q("extra2")+" FROM "+k.q(table)).Scan(&got); err != nil || got != long {
					t.Errorf("Dialect.AlterTableAlterColumn: the widened column reads %q (%v)", got, err)
				}
			}
		}
	}

	if step("AlterTableDropColumn", k.d.AlterTableDropColumn(table, "extra2")) && k.columnExists(table, "extra2") {
		t.Error("Dialect.AlterTableDropColumn: the column is still there")
	}
	if step("RenameTable", k.d.RenameTable(table, renamed)) {
		if n, err := k.count(renamed); err != nil || n != 1 {
			t.Errorf("Dialect.RenameTable: the renamed table holds %d rows (%v), want the one row", n, err)
		}
		if k.tableExists(table) {
			t.Error("Dialect.RenameTable: the old name still answers")
		}
	}
}

// checkColumnTypeMapper: a hand-built plan carries a generic type ("TEXT"),
// which the mapper turns into the engine's own; the column has to take a
// long text.
func checkColumnTypeMapper(t *testing.T, k *kit) {
	if _, ok := k.d.(quarkdriver.ColumnTypeMapper); !ok {
		t.Log("the dialect does not implement quarkdriver.ColumnTypeMapper: a hand-built plan's TEXT reaches the engine as written")
	}
	k.fresh(t, &kitAlter{})
	table := kitAlter{}.TableName()
	plan := quark.Plan{Ops: []quark.Operation{quark.OpAddColumn{Table: table, Column: quark.Column{Name: "body", Type: "TEXT", Nullable: true}}}}
	if err := k.apply(plan); err != nil {
		t.Fatalf("ColumnTypeMapper.MapColumnType: ApplyPlan of a column typed TEXT: %v", err)
	}
	long := strings.Repeat("t", 5000)
	if err := k.exec("INSERT INTO "+k.q(table)+" ("+k.q("name")+", "+k.q("body")+") VALUES ("+k.d.Placeholder(1)+", "+k.d.Placeholder(2)+")", "long", long); err != nil {
		t.Fatalf("ColumnTypeMapper.MapColumnType: a 5000-character value into the TEXT column: %v", err)
	}
	var got string
	if err := k.db.QueryRowContext(k.ctx, "SELECT "+k.q("body")+" FROM "+k.q(table)+" WHERE "+k.q("name")+" = "+k.d.Placeholder(1), "long").Scan(&got); err != nil || got != long {
		t.Errorf("ColumnTypeMapper.MapColumnType: the TEXT column read back %d characters (%v), want 5000", len(got), err)
	}
}

// checkMigrationLocker: one holder at a time, a timeout that says so, and a
// release that lets the next one in.
func checkMigrationLocker(t *testing.T, k *kit) {
	name := "qk_kit_" + k.run
	if _, ok := k.d.(quarkdriver.MigrationLocker); !ok {
		_, err := k.client.AcquireMigrationLock(k.ctx, name, time.Second)
		if !isUnsupported(err) {
			t.Errorf("AcquireMigrationLock on a dialect without quarkdriver.MigrationLocker returned %v, want ErrUnsupportedFeature", err)
		}
		t.Skip("the dialect does not implement quarkdriver.MigrationLocker: AcquireMigrationLock returns ErrUnsupportedFeature")
	}
	first, err := k.client.AcquireMigrationLock(k.ctx, name, 5*time.Second)
	if err != nil {
		t.Fatalf("MigrationLocker.AcquireMigrationLock: %v", err)
	}
	released := false
	defer func() {
		if !released {
			_ = first.Release(k.ctx)
		}
	}()

	start := time.Now()
	second, err := k.client.AcquireMigrationLock(k.ctx, name, time.Second)
	switch {
	case err == nil:
		_ = second.Release(k.ctx)
		t.Fatal("MigrationLocker.AcquireMigrationLock: a second holder took the lock while the first held it")
	case !errors.Is(err, quarkdriver.ErrLockTimeout):
		t.Errorf("MigrationLocker.AcquireMigrationLock: a second holder failed with %v, want an error that is ErrLockTimeout", err)
	case time.Since(start) > 20*time.Second:
		t.Errorf("MigrationLocker.AcquireMigrationLock: a 1s timeout took %v", time.Since(start))
	}

	if err := first.Release(k.ctx); err != nil {
		t.Fatalf("MigrationLock.Release: %v", err)
	}
	released = true
	if err := first.Release(k.ctx); err != nil {
		t.Errorf("MigrationLock.Release a second time: %v — it is documented as a no-op", err)
	}

	// After the release the next holder gets in.
	again, err := k.client.AcquireMigrationLock(k.ctx, name, 5*time.Second)
	if err != nil {
		t.Fatalf("MigrationLocker.AcquireMigrationLock after the release: %v", err)
	}
	_ = again.Release(k.ctx)
}

// checkPlanMigration is the schema path end to end from models: Migrate
// writes V1, PlanMigration proposes V2's column and index, ApplyPlan creates
// them and the next plan is empty.
func checkPlanMigration(t *testing.T, k *kit) {
	k.needIntrospector(t)
	k.fresh(t, &kitPlanV1{})
	if err := quark.For[kitPlanV1](k.ctx, k.client).Create(&kitPlanV1{Name: "v1"}); err != nil {
		t.Fatalf("setup: %v", err)
	}
	if plan := k.planFor(t, &kitPlanV1{}); !plan.IsEmpty() {
		t.Fatalf("PlanMigration for the model Migrate just wrote is not empty:\n%s", plan)
	}
	plan := k.planFor(t, &kitPlanV2{})
	var addCol, addIdx bool
	for _, op := range plan.Ops {
		switch o := op.(type) {
		case quark.OpAddColumn:
			addCol = strings.EqualFold(o.Column.Name, "extra")
		case quark.OpCreateIndex:
			addIdx = len(o.Index.Columns) == 1 && strings.EqualFold(o.Index.Columns[0], "extra")
		default:
			t.Errorf("PlanMigration from V1 to V2 proposes %s, which V2 does not ask for", op)
		}
	}
	if !addCol || !addIdx {
		t.Fatalf("PlanMigration from V1 to V2 should add the column extra and its index, and proposes:\n%s", plan)
	}
	if err := k.apply(plan); err != nil {
		t.Fatalf("ApplyPlan of V1 → V2: %v", err)
	}
	if again := k.planFor(t, &kitPlanV2{}); !again.IsEmpty() {
		t.Errorf("PlanMigration after ApplyPlan is not empty — the applied column or index reads back differently:\n%s", again)
	}
	if err := quark.For[kitPlanV2](k.ctx, k.client).Create(&kitPlanV2{Name: "v2", Extra: 7}); err != nil {
		t.Fatalf("insert through V2: %v", err)
	}
	if got, err := quark.For[kitPlanV2](k.ctx, k.client).Where("extra", "=", 7).List(); err != nil || len(got) != 1 {
		t.Errorf("read through the new column: %d rows, %v", len(got), err)
	}
}

// checkSync: Sync adds the column a model gained, keeps the rows, and the
// next plan is empty.
func checkSync(t *testing.T, k *kit) {
	k.needIntrospector(t)
	k.fresh(t, &kitSyncV1{})
	if err := quark.For[kitSyncV1](k.ctx, k.client).Create(&kitSyncV1{Name: "kept"}); err != nil {
		t.Fatalf("setup: %v", err)
	}
	if err := k.client.Sync(k.ctx, quark.SyncOptions{}, &kitSyncV2{}); err != nil {
		t.Fatalf("Sync to a model with one more column: %v", err)
	}
	added := "a"
	if err := quark.For[kitSyncV2](k.ctx, k.client).Create(&kitSyncV2{Name: "new", Added: &added}); err != nil {
		t.Fatalf("Sync: insert through the added column: %v", err)
	}
	rows, err := quark.For[kitSyncV2](k.ctx, k.client).OrderBy("id", "ASC").List()
	if err != nil || len(rows) != 2 || rows[0].Name != "kept" || rows[0].Added != nil || rows[1].Added == nil || *rows[1].Added != "a" {
		t.Errorf("Sync: the table reads %+v (%v), want the old row with no value and the new one with \"a\"", rows, err)
	}
	if plan := k.planFor(t, &kitSyncV2{}); !plan.IsEmpty() {
		t.Errorf("PlanMigration after Sync is not empty:\n%s", plan)
	}
}

// parentChild creates the two tables the constraint operations work on, with
// one parent row and one child row pointing at it, and returns the parent's
// key for the rows the checks insert.
func parentChild(t *testing.T, k *kit) (parent, child string, pid int64) {
	t.Helper()
	k.fresh(t, &kitChild{}, &kitParent{})
	p := kitParent{Name: "p"}
	if err := quark.For[kitParent](k.ctx, k.client).Create(&p); err != nil {
		t.Fatalf("setup: %v", err)
	}
	if err := quark.For[kitChild](k.ctx, k.client).Create(&kitChild{ParentID: p.ID, Code: "c1", Qty: 1, Note: "n"}); err != nil {
		t.Fatalf("setup: %v", err)
	}
	return kitParent{}.TableName(), kitChild{}.TableName(), p.ID
}

// insertChild writes a child row by hand and returns the engine's answer.
func (k *kit) insertChild(parentID int64, code string, qty int64) error {
	child := kitChild{}.TableName()
	return k.exec("INSERT INTO "+k.q(child)+" ("+k.q("parent_id")+", "+k.q("code")+", "+k.q("qty")+", "+k.q("note")+") VALUES ("+
		k.d.Placeholder(1)+", "+k.d.Placeholder(2)+", "+k.d.Placeholder(3)+", "+k.d.Placeholder(4)+")", parentID, code, qty, "n")
}

// fkHolds tells whether a foreign key from child.parent_id is in force:
// the engine refuses an orphan, or — on an engine that does not enforce
// foreign keys on this connection — the catalog lists it.
func (k *kit) fkHolds(t *testing.T, name string) bool {
	t.Helper()
	child := kitChild{}.TableName()
	if err := k.insertChild(999999, "orphan", 1); err != nil {
		return true
	}
	_ = k.exec("DELETE FROM "+k.q(child)+" WHERE "+k.q("code")+" = "+k.d.Placeholder(1), "orphan")
	if !k.introspects() {
		return false
	}
	tb, _ := k.introspected(t, child)
	for _, fk := range tb.ForeignKeys {
		if strings.EqualFold(fk.Name, name) || (len(fk.Columns) == 1 && strings.EqualFold(fk.Columns[0], "parent_id")) {
			return true
		}
	}
	return false
}

// checkApplyPlan: the operations a hand-built plan carries, each judged by
// what the engine then accepts or refuses.
func checkApplyPlan(t *testing.T, k *kit) {
	parent, child, pid := parentChild(t, k)
	created := "qk_kit_created"
	k.drop(created)
	t.Cleanup(func() { k.drop(created) })
	fk := quark.ForeignKey{Name: "qk_kit_fk_parent", Columns: []string{"parent_id"}, RefTable: parent, RefColumns: []string{"id"}, OnDelete: "CASCADE"}

	t.Run("OpCreateIndex", func(t *testing.T) {
		ix := quark.Index{Name: "qk_kit_ux_code", Columns: []string{"code"}, Unique: true}
		if err := k.apply(quark.Plan{Ops: []quark.Operation{quark.OpCreateIndex{Table: child, Index: ix}}}); err != nil {
			t.Fatalf("ApplyPlan OpCreateIndex (unique): %v", err)
		}
		if err := k.insertChild(pid, "c1", 2); err == nil {
			t.Error("ApplyPlan OpCreateIndex: the unique index admits a duplicate code")
		}
	})
	t.Run("OpAddForeignKey", func(t *testing.T) {
		if err := k.apply(quark.Plan{Ops: []quark.Operation{quark.OpAddForeignKey{Table: child, ForeignKey: fk}}}); err != nil {
			t.Fatalf("ApplyPlan OpAddForeignKey: %v", err)
		}
		if !k.fkHolds(t, fk.Name) {
			t.Error("ApplyPlan OpAddForeignKey: the engine accepts an orphan child and the catalog lists no foreign key")
		}
	})
	t.Run("OpAddCheck", func(t *testing.T) {
		chk := quark.Check{Name: "qk_kit_ck_qty", Expression: k.q("qty") + " > 0"}
		if err := k.apply(quark.Plan{Ops: []quark.Operation{quark.OpAddCheck{Table: child, Check: chk}}}); err != nil {
			t.Fatalf("ApplyPlan OpAddCheck: %v", err)
		}
		if err := k.insertChild(pid, "neg", -1); err == nil {
			t.Error("ApplyPlan OpAddCheck: the engine accepts a row the check refuses")
		}
	})
	t.Run("OpDropColumn", func(t *testing.T) {
		if err := k.apply(quark.Plan{Ops: []quark.Operation{quark.OpDropColumn{Table: child, Column: "note"}}}); err != nil {
			t.Fatalf("ApplyPlan OpDropColumn: %v", err)
		}
		if k.columnExists(child, "note") {
			t.Error("ApplyPlan OpDropColumn: the column is still there")
		}
		if n, err := k.count(child); err != nil || n != 1 {
			t.Errorf("ApplyPlan OpDropColumn: the table holds %d rows (%v), want its one row", n, err)
		}
	})
	t.Run("OpCreateTable", func(t *testing.T) {
		table := created
		// The key is text: an integer key is written as the engine's
		// auto-increment column (AutoIncrementer), and SQL Server's IDENTITY
		// refuses an explicit value.
		def := quark.Table{
			Name: table,
			Columns: []quark.Column{
				{Name: "code", Type: k.columnType(quarkdriver.ColumnSpec{Kind: quarkdriver.KindString, Size: 20}), PrimaryKey: true},
				{Name: "parent_id", Type: k.columnType(quarkdriver.ColumnSpec{Kind: quarkdriver.KindInt64})},
				{Name: "label", Type: k.columnType(quarkdriver.ColumnSpec{Kind: quarkdriver.KindString, Size: 20}), Nullable: true},
			},
			Indexes:     []quark.Index{{Name: "qk_kit_ux_label", Columns: []string{"label"}, Unique: true}},
			ForeignKeys: []quark.ForeignKey{{Name: "qk_kit_fk_created", Columns: []string{"parent_id"}, RefTable: parent, RefColumns: []string{"id"}}},
		}
		if err := k.apply(quark.Plan{Ops: []quark.Operation{quark.OpCreateTable{Table: def}}}); err != nil {
			t.Fatalf("ApplyPlan OpCreateTable: %v", err)
		}
		ins := "INSERT INTO " + k.q(table) + " (" + k.q("code") + ", " + k.q("parent_id") + ", " + k.q("label") + ") VALUES (" + k.d.Placeholder(1) + ", " + k.d.Placeholder(2) + ", " + k.d.Placeholder(3) + ")"
		if err := k.exec(ins, "k1", pid, "a"); err != nil {
			t.Fatalf("ApplyPlan OpCreateTable: insert into the created table: %v", err)
		}
		if err := k.exec(ins, "k2", pid, "a"); err == nil {
			t.Error("ApplyPlan OpCreateTable: the table's unique index admits a duplicate")
		}
		if err := k.exec(ins, "k1", pid, "b"); err == nil {
			t.Error("ApplyPlan OpCreateTable: the table's primary key admits a duplicate")
		}
	})
	t.Run("OpDropTable", func(t *testing.T) {
		table := created
		if !k.tableExists(table) {
			t.Skip("OpCreateTable did not create the table to drop")
		}
		if err := k.apply(quark.Plan{Ops: []quark.Operation{quark.OpDropTable{Table: table}}}); err != nil {
			t.Fatalf("ApplyPlan OpDropTable: %v", err)
		}
		if k.tableExists(table) {
			t.Error("ApplyPlan OpDropTable: the table still answers")
		}
	})
	t.Run("a plan that fails half-way", func(t *testing.T) {
		// The second op adds the column the first one added: every engine
		// refuses it. With transactional DDL the first op is undone; without
		// it the first op stays, and ApplyPlan has said so by not wrapping
		// the plan.
		col := quark.Column{Name: "half", Type: k.columnType(quarkdriver.ColumnSpec{Kind: quarkdriver.KindInt64}), Nullable: true}
		plan := quark.Plan{Ops: []quark.Operation{quark.OpAddColumn{Table: parent, Column: col}, quark.OpAddColumn{Table: parent, Column: col}}}
		if err := k.apply(plan); err == nil {
			t.Fatal("ApplyPlan of a plan adding the same column twice succeeded")
		}
		kept := k.columnExists(parent, "half")
		if k.d.SupportsTransactionalDDL() && kept {
			t.Error("Dialect.SupportsTransactionalDDL() is true, but the first op of a failed plan was kept: ApplyPlan ran the plan in a transaction the engine did not roll back")
		}
		if !k.d.SupportsTransactionalDDL() && !kept {
			t.Error("Dialect.SupportsTransactionalDDL() is false, but the first op of a failed plan was undone: the dialect understates its engine (harmless) or the op did not run")
		}
	})
}

// checkColumnAlterer: ApplyPlan's OpAlterColumn, on a table with a row, for
// the type, the nullability and the default.
func checkColumnAlterer(t *testing.T, k *kit) {
	who := "ColumnAlterer.AlterColumn"
	if r, ok := k.d.(quarkdriver.TableRebuilder); ok && r.RebuildsTables() {
		who = "TableRebuilder (the table is rebuilt for a column change)"
	} else if _, ok := k.d.(quarkdriver.ColumnAlterer); !ok {
		who = "the SQL standard's ALTER COLUMN (the dialect does not implement quarkdriver.ColumnAlterer)"
	}
	_, child, pid := parentChild(t, k)
	narrow := k.columnType(quarkdriver.ColumnSpec{Kind: quarkdriver.KindString, Size: 20})
	wide := k.columnType(quarkdriver.ColumnSpec{Kind: quarkdriver.KindString, Size: 60})
	old := quark.Column{Name: "note", Type: narrow, Nullable: true}

	widened := old
	widened.Type = wide
	if err := k.apply(quark.Plan{Ops: []quark.Operation{quark.OpAlterColumn{Table: child, Old: old, New: widened}}}); err != nil {
		t.Fatalf("%s: ApplyPlan OpAlterColumn (type %s → %s): %v", who, narrow, wide, err)
	}
	long := strings.Repeat("w", 50)
	if err := k.exec("UPDATE "+k.q(child)+" SET "+k.q("note")+" = "+k.d.Placeholder(1), long); err != nil {
		t.Errorf("%s: the column widened to %s does not take 50 characters: %v", who, wide, err)
	}
	if n, err := k.count(child); err != nil || n != 1 {
		t.Errorf("%s: after the type change the table holds %d rows (%v), want its one row", who, n, err)
	}

	notNull := widened
	notNull.Nullable = false
	if err := k.apply(quark.Plan{Ops: []quark.Operation{quark.OpAlterColumn{Table: child, Old: widened, New: notNull}}}); err != nil {
		t.Fatalf("%s: ApplyPlan OpAlterColumn (nullable → NOT NULL): %v", who, err)
	}
	if err := k.exec("UPDATE " + k.q(child) + " SET " + k.q("note") + " = NULL"); err == nil {
		t.Errorf("%s: after NOT NULL the column still takes NULL", who)
	}

	def := "'dflt'"
	withDefault := notNull
	withDefault.Default = &def
	if err := k.apply(quark.Plan{Ops: []quark.Operation{quark.OpAlterColumn{Table: child, Old: notNull, New: withDefault}}}); err != nil {
		t.Fatalf("%s: ApplyPlan OpAlterColumn (set a default): %v", who, err)
	}
	if err := k.exec("INSERT INTO "+k.q(child)+" ("+k.q("parent_id")+", "+k.q("code")+", "+k.q("qty")+") VALUES ("+
		k.d.Placeholder(1)+", "+k.d.Placeholder(2)+", "+k.d.Placeholder(3)+")", pid, "dflt", 1); err != nil {
		t.Fatalf("%s: insert without the defaulted column: %v", who, err)
	}
	var got string
	if err := k.db.QueryRowContext(k.ctx, "SELECT "+k.q("note")+" FROM "+k.q(child)+" WHERE "+k.q("code")+" = "+k.d.Placeholder(1), "dflt").Scan(&got); err != nil || got != "dflt" {
		t.Errorf("%s: a row inserted without the column reads %q (%v), want the default dflt", who, got, err)
	}
}

// checkObjectDropper: ApplyPlan's drops, each judged by the constraint no
// longer refusing what it refused.
func checkObjectDropper(t *testing.T, k *kit) {
	if _, ok := k.d.(quarkdriver.ObjectDropper); !ok {
		t.Log("the dialect does not implement quarkdriver.ObjectDropper: DROP INDEX <index> and ALTER TABLE … DROP CONSTRAINT are what is checked")
	}
	parent, child, pid := parentChild(t, k)
	ix := quark.Index{Name: "qk_kit_ux_code", Columns: []string{"code"}, Unique: true}
	fk := quark.ForeignKey{Name: "qk_kit_fk_parent", Columns: []string{"parent_id"}, RefTable: parent, RefColumns: []string{"id"}}
	chk := quark.Check{Name: "qk_kit_ck_qty", Expression: k.q("qty") + " > 0"}
	if err := k.apply(quark.Plan{Ops: []quark.Operation{
		quark.OpCreateIndex{Table: child, Index: ix},
		quark.OpAddForeignKey{Table: child, ForeignKey: fk},
		quark.OpAddCheck{Table: child, Check: chk},
	}}); err != nil {
		t.Fatalf("setup: ApplyPlan of an index, a foreign key and a check: %v", err)
	}

	t.Run("DropIndex", func(t *testing.T) {
		if err := k.apply(quark.Plan{Ops: []quark.Operation{quark.OpDropIndex{Table: child, Index: ix.Name}}}); err != nil {
			t.Fatalf("ObjectDropper.DropIndex: ApplyPlan OpDropIndex: %v", err)
		}
		if err := k.insertChild(pid, "c1", 3); err != nil {
			t.Errorf("ObjectDropper.DropIndex: the dropped unique index still refuses a duplicate: %v", err)
		}
	})
	t.Run("DropCheck", func(t *testing.T) {
		if err := k.apply(quark.Plan{Ops: []quark.Operation{quark.OpDropCheck{Table: child, Check: chk.Name}}}); err != nil {
			t.Fatalf("ObjectDropper.DropCheck: ApplyPlan OpDropCheck: %v", err)
		}
		if err := k.insertChild(pid, "neg", -1); err != nil {
			t.Errorf("ObjectDropper.DropCheck: the dropped check still refuses a row: %v", err)
		}
	})
	t.Run("DropForeignKey", func(t *testing.T) {
		if err := k.apply(quark.Plan{Ops: []quark.Operation{quark.OpDropForeignKey{Table: child, ForeignKey: fk.Name}}}); err != nil {
			t.Fatalf("ObjectDropper.DropForeignKey: ApplyPlan OpDropForeignKey: %v", err)
		}
		if err := k.insertChild(999999, "orphan", 1); err != nil {
			t.Errorf("ObjectDropper.DropForeignKey: the dropped foreign key still refuses an orphan: %v", err)
		}
	})
}

// referentialActions are the SQL standard's five, which the kit asks about in
// each clause of a foreign key.
var referentialActions = []string{"NO ACTION", "RESTRICT", "CASCADE", "SET NULL", "SET DEFAULT"}

// stmtCounter is a QueryObserver that counts the statements a client sends.
type stmtCounter struct {
	mu sync.Mutex
	n  int
}

func (c *stmtCounter) ObserveQuery(quark.QueryEvent) {
	c.mu.Lock()
	c.n++
	c.mu.Unlock()
}

func (c *stmtCounter) count() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.n
}

// checkReferentialActioner: each of the SQL standard's five actions in each
// clause of a foreign key, judged by the engine. An action the dialect has
// Quark write is accepted and the foreign key holds; one it has Quark leave
// out is what the engine does without a clause, so deleting a referenced row
// is refused; one it refuses never reaches the engine — ApplyPlan and
// AddForeignKey fail with ErrUnsupportedFeature naming it, having sent
// nothing, so not even the operation before it in the plan ran.
func checkReferentialActioner(t *testing.T, k *kit) {
	ra, asks := k.d.(quarkdriver.ReferentialActioner)
	if !asks {
		t.Log("the dialect does not implement quarkdriver.ReferentialActioner: every action is written as given, and the engine is checked to accept the SQL standard's five in both clauses")
	}
	parent, child, pid := parentChild(t, k)
	n := 0
	for _, event := range []quarkdriver.ReferentialEvent{quarkdriver.OnDelete, quarkdriver.OnUpdate} {
		for _, action := range referentialActions {
			n++
			support := quarkdriver.ActionWritten
			if asks {
				support = ra.ReferentialAction(event, action)
			}
			fk := quark.ForeignKey{Name: fmt.Sprintf("qk_kit_fk_ra%d", n), Columns: []string{"parent_id"}, RefTable: parent, RefColumns: []string{"id"}}
			if event == quarkdriver.OnDelete {
				fk.OnDelete = action
			} else {
				fk.OnUpdate = action
			}
			clause := event.String() + " " + action
			t.Run(clause, func(t *testing.T) {
				if support == quarkdriver.ActionUnsupported {
					k.refusedBeforeSending(t, child, fk, clause)
					return
				}
				who := "ReferentialActioner.ReferentialAction answered ActionWritten"
				if support == quarkdriver.ActionImplied {
					who = "ReferentialActioner.ReferentialAction answered ActionImplied"
				} else if !asks {
					who = "the dialect writes every action as given"
				}
				if err := k.apply(quark.Plan{Ops: []quark.Operation{quark.OpAddForeignKey{Table: child, ForeignKey: fk}}}); err != nil {
					t.Fatalf("%s for %s, and the engine refused the foreign key: %v", who, clause, err)
				}
				defer func() {
					if err := k.apply(quark.Plan{Ops: []quark.Operation{quark.OpDropForeignKey{Table: child, ForeignKey: fk.Name}}}); err != nil {
						t.Errorf("cleanup: drop the foreign key %s: %v", fk.Name, err)
					}
				}()
				if !k.fkHolds(t, fk.Name) {
					t.Errorf("%s for %s: the engine accepted the foreign key, and it admits an orphan and the catalog lists none", who, clause)
				}
				if support != quarkdriver.ActionImplied {
					return
				}
				switch action {
				case "NO ACTION", "RESTRICT":
					if event != quarkdriver.OnDelete {
						return // the kit's parent key is generated, and not every engine lets it change
					}
					if err := k.exec("DELETE FROM "+k.q(parent)+" WHERE "+k.q("id")+" = "+k.d.Placeholder(1), pid); err == nil {
						t.Errorf("%s for %s, so Quark wrote no clause, and the engine deleted a row a child references: what it does without a clause is not %s", who, clause, action)
					}
				default:
					t.Errorf("%s for %s: Quark leaves the clause out, and an engine does %s for a foreign key that names no action only if that is its default, which is NO ACTION", who, clause, action)
				}
			})
		}
	}
}

// refusedBeforeSending checks an action the dialect answers
// ActionUnsupported for: ApplyPlan — with an added column as its first
// operation — and AddForeignKey both fail with ErrUnsupportedFeature naming
// the clause, and send nothing. The calls go through a client of their own
// that counts what reaches the engine.
func (k *kit) refusedBeforeSending(t *testing.T, child string, fk quark.ForeignKey, clause string) {
	t.Helper()
	driverName := k.c.DriverName
	if driverName == "" {
		driverName = "drivertest"
	}
	sent := &stmtCounter{}
	counted, err := quark.NewWithDB(driverName, k.db, quark.WithDialect(k.d), quark.WithLogger(quiet), quark.WithQueryObserver(sent))
	if err != nil {
		t.Fatalf("quark.NewWithDB with the dialect under test: %v", err)
	}
	defer func() { _ = counted.Close() }() // a borrowed pool is not closed
	before := sent.count()

	probe := quark.Column{Name: "qk_kit_ra", Type: k.columnType(quarkdriver.ColumnSpec{Kind: quarkdriver.KindInt64}), Nullable: true}
	plan := quark.Plan{Ops: []quark.Operation{quark.OpAddColumn{Table: child, Column: probe}, quark.OpAddForeignKey{Table: child, ForeignKey: fk}}}
	k.clearCheckpoint(plan)
	err = counted.ApplyPlan(k.ctx, plan)
	if !errors.Is(err, quarkdriver.ErrUnsupportedFeature) || !strings.Contains(err.Error(), clause) {
		t.Errorf("ReferentialActioner.ReferentialAction answered ActionUnsupported for %s: ApplyPlan should fail with ErrUnsupportedFeature naming it, got %v", clause, err)
	}
	err = counted.AddForeignKey(k.ctx, child, fk.Name, fk.Columns, fk.RefTable, fk.RefColumns, fk.OnDelete, fk.OnUpdate)
	if !errors.Is(err, quarkdriver.ErrUnsupportedFeature) || !strings.Contains(err.Error(), clause) {
		t.Errorf("ReferentialActioner.ReferentialAction answered ActionUnsupported for %s: AddForeignKey should fail with ErrUnsupportedFeature naming it, got %v", clause, err)
	}
	if n := sent.count() - before; n != 0 {
		t.Errorf("ReferentialActioner.ReferentialAction answered ActionUnsupported for %s, and Quark sent %d statements before refusing it", clause, n)
	}
	if k.columnExists(child, probe.Name) {
		t.Errorf("ReferentialActioner.ReferentialAction answered ActionUnsupported for %s, and the operation before it in the plan ran", clause)
		_ = k.apply(quark.Plan{Ops: []quark.Operation{quark.OpDropColumn{Table: child, Column: probe.Name}}})
	}
}

// checkTableRebuilder: a dialect that rebuilds tables keeps what the table
// had — rows, indexes, foreign keys — through the rebuild.
func checkTableRebuilder(t *testing.T, k *kit) {
	r, ok := k.d.(quarkdriver.TableRebuilder)
	if !ok || !r.RebuildsTables() {
		t.Skip("the dialect does not rebuild tables (quarkdriver.TableRebuilder): ApplyPlan changes them in place, which ColumnAlterer and ObjectDropper check")
	}
	parent, child, pid := parentChild(t, k)
	ix := quark.Index{Name: "qk_kit_ux_code", Columns: []string{"code"}, Unique: true}
	fk := quark.ForeignKey{Name: "qk_kit_fk_parent", Columns: []string{"parent_id"}, RefTable: parent, RefColumns: []string{"id"}}
	if err := k.apply(quark.Plan{Ops: []quark.Operation{quark.OpCreateIndex{Table: child, Index: ix}, quark.OpAddForeignKey{Table: child, ForeignKey: fk}}}); err != nil {
		t.Fatalf("setup: %v", err)
	}
	old := quark.Column{Name: "note", Type: k.columnType(quarkdriver.ColumnSpec{Kind: quarkdriver.KindString, Size: 20}), Nullable: true}
	changed := old
	changed.Type = k.columnType(quarkdriver.ColumnSpec{Kind: quarkdriver.KindString, Size: 60})
	if err := k.apply(quark.Plan{Ops: []quark.Operation{
		quark.OpAlterColumn{Table: child, Old: old, New: changed},
		quark.OpAddCheck{Table: child, Check: quark.Check{Name: "qk_kit_ck_qty", Expression: k.q("qty") + " > 0"}},
	}}); err != nil {
		t.Fatalf("TableRebuilder: ApplyPlan of a column change and a check: %v", err)
	}
	if n, err := k.count(child); err != nil || n != 1 {
		t.Errorf("TableRebuilder: the rebuilt table holds %d rows (%v), want its one row", n, err)
	}
	if err := k.insertChild(pid, "c1", 5); err == nil {
		t.Error("TableRebuilder: the unique index did not survive the rebuild")
	}
	if !k.fkHolds(t, fk.Name) {
		t.Error("TableRebuilder: the foreign key did not survive the rebuild")
	}
	if err := k.insertChild(pid, "neg", -1); err == nil {
		t.Error("TableRebuilder: the check added by the rebuild is not enforced")
	}
}

var engineChecks = []engineCheck{
	{"Registration", checkRegistration},
	{"Placeholder", checkPlaceholder},
	{"Quote", checkQuote},
	{"LimitOffset", checkLimitOffset},
	{"Returning", checkReturning},
	{"CurrentTimestamp", checkCurrentTimestamp},
	{"JSONExtract", checkJSONExtract},
	{"UpsertSQL", checkUpsertSQL},
	{"LockSuffix", checkLockSuffix},
	{"SavepointDialect", checkSavepoints},
	{"BuildRoutineQuery+BuildProcedureCall", checkRoutines},
	{"SupportsTransactionalDDL", checkSupportsTransactionalDDL},
	{"AlterTable", checkAlterTable},
	{"ColumnTyper", checkColumnTyper},
	{"AutoIncrementer", checkAutoIncrementer},
	{"IdempotentDDL", checkIdempotentDDL},
	{"SchemaIntrospector", checkSchemaIntrospector},
	{"ColumnTypeMapper", checkColumnTypeMapper},
	{"MigrationLocker", checkMigrationLocker},
	{"PlanMigration", checkPlanMigration},
	{"Sync", checkSync},
	{"ApplyPlan", checkApplyPlan},
	{"ColumnAlterer", checkColumnAlterer},
	{"ObjectDropper", checkObjectDropper},
	{"ReferentialActioner", checkReferentialActioner},
	{"TableRebuilder", checkTableRebuilder},
}
