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

// TestVerifyRLSPolicies_PredicateIsChecked fabricates the dangerous
// condition instead of the happy path (QCD-QK-2).
//
// The package presents itself as the preflight that makes the silent
// Native-RLS leak loud. Until this test it only asked pg_policies whether
// a policy NAMED <table>_tenant_isolation existed — never what that policy
// says. A policy with the right name and `USING (true)` therefore earned a
// green light while every tenant read every row, which is worse than no
// check at all: a green check is what stops an operator from looking.
//
// Each sub-test installs the correct policy, then replaces it with one
// that is permissive in a different way, and demands that verify fails.
func TestVerifyRLSPolicies_PredicateIsChecked(t *testing.T) {
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

	type qpredOrder struct {
		ID       int64  `db:"id" pk:"true"`
		TenantID string `db:"tenant_id"`
		Status   string `db:"status"`
	}

	cleanup := func() { _ = client.Exec(ctx, "DROP TABLE IF EXISTS qpred_orders CASCADE") }
	cleanup()
	t.Cleanup(cleanup)

	if err := client.RegisterModel(&qpredOrder{}); err != nil {
		t.Fatalf("register: %v", err)
	}
	if err := client.MigrateRegistered(ctx); err != nil {
		t.Fatalf("migrate: %v", err)
	}

	opts := quarktenant.DefaultInstallOptions()
	if _, err := quarktenant.InstallRLSPolicies(ctx, client, opts); err != nil {
		t.Fatalf("install: %v", err)
	}
	// Sanity: the correctly installed policy must pass, or the rest of this
	// test proves nothing.
	if findings, err := quarktenant.VerifyRLSPolicies(ctx, client, opts); err != nil || len(findings) != 0 {
		t.Fatalf("the installed policy must verify clean, got findings=%+v err=%v", findings, err)
	}

	// replace swaps the isolation policy for one the caller describes.
	replace := func(t *testing.T, using, withCheck string) {
		t.Helper()
		if err := client.Exec(ctx, `DROP POLICY IF EXISTS qpred_orders_tenant_isolation ON qpred_orders`); err != nil {
			t.Fatalf("drop policy: %v", err)
		}
		stmt := `CREATE POLICY qpred_orders_tenant_isolation ON qpred_orders USING (` + using + `)`
		if withCheck != "" {
			stmt += ` WITH CHECK (` + withCheck + `)`
		}
		if err := client.Exec(ctx, stmt); err != nil {
			t.Fatalf("create permissive policy: %v", err)
		}
	}

	cases := []struct {
		name      string
		using     string
		withCheck string
		wantIn    string
	}{
		{
			// The demo's exact repro: right name, no predicate at all.
			name:      "using_true_reads_every_tenant",
			using:     "true",
			withCheck: "true",
			wantIn:    "predicate",
		},
		{
			// Filters on a column that is not the tenant column: looks
			// deliberate, isolates nothing.
			name:      "filters_the_wrong_column",
			using:     "status = current_setting('app.tenant_id', true)",
			withCheck: "status = current_setting('app.tenant_id', true)",
			wantIn:    "tenant_id",
		},
		{
			// Reads the wrong session variable: the router sets
			// app.tenant_id, so this predicate is always NULL → no rows,
			// or worse, whatever an unrelated variable holds.
			name:      "reads_the_wrong_session_variable",
			using:     "tenant_id = current_setting('app.something_else', true)",
			withCheck: "tenant_id = current_setting('app.something_else', true)",
			wantIn:    "app.tenant_id",
		},
		{
			// Reads are isolated, writes are not: a tenant can plant rows
			// under another tenant's id.
			name:      "write_path_left_open",
			using:     "tenant_id = current_setting('app.tenant_id', true)",
			withCheck: "true",
			wantIn:    "WITH CHECK",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			replace(t, tc.using, tc.withCheck)

			findings, err := quarktenant.VerifyRLSPolicies(ctx, client, opts)
			if !errors.Is(err, quarktenant.ErrRLSNotEnforced) {
				t.Fatalf("a policy that does not isolate must FAIL the preflight; got err=%v findings=%+v", err, findings)
			}
			if len(findings) != 1 || findings[0].Table != "qpred_orders" {
				t.Fatalf("want exactly the permissive table, got %+v", findings)
			}
			if !strings.Contains(err.Error(), tc.wantIn) {
				t.Errorf("the error must explain WHAT is wrong (want %q), got:\n%v", tc.wantIn, err)
			}
		})
	}
}
