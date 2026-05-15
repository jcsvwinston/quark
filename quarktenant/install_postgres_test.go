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

// TestInstallRLSPolicies_Postgres exercises the F5-3 install
// pipeline end-to-end against a real PostgreSQL engine: register
// three models, run dry-run, apply, then assert pg_policies records
// the resulting policies. Skips when QUARK_TEST_POSTGRES_DSN is not
// set (and the integration build tag isn't active to spin a
// container) — same convention as the rest of the PG suite.
func TestInstallRLSPolicies_Postgres(t *testing.T) {
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

	// Models scoped by tenant_id (default column).
	type qtenantOrder struct {
		ID       int64  `db:"id" pk:"true"`
		TenantID string `db:"tenant_id"`
		Status   string `db:"status"`
	}
	type qtenantInvoice struct {
		ID       int64  `db:"id" pk:"true"`
		TenantID string `db:"tenant_id"`
		Amount   int64  `db:"amount"`
	}

	tables := []string{"qtenant_orders", "qtenant_invoices"}
	// Idempotent cleanup before AND after — leftover policies from
	// a previous failed run would otherwise produce duplicate-object
	// errors on apply.
	cleanup := func() {
		for _, tbl := range tables {
			_ = client.Exec(ctx, "DROP TABLE IF EXISTS "+tbl+" CASCADE")
		}
	}
	cleanup()
	t.Cleanup(cleanup)

	if err := client.RegisterModel(&qtenantOrder{}, &qtenantInvoice{}); err != nil {
		t.Fatalf("register: %v", err)
	}
	if err := client.MigrateRegistered(ctx); err != nil {
		t.Fatalf("migrate: %v", err)
	}

	// --- dry-run path: every statement rendered, no DB change ---
	t.Run("dry_run_renders_without_apply", func(t *testing.T) {
		opts := quarktenant.DefaultInstallOptions()
		opts.DryRun = true
		stmts, err := quarktenant.InstallRLSPolicies(ctx, client, opts)
		if err != nil {
			t.Fatalf("dry-run: %v", err)
		}
		// 3 statements per model (ENABLE, FORCE, CREATE POLICY) × 2 models.
		if len(stmts) != 6 {
			t.Fatalf("dry-run produced %d stmts, want 6 (3 per model × 2 models)", len(stmts))
		}
		// Each table should appear in ENABLE and CREATE POLICY.
		for _, tbl := range tables {
			found := 0
			for _, s := range stmts {
				if strings.Contains(s, `"`+tbl+`"`) {
					found++
				}
			}
			if found < 3 {
				t.Errorf("table %s appears in %d stmts, want >=3", tbl, found)
			}
		}
		// Dry-run must NOT touch pg_policies.
		if got := countPolicies(t, client, tables[0]); got != 0 {
			t.Fatalf("dry-run left %d policies on %s; should leave 0", got, tables[0])
		}
	})

	// --- apply path: pg_policies grows by one per table ---
	t.Run("apply_installs_policies", func(t *testing.T) {
		opts := quarktenant.DefaultInstallOptions()
		opts.DryRun = false
		stmts, err := quarktenant.InstallRLSPolicies(ctx, client, opts)
		if err != nil {
			t.Fatalf("apply: %v\nstmts: %v", err, stmts)
		}
		for _, tbl := range tables {
			if got := countPolicies(t, client, tbl); got != 1 {
				t.Errorf("apply: table %s has %d policies, want 1", tbl, got)
			}
		}
		// Verify the policy name follows the documented convention.
		for _, tbl := range tables {
			name := policyName(t, client, tbl)
			want := tbl + "_tenant_isolation"
			if name != want {
				t.Errorf("apply: table %s policy = %q, want %q", tbl, name, want)
			}
		}
	})

	// --- re-apply on top of an existing policy fails loudly ---
	t.Run("apply_twice_fails_with_duplicate_object_sqlstate", func(t *testing.T) {
		opts := quarktenant.DefaultInstallOptions()
		_, err := quarktenant.InstallRLSPolicies(ctx, client, opts)
		if err == nil {
			t.Fatal("expected duplicate-policy error on re-apply, got nil")
		}
		// PostgreSQL surfaces 42710 (duplicate_object). Asserting on
		// SQLSTATE rather than the message text is robust against
		// driver wording changes — same pattern as isPGLockTimeout in
		// dialect_migration_lock.go.
		type sqlStater interface{ SQLState() string }
		var se sqlStater
		if !errors.As(err, &se) || se.SQLState() != "42710" {
			t.Errorf("expected SQLSTATE 42710 (duplicate_object), got %v", err)
		}
		// Sanity check: the wrapped error still mentions the policy
		// name so an operator reading the log knows which object
		// collided.
		if !strings.Contains(err.Error(), "tenant_isolation") {
			t.Errorf("error should mention policy name, got %v", err)
		}
	})
}

// TestInstallRLSPolicies_Postgres_NoTenantColumn exercises the
// validation branch: a model missing the configured column should be
// rejected before any DDL is rendered.
func TestInstallRLSPolicies_Postgres_NoTenantColumn(t *testing.T) {
	dsn := postgresDSN(t)
	if dsn == "" {
		t.Skip("postgres DSN unavailable: set QUARK_TEST_POSTGRES_DSN or run with -tags=integration")
	}
	ctx := context.Background()
	client, err := quark.New("pgx", dsn)
	if err != nil {
		t.Fatalf("new: %v", err)
	}
	t.Cleanup(func() { _ = client.Close() })

	type qtenantNoCol struct {
		ID   int64  `db:"id" pk:"true"`
		Name string `db:"name"`
	}
	if err := client.RegisterModel(&qtenantNoCol{}); err != nil {
		t.Fatalf("register: %v", err)
	}

	_, err = quarktenant.InstallRLSPolicies(ctx, client, quarktenant.DefaultInstallOptions())
	if err == nil {
		t.Fatal("expected ErrNoTenantColumn, got nil")
	}
	if !strings.Contains(err.Error(), "missing the configured TenantColumn") {
		t.Errorf("error should be ErrNoTenantColumn, got %v", err)
	}
}

// postgresDSN is defined in postgres_dsn_default_test.go and
// postgres_dsn_integration_test.go. Default build returns
// `os.Getenv("QUARK_TEST_POSTGRES_DSN")`; under `-tags=integration`
// it boots a testcontainers PG container when the env var is empty.

// countPolicies returns the number of pg_policies rows for the given
// table.
func countPolicies(t *testing.T, client *quark.Client, table string) int {
	t.Helper()
	rows, err := client.RawQuery(t.Context(),
		"SELECT COUNT(*) FROM pg_policies WHERE tablename = $1", table)
	if err != nil {
		t.Fatalf("count policies: %v", err)
	}
	defer rows.Close()
	if !rows.Next() {
		t.Fatalf("no row from count(*)")
	}
	var n int
	if err := rows.Scan(&n); err != nil {
		t.Fatalf("scan count: %v", err)
	}
	return n
}

// policyName returns the policy name installed on the given table.
// Assumes exactly one policy exists; the caller has already asserted
// that.
func policyName(t *testing.T, client *quark.Client, table string) string {
	t.Helper()
	rows, err := client.RawQuery(t.Context(),
		"SELECT policyname FROM pg_policies WHERE tablename = $1", table)
	if err != nil {
		t.Fatalf("policy name: %v", err)
	}
	defer rows.Close()
	if !rows.Next() {
		t.Fatalf("no policy row")
	}
	var name string
	if err := rows.Scan(&name); err != nil {
		t.Fatalf("scan name: %v", err)
	}
	return name
}
