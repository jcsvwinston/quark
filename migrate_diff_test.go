// Copyright 2026 jcsvwinston
// SPDX-License-Identifier: Apache-2.0

package quark

import (
	"fmt"
	"strings"
	"testing"
)

// ptr is a tiny helper for building Column.Default test values without
// littering the table-driven cases with `func() *string { s := "0"; return &s }()`.
func ptr(s string) *string { return &s }

// TestDiff_NoChanges asserts the algorithm's most important guarantee:
// when the desired and current Schemas are structurally identical, the
// diff is empty. A non-empty diff here would mean every roundtrip
// (model → DDL → introspect → diff) emits noise — the migration plan
// would be perpetually "dirty".
func TestDiff_NoChanges(t *testing.T) {
	s := Schema{Tables: []Table{
		{
			Name:    "users",
			Columns: []Column{{Name: "id", Type: "bigint", Nullable: false}, {Name: "name", Type: "text", Nullable: true}},
			Indexes: []Index{{Name: "idx_users_name", Columns: []string{"name"}, Unique: false}},
			ForeignKeys: []ForeignKey{{
				Name: "fk_users_org", Columns: []string{"org_id"},
				RefTable: "orgs", RefColumns: []string{"id"},
				OnDelete: "CASCADE", OnUpdate: "NO ACTION",
			}},
			Checks: []Check{{Name: "chk_users_name_nonempty", Expression: "(length(name) > 0)"}},
		},
	}}
	if ops := Diff(s, s); len(ops) != 0 {
		t.Fatalf("identical schemas should diff to no ops, got %d: %s", len(ops), opsString(ops))
	}
}

// TestDiff_CreateTable covers the simplest add: a fresh table that
// the current schema doesn't know about. Order matters — CREATE TABLE
// must come first so subsequent ops can reference it.
func TestDiff_CreateTable(t *testing.T) {
	desired := Schema{Tables: []Table{
		{Name: "orders", Columns: []Column{{Name: "id", Type: "bigint"}}},
		{Name: "users", Columns: []Column{{Name: "id", Type: "bigint"}}},
	}}
	current := Schema{Tables: []Table{
		{Name: "users", Columns: []Column{{Name: "id", Type: "bigint"}}},
	}}
	ops := Diff(desired, current)
	if len(ops) != 1 {
		t.Fatalf("want 1 op, got %d: %s", len(ops), opsString(ops))
	}
	create, ok := ops[0].(OpCreateTable)
	if !ok {
		t.Fatalf("want OpCreateTable, got %T", ops[0])
	}
	if create.Table.Name != "orders" {
		t.Errorf("create.Table.Name: want orders, got %q", create.Table.Name)
	}
}

// TestDiff_DropTable: the inverse. DROP must come LAST so any FKs
// referencing the dropped table on tables that exist in both schemas
// can be dropped first.
func TestDiff_DropTable(t *testing.T) {
	desired := Schema{Tables: []Table{
		{Name: "users", Columns: []Column{{Name: "id", Type: "bigint"}}},
	}}
	current := Schema{Tables: []Table{
		{Name: "legacy_audit", Columns: []Column{{Name: "id", Type: "bigint"}}},
		{Name: "users", Columns: []Column{{Name: "id", Type: "bigint"}}},
	}}
	ops := Diff(desired, current)
	if len(ops) != 1 {
		t.Fatalf("want 1 op, got %d: %s", len(ops), opsString(ops))
	}
	drop, ok := ops[0].(OpDropTable)
	if !ok || drop.Table != "legacy_audit" {
		t.Errorf("want OpDropTable{legacy_audit}, got %T %+v", ops[0], ops[0])
	}
}

// TestDiff_ColumnOps exercises ADD / DROP / ALTER COLUMN in a single
// table. The relative order matters: ADD before ALTER before DROP so
// every new shape is in place before destructive moves.
func TestDiff_ColumnOps(t *testing.T) {
	current := Schema{Tables: []Table{{Name: "users", Columns: []Column{
		{Name: "id", Type: "bigint", Nullable: false},
		{Name: "name", Type: "varchar(255)", Nullable: true},
		{Name: "legacy_field", Type: "text", Nullable: true},
	}}}}
	desired := Schema{Tables: []Table{{Name: "users", Columns: []Column{
		{Name: "id", Type: "bigint", Nullable: false},
		{Name: "name", Type: "varchar(255)", Nullable: false},    // alter: nullable changed
		{Name: "created_at", Type: "timestamp", Nullable: false}, // add
	}}}}

	ops := Diff(desired, current)
	if len(ops) != 3 {
		t.Fatalf("want 3 ops, got %d: %s", len(ops), opsString(ops))
	}
	if _, ok := ops[0].(OpAddColumn); !ok {
		t.Errorf("ops[0]: want OpAddColumn, got %T", ops[0])
	}
	if _, ok := ops[1].(OpAlterColumn); !ok {
		t.Errorf("ops[1]: want OpAlterColumn, got %T", ops[1])
	}
	if _, ok := ops[2].(OpDropColumn); !ok {
		t.Errorf("ops[2]: want OpDropColumn, got %T", ops[2])
	}
}

// TestDiff_AlterColumn_AllAttributes asserts that all three column
// attributes (Type / Nullable / Default) trigger an alter, and that
// a single op captures all three deltas — we don't emit three
// separate ops.
func TestDiff_AlterColumn_AllAttributes(t *testing.T) {
	cur := Column{Name: "age", Type: "int", Nullable: true, Default: nil}
	des := Column{Name: "age", Type: "bigint", Nullable: false, Default: ptr("0")}

	ops := Diff(
		Schema{Tables: []Table{{Name: "users", Columns: []Column{cur}}}},
		Schema{Tables: []Table{{Name: "users", Columns: []Column{cur}}}},
	)
	if len(ops) != 0 {
		t.Fatalf("identical column should diff to no ops, got %s", opsString(ops))
	}

	ops = Diff(
		Schema{Tables: []Table{{Name: "users", Columns: []Column{des}}}},
		Schema{Tables: []Table{{Name: "users", Columns: []Column{cur}}}},
	)
	if len(ops) != 1 {
		t.Fatalf("want 1 op for full alter, got %d: %s", len(ops), opsString(ops))
	}
	alt, ok := ops[0].(OpAlterColumn)
	if !ok {
		t.Fatalf("want OpAlterColumn, got %T", ops[0])
	}
	// String() should mention all three deltas.
	s := alt.String()
	for _, kw := range []string{"type", "nullable", "default"} {
		if !strings.Contains(s, kw) {
			t.Errorf("OpAlterColumn.String() should mention %q, got %q", kw, s)
		}
	}
}

// TestDiff_IndexChange_IsDropAndCreate: shape changes (columns or
// unique flag) generate DROP + CREATE rather than a single ALTER —
// no engine supports altering an index in place.
func TestDiff_IndexChange_IsDropAndCreate(t *testing.T) {
	desired := Schema{Tables: []Table{{
		Name:    "users",
		Columns: []Column{{Name: "id", Type: "bigint"}},
		Indexes: []Index{{Name: "idx_users", Columns: []string{"id", "email"}, Unique: true}},
	}}}
	current := Schema{Tables: []Table{{
		Name:    "users",
		Columns: []Column{{Name: "id", Type: "bigint"}},
		Indexes: []Index{{Name: "idx_users", Columns: []string{"id"}, Unique: false}},
	}}}
	ops := Diff(desired, current)
	if len(ops) != 2 {
		t.Fatalf("want 2 ops (drop + create), got %d: %s", len(ops), opsString(ops))
	}
	if _, ok := ops[0].(OpDropIndex); !ok {
		t.Errorf("ops[0]: want OpDropIndex, got %T", ops[0])
	}
	if _, ok := ops[1].(OpCreateIndex); !ok {
		t.Errorf("ops[1]: want OpCreateIndex, got %T", ops[1])
	}
}

// TestDiff_FK_MatchByCompositeKeyWhenNameEmpty: the SQLite contract
// — when Name="" on both sides, match by (Columns, RefTable,
// RefColumns) tuple so the FK round-trips clean.
func TestDiff_FK_MatchByCompositeKeyWhenNameEmpty(t *testing.T) {
	fk := ForeignKey{
		Name: "", Columns: []string{"parent_id"},
		RefTable: "parents", RefColumns: []string{"id"},
		OnDelete: "CASCADE", OnUpdate: "NO ACTION",
	}
	s := Schema{Tables: []Table{{
		Name:        "children",
		Columns:     []Column{{Name: "parent_id", Type: "bigint"}},
		ForeignKeys: []ForeignKey{fk},
	}}}
	if ops := Diff(s, s); len(ops) != 0 {
		t.Fatalf("identical anonymous FK should diff to no ops, got %s", opsString(ops))
	}
}

// TestDiff_FK_RestrictEqualsNoAction: the MySQL/MariaDB catalog
// asymmetry (MariaDB stores SQL-default as RESTRICT, MySQL as
// NO ACTION) must not generate spurious DROP+ADD on every
// introspection round-trip — they're semantically equivalent.
func TestDiff_FK_RestrictEqualsNoAction(t *testing.T) {
	mariaFK := ForeignKey{
		Name: "fk_x", Columns: []string{"a"}, RefTable: "b", RefColumns: []string{"id"},
		OnDelete: "CASCADE", OnUpdate: "RESTRICT", // MariaDB default for unspecified
	}
	mysqlFK := ForeignKey{
		Name: "fk_x", Columns: []string{"a"}, RefTable: "b", RefColumns: []string{"id"},
		OnDelete: "CASCADE", OnUpdate: "NO ACTION", // MySQL default for unspecified
	}
	desired := Schema{Tables: []Table{{Name: "t", ForeignKeys: []ForeignKey{mysqlFK}}}}
	current := Schema{Tables: []Table{{Name: "t", ForeignKeys: []ForeignKey{mariaFK}}}}
	if ops := Diff(desired, current); len(ops) != 0 {
		t.Errorf("RESTRICT ≡ NO ACTION should diff to no ops, got %s", opsString(ops))
	}
}

// TestDiff_Checks_SkippedWhenEitherSideNil: the SQLite contract —
// Checks=nil means "not introspectable", not "no checks". Comparing
// against it must NOT emit DropCheck for every check on the other
// side (which would generate massive spurious drops on every plan
// on SQLite).
func TestDiff_Checks_SkippedWhenEitherSideNil(t *testing.T) {
	withChecks := Schema{Tables: []Table{{
		Name:   "t",
		Checks: []Check{{Name: "chk_a", Expression: "(a > 0)"}},
	}}}
	withoutChecks := Schema{Tables: []Table{{
		Name:   "t",
		Checks: nil, // SQLite contract
	}}}

	// Either direction: zero ops.
	if ops := Diff(withChecks, withoutChecks); len(ops) != 0 {
		t.Errorf("desired-has-checks + current-nil → want 0 ops, got %s", opsString(ops))
	}
	if ops := Diff(withoutChecks, withChecks); len(ops) != 0 {
		t.Errorf("desired-nil + current-has-checks → want 0 ops, got %s", opsString(ops))
	}
}

// TestDiff_Checks_EmptyVsNil: empty []Check{} ≠ nil. An empty slice
// means "introspected and found nothing", which is comparable; a
// nil slice means "not introspected" (SQLite). The test pins this
// distinction.
func TestDiff_Checks_EmptyVsNil(t *testing.T) {
	withChecks := Schema{Tables: []Table{{
		Name:   "t",
		Checks: []Check{{Name: "chk_a", Expression: "(a > 0)"}},
	}}}
	emptyChecks := Schema{Tables: []Table{{
		Name:   "t",
		Checks: []Check{}, // introspected, found none
	}}}
	// desired=has, current=empty → emit ADD
	ops := Diff(withChecks, emptyChecks)
	if len(ops) != 1 {
		t.Fatalf("want 1 op (add check), got %d: %s", len(ops), opsString(ops))
	}
	if _, ok := ops[0].(OpAddCheck); !ok {
		t.Errorf("want OpAddCheck, got %T", ops[0])
	}
	// inverse: desired=empty, current=has → emit DROP
	ops = Diff(emptyChecks, withChecks)
	if len(ops) != 1 {
		t.Fatalf("inverse: want 1 op (drop check), got %d: %s", len(ops), opsString(ops))
	}
	if _, ok := ops[0].(OpDropCheck); !ok {
		t.Errorf("want OpDropCheck, got %T", ops[0])
	}
}

// TestDiff_Checks_EmptyName_IsUndefined pins the documented
// limitation: two checks with Name="" collide in the byName map,
// and Diff treats them as a single entry. The catalog readers
// never produce this shape (every dialect that exposes CHECK
// constraints names them), so this is undefined behaviour at the
// Diff level — the test exists to lock the contract, not to bless
// the behaviour. If F3-3-checks-anon ever adds expression-based
// matching for anonymous checks, this test flips into a real
// assertion.
func TestDiff_Checks_EmptyName_IsUndefined(t *testing.T) {
	twoAnon := Schema{Tables: []Table{{
		Name: "t",
		Checks: []Check{
			{Name: "", Expression: "(a > 0)"},
			{Name: "", Expression: "(b > 0)"},
		},
	}}}
	// The two anonymous checks should collapse to one entry in
	// the byName map; comparing the schema against itself emits
	// no ops despite the duplicate-named rows because the map
	// collapse hides one of them entirely.
	if ops := Diff(twoAnon, twoAnon); len(ops) != 0 {
		t.Fatalf("Diff vs itself should be empty even with anon collision, got %s", opsString(ops))
	}
}

// TestDiff_OrderingAcrossMixedOps: a single table with adds + drops +
// alters of multiple categories. Verifies the documented order:
//
//	adds → alters → drop checks → drop FKs → drop indexes → drop cols
//	→ create indexes → add FKs → add checks
func TestDiff_OrderingAcrossMixedOps(t *testing.T) {
	current := Schema{Tables: []Table{{
		Name: "t",
		Columns: []Column{
			{Name: "id", Type: "bigint"},
			{Name: "legacy", Type: "text"},
			{Name: "type_change", Type: "int"},
		},
		Indexes:     []Index{{Name: "idx_legacy", Columns: []string{"legacy"}}},
		ForeignKeys: []ForeignKey{{Name: "fk_legacy", Columns: []string{"legacy"}, RefTable: "other", RefColumns: []string{"id"}}},
		Checks:      []Check{{Name: "chk_legacy", Expression: "(legacy IS NOT NULL)"}},
	}}}
	desired := Schema{Tables: []Table{{
		Name: "t",
		Columns: []Column{
			{Name: "id", Type: "bigint"},
			{Name: "type_change", Type: "bigint"}, // alter
			{Name: "new_col", Type: "text"},       // add
		},
		Indexes:     []Index{{Name: "idx_new", Columns: []string{"new_col"}}},
		ForeignKeys: []ForeignKey{{Name: "fk_new", Columns: []string{"new_col"}, RefTable: "ref", RefColumns: []string{"id"}}},
		Checks:      []Check{{Name: "chk_new", Expression: "(new_col IS NOT NULL)"}},
	}}}

	ops := Diff(desired, current)
	// Expected sequence by type:
	want := []string{
		"OpAddColumn",      // new_col
		"OpAlterColumn",    // type_change
		"OpDropCheck",      // chk_legacy
		"OpDropForeignKey", // fk_legacy
		"OpDropIndex",      // idx_legacy
		"OpDropColumn",     // legacy
		"OpCreateIndex",    // idx_new
		"OpAddForeignKey",  // fk_new
		"OpAddCheck",       // chk_new
	}
	if len(ops) != len(want) {
		t.Fatalf("want %d ops, got %d: %s", len(want), len(ops), opsString(ops))
	}
	for i, w := range want {
		got := fmt.Sprintf("%T", ops[i])
		// trim package qualifier (`quark.OpAddColumn` → `OpAddColumn`)
		if dot := strings.LastIndex(got, "."); dot >= 0 {
			got = got[dot+1:]
		}
		if got != w {
			t.Errorf("ops[%d]: want %s, got %s — %s", i, w, got, ops[i].String())
		}
	}
}

// TestDiff_StableOrdering: same input always produces same output.
// Pins that map iteration order isn't bleeding through.
func TestDiff_StableOrdering(t *testing.T) {
	current := Schema{Tables: []Table{
		{Name: "z", Columns: []Column{{Name: "id", Type: "bigint"}}},
		{Name: "a", Columns: []Column{{Name: "id", Type: "bigint"}}},
	}}
	desired := Schema{Tables: []Table{
		{Name: "z", Columns: []Column{{Name: "id", Type: "bigint"}, {Name: "extra1", Type: "text"}}},
		{Name: "a", Columns: []Column{{Name: "id", Type: "bigint"}, {Name: "extra2", Type: "text"}}},
		{Name: "m", Columns: []Column{{Name: "id", Type: "bigint"}}}, // new
	}}

	var prev string
	for i := 0; i < 10; i++ {
		ops := Diff(desired, current)
		got := opsString(ops)
		if i > 0 && got != prev {
			t.Fatalf("run %d differs from run 0:\nprev: %s\ngot:  %s", i, prev, got)
		}
		prev = got
	}
}

// opsString renders an op slice as "TypeA: msgA | TypeB: msgB" so
// failure messages are readable.
func opsString(ops []Operation) string {
	if len(ops) == 0 {
		return "<empty>"
	}
	parts := make([]string, len(ops))
	for i, op := range ops {
		t := fmt.Sprintf("%T", op)
		if dot := strings.LastIndex(t, "."); dot >= 0 {
			t = t[dot+1:]
		}
		parts[i] = fmt.Sprintf("%s: %s", t, op.String())
	}
	return strings.Join(parts, " | ")
}
