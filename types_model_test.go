// Copyright 2026 jcsvwinston
// SPDX-License-Identifier: Apache-2.0

package quark

import (
	"context"
	"database/sql/driver"
	"fmt"
	"reflect"
	"strings"
	"testing"

	"github.com/jcsvwinston/quark/internal/migrate"
)

// A8 S5 (TYP-01, TYP-02, TYP-03; QK-29): a UUID-shaped value gets the
// engine's uuid type, a mapped key keeps its key, and a model declares the
// CHECK of an enum column.

type s5UUID [16]byte

func (u s5UUID) Value() (driver.Value, error) {
	return fmt.Sprintf("%x-%x-%x-%x-%x", u[0:4], u[4:6], u[6:8], u[8:10], u[10:16]), nil
}

func (u *s5UUID) Scan(src any) error {
	var s string
	switch v := src.(type) {
	case string:
		s = v
	case []byte:
		s = string(v)
	default:
		return fmt.Errorf("s5UUID: cannot scan %T", src)
	}
	s = strings.ReplaceAll(s, "-", "")
	if len(s) != 32 {
		return fmt.Errorf("s5UUID: bad length %d", len(s))
	}
	for i := 0; i < 16; i++ {
		var b byte
		if _, err := fmt.Sscanf(s[2*i:2*i+2], "%02x", &b); err != nil {
			return err
		}
		u[i] = b
	}
	return nil
}

func TestUUIDShapedTypePerDialect(t *testing.T) {
	typ := reflect.TypeOf(s5UUID{})
	if !migrate.IsUUIDShaped(typ) || !migrate.IsUUIDShaped(reflect.TypeOf(&s5UUID{})) {
		t.Fatal("a 16-byte array is UUID-shaped, pointer or not")
	}
	if migrate.IsUUIDShaped(reflect.TypeOf([17]byte{})) || migrate.IsUUIDShaped(reflect.TypeOf([]byte{})) {
		t.Fatal("17 bytes or a slice is not")
	}
	want := map[string]string{"postgres": "UUID", "sqlite": "UUID", "mysql": "CHAR(36)", "mariadb": "CHAR(36)", "mssql": "NCHAR(36)", "oracle": "VARCHAR2(36)"}
	for d, w := range want {
		if got := migrate.SQLTypeWithOpts(d, typ, migrate.TypeOptions{}); got != w {
			t.Errorf("%s: %q, want %q", d, got, w)
		}
		if got := migrate.SQLTypeWithOpts(d, typ, migrate.TypeOptions{IsPK: true}); got != w+" PRIMARY KEY" {
			t.Errorf("%s as key: %q, want %q", d, got, w+" PRIMARY KEY")
		}
	}
}

type s5Doc struct {
	ID  int64  `db:"id" pk:"true"`
	Ref s5UUID `db:"ref"`
}

func (s5Doc) TableName() string { return "s5_docs" }

func TestUUIDShapedValueRoundTrips(t *testing.T) {
	c := constraintsClient(t, "s5_uuid")
	ctx := context.Background()
	if err := c.Migrate(ctx, &s5Doc{}); err != nil {
		t.Fatal(err)
	}
	row := &s5Doc{Ref: s5UUID{1, 2, 3, 0xab}}
	if err := For[s5Doc](ctx, c).Create(row); err != nil {
		t.Fatal(err)
	}
	got, err := For[s5Doc](ctx, c).Find(row.ID)
	if err != nil || got.Ref != row.Ref {
		t.Fatalf("round trip: %v %x", err, got.Ref)
	}
	if col := columnNamed(t, tableNamed(t, c, "s5_docs"), "ref"); !strings.EqualFold(col.Type, "UUID") {
		t.Fatalf("column type %q", col.Type)
	}
}

// QK-29: a mapped key column keeps its key.
type s5Key string
type s5Keyed struct {
	ID   s5Key  `db:"id" pk:"true"`
	Name string `db:"name"`
}

func (s5Keyed) TableName() string { return "s5_keyed" }

func TestMappedPrimaryKeyKeepsItsKey(t *testing.T) {
	RegisterTypeMapper(reflect.TypeOf(s5Key("")), func(string, TypeOptions) string { return "CHAR(26)" })
	c := constraintsClient(t, "s5_mapped")
	ctx := context.Background()
	if err := c.Migrate(ctx, &s5Keyed{}); err != nil {
		t.Fatal(err)
	}
	if col := columnNamed(t, tableNamed(t, c, "s5_keyed"), "id"); !col.PrimaryKey || !strings.EqualFold(col.Type, "CHAR(26)") {
		t.Fatalf("mapped key column: %+v", col)
	}
	if err := For[s5Keyed](ctx, c).Create(&s5Keyed{ID: "01HZY", Name: "first"}); err != nil {
		t.Fatal(err)
	}
	if err := For[s5Keyed](ctx, c).Create(&s5Keyed{ID: "01HZY", Name: "second"}); err == nil {
		t.Fatal("the same id landed twice: the key is gone")
	}
	if got := migrate.SQLTypeWithOpts("sqlite", reflect.TypeOf(s5Key("")), migrate.TypeOptions{IsPK: true}); strings.Count(strings.ToUpper(got), "PRIMARY KEY") != 1 {
		t.Fatalf("suffix: %q", got)
	}
}

// TYP-03: both grammars, and the check enforced.
type s5Enum struct {
	ID     int64  `db:"id" pk:"true"`
	Status string `db:"status" quark:"check=status IN ('draft','live'),not_null"`
	Kind   string `db:"kind,enum=a|b'c"`
}

func (s5Enum) TableName() string { return "s5_enum" }

func TestModelDeclaredChecks(t *testing.T) {
	meta := GetModelMeta[s5Enum]()
	if meta.TagError != nil {
		t.Fatal(meta.TagError)
	}
	chks := modelChecks(meta)
	if len(chks) != 2 || chks[0].Name != "ck_s5_enum_status" || chks[0].Expression != "status IN ('draft','live')" ||
		chks[1].Expression != "kind IN ('a', 'b''c')" {
		t.Fatalf("modelChecks = %+v", chks)
	}
	for _, f := range meta.Fields {
		if f.Column == "status" && !f.NotNull {
			t.Fatal("the token after check=… was lost: the comma inside the expression split the tag")
		}
	}
	c := constraintsClient(t, "s5_enum")
	ctx := context.Background()
	if err := c.Migrate(ctx, &s5Enum{}); err != nil {
		t.Fatal(err)
	}
	if err := For[s5Enum](ctx, c).Create(&s5Enum{Status: "draft", Kind: "a"}); err != nil {
		t.Fatalf("a valid row: %v", err)
	}
	if err := For[s5Enum](ctx, c).Create(&s5Enum{Status: "gone", Kind: "a"}); err == nil {
		t.Fatal("the check= constraint is not enforced")
	}
	if err := For[s5Enum](ctx, c).Create(&s5Enum{Status: "live", Kind: "zzz"}); err == nil {
		t.Fatal("the enum= constraint is not enforced")
	}
	plan, err := c.PlanMigration(ctx, &s5Enum{})
	if err != nil || !plan.IsEmpty() {
		t.Fatalf("plan after migrate: %v %s", err, plan)
	}
	old := columnNamed(t, tableNamed(t, c, "s5_enum"), "kind")
	next := old
	next.Nullable = false
	if err := c.ApplyPlan(ctx, Plan{Ops: []Operation{OpAlterColumn{Table: "s5_enum", Old: old, New: next}}}); err != nil {
		t.Fatalf("rebuild with model checks: %v", err)
	}
	if err := For[s5Enum](ctx, c).Create(&s5Enum{Status: "gone", Kind: "a"}); err == nil {
		t.Fatal("the check did not survive the rebuild")
	}
}

type s5BadEnum struct {
	ID   int64  `db:"id" pk:"true"`
	Kind string `db:"kind,enum="`
}

type s5BadCheck struct {
	ID   int64  `db:"id" pk:"true"`
	Kind string `db:"kind" quark:"check="`
}

func TestEmptyCheckAndEnumAreTagErrors(t *testing.T) {
	if meta := GetModelMeta[s5BadEnum](); meta.TagError == nil || !strings.Contains(meta.TagError.Error(), "enum") {
		t.Fatalf("enum= with no values accepted: %v", meta.TagError)
	}
	if meta := GetModelMeta[s5BadCheck](); meta.TagError == nil || !strings.Contains(meta.TagError.Error(), "check=") {
		t.Fatalf("check= with no expression accepted: %v", meta.TagError)
	}
}
