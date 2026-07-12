// Copyright 2026 jcsvwinston
// SPDX-License-Identifier: Apache-2.0

package db

import (
	"strings"
	"testing"

	"github.com/spf13/viper"
)

// QK-P1-3: tenant client resolution must never fall back to the default
// database. db_per_tenant needs an explicit DSN template with a {tenant}
// placeholder; schema_per_tenant is not resolvable from static config.
func TestGetTenantQuarkClientGuards(t *testing.T) {
	t.Cleanup(func() {
		viper.Set("tenant.strategy", "")
		viper.Set("tenant.dsn_template", "")
		viper.Set("database.default.driver", "")
	})

	viper.Set("tenant.strategy", "db_per_tenant")
	viper.Set("tenant.dsn_template", "")
	if _, err := GetTenantQuarkClient("acme"); err == nil || !strings.Contains(err.Error(), "tenant.dsn_template is not configured") {
		t.Fatalf("missing template: got %v", err)
	}

	viper.Set("tenant.dsn_template", "postgres://u:p@localhost/fixed")
	if _, err := GetTenantQuarkClient("acme"); err == nil || !strings.Contains(err.Error(), "no {tenant} placeholder") {
		t.Fatalf("template without placeholder: got %v", err)
	}

	viper.Set("tenant.strategy", "schema_per_tenant")
	if _, err := GetTenantQuarkClient("acme"); err == nil || !strings.Contains(err.Error(), "schema_per_tenant migrations are not supported") {
		t.Fatalf("schema_per_tenant: got %v", err)
	}

	viper.Set("tenant.strategy", "row_level")
	if _, err := GetTenantQuarkClient("acme"); err == nil || !strings.Contains(err.Error(), "unsupported strategy") {
		t.Fatalf("unknown strategy: got %v", err)
	}
}

// The happy path substitutes the tenant into the template and opens a real
// client — verifiable end-to-end with SQLite file DSNs.
func TestGetTenantQuarkClientSubstitutesTenant(t *testing.T) {
	t.Cleanup(func() {
		viper.Set("tenant.strategy", "")
		viper.Set("tenant.dsn_template", "")
		viper.Set("database.default.driver", "")
	})

	dir := t.TempDir()
	viper.Set("tenant.strategy", "db_per_tenant")
	viper.Set("tenant.dsn_template", "file:"+dir+"/{tenant}.db")
	viper.Set("database.default.driver", "sqlite")

	client, err := GetTenantQuarkClient("acme")
	if err != nil {
		t.Fatalf("GetTenantQuarkClient: %v", err)
	}
	defer client.Close()

	if client.Dialect().Name() != "sqlite" {
		t.Errorf("dialect = %q", client.Dialect().Name())
	}
}
