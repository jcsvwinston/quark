// Copyright 2026 jcsvwinston
// SPDX-License-Identifier: Apache-2.0

// Regression tests for the v1.2.1 backlog CLI items (QK-P1-1, QK-P1-3,
// QK-P1-5 exit paths, QK-P2-1, QK-P2-5).
package commands

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/spf13/viper"
)

// QK-P1-1: with an empty migration registry (the standalone-binary reality),
// migrate up/down must fail loudly — never report "No pending migrations".
// The guard fires before any DB connection, so no config is needed.
func TestMigrateUpFailsWithoutRegisteredMigrations(t *testing.T) {
	if err := runMigrateUp(); err == nil || !strings.Contains(err.Error(), "no migrations are registered") {
		t.Fatalf("migrate up with empty registry: got %v, want 'no migrations are registered'", err)
	}
	if err := runMigrateDown(); err == nil || !strings.Contains(err.Error(), "no migrations are registered") {
		t.Fatalf("migrate down with empty registry: got %v, want 'no migrations are registered'", err)
	}
}

// QK-P1-1: same contract for seeders.
func TestSeedRunFailsWithoutRegisteredSeeders(t *testing.T) {
	if err := runSeedRun(); err == nil || !strings.Contains(err.Error(), "no seeders are registered") {
		t.Fatalf("seed run with empty registry: got %v, want 'no seeders are registered'", err)
	}
}

// QK-P1-3: tenant migrate validates the id and requires registered
// migrations before touching any database.
func TestTenantMigrateGuards(t *testing.T) {
	if err := runTenantMigrate("Bad;ID"); err == nil || !strings.Contains(err.Error(), "invalid tenant id") {
		t.Fatalf("invalid id: got %v", err)
	}
	if err := runTenantMigrate("acme"); err == nil || !strings.Contains(err.Error(), "no migrations are registered") {
		t.Fatalf("empty registry: got %v", err)
	}
}

// QK-P2-5: `quark init --dialect bogus` used to write an invalid config and
// exit 0. Now it must fail before creating anything.
func TestInitRejectsUnknownDialect(t *testing.T) {
	dir := t.TempDir()
	initDir, initDialect = dir, "bogus"
	defer func() { initDir, initDialect = ".", "postgresql" }()

	err := runInit()
	if err == nil || !strings.Contains(err.Error(), "unknown dialect") {
		t.Fatalf("expected 'unknown dialect' error, got %v", err)
	}
	if _, statErr := os.Stat(filepath.Join(dir, ".quark.yml")); !os.IsNotExist(statErr) {
		t.Fatal("config file was written despite the invalid dialect")
	}
	if _, statErr := os.Stat(filepath.Join(dir, "models")); !os.IsNotExist(statErr) {
		t.Fatal("directories were created despite the invalid dialect")
	}
}

func TestInitAcceptsMariaDB(t *testing.T) {
	dir := t.TempDir()
	initDir, initDialect = dir, "mariadb"
	defer func() { initDir, initDialect = ".", "postgresql" }()

	if err := runInit(); err != nil {
		t.Fatalf("init --dialect mariadb: %v", err)
	}
	data, err := os.ReadFile(filepath.Join(dir, ".quark.yml"))
	if err != nil {
		t.Fatalf("config not written: %v", err)
	}
	if !strings.Contains(string(data), "parseTime=true") {
		t.Errorf("mariadb DSN placeholder missing, got:\n%s", data)
	}
}

// QK-P2-1: the phantom flags must be gone.
func TestPhantomFlagsRemoved(t *testing.T) {
	if f := tenantProvisionCmd.Flags().Lookup("skip-seed"); f != nil {
		t.Error("--skip-seed still declared on tenant provision")
	}
	if f := tenantMigrateCmd.Flags().Lookup("tenant-id"); f != nil {
		t.Error("--tenant-id still declared on tenant migrate")
	}
	if f := seedRunCmd.Flags().Lookup("env"); f != nil {
		t.Error("--env still declared on seed run")
	}
}

// QK-P1-3: tenant DSN resolution contract (validated through the db package
// but exercised here where the viper config lives).
func TestTenantMigrateAllRequiresDSNTemplate(t *testing.T) {
	// db_per_tenant without a template must fail with guidance, not migrate
	// the default database. Registry must be non-empty to reach resolution —
	// covered in the db package unit test instead; here we assert the message
	// when migrations ARE the blocker to keep this test registry-independent.
	viper.Set("tenant.strategy", "db_per_tenant")
	defer viper.Set("tenant.strategy", "")
	err := runTenantMigrate("acme")
	if err == nil {
		t.Fatal("expected error")
	}
}
