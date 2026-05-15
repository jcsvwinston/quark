// Copyright 2026 jcsvwinston
// SPDX-License-Identifier: Apache-2.0

package quark_test

import (
	"context"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/jcsvwinston/quark"

	_ "github.com/jackc/pgx/v5/stdlib"
)

// swapPGUser produces a DSN identical to dsn but with the user and
// password fields replaced. Used by the RLS native test to derive a
// non-superuser DSN from the testcontainers-go default (which creates
// the configured user as SUPERUSER, and superusers bypass RLS
// unconditionally — even with FORCE ROW LEVEL SECURITY).
//
// Supports URL-form DSNs (postgres://… or postgresql://…), which is
// what testcontainers-go and the standard CI matrix use. Returns the
// original DSN unchanged when the form is key-value — that path skips
// the role swap and the test will fall through to the superuser DSN;
// callers can handle that case by skipping the RLS isolation
// assertions when running outside the canonical URL DSN form.
func swapPGUser(dsn, user, password string) (string, bool) {
	if !(strings.HasPrefix(dsn, "postgres://") || strings.HasPrefix(dsn, "postgresql://")) {
		return dsn, false
	}
	u, err := url.Parse(dsn)
	if err != nil {
		return dsn, false
	}
	u.User = url.UserPassword(user, password)
	return u.String(), true
}

// TestRowLevelSecurityNativePostgresIsolation exercises the F5-2
// guarantee against a real PostgreSQL engine: with `CREATE POLICY`
// referencing the session variable, both router.Tx and the implicit-tx
// For[T] path return only the rows visible to the resolved tenant.
//
// Runs only when QUARK_TEST_POSTGRES_DSN is set (env-var path) or
// under `-tags=integration` (testcontainers path). Skips otherwise —
// SQLite cannot honour `CREATE POLICY` so this test must be PG-bound.
func TestRowLevelSecurityNativePostgresIsolation(t *testing.T) {
	dsn := resolvePostgresDSN(t)
	if dsn == "" {
		t.Skip("QUARK_TEST_POSTGRES_DSN not set (rebuild with -tags=integration to spin up a container)")
	}

	ctx := context.Background()

	// AllowRawQueries=true is required because the test installs the
	// CREATE POLICY DDL via baseClient.Exec — that path goes through
	// the SQLGuard, which rejects raw queries by default.
	adminLimits := quark.Limits{
		AllowRawQueries: true,
		MaxResults:      1000,
		QueryTimeout:    30 * time.Second,
	}
	adminClient, err := quark.New("pgx", dsn, quark.WithLimits(adminLimits))
	if err != nil {
		t.Fatalf("new admin pgx client: %v", err)
	}
	t.Cleanup(func() { _ = adminClient.Close() })

	type RLSNativeOrder struct {
		ID       int64  `db:"id" pk:"true"`
		TenantID string `db:"tenant_id"`
		Status   string `db:"status"`
	}

	const testRole = "quark_rls_test"
	const testPassword = "quark_rls_test_password"

	// Tear-down: drop role and table from any previous run, plus this
	// run's leftovers. CASCADE on the role cleans up the grants it
	// owns; on the table it removes orphaned policies.
	cleanup := func() {
		// REASSIGN is required before DROP ROLE when the role owns
		// objects from prior partial runs.
		_ = adminClient.Exec(ctx, `DROP TABLE IF EXISTS rls_native_orders CASCADE`)
		_ = adminClient.Exec(ctx, `DROP OWNED BY `+testRole+` CASCADE`)
		_ = adminClient.Exec(ctx, `DROP ROLE IF EXISTS `+testRole)
	}
	cleanup()
	t.Cleanup(cleanup)

	if err := adminClient.Migrate(ctx, &RLSNativeOrder{}); err != nil {
		t.Fatalf("migrate: %v", err)
	}

	// testcontainers-go's PostgreSQL module creates the configured
	// user as SUPERUSER. Superusers — and only superusers — bypass
	// row-level security even with FORCE ROW LEVEL SECURITY enabled.
	// To actually exercise the policies, we create a non-superuser
	// role, grant it the privileges it needs, and reconnect the test
	// session under that role. If the DSN form is key-value rather
	// than URL (which the swap helper can't rewrite), we skip the
	// isolation assertions because the test would produce false
	// positives under a superuser session.
	nonSuperDSN, swapped := swapPGUser(dsn, testRole, testPassword)
	if !swapped {
		t.Skip("RLS isolation test requires URL-form DSN to swap to a non-superuser role; got key-value form")
	}

	roleSetup := []string{
		`CREATE ROLE ` + testRole + ` WITH LOGIN NOSUPERUSER NOBYPASSRLS PASSWORD '` + testPassword + `'`,
		`GRANT USAGE ON SCHEMA public TO ` + testRole,
		`GRANT SELECT, INSERT, UPDATE, DELETE ON TABLE rls_native_orders TO ` + testRole,
		`GRANT USAGE, SELECT ON ALL SEQUENCES IN SCHEMA public TO ` + testRole,
	}
	for _, stmt := range roleSetup {
		if err := adminClient.Exec(ctx, stmt); err != nil {
			t.Fatalf("role setup %q: %v", stmt, err)
		}
	}

	// Install the policy referencing the session variable. The
	// `quark tenant install-rls-policies` CLI (F5-3) will eventually
	// emit this; for the F5-2 test we install it manually so the test
	// is self-contained. FORCE ROW LEVEL SECURITY is what stops the
	// table OWNER from bypassing the policy — relevant because the
	// non-superuser role here was granted DML privileges but not
	// table ownership; with FORCE, the policy applies regardless.
	policyDDL := []string{
		`ALTER TABLE rls_native_orders ENABLE ROW LEVEL SECURITY`,
		`ALTER TABLE rls_native_orders FORCE ROW LEVEL SECURITY`,
		`CREATE POLICY rls_native_orders_tenant_isolation ON rls_native_orders
		    USING (tenant_id = current_setting('app.tenant_id', true)::text)
		    WITH CHECK (tenant_id = current_setting('app.tenant_id', true)::text)`,
	}
	for _, stmt := range policyDDL {
		if err := adminClient.Exec(ctx, stmt); err != nil {
			t.Fatalf("policy DDL %q: %v", stmt, err)
		}
	}

	// Build the non-superuser baseClient that the rest of the test
	// uses for every router/For[T] interaction. The policy applies to
	// this role unconditionally.
	baseClient, err := quark.New("pgx", nonSuperDSN, quark.WithLimits(quark.Limits{
		AllowRawQueries: false,
		MaxResults:      1000,
		QueryTimeout:    30 * time.Second,
	}))
	if err != nil {
		t.Fatalf("new non-super pgx client: %v", err)
	}
	t.Cleanup(func() { _ = baseClient.Close() })

	// Build the Native router on the non-superuser client. Seeding
	// goes through router.Tx so each insert runs under the right
	// `set_config` and satisfies the policy's WITH CHECK clause.
	cfg := quark.DefaultTenantConfig()
	cfg.Strategy = quark.RowLevelSecurityNative
	cfg.BaseClient = baseClient

	router := quark.NewTenantRouter(cfg,
		func(c context.Context) string {
			if v, ok := c.Value(testTenantKey).(string); ok {
				return v
			}
			return ""
		},
		nil,
	)

	seed := func(tenantID string, rows []RLSNativeOrder) {
		t.Helper()
		seedCtx := context.WithValue(ctx, testTenantKey, tenantID)
		err := router.Tx(seedCtx, func(tx *quark.Tx) error {
			for i := range rows {
				if err := quark.ForTx[RLSNativeOrder](seedCtx, tx).Create(&rows[i]); err != nil {
					return err
				}
			}
			return nil
		})
		if err != nil {
			t.Fatalf("seed tenant %s: %v", tenantID, err)
		}
	}
	seed("ta", []RLSNativeOrder{
		{TenantID: "ta", Status: "pending"},
		{TenantID: "ta", Status: "paid"},
		{TenantID: "ta", Status: "shipped"},
	})
	seed("tb", []RLSNativeOrder{
		{TenantID: "tb", Status: "pending"},
		{TenantID: "tb", Status: "paid"},
	})

	ctxTA := context.WithValue(ctx, testTenantKey, "ta")
	ctxTB := context.WithValue(ctx, testTenantKey, "tb")

	// --- router.Tx path: explicit transaction, single set_config emit ---
	t.Run("router.Tx_ta_sees_only_ta_rows", func(t *testing.T) {
		var got []RLSNativeOrder
		err := router.Tx(ctxTA, func(tx *quark.Tx) error {
			var inner error
			got, inner = quark.ForTx[RLSNativeOrder](ctxTA, tx).List()
			return inner
		})
		if err != nil {
			t.Fatalf("router.Tx: %v", err)
		}
		if len(got) != 3 {
			t.Fatalf("ta should see 3 rows (its own); got %d: %+v", len(got), got)
		}
		for _, r := range got {
			if r.TenantID != "ta" {
				t.Errorf("ta saw row from tenant %s: %+v", r.TenantID, r)
			}
		}
	})
	t.Run("router.Tx_tb_sees_only_tb_rows", func(t *testing.T) {
		var got []RLSNativeOrder
		err := router.Tx(ctxTB, func(tx *quark.Tx) error {
			var inner error
			got, inner = quark.ForTx[RLSNativeOrder](ctxTB, tx).List()
			return inner
		})
		if err != nil {
			t.Fatalf("router.Tx: %v", err)
		}
		if len(got) != 2 {
			t.Fatalf("tb should see 2 rows (its own); got %d: %+v", len(got), got)
		}
		for _, r := range got {
			if r.TenantID != "tb" {
				t.Errorf("tb saw row from tenant %s: %+v", r.TenantID, r)
			}
		}
	})

	// --- For[T] implicit-tx path: each operation gets its own tx + set_config ---
	t.Run("For_T_implicit_tx_ta", func(t *testing.T) {
		got, err := quark.For[RLSNativeOrder](ctxTA, router).List()
		if err != nil {
			t.Fatalf("For[T].List under Native (ta): %v", err)
		}
		if len(got) != 3 {
			t.Fatalf("ta For[T] should see 3 rows; got %d", len(got))
		}
		for _, r := range got {
			if r.TenantID != "ta" {
				t.Errorf("For[T] ta saw row from tenant %s: %+v", r.TenantID, r)
			}
		}
	})
	t.Run("For_T_implicit_tx_tb", func(t *testing.T) {
		got, err := quark.For[RLSNativeOrder](ctxTB, router).List()
		if err != nil {
			t.Fatalf("For[T].List under Native (tb): %v", err)
		}
		if len(got) != 2 {
			t.Fatalf("tb For[T] should see 2 rows; got %d", len(got))
		}
	})

	// --- Count is QueryRow path: validates the *sql.Row branch of nativeRLSExecutor ---
	t.Run("For_T_Count_via_QueryRow", func(t *testing.T) {
		n, err := quark.For[RLSNativeOrder](ctxTA, router).Count()
		if err != nil {
			t.Fatalf("Count under Native (ta): %v", err)
		}
		if n != 3 {
			t.Fatalf("ta Count = %d, want 3", n)
		}
	})

	// --- ExecContext path: Create under Native via For[T] hits ExecContext / QueryRowContext ---
	t.Run("For_T_Create_under_native_inserts_for_correct_tenant", func(t *testing.T) {
		newRow := RLSNativeOrder{TenantID: "ta", Status: "delivered"}
		if err := quark.For[RLSNativeOrder](ctxTA, router).Create(&newRow); err != nil {
			t.Fatalf("Create under Native (ta): %v", err)
		}
		if newRow.ID == 0 {
			t.Fatal("Create did not populate PK from RETURNING")
		}
		// ta now has 4 rows; tb still sees 2.
		n, err := quark.For[RLSNativeOrder](ctxTA, router).Count()
		if err != nil {
			t.Fatalf("Count after insert (ta): %v", err)
		}
		if n != 4 {
			t.Fatalf("ta Count after insert = %d, want 4", n)
		}
		n, err = quark.For[RLSNativeOrder](ctxTB, router).Count()
		if err != nil {
			t.Fatalf("Count after insert (tb): %v", err)
		}
		if n != 2 {
			t.Fatalf("tb Count after insert = %d, want 2", n)
		}
	})
}
