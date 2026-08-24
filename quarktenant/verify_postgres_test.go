// Copyright 2026 jcsvwinston
// SPDX-License-Identifier: Apache-2.0

package quarktenant_test

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/jcsvwinston/quark"
	"github.com/jcsvwinston/quark/quarktenant"

	_ "github.com/jackc/pgx/v5/stdlib"
)

// TestVerifyRLSPolicies_Postgres exercises the boot-time guardrail against
// a real engine, in the exact order the dangerous misconfiguration
// happens: tables migrated but the RLS DDL never applied (the router would
// serve every tenant every row) → verify must FAIL naming the tables;
// after InstallRLSPolicies → verify passes; with the FORCE variant
// stripped → verify fails again naming the owner-bypass gap.
func TestVerifyRLSPolicies_Postgres(t *testing.T) {
	dsn := postgresDSN(t)
	if dsn == "" {
		t.Skip("postgres DSN unavailable: set QUARK_TEST_POSTGRES_DSN or run with -tags=integration to boot a container")
	}

	ctx := context.Background()

	client, err := quark.New("pgx", dsn, quark.WithLimits(quark.Limits{
		AllowRawQueries: true,
		MaxResults:      1000,
		QueryTimeout:    30 * time.Second,
	}))
	if err != nil {
		t.Fatalf("new pgx client: %v", err)
	}
	t.Cleanup(func() { _ = client.Close() })

	type qverifyOrder struct {
		ID       int64  `db:"id" pk:"true"`
		TenantID string `db:"tenant_id"`
		Status   string `db:"status"`
	}
	type qverifyInvoice struct {
		ID       int64  `db:"id" pk:"true"`
		TenantID string `db:"tenant_id"`
		Amount   int64  `db:"amount"`
	}

	tables := []string{"qverify_orders", "qverify_invoices"}
	cleanup := func() {
		for _, tbl := range tables {
			_ = client.Exec(ctx, "DROP TABLE IF EXISTS "+tbl+" CASCADE")
		}
	}
	cleanup()
	t.Cleanup(cleanup)

	if err := client.RegisterModel(&qverifyOrder{}, &qverifyInvoice{}); err != nil {
		t.Fatalf("register: %v", err)
	}
	if err := client.MigrateRegistered(ctx); err != nil {
		t.Fatalf("migrate: %v", err)
	}

	opts := quarktenant.DefaultInstallOptions()

	// --- the leak scenario: migrated, never installed ---
	t.Run("unenforced_tables_fail_loudly", func(t *testing.T) {
		findings, err := quarktenant.VerifyRLSPolicies(ctx, client, opts)
		if !errors.Is(err, quarktenant.ErrRLSNotEnforced) {
			t.Fatalf("want ErrRLSNotEnforced, got %v", err)
		}
		if len(findings) != 2 {
			t.Fatalf("want 2 findings (both tables unenforced), got %d: %+v", len(findings), findings)
		}
		for _, want := range []string{"qverify_orders", "qverify_invoices", "ENABLE ROW LEVEL SECURITY", "install-rls-policies"} {
			if !strings.Contains(err.Error(), want) {
				t.Errorf("error must name %q, got:\n%v", want, err)
			}
		}
	})

	// --- after install: enforced ---
	t.Run("installed_tables_verify_clean", func(t *testing.T) {
		if _, err := quarktenant.InstallRLSPolicies(ctx, client, opts); err != nil {
			t.Fatalf("install: %v", err)
		}
		findings, err := quarktenant.VerifyRLSPolicies(ctx, client, opts)
		if err != nil || len(findings) != 0 {
			t.Fatalf("want clean verify after install, got findings=%+v err=%v", findings, err)
		}
	})

	// --- the decorative-policy scenario: FORCE stripped, owner bypasses ---
	t.Run("missing_force_is_a_finding", func(t *testing.T) {
		if err := client.Exec(ctx, `ALTER TABLE qverify_orders NO FORCE ROW LEVEL SECURITY`); err != nil {
			t.Fatalf("strip FORCE: %v", err)
		}
		findings, err := quarktenant.VerifyRLSPolicies(ctx, client, opts)
		if !errors.Is(err, quarktenant.ErrRLSNotEnforced) {
			t.Fatalf("want ErrRLSNotEnforced with FORCE stripped, got %v", err)
		}
		if len(findings) != 1 || findings[0].Table != "qverify_orders" {
			t.Fatalf("want exactly the stripped table, got %+v", findings)
		}
		if !strings.Contains(err.Error(), "owner bypasses") {
			t.Errorf("error must explain the owner bypass, got:\n%v", err)
		}
		// The same state is fine when the caller opted out of FORCE.
		noForce := opts
		noForce.ForceRLS = false
		if findings, err := quarktenant.VerifyRLSPolicies(ctx, client, noForce); err != nil || len(findings) != 0 {
			t.Fatalf("ForceRLS=false must accept the stripped table, got findings=%+v err=%v", findings, err)
		}
	})

	// --- runner surface: exit 1 (not-enforced) is distinct from 2 ---
	t.Run("runner_exit_codes", func(t *testing.T) {
		var out, errOut strings.Builder
		code := quarktenant.RunWithIO(ctx, []string{"verify-rls-policies"}, client, &out, &errOut)
		if code != quarktenant.ExitNotEnforced {
			t.Fatalf("want exit %d (not enforced), got %d\nstderr: %s", quarktenant.ExitNotEnforced, code, errOut.String())
		}
		if !strings.Contains(out.String(), "NOT ENFORCED\tqverify_orders") {
			t.Errorf("stdout must carry the machine line, got %q", out.String())
		}
		out.Reset()
		errOut.Reset()
		code = quarktenant.RunWithIO(ctx, []string{"verify-rls-policies", "--no-force-rls"}, client, &out, &errOut)
		if code != quarktenant.ExitSuccess {
			t.Fatalf("want exit 0 with --no-force-rls, got %d\nstderr: %s", code, errOut.String())
		}
	})
}
