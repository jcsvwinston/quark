// Copyright 2026 jcsvwinston
// SPDX-License-Identifier: Apache-2.0

package quark_test

import (
	"context"
	"testing"
	"time"

	"github.com/jcsvwinston/quark"
	"github.com/jcsvwinston/quark/cache/memory"

	_ "github.com/jackc/pgx/v5/stdlib"
)

// TestRowLevelSecurityNativeCacheIsTenantScoped pins that the L2 cache is
// part of the tenant isolation boundary under RowLevelSecurityNative.
//
// Under the native strategy For[T] injects no WHERE predicate and fills
// neither q.tenantID nor q.schema — the PostgreSQL policy does the
// filtering server-side. But generateCacheKey hashes exactly
// (dialect, tenantID, schema, SQL, args), so two tenants issuing the same
// builder query produce the SAME key over the SAME shared cacheStore
// (one BaseClient, one store). The engine's RLS protects the database;
// nothing protected the cache: tenant A's .Cache() fill was served to
// tenant B verbatim.
//
// The test drives the REAL doors only — quark.New + NewTenantRouter +
// For[T].Cache().List() — because the unit tests of generateCacheKey
// construct BaseQuery by hand with tenantID pre-filled, which encodes the
// assumption that some strategy always fills it. Native does not.
func TestRowLevelSecurityNativeCacheIsTenantScoped(t *testing.T) {
	dsn := resolvePostgresDSN(t)
	if dsn == "" {
		t.Skip("QUARK_TEST_POSTGRES_DSN not set (rebuild with -tags=integration to spin up a container)")
	}

	ctx := context.Background()

	adminClient, err := quark.New("pgx", dsn, quark.WithLimits(quark.Limits{
		AllowRawQueries: true,
		MaxResults:      1000,
		QueryTimeout:    30 * time.Second,
	}))
	if err != nil {
		t.Fatalf("new admin pgx client: %v", err)
	}
	t.Cleanup(func() { _ = adminClient.Close() })

	type CacheRLSOrder struct {
		ID       int64  `db:"id" pk:"true"`
		TenantID string `db:"tenant_id"`
		Status   string `db:"status"`
	}

	const testRole = "quark_rls_cache_test"
	const testPassword = "quark_rls_cache_test_password"

	cleanup := func() {
		_ = adminClient.Exec(ctx, `DROP TABLE IF EXISTS cache_rls_orders CASCADE`)
		_ = adminClient.Exec(ctx, `DROP OWNED BY `+testRole+` CASCADE`)
		_ = adminClient.Exec(ctx, `DROP ROLE IF EXISTS `+testRole)
	}
	cleanup()
	t.Cleanup(cleanup)

	if err := adminClient.Migrate(ctx, &CacheRLSOrder{}); err != nil {
		t.Fatalf("migrate: %v", err)
	}

	nonSuperDSN, swapped := swapPGUser(dsn, testRole, testPassword)
	if !swapped {
		t.Skip("RLS cache test requires URL-form DSN to swap to a non-superuser role; got key-value form")
	}

	roleSetup := []string{
		`CREATE ROLE ` + testRole + ` WITH LOGIN NOSUPERUSER NOBYPASSRLS PASSWORD '` + testPassword + `'`,
		`GRANT USAGE ON SCHEMA public TO ` + testRole,
		`GRANT SELECT, INSERT, UPDATE, DELETE ON TABLE cache_rls_orders TO ` + testRole,
		`GRANT USAGE, SELECT ON ALL SEQUENCES IN SCHEMA public TO ` + testRole,
	}
	for _, stmt := range roleSetup {
		if err := adminClient.Exec(ctx, stmt); err != nil {
			t.Fatalf("role setup %q: %v", stmt, err)
		}
	}

	policyDDL := []string{
		`ALTER TABLE cache_rls_orders ENABLE ROW LEVEL SECURITY`,
		`ALTER TABLE cache_rls_orders FORCE ROW LEVEL SECURITY`,
		`CREATE POLICY cache_rls_orders_tenant_isolation ON cache_rls_orders
		    USING (tenant_id = current_setting('app.tenant_id', true)::text)
		    WITH CHECK (tenant_id = current_setting('app.tenant_id', true)::text)`,
	}
	for _, stmt := range policyDDL {
		if err := adminClient.Exec(ctx, stmt); err != nil {
			t.Fatalf("policy DDL %q: %v", stmt, err)
		}
	}

	// ONE base client, ONE shared cache store: exactly the production
	// shape (client.go builds a single cacheStore per client, and the
	// native router hands every tenant the same BaseClient).
	baseClient, err := quark.New("pgx", nonSuperDSN,
		quark.WithLimits(quark.Limits{
			MaxResults:   1000,
			QueryTimeout: 30 * time.Second,
		}),
		quark.WithCacheStore(memory.New()),
	)
	if err != nil {
		t.Fatalf("new non-super pgx client: %v", err)
	}
	t.Cleanup(func() { _ = baseClient.Close() })

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

	seed := func(tenantID string, rows []CacheRLSOrder) {
		t.Helper()
		seedCtx := context.WithValue(ctx, testTenantKey, tenantID)
		err := router.Tx(seedCtx, func(tx *quark.Tx) error {
			for i := range rows {
				if err := quark.ForTx[CacheRLSOrder](seedCtx, tx).Create(&rows[i]); err != nil {
					return err
				}
			}
			return nil
		})
		if err != nil {
			t.Fatalf("seed tenant %s: %v", tenantID, err)
		}
	}
	seed("ta", []CacheRLSOrder{
		{TenantID: "ta", Status: "pending"},
		{TenantID: "ta", Status: "paid"},
		{TenantID: "ta", Status: "shipped"},
	})
	seed("tb", []CacheRLSOrder{
		{TenantID: "tb", Status: "pending"},
		{TenantID: "tb", Status: "paid"},
	})

	ctxTA := context.WithValue(ctx, testTenantKey, "ta")
	ctxTB := context.WithValue(ctx, testTenantKey, "tb")

	// Control: WITHOUT the cache the engine isolates. If this fails the
	// environment is broken and the cache assertions below prove nothing.
	plainTB, err := quark.For[CacheRLSOrder](ctxTB, router).OrderBy("id", "ASC").List()
	if err != nil {
		t.Fatalf("control list tb (no cache): %v", err)
	}
	if len(plainTB) != 2 {
		t.Fatalf("control: engine RLS must give tb exactly its 2 rows, got %d", len(plainTB))
	}

	// Tenant A fills the cache with ITS rows.
	listTA, err := quark.For[CacheRLSOrder](ctxTA, router).OrderBy("id", "ASC").Cache(time.Minute).List()
	if err != nil {
		t.Fatalf("cached list ta: %v", err)
	}
	if len(listTA) != 3 {
		t.Fatalf("precondition: ta must see its 3 rows, got %d", len(listTA))
	}

	// Tenant B issues the SAME builder query. The engine would answer
	// with tb's 2 rows; a tenant-blind cache key answers with ta's 3.
	listTB, err := quark.For[CacheRLSOrder](ctxTB, router).OrderBy("id", "ASC").Cache(time.Minute).List()
	if err != nil {
		t.Fatalf("cached list tb: %v", err)
	}
	for _, row := range listTB {
		if row.TenantID != "tb" {
			t.Fatalf("CROSS-TENANT LEAK: tenant tb was served a cached row of tenant %q (id=%d status=%s)",
				row.TenantID, row.ID, row.Status)
		}
	}
	if len(listTB) != 2 {
		t.Fatalf("tenant tb must see exactly its 2 rows through the cache path, got %d", len(listTB))
	}
}
