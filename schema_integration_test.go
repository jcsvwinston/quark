// Copyright 2026 jcsvwinston
// SPDX-License-Identifier: Apache-2.0

package quark_test

import (
	"context"
	"errors"
	"testing"

	"github.com/jcsvwinston/quark"
)

// schemaFixture is the canonical fixture the F3-2 integration test
// migrates and then introspects. Three columns of different shape:
//   - id          BIGINT PRIMARY KEY, NOT NULL, auto-increment-ish
//   - name        TEXT NOT NULL
//   - description TEXT NULL (nullable)
//
// The shape is deliberately the smallest that exercises the
// nullable / non-nullable distinction and (in PG) the
// parameter-bearing type reassembly (`character varying(N)` is the
// PG default for size-tagged Go strings).
type schemaFixture struct {
	ID          int64  `db:"id" pk:"true"`
	Name        string `db:"name" quark:"not_null"`
	Description string `db:"description"`
}

// testSchemaIntrospection runs the F3-2 contract against any dialect
// the SharedSuite covers. On dialects that don't implement the
// introspector yet (MySQL, MariaDB, MSSQL, Oracle), the test asserts
// `ErrUnsupportedFeature` so the follow-up PR knows what to remove.
func testSchemaIntrospection(ctx context.Context, t *testing.T, baseClient *quark.Client) {
	t.Helper()

	dialect := baseClient.Dialect().Name()

	// Dialects without an introspector yet: must surface
	// ErrUnsupportedFeature. When their follow-up PR lands, this
	// branch goes away and the dialect joins the main path below.
	switch dialect {
	case "mysql", "mariadb", "sqlserver", "mssql", "oracle":
		_, err := baseClient.IntrospectSchema(ctx)
		if !errors.Is(err, quark.ErrUnsupportedFeature) {
			t.Errorf("dialect %s should return ErrUnsupportedFeature until F3-2-%s lands, got %v",
				dialect, dialect, err)
		}
		return
	}

	// Supported path: SQLite + PostgreSQL.
	dropTable(baseClient, "schema_fixtures")
	if err := baseClient.Migrate(ctx, &schemaFixture{}); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	defer dropTable(baseClient, "schema_fixtures")

	t.Run("ListsFixtureTable", func(t *testing.T) {
		schema, err := baseClient.IntrospectSchema(ctx)
		if err != nil {
			t.Fatalf("IntrospectSchema: %v", err)
		}
		var fixture *quark.Table
		for i := range schema.Tables {
			if schema.Tables[i].Name == "schema_fixtures" {
				fixture = &schema.Tables[i]
				break
			}
		}
		if fixture == nil {
			names := make([]string, 0, len(schema.Tables))
			for _, t := range schema.Tables {
				names = append(names, t.Name)
			}
			t.Fatalf("schema_fixtures table missing from introspection result. Saw: %v", names)
		}

		// Three columns expected in declaration order.
		if len(fixture.Columns) != 3 {
			t.Fatalf("expected 3 columns, got %d (%+v)", len(fixture.Columns), fixture.Columns)
		}
		byName := map[string]quark.Column{}
		for _, c := range fixture.Columns {
			byName[c.Name] = c
		}
		if _, ok := byName["id"]; !ok {
			t.Errorf("id column missing")
		}
		if name, ok := byName["name"]; !ok {
			t.Errorf("name column missing")
		} else if name.Nullable {
			t.Errorf("name column should be NOT NULL (quark:\"not_null\" tag), got Nullable=true")
		}
		if desc, ok := byName["description"]; !ok {
			t.Errorf("description column missing")
		} else if !desc.Nullable {
			t.Errorf("description column should be nullable, got Nullable=false")
		}
	})

	t.Run("FiltersInternalTables", func(t *testing.T) {
		// `quark_*` tables (used internally for migration state /
		// future use) must not surface in the user-facing schema view.
		// SQLite also has `sqlite_*` system tables — same filter.
		schema, err := baseClient.IntrospectSchema(ctx)
		if err != nil {
			t.Fatalf("IntrospectSchema: %v", err)
		}
		for _, table := range schema.Tables {
			if len(table.Name) >= 6 && table.Name[:6] == "quark_" {
				t.Errorf("internal table %q leaked into introspection result", table.Name)
			}
			if len(table.Name) >= 7 && table.Name[:7] == "sqlite_" {
				t.Errorf("SQLite system table %q leaked into introspection result", table.Name)
			}
		}
	})
}
