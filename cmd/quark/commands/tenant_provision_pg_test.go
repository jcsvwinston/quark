// Copyright 2026 jcsvwinston
// SPDX-License-Identifier: Apache-2.0

// Regression test for QCD-CLI-3: `tenant provision` with schema_per_tenant
// accepted the strategy, created the schema and the quark_tenants row, then
// unconditionally chained `tenant migrate` — which rejects schema_per_tenant
// by design. The command could NEVER complete: every run ended exit 1 with a
// partial, non-idempotent provision (a retry blew up on duplicate CREATE
// SCHEMA).
//
// Chosen behavior (option A): provision creates the schema and registry row
// and explicitly SKIPS the migration step with a message pointing at the
// TenantRouter runner — a complete, honest effect. Retrying an
// already-provisioned id must fail with a clear "already provisioned" error
// BEFORE any DDL runs.
package commands

import (
	"database/sql"
	"strings"
	"testing"

	"github.com/spf13/viper"
)

const provisionTestTenant = "qcd3_shard"

func setupProvisionTest(t *testing.T) *sql.DB {
	t.Helper()
	dsn := provisionTestPostgresDSN(t)
	if dsn == "" {
		t.Skip("QUARK_TEST_POSTGRES_DSN not set (rebuild with -tags=integration to spin up a container)")
	}

	raw, err := sql.Open("pgx", dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { raw.Close() })

	// Start from a clean slate so the test owns the full provision cycle.
	for _, q := range []string{
		"DROP SCHEMA IF EXISTS " + provisionTestTenant + " CASCADE",
		"DROP TABLE IF EXISTS quark_tenants",
	} {
		if _, err := raw.Exec(q); err != nil {
			t.Fatalf("resetting state (%s): %v", q, err)
		}
	}

	for _, kv := range [][2]string{
		{"tenant.strategy", "schema_per_tenant"},
		{"database.admin.driver", "postgres"},
		{"database.admin.dsn", dsn},
	} {
		old := viper.GetString(kv[0])
		key := kv[0]
		viper.Set(key, kv[1])
		t.Cleanup(func() { viper.Set(key, old) })
	}
	return raw
}

func TestTenantProvisionSchemaPerTenantPostgres(t *testing.T) {
	raw := setupProvisionTest(t)

	// First provision must COMPLETE: schema + registry row, migrations
	// explicitly skipped (they need a TenantRouter, which static CLI config
	// cannot build). Today this errors after creating both effects.
	if err := runTenantProvision(provisionTestTenant); err != nil {
		t.Fatalf("provision schema_per_tenant must complete (schema + registry, migrations skipped), got: %v", err)
	}

	var schemaCount int
	if err := raw.QueryRow(
		"SELECT COUNT(*) FROM information_schema.schemata WHERE schema_name = $1",
		provisionTestTenant).Scan(&schemaCount); err != nil {
		t.Fatal(err)
	}
	if schemaCount != 1 {
		t.Errorf("schema %s not created", provisionTestTenant)
	}
	var registered int
	if err := raw.QueryRow(
		"SELECT COUNT(*) FROM quark_tenants WHERE id = $1 AND strategy = 'schema_per_tenant'",
		provisionTestTenant).Scan(&registered); err != nil {
		t.Fatal(err)
	}
	if registered != 1 {
		t.Errorf("tenant %s not registered in quark_tenants", provisionTestTenant)
	}

	// Retry must fail with a clear "already provisioned" error BEFORE any
	// DDL — today it blows up on duplicate CREATE SCHEMA after touching the
	// database.
	err := runTenantProvision(provisionTestTenant)
	if err == nil {
		t.Fatal("second provision of the same id must fail with a clear error")
	}
	if !strings.Contains(err.Error(), "already provisioned") {
		t.Errorf("second provision: want 'already provisioned', got: %v", err)
	}
}
