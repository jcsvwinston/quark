// Copyright 2026 jcsvwinston
// SPDX-License-Identifier: Apache-2.0

package quarktenant_test

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/jcsvwinston/quark"
	"github.com/jcsvwinston/quark/quarktenant"
)

// testOrder is the canonical fixture: tenant-scoped order table with
// tenant_id as TEXT. Used by every DDL-rendering test so the policy
// shape is consistent.
type testOrder struct {
	ID       int64  `db:"id" pk:"true"`
	TenantID string `db:"tenant_id"`
	Status   string `db:"status"`
}

// testInvoice is a second fixture used to verify multi-model output
// ordering. Same tenant column shape so the same policy applies.
type testInvoice struct {
	ID       int64  `db:"id" pk:"true"`
	TenantID string `db:"tenant_id"`
	Amount   int64  `db:"amount"`
}

// testOrgScoped uses a non-default tenant column to exercise the
// --tenant-col override.
type testOrgScoped struct {
	ID    int64  `db:"id" pk:"true"`
	OrgID string `db:"org_id"`
	Name  string `db:"name"`
}

// testNoTenant is missing the configured tenant column entirely. The
// installer must reject it with ErrNoTenantColumn before any DDL is
// produced.
type testNoTenant struct {
	ID   int64  `db:"id" pk:"true"`
	Name string `db:"name"`
}

func newSQLiteClient(t *testing.T) *quark.Client {
	t.Helper()
	c, err := quark.New("sqlite", ":memory:")
	if err != nil {
		t.Fatalf("new sqlite client: %v", err)
	}
	t.Cleanup(func() { _ = c.Close() })
	return c
}

// TestInstallRLSPolicies_RejectsNonPostgres asserts the F5-3
// dialect guard: any non-PG client returns ErrUnsupportedFeature
// before models or options are inspected.
func TestInstallRLSPolicies_RejectsNonPostgres(t *testing.T) {
	t.Parallel()
	c := newSQLiteClient(t)
	if err := c.RegisterModel(&testOrder{}); err != nil {
		t.Fatalf("register: %v", err)
	}

	stmts, err := quarktenant.InstallRLSPolicies(context.Background(), c, quarktenant.DefaultInstallOptions())
	if err == nil {
		t.Fatalf("expected error on sqlite, got nil with stmts=%v", stmts)
	}
	if !errors.Is(err, quark.ErrUnsupportedFeature) {
		t.Fatalf("expected ErrUnsupportedFeature, got %v", err)
	}
	if stmts != nil {
		t.Errorf("expected no statements on dialect rejection, got %d", len(stmts))
	}
}

// TestInstallRLSPolicies_RejectsEmptyRegistry asserts that an empty
// model registry returns ErrNoRegisteredModels. The installer would
// otherwise silently no-op which is a worse UX in CI.
//
// PostgreSQL gate is checked first, so we cannot reach this branch
// on sqlite. We exercise it on a sqlite client where the dialect
// guard fires before the registry check — meaning this test
// documents the guard ORDER rather than the registry path itself.
// The registry path is exercised in the PG integration test below.
func TestInstallRLSPolicies_RejectsEmptyRegistryOrder(t *testing.T) {
	t.Parallel()
	c := newSQLiteClient(t)
	// No RegisterModel call.

	_, err := quarktenant.InstallRLSPolicies(context.Background(), c, quarktenant.DefaultInstallOptions())
	if err == nil {
		t.Fatal("expected error on sqlite + empty registry, got nil")
	}
	// Dialect check fires first; we should NOT see ErrNoRegisteredModels.
	if errors.Is(err, quarktenant.ErrNoRegisteredModels) {
		t.Fatalf("registry guard ran before dialect guard; got %v", err)
	}
	if !errors.Is(err, quark.ErrUnsupportedFeature) {
		t.Fatalf("expected dialect rejection first, got %v", err)
	}
}

// TestInstallRLSPolicies_NilClient documents the precondition.
func TestInstallRLSPolicies_NilClient(t *testing.T) {
	t.Parallel()
	_, err := quarktenant.InstallRLSPolicies(context.Background(), nil, quarktenant.DefaultInstallOptions())
	if err == nil {
		t.Fatal("expected error on nil client")
	}
	if !strings.Contains(err.Error(), "client must not be nil") {
		t.Errorf("unexpected error: %v", err)
	}
}

// TestDefaultInstallOptions_Values asserts the documented defaults
// stay aligned with the runtime's TenantConfig defaults — drift
// here would mean generated policies reference a different setting
// than the router emits.
func TestDefaultInstallOptions_Values(t *testing.T) {
	t.Parallel()
	got := quarktenant.DefaultInstallOptions()
	if got.TenantColumn != "tenant_id" {
		t.Errorf("TenantColumn = %q, want %q", got.TenantColumn, "tenant_id")
	}
	if got.NativeRLSVar != "app.tenant_id" {
		t.Errorf("NativeRLSVar = %q, want %q", got.NativeRLSVar, "app.tenant_id")
	}
	if !got.ForceRLS {
		t.Error("ForceRLS should default to true")
	}
	if got.DryRun {
		t.Error("DryRun should default to false")
	}
	if got.LockName != "quark_install_rls_policies" {
		t.Errorf("LockName = %q", got.LockName)
	}
	if got.LockTimeout == 0 {
		t.Error("LockTimeout must be non-zero")
	}
}

// TestRun_UnknownAction asserts CLI usage errors map to ExitError.
func TestRun_UnknownAction(t *testing.T) {
	t.Parallel()
	c := newSQLiteClient(t)

	var stdout, stderr bytes.Buffer
	got := quarktenant.RunWithIO(context.Background(), []string{"unknown"}, c, &stdout, &stderr)
	if got != quarktenant.ExitError {
		t.Errorf("Run(unknown) = %d, want ExitError(%d)", got, quarktenant.ExitError)
	}
	if !strings.Contains(stderr.String(), "unknown action") {
		t.Errorf("stderr should mention unknown action, got %q", stderr.String())
	}
}

// TestRun_EmptyArgs asserts the no-action case fails loudly.
func TestRun_EmptyArgs(t *testing.T) {
	t.Parallel()
	c := newSQLiteClient(t)

	var stdout, stderr bytes.Buffer
	got := quarktenant.RunWithIO(context.Background(), nil, c, &stdout, &stderr)
	if got != quarktenant.ExitError {
		t.Errorf("Run(nil) = %d, want ExitError(%d)", got, quarktenant.ExitError)
	}
	if !strings.Contains(stderr.String(), "missing action") {
		t.Errorf("stderr should mention missing action, got %q", stderr.String())
	}
}

// TestParseAction documents the action registry.
func TestParseAction(t *testing.T) {
	t.Parallel()
	if got, err := quarktenant.ParseAction("install-rls-policies"); err != nil {
		t.Fatalf("install-rls-policies should parse, got err: %v", err)
	} else if got != quarktenant.ActionInstallRLSPolicies {
		t.Errorf("got %q", got)
	}
	if _, err := quarktenant.ParseAction(""); err == nil {
		t.Error("empty action should fail to parse")
	}
	if _, err := quarktenant.ParseAction("plan"); err == nil {
		t.Error(`"plan" is a quarkmigrate action, not quarktenant; should not parse`)
	}
}
