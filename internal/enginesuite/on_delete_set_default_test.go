// Copyright 2026 jcsvwinston
// SPDX-License-Identifier: Apache-2.0

package enginesuite

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/jcsvwinston/quark"
)

// testOnDeleteSetDefault pins what the engine this lane runs does with a
// foreign key that declares ON DELETE SET DEFAULT (QK-60): the action means
// the same on the six. PostgreSQL, SQLite and SQL Server run it: deleting
// the referenced row writes the column's default into the rows that
// referenced it. Oracle has no such clause, and MySQL and MariaDB accept it
// and run NO ACTION instead (InnoDB refuses the delete, and MariaDB's
// catalog records RESTRICT), so on those three the dialect refuses it before
// anything is sent (ErrUnsupportedFeature, as TestReferentialClauses pins in
// the root module; DEP-2026-007).
func testOnDeleteSetDefault(ctx context.Context, t *testing.T, client *quark.Client) {
	engine := client.Dialect().Name()
	var runsIt, refuses bool
	switch engine {
	case "postgres", "sqlite", "mssql":
		runsIt = true
	case "mysql", "mariadb", "oracle":
		refuses = true
	default:
		t.Skipf("no behaviour pinned for ON DELETE SET DEFAULT on dialect %q", engine)
	}
	dropTable(client, "sd_child")
	dropTable(client, "sd_parent")
	t.Cleanup(func() { dropTable(client, "sd_child"); dropTable(client, "sd_parent") })

	// The parent key is text, so no engine makes it an identity and the
	// rows can carry the keys the test names.
	intType, textType := "INTEGER", "TEXT"
	switch engine {
	case "postgres":
		intType, textType = "bigint", "varchar(16)"
	case "mysql", "mariadb":
		intType, textType = "BIGINT", "VARCHAR(16)"
	case "mssql":
		intType, textType = "BIGINT", "NVARCHAR(16)"
	case "oracle":
		intType, textType = "NUMBER(19)", "VARCHAR2(16 CHAR)"
	}
	keep := "'keep'"
	desired := quark.Schema{Tables: []quark.Table{
		{
			Name:    "sd_parent",
			Columns: []quark.Column{{Name: "code", Type: textType, PrimaryKey: true}},
		},
		{
			Name: "sd_child",
			Columns: []quark.Column{
				{Name: "id", Type: intType, PrimaryKey: true},
				{Name: "parent_code", Type: textType, Nullable: true, Default: &keep},
			},
			ForeignKeys: []quark.ForeignKey{{
				Name: "fk_sd_child_parent", Columns: []string{"parent_code"},
				RefTable: "sd_parent", RefColumns: []string{"code"}, OnDelete: "SET DEFAULT",
			}},
		},
	}}
	current, err := client.IntrospectSchema(ctx)
	if err != nil {
		t.Fatalf("introspect on %s: %v", engine, err)
	}
	plan := quark.Plan{Ops: quark.Diff(desired, current)}
	err = client.ApplyPlan(ctx, plan)
	if refuses {
		if !errors.Is(err, quark.ErrUnsupportedFeature) || !strings.Contains(err.Error(), "ON DELETE SET DEFAULT") {
			t.Fatalf("on %s a plan with ON DELETE SET DEFAULT = %v, want ErrUnsupportedFeature naming the clause, sent nothing", engine, err)
		}
		return
	}
	if err != nil {
		t.Fatalf("on %s the engine refused a foreign key with ON DELETE SET DEFAULT: %v\n%s", engine, err, plan)
	}

	exec := func(stmt string, args ...any) error {
		_, err := client.Raw().ExecContext(ctx, stmt, args...)
		return err
	}
	parent, child := q(client, "sd_parent"), q(client, "sd_child")
	for _, code := range []string{"keep", "gone"} {
		if err := exec(fmt.Sprintf("INSERT INTO %s (%s) VALUES (%s)", parent, q(client, "code"), client.Dialect().Placeholder(1)), code); err != nil {
			t.Fatalf("seed parent %q on %s: %v", code, engine, err)
		}
	}
	if err := exec(fmt.Sprintf("INSERT INTO %s (%s) VALUES (%s)", child, q(client, "parent_code"), client.Dialect().Placeholder(1)), "gone"); err != nil {
		t.Fatalf("seed child on %s: %v", engine, err)
	}

	// Delete the referenced row. SQLite enforces foreign keys per connection
	// and only when asked, so the delete there runs on a connection that
	// has them on: the point is what the engine does when it does enforce.
	del := fmt.Sprintf("DELETE FROM %s WHERE %s = %s", parent, q(client, "code"), client.Dialect().Placeholder(1))
	var delErr error
	if engine == "sqlite" {
		conn, err := client.Raw().Conn(ctx)
		if err != nil {
			t.Fatalf("pin a connection: %v", err)
		}
		defer conn.Close()
		if _, err := conn.ExecContext(ctx, "PRAGMA foreign_keys = ON"); err != nil {
			t.Fatalf("PRAGMA foreign_keys: %v", err)
		}
		_, delErr = conn.ExecContext(ctx, del, "gone")
	} else {
		delErr = exec(del, "gone")
	}

	var parentCode sql.NullString
	if err := client.Raw().QueryRowContext(ctx, fmt.Sprintf("SELECT %s FROM %s", q(client, "parent_code"), child)).Scan(&parentCode); err != nil {
		t.Fatalf("read the child on %s: %v", engine, err)
	}
	if runsIt {
		if delErr != nil {
			t.Fatalf("on %s the delete of a referenced row under ON DELETE SET DEFAULT failed: %v, want the action to run", engine, delErr)
		}
		if parentCode.String != "keep" {
			t.Errorf("on %s the child references %q after the delete, want the column's default %q", engine, parentCode.String, "keep")
		}
	}

	// For the record: what the catalog says the action is, and whether the
	// applied schema re-plans the key.
	live, err := client.IntrospectSchema(ctx)
	if err != nil {
		t.Fatalf("introspect after apply on %s: %v", engine, err)
	}
	for _, tb := range live.Tables {
		if !strings.EqualFold(tb.Name, "sd_child") {
			continue
		}
		for _, fk := range tb.ForeignKeys {
			t.Logf("%s catalog: %s ON DELETE %q", engine, fk.Name, fk.OnDelete)
		}
	}
	if residual := quark.Diff(desired, live); len(residual) != 0 {
		names := make([]string, len(residual))
		for i, op := range residual {
			names[i] = op.String()
		}
		t.Logf("%s re-plans the applied schema: %s", engine, strings.Join(names, " | "))
	}
}
