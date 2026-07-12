// Copyright 2026 jcsvwinston
// SPDX-License-Identifier: Apache-2.0

package commands

import (
	"strings"
	"testing"

	"github.com/spf13/viper"
)

// QK-P0-1: tenant ids come straight from argv and used to be concatenated
// into CREATE DATABASE / CREATE SCHEMA / INSERT statements. The id must be
// rejected by the tenant-id contract BEFORE any connection or SQL happens —
// these malicious ids all fail fast with a validation error, never a
// "connecting to admin database" one.
func TestTenantProvisionRejectsMaliciousID(t *testing.T) {
	for _, id := range []string{
		"x; DROP TABLE users; --",
		`x" CASCADE; --`,
		"x'y",
		"X_UPPER", // uppercase outside the ^[a-z0-9_-]+$ contract
		"",
	} {
		err := runTenantProvision(id)
		if err == nil {
			t.Fatalf("id %q: expected validation error, got nil", id)
		}
		if !strings.Contains(err.Error(), "invalid tenant id") {
			t.Fatalf("id %q: expected 'invalid tenant id' validation error, got: %v", id, err)
		}
	}
}

// The strategy comes from config (viper), not argv, but it also reaches SQL
// decisions and the registry INSERT — it must be checked against the enum
// before any connection attempt.
func TestTenantProvisionRejectsUnknownStrategy(t *testing.T) {
	viper.Set("tenant.strategy", "evil','x'); DROP TABLE quark_tenants; --")
	defer viper.Set("tenant.strategy", "")

	err := runTenantProvision("good-tenant")
	if err == nil {
		t.Fatal("expected error for unknown strategy, got nil")
	}
	if !strings.Contains(err.Error(), "unsupported strategy") {
		t.Fatalf("expected 'unsupported strategy' error, got: %v", err)
	}
}
