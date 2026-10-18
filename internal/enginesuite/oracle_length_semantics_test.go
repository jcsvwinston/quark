// Copyright 2026 jcsvwinston
// SPDX-License-Identifier: Apache-2.0

package enginesuite

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/jcsvwinston/quark"
)

// testOracleLengthSemantics proves, on the Oracle lane, that a length means
// characters (DEP-2026-010, QK-50): the introspection reads CHAR_USED beside
// CHAR_LENGTH, so a byte column — created before 2.0 under Oracle's default
// NLS_LENGTH_SEMANTICS, or by hand — is reported as VARCHAR2(n BYTE), a
// hand-written bare VARCHAR2(n) in a desired Schema matches it and does not
// re-plan, and the VARCHAR2(n CHAR) the plan renders for a sized string
// differs from it: Diff plans one MODIFY, Oracle runs it in place, the
// column then holds n characters of any width, and the loop converges.
func testOracleLengthSemantics(ctx context.Context, t *testing.T, client *quark.Client) {
	if client.Dialect().Name() != "oracle" {
		t.Skip("length semantics are Oracle's: the other five engines count characters")
	}
	const table = "ls_rows"
	dropTable(client, table)
	t.Cleanup(func() { dropTable(client, table) })
	exec := func(stmt string, args ...any) error {
		_, err := client.Raw().ExecContext(ctx, stmt, args...)
		return err
	}
	tb, id, name, code, tag := q(client, table), q(client, "id"), q(client, "name"), q(client, "code"), q(client, "tag")
	if err := exec(fmt.Sprintf(`CREATE TABLE %s (%s NUMBER(19) NOT NULL, %s VARCHAR2(20 BYTE), %s VARCHAR2(8 CHAR), %s NCHAR(4), CONSTRAINT %s PRIMARY KEY (%s))`,
		tb, id, name, code, tag, q(client, "pk_ls_rows"), id)); err != nil {
		t.Fatalf("create the table by hand: %v", err)
	}

	columnTypes := func() map[string]string {
		t.Helper()
		live, err := client.IntrospectSchema(ctx)
		if err != nil {
			t.Fatalf("introspect: %v", err)
		}
		for _, tbl := range live.Tables {
			if !strings.EqualFold(tbl.Name, table) {
				continue
			}
			types := map[string]string{}
			for _, c := range tbl.Columns {
				types[c.Name] = c.Type
			}
			return types
		}
		t.Fatalf("introspect: %s is not in the schema", table)
		return nil
	}
	types := columnTypes()
	for col, want := range map[string]string{"name": "VARCHAR2(20 BYTE)", "code": "VARCHAR2(8 CHAR)", "tag": "NCHAR(4)"} {
		if types[col] != want {
			t.Errorf("introspected %s as %q, want %q (CHAR_USED beside CHAR_LENGTH)", col, types[col], want)
		}
	}

	// Twenty two-byte characters do not fit the byte column.
	wide := strings.Repeat("ñ", 20)
	insert := fmt.Sprintf("INSERT INTO %s (%s, %s) VALUES (%s, %s)", tb, id, name, client.Dialect().Placeholder(1), client.Dialect().Placeholder(2))
	if err := exec(insert, 1, wide); err == nil || !strings.Contains(err.Error(), "ORA-12899") {
		t.Fatalf("a byte column took 20 two-byte characters: %v, want ORA-12899", err)
	}

	desired := func(nameType string) quark.Schema {
		return quark.Schema{Tables: []quark.Table{{
			Name: table,
			Columns: []quark.Column{
				{Name: "id", Type: "NUMBER(19)", PrimaryKey: true},
				{Name: "name", Type: nameType, Nullable: true},
				{Name: "code", Type: "VARCHAR2(8 CHAR)", Nullable: true},
				{Name: "tag", Type: "NCHAR(4)", Nullable: true},
			},
		}}}
	}
	current, err := client.IntrospectSchema(ctx)
	if err != nil {
		t.Fatalf("introspect: %v", err)
	}
	// A bare length is Oracle's default, BYTE: the hand-written schema
	// matches the column it created and nothing is re-planned.
	if ops := quark.Diff(desired("VARCHAR2(20)"), current); len(ops) != 0 {
		t.Errorf("a bare VARCHAR2(20) re-plans the byte column it created: %s", ops)
	}
	// The migrator's form is a type change, one MODIFY.
	ops := quark.Diff(desired("VARCHAR2(20 CHAR)"), current)
	if len(ops) != 1 {
		t.Fatalf("VARCHAR2(20 CHAR) over a byte column plans %d ops, want one MODIFY: %s", len(ops), ops)
	}
	alter, ok := ops[0].(quark.OpAlterColumn)
	if !ok || alter.New.Name != "name" || alter.New.Type != "VARCHAR2(20 CHAR)" {
		t.Fatalf("want an OpAlterColumn of name to VARCHAR2(20 CHAR), got %s", ops[0])
	}
	if err := client.ApplyPlan(ctx, quark.Plan{Ops: ops}); err != nil {
		t.Fatalf("apply the MODIFY: %v", err)
	}
	if got := columnTypes()["name"]; got != "VARCHAR2(20 CHAR)" {
		t.Errorf("after the MODIFY the column is %q, want VARCHAR2(20 CHAR)", got)
	}
	if err := exec(insert, 1, wide); err != nil {
		t.Errorf("after the MODIFY the column refuses 20 two-byte characters: %v", err)
	}
	live, err := client.IntrospectSchema(ctx)
	if err != nil {
		t.Fatalf("introspect after apply: %v", err)
	}
	if residual := quark.Diff(desired("VARCHAR2(20 CHAR)"), live); len(residual) != 0 {
		t.Errorf("the loop does not converge after the MODIFY: %s", residual)
	}
}
