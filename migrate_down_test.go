// Copyright 2026 jcsvwinston
// SPDX-License-Identifier: Apache-2.0

package quark

import (
	"errors"
	"strings"
	"testing"
)

func TestPlanDownInvertsAndReverses(t *testing.T) {
	// Reverse order matters as much as the inversion: dropping the table
	// before its index would refer to something that no longer exists.
	p := Plan{Ops: []Operation{
		OpCreateTable{Table: Table{Name: "users"}},
		OpCreateIndex{Table: "users", Index: Index{Name: "idx_users_email"}},
	}}
	down, err := p.Down()
	if err != nil {
		t.Fatalf("Down: %v", err)
	}
	if len(down.Ops) != 2 {
		t.Fatalf("got %d ops, want 2", len(down.Ops))
	}
	if _, ok := down.Ops[0].(OpDropIndex); !ok {
		t.Errorf("first op is %T, want OpDropIndex — the index must go before the table", down.Ops[0])
	}
	if _, ok := down.Ops[1].(OpDropTable); !ok {
		t.Errorf("second op is %T, want OpDropTable", down.Ops[1])
	}
}

func TestPlanDownSwapsAlterColumn(t *testing.T) {
	p := Plan{Ops: []Operation{OpAlterColumn{
		Table: "users",
		Old:   Column{Name: "age", Type: "INTEGER"},
		New:   Column{Name: "age", Type: "BIGINT"},
	}}}
	down, err := p.Down()
	if err != nil {
		t.Fatalf("Down: %v", err)
	}
	alter, ok := down.Ops[0].(OpAlterColumn)
	if !ok {
		t.Fatalf("got %T, want OpAlterColumn", down.Ops[0])
	}
	if alter.Old.Type != "BIGINT" || alter.New.Type != "INTEGER" {
		t.Errorf("got %s→%s, want BIGINT→INTEGER", alter.Old.Type, alter.New.Type)
	}
}

func TestPlanDownRefusesIrreversibleOps(t *testing.T) {
	// Each of these records a NAME and not the shape needed to rebuild it.
	// Erroring beats a rollback that reports success and leaves the schema
	// subtly different.
	for _, tc := range []struct {
		name string
		op   Operation
		want string
	}{
		{"drop table", OpDropTable{Table: "users"}, "shape"},
		{"drop column", OpDropColumn{Table: "users", Column: "age"}, "type"},
		{"drop index", OpDropIndex{Table: "users", Index: "idx"}, "columns"},
		{"drop fk", OpDropForeignKey{Table: "posts", ForeignKey: "fk"}, "referenced"},
		{"drop check", OpDropCheck{Table: "users", Check: "ck"}, "expression"},
	} {
		_, err := Plan{Ops: []Operation{tc.op}}.Down()
		if !errors.Is(err, ErrIrreversibleOperation) {
			t.Errorf("%s: err = %v, want ErrIrreversibleOperation", tc.name, err)
			continue
		}
		// The message has to name WHAT is missing, or the caller cannot tell
		// whether writing the down by hand is feasible.
		if !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%s: err = %q, want it to mention %q", tc.name, err, tc.want)
		}
	}
}

func TestPlanDownOfEmptyPlanIsEmpty(t *testing.T) {
	down, err := Plan{}.Down()
	if err != nil {
		t.Fatalf("Down of an empty plan: %v", err)
	}
	if !down.IsEmpty() {
		t.Errorf("got %d ops, want none", len(down.Ops))
	}
}
