// Copyright 2026 jcsvwinston
// SPDX-License-Identifier: Apache-2.0

package quark

import (
	"context"
	"reflect"
	"strings"
	"testing"

	"github.com/jcsvwinston/quark/internal/migrate"
)

// A8 S7 (TYP-11, QK-28): precision/scale refines a float into a fixed-point
// decimal and leaves every other kind alone; the diff reads the three
// spellings of that family as one.

type s7Row struct {
	ID    int64             `db:"id" pk:"true"`
	Price float64           `db:"price,precision=10,scale=2"`
	Rate  Nullable[float64] `db:"rate,precision=5,scale=4"`
	Whole float32           `db:"whole,precision=8"`
	Name  string            `db:"name,precision=10,scale=2"`
	Flag  bool              `db:"flag,precision=3"`
}

func (s7Row) TableName() string { return "s7_rows" }

func TestPrecisionScaleRefinesFloatsOnly(t *testing.T) {
	for _, tc := range []struct {
		d, price, rate, whole, name string
	}{
		{"sqlite", "DECIMAL(10,2)", "DECIMAL(5,4)", "DECIMAL(8)", "TEXT"},
		{"postgres", "DECIMAL(10,2)", "DECIMAL(5,4)", "DECIMAL(8)", "TEXT"},
		{"mysql", "DECIMAL(10,2)", "DECIMAL(5,4)", "DECIMAL(8)", "VARCHAR(255)"},
		{"mssql", "DECIMAL(10,2)", "DECIMAL(5,4)", "DECIMAL(8)", "NVARCHAR(255)"},
		{"oracle", "NUMBER(10,2)", "NUMBER(5,4)", "NUMBER(8)", "VARCHAR2(255)"},
	} {
		typ := reflect.TypeOf(s7Row{})
		get := func(field string, opts migrate.TypeOptions) string {
			f, _ := typ.FieldByName(field)
			return migrate.SQLTypeWithOpts(tc.d, f.Type, opts)
		}
		if got := get("Price", migrate.TypeOptions{Precision: 10, Scale: 2}); got != tc.price {
			t.Errorf("%s float64: %q, want %q", tc.d, got, tc.price)
		}
		if got := get("Rate", migrate.TypeOptions{Precision: 5, Scale: 4}); got != tc.rate {
			t.Errorf("%s Nullable[float64]: %q, want %q", tc.d, got, tc.rate)
		}
		if got := get("Whole", migrate.TypeOptions{Precision: 8}); got != tc.whole {
			t.Errorf("%s float32 precision only: %q, want %q", tc.d, got, tc.whole)
		}
		if got := get("Name", migrate.TypeOptions{Precision: 10, Scale: 2}); got != tc.name {
			t.Errorf("%s string with the hint: %q, want the base type %q untouched", tc.d, got, tc.name)
		}
		// A bool is NUMBER(1) on Oracle by itself; the hint must leave it
		// exactly as it is without the hint.
		if got, base := get("Flag", migrate.TypeOptions{Precision: 3}), get("Flag", migrate.TypeOptions{}); got != base {
			t.Errorf("%s bool with the hint became %q, want %q", tc.d, got, base)
		}
	}
}

// The hint on a non-float is a warning the author sees, not a silent no-op.
func TestPrecisionOnNonDecimalWarns(t *testing.T) {
	meta := GetModelMeta[s7Row]()
	if meta.TagError != nil {
		t.Fatal(meta.TagError)
	}
	var warned []string
	for _, w := range meta.TagWarnings {
		if strings.Contains(w, "precision/scale") {
			warned = append(warned, w)
		}
	}
	if len(warned) != 2 || !strings.Contains(warned[0], "Name") || !strings.Contains(warned[1], "Flag") {
		t.Fatalf("warnings: %v", meta.TagWarnings)
	}
}

// The DDL: the decimal sized, the others their base type, on SQLite.
func TestPrecisionScaleInMigrate(t *testing.T) {
	c := constraintsClient(t, "s7_migrate")
	ctx := context.Background()
	if err := c.Migrate(ctx, &s7Row{}); err != nil {
		t.Fatal(err)
	}
	tb := tableNamed(t, c, "s7_rows")
	if col := columnNamed(t, tb, "price"); !strings.EqualFold(col.Type, "DECIMAL(10,2)") {
		t.Fatalf("price: %q", col.Type)
	}
	if col := columnNamed(t, tb, "name"); !strings.EqualFold(col.Type, "TEXT") {
		t.Fatalf("name was retyped: %q", col.Type)
	}
	if col := columnNamed(t, tb, "flag"); strings.HasPrefix(strings.ToUpper(col.Type), "DECIMAL") {
		t.Fatalf("flag was retyped: %q", col.Type)
	}
	if plan, err := c.PlanMigration(ctx, &s7Row{}); err != nil || !plan.IsEmpty() {
		t.Fatalf("plan after migrate: %v %s", err, plan)
	}
}

// The three spellings of the family are one to the diff; Oracle's bare
// NUMBER still matches its sized forms.
func TestDecimalFamilySpellings(t *testing.T) {
	for _, pair := range [][2]string{
		{"DECIMAL(10,2)", "numeric(10,2)"},
		{"DECIMAL(10,2)", "NUMBER(10,2)"},
		{"DECIMAL(8)", "numeric(8)"},
		{"NUMBER", "NUMBER(10,2)"},
		{"NUMBER", "NUMBER(19)"},
	} {
		if !typesEqual(pair[0], pair[1]) {
			t.Errorf("typesEqual(%q, %q) = false", pair[0], pair[1])
		}
	}
	if typesEqual("NUMBER(19)", "DECIMAL(10,2)") {
		t.Error("an integer NUMBER(19) is not a decimal(10,2)")
	}
}
