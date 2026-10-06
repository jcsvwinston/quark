// Copyright 2026 jcsvwinston
// SPDX-License-Identifier: Apache-2.0

package quark

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/jcsvwinston/quark/quarkdriver"
)

// A foreign key's ON DELETE and ON UPDATE are written as the dialect's engine
// takes them (quarkdriver.ReferentialActioner, QK-49). Oracle refused ON
// UPDATE and ON DELETE NO ACTION, and Quark passed both through, so the DDL
// failed on the engine; now NO ACTION is left out where the engine has no
// clause for it, and an action the engine lacks is refused, named, before
// any statement is sent.

func TestReferentialClauses(t *testing.T) {
	for _, tc := range []struct {
		name               string
		d                  Dialect
		onDelete, onUpdate string
		want               string
		refused            []string // each must appear in the error
	}{
		// Without a ReferentialActioner every action is written as given.
		{"postgres writes all", PostgreSQL(), "SET DEFAULT", "RESTRICT", " ON DELETE SET DEFAULT ON UPDATE RESTRICT", nil},
		{"postgres writes as given", PostgreSQL(), "cascade", "", " ON DELETE cascade", nil},
		{"mysql writes all", MySQL(), "SET NULL", "CASCADE", " ON DELETE SET NULL ON UPDATE CASCADE", nil},
		{"sqlite writes all", SQLite(), "NO ACTION", "SET DEFAULT", " ON DELETE NO ACTION ON UPDATE SET DEFAULT", nil},
		{"empty writes nothing", Oracle(), "", "", "", nil},

		// SQL Server has no RESTRICT.
		{"mssql writes its four", MSSQL(), "SET DEFAULT", "NO ACTION", " ON DELETE SET DEFAULT ON UPDATE NO ACTION", nil},
		{"mssql cascade both", MSSQL(), "CASCADE", "CASCADE", " ON DELETE CASCADE ON UPDATE CASCADE", nil},
		{"mssql refuses restrict", MSSQL(), "RESTRICT", "", "", []string{"ON DELETE RESTRICT"}},
		{"mssql refuses restrict on update", MSSQL(), "CASCADE", "restrict", "", []string{"ON UPDATE RESTRICT"}},

		// Oracle: NO ACTION is what it does without a clause; ON DELETE
		// CASCADE and SET NULL are its only clauses.
		{"oracle leaves no action out", Oracle(), "NO ACTION", "NO ACTION", "", nil},
		{"oracle normalises before asking", Oracle(), " no  action ", "No Action", "", nil},
		{"oracle cascade", Oracle(), "CASCADE", "NO ACTION", " ON DELETE CASCADE", nil},
		{"oracle set null", Oracle(), "SET NULL", "", " ON DELETE SET NULL", nil},
		{"oracle refuses on update cascade", Oracle(), "CASCADE", "CASCADE", "", []string{"ON UPDATE CASCADE"}},
		{"oracle refuses restrict", Oracle(), "RESTRICT", "", "", []string{"ON DELETE RESTRICT"}},
		{"oracle refuses set default", Oracle(), "SET DEFAULT", "", "", []string{"ON DELETE SET DEFAULT"}},
		{"oracle names both", Oracle(), "RESTRICT", "SET NULL", "", []string{"ON DELETE RESTRICT", "ON UPDATE SET NULL"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := referentialClauses(tc.d, "fk_x", tc.onDelete, tc.onUpdate)
			if len(tc.refused) == 0 {
				if err != nil {
					t.Fatalf("%s: %q / %q refused: %v", tc.d.Name(), tc.onDelete, tc.onUpdate, err)
				}
				if got != tc.want {
					t.Errorf("%s: %q / %q wrote %q, want %q", tc.d.Name(), tc.onDelete, tc.onUpdate, got, tc.want)
				}
				return
			}
			if !errors.Is(err, ErrUnsupportedFeature) {
				t.Fatalf("%s: %q / %q: want ErrUnsupportedFeature, got %q, %v", tc.d.Name(), tc.onDelete, tc.onUpdate, got, err)
			}
			for _, r := range append(tc.refused, "fk_x", tc.d.Name()) {
				if !strings.Contains(err.Error(), r) {
					t.Errorf("%s: the error does not name %q: %v", tc.d.Name(), r, err)
				}
			}
		})
	}
}

// recordSchemaErr is recordSchema for a call that is meant to fail: the
// statements sent, and the error.
func recordSchemaErr(t *testing.T, d Dialect, f func(ctx context.Context, c *Client) error) ([]string, error) {
	t.Helper()
	c := clientOf(t, d)
	schemaRecMu.Lock()
	schemaRecLog = nil
	schemaRecMu.Unlock()
	err := f(context.Background(), c)
	schemaRecMu.Lock()
	defer schemaRecMu.Unlock()
	return append([]string(nil), schemaRecLog...), err
}

func TestApplyPlanRefusesAnActionTheEngineLacksBeforeTheFirstOp(t *testing.T) {
	added := OpAddColumn{Table: "t", Column: Column{Name: "x", Type: "INTEGER", Nullable: true}}
	fk := ForeignKey{Name: "fk", Columns: []string{"c"}, RefTable: "p", RefColumns: []string{"id"}}
	onUpdate := fk
	onUpdate.OnUpdate = "CASCADE"
	restrict := fk
	restrict.OnDelete = "RESTRICT"
	for _, tc := range []struct {
		name string
		d    Dialect
		op   Operation
		want string
	}{
		// Oracle commits each statement: the column of the first op would
		// stay if the refusal came when the second op ran.
		{"oracle add fk", Oracle(), OpAddForeignKey{Table: "t", ForeignKey: onUpdate}, "ON UPDATE CASCADE"},
		{"oracle create table", Oracle(), OpCreateTable{Table: Table{Name: "c", Columns: []Column{{Name: "c", Type: "NUMBER(19)"}}, ForeignKeys: []ForeignKey{restrict}}}, "ON DELETE RESTRICT"},
		{"mssql add fk", MSSQL(), OpAddForeignKey{Table: "t", ForeignKey: restrict}, "ON DELETE RESTRICT"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			sent, err := recordSchemaErr(t, tc.d, func(ctx context.Context, c *Client) error {
				return c.ApplyPlan(ctx, Plan{Ops: []Operation{added, tc.op}})
			})
			if !errors.Is(err, ErrUnsupportedFeature) || !strings.Contains(err.Error(), tc.want) || !strings.Contains(err.Error(), "op 1") {
				t.Fatalf("ApplyPlan: want ErrUnsupportedFeature naming op 1 and %s, got %v", tc.want, err)
			}
			if len(sent) != 0 {
				t.Errorf("ApplyPlan sent %d statements before refusing the plan:\n  %s", len(sent), strings.Join(sent, "\n  "))
			}
		})
	}
}

func TestAddForeignKeyAsTheEngineTakesIt(t *testing.T) {
	sent, err := recordSchemaErr(t, Oracle(), func(ctx context.Context, c *Client) error {
		return c.AddForeignKey(ctx, "orders", "fk_orders_user", []string{"user_id"}, "users", []string{"id"}, "RESTRICT", "")
	})
	if !errors.Is(err, ErrUnsupportedFeature) || !strings.Contains(err.Error(), "ON DELETE RESTRICT") {
		t.Fatalf("AddForeignKey ON DELETE RESTRICT on Oracle: want ErrUnsupportedFeature naming it, got %v", err)
	}
	if len(sent) != 0 {
		t.Errorf("AddForeignKey sent %v before refusing", sent)
	}

	sent, err = recordSchemaErr(t, Oracle(), func(ctx context.Context, c *Client) error {
		return c.AddForeignKey(ctx, "orders", "fk_orders_user", []string{"user_id"}, "users", []string{"id"}, "NO ACTION", "NO ACTION")
	})
	if err != nil {
		t.Fatalf("AddForeignKey NO ACTION / NO ACTION on Oracle: %v", err)
	}
	want := `ALTER TABLE "ORDERS" ADD CONSTRAINT "FK_ORDERS_USER" FOREIGN KEY ("USER_ID") REFERENCES "USERS" ("ID")`
	if len(sent) != 1 || sent[0] != want {
		t.Errorf("AddForeignKey NO ACTION / NO ACTION on Oracle sent %q, want %q", sent, want)
	}

	// A plan's CREATE TABLE writes its foreign keys through the same answer.
	// The op is applied on its own: the recording driver has no checkpoint
	// table for Oracle's resumable path to read.
	sent, err = recordSchemaErr(t, Oracle(), func(ctx context.Context, c *Client) error {
		return c.applyOne(ctx, c.schemaExec(c.db), OpCreateTable{Table: Table{
			Name:        "c",
			Columns:     []Column{{Name: "p", Type: "NUMBER(19)", Nullable: true}},
			ForeignKeys: []ForeignKey{{Name: "fk_c_p", Columns: []string{"p"}, RefTable: "p", RefColumns: []string{"id"}, OnDelete: "SET NULL", OnUpdate: "NO ACTION"}},
		}})
	})
	if err != nil {
		t.Fatalf("OpCreateTable ON DELETE SET NULL ON UPDATE NO ACTION on Oracle: %v", err)
	}
	created := ""
	for _, s := range sent {
		if strings.HasPrefix(s, `CREATE TABLE "C"`) {
			created = s
		}
	}
	if !strings.Contains(created, `REFERENCES "P" ("ID") ON DELETE SET NULL )`) {
		t.Errorf("the CREATE TABLE does not end its foreign key with ON DELETE SET NULL alone: %q (all: %q)", created, sent)
	}
}

// TestBuiltinsAnswerReferentialActions pins which built-ins implement
// ReferentialActioner: a wrapper that renames one has to forward it.
func TestBuiltinsAnswerReferentialActions(t *testing.T) {
	for _, tc := range []struct {
		d    Dialect
		asks bool
	}{
		{PostgreSQL(), false}, {MySQL(), false}, {MariaDB(), false},
		{SQLite(), false}, {MSSQL(), true}, {Oracle(), true},
	} {
		if _, ok := tc.d.(quarkdriver.ReferentialActioner); ok != tc.asks {
			t.Errorf("%s: ReferentialActioner %v, want %v", tc.d.Name(), ok, tc.asks)
		}
	}
}
