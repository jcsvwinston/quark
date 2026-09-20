// Copyright 2026 jcsvwinston
// SPDX-License-Identifier: Apache-2.0

package commands

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jcsvwinston/quark"
)

// A8 S10 (RLS-06): the documented commands exist in the binary.

func mkdirAllForFile(path string) error { return os.MkdirAll(filepath.Dir(path), 0o755) }
func writeFileString(path, content string) error {
	return os.WriteFile(path, []byte(content), 0o644)
}

func TestTenantInstallRLSRendersTheRunnersDDLAndRequiresPostgres(t *testing.T) {
	t.Setenv("GOWORK", "off")
	chdirBack := chdirForTest(t)
	defer chdirBack()
	stmts := rlsPolicyDDL("orders", "tenant_id", "app.tenant_id", "", true)
	joined := strings.Join(stmts, "\n")
	for _, want := range []string{
		`ALTER TABLE "orders" ENABLE ROW LEVEL SECURITY`,
		`ALTER TABLE "orders" FORCE ROW LEVEL SECURITY`,
		`DROP POLICY IF EXISTS "orders_tenant_isolation" ON "orders"`,
		`CREATE POLICY "orders_tenant_isolation" ON "orders" USING ("tenant_id" = current_setting('app.tenant_id', true)::text) WITH CHECK ("tenant_id" = current_setting('app.tenant_id', true)::text)`,
	} {
		if !strings.Contains(joined, want) {
			t.Errorf("DDL lacks %q:\n%s", want, joined)
		}
	}

	dir := t.TempDir()
	writeFile(t, dir+"/go.mod", "module example.com/rls\n\ngo 1.25\n")
	enterDir(t, dir)
	writeFile(t, dir+"/models/m.go", "package models\n\ntype Order struct {\n\tID       int64  `db:\"id\" pk:\"true\"`\n\tTenantID string `db:\"tenant_id\"`\n}\n\ntype Plain struct {\n\tID int64 `db:\"id\" pk:\"true\"`\n}\n")
	models, err := loadModelsForDDL("./models")
	if err != nil {
		t.Fatal(err)
	}
	if got := tenantTables(models, "tenant_id"); len(got) != 1 || got[0] != "orders" {
		t.Fatalf("tenant tables: %v", got)
	}

	// The gate: on SQLite both commands refuse with the dialect error, before
	// touching anything.
	useSQLite(t, "s10_rls")
	for _, args := range [][]string{
		{"tenant", "install-rls-policies", "--from-models", "./models", "--dry-run"},
		{"tenant", "verify-rls-policies", "--from-models", "./models"},
	} {
		_, err := runCLI(t, args...)
		if !errors.Is(err, quark.ErrUnsupportedFeature) || !strings.Contains(err.Error(), "requires PostgreSQL") {
			t.Errorf("%v: %v, want the PostgreSQL gate", args, err)
		}
	}
}
