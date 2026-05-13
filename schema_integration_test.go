// Copyright 2026 jcsvwinston
// SPDX-License-Identifier: Apache-2.0

package quark_test

import (
	"context"
	"errors"
	"fmt"
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
	case "oracle":
		_, err := baseClient.IntrospectSchema(ctx)
		if !errors.Is(err, quark.ErrUnsupportedFeature) {
			t.Errorf("dialect %s should return ErrUnsupportedFeature until F3-2-%s lands, got %v",
				dialect, dialect, err)
		}
		return
	}

	// Supported path: SQLite + PostgreSQL + MySQL + MariaDB + MSSQL.
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

	t.Run("ListsCreatedIndex", func(t *testing.T) {
		// F3-2-indexes contract: a UNIQUE INDEX created via
		// CreateIndex must surface in Table.Indexes with the right
		// shape (name, columns, unique flag).
		//
		// We use CreateIndex rather than raw DDL because:
		//   - It's the public Quark API the diff layer will compare
		//     against, so the introspector and the migrator have to
		//     agree on the *same* surface.
		//   - It handles per-dialect quirks (MySQL no `IF NOT EXISTS`,
		//     MSSQL `IF NOT EXISTS (SELECT … sys.indexes …)`) so the
		//     test stays dialect-neutral.
		idxName := "idx_schema_fixtures_name"
		if err := baseClient.CreateIndex(ctx, "schema_fixtures", idxName,
			[]string{"name"}, true); err != nil {
			t.Fatalf("CreateIndex: %v", err)
		}
		// The parent test already `defer dropTable(... schema_fixtures)`
		// which cascades the index in most engines, but we also
		// best-effort drop the index by name on subtest exit so a
		// re-run in the same process (rare locally; happens with
		// `-run` flag matching) doesn't trip a "already exists" path
		// the CreateIndex helper has to ignore.
		defer func() {
			_, _ = baseClient.Raw().ExecContext(ctx,
				fmt.Sprintf("DROP INDEX %s", idxName))
		}()

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
			t.Fatalf("schema_fixtures missing from introspection result")
		}

		var found *quark.Index
		for i := range fixture.Indexes {
			if fixture.Indexes[i].Name == idxName {
				found = &fixture.Indexes[i]
				break
			}
		}
		if found == nil {
			names := make([]string, 0, len(fixture.Indexes))
			for _, idx := range fixture.Indexes {
				names = append(names, idx.Name)
			}
			t.Fatalf("index %q missing from introspection result. Saw: %v", idxName, names)
		}
		if !found.Unique {
			t.Errorf("index %q should be UNIQUE, got Unique=false", idxName)
		}
		if len(found.Columns) != 1 || found.Columns[0] != "name" {
			t.Errorf("index %q columns: want [name], got %v", idxName, found.Columns)
		}
	})

	t.Run("FiltersInternalTables", func(t *testing.T) {
		// `quark_*` tables (used internally for migration state /
		// future use) must not surface in the user-facing schema view.
		// SQLite also has `sqlite_*` system tables — same filter.
		//
		// The contract under test is "the filter actually filters",
		// not "no internal tables happen to exist". To make the
		// assertion meaningful we **create** a `quark_filter_probe`
		// table before introspecting and verify it's NOT in the
		// result. Without this seed the test would pass even if the
		// `NOT LIKE 'quark_%'` clause were removed from the
		// introspector — a regression invisible to the suite.
		// Seed a `quark_*` table to actually exercise the filter — see
		// the rationale below. If the dialect rejects the raw DDL
		// (strict modes, picky type names), we skip the filter
		// assertion rather than fail the suite — the broken DDL is a
		// test-side issue, not a regression in the introspector.
		dropTable(baseClient, "quark_filter_probe")
		if _, err := baseClient.Raw().ExecContext(ctx,
			`CREATE TABLE quark_filter_probe (id INTEGER PRIMARY KEY)`); err != nil {
			t.Skipf("seed quark_filter_probe failed on dialect %s: %v — skipping filter assertion", dialect, err)
		}
		defer dropTable(baseClient, "quark_filter_probe")

		schema, err := baseClient.IntrospectSchema(ctx)
		if err != nil {
			t.Fatalf("IntrospectSchema: %v", err)
		}
		var sawProbe bool
		for _, table := range schema.Tables {
			if table.Name == "quark_filter_probe" {
				sawProbe = true
			}
			if len(table.Name) >= 6 && table.Name[:6] == "quark_" {
				t.Errorf("internal table %q leaked into introspection result", table.Name)
			}
			if len(table.Name) >= 7 && table.Name[:7] == "sqlite_" {
				t.Errorf("SQLite system table %q leaked into introspection result", table.Name)
			}
		}
		if sawProbe {
			t.Errorf("quark_filter_probe leaked — the NOT LIKE filter is not effective")
		}
	})
}
