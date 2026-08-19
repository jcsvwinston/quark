// Copyright 2026 jcsvwinston
// SPDX-License-Identifier: Apache-2.0

// QCD-FW §1 investigation (external coverage demo, 2026-08-19): under
// RowLevelSecurityNative on PostgreSQL, a write can be NOT YET COMMITTED
// when the call returns. Path, on record:
//
//	Create → saveAny → executeQueryRow (query_crud.go:407)
//	       → nativeRLSExecutor.QueryRowContext (rls_native.go:462)
//	       → tx left open + context.AfterFunc(ctx, commit)
//
// Create DOES derive an operation-scoped ctx and cancels it on return
// (query_crud.go:514 — the #252 fix), but context.AfterFunc runs its
// callback IN A SEPARATE GOROUTINE: there is no happens-before between
// Create returning and the COMMIT executing. A handler can answer 2xx and
// an immediate reader on another connection may not see the row — the
// demo's 1-in-27 flake, and the same class the v1.3.1 notes fixed once
// ("the implicit transaction's lifecycle was tied to the request context,
// and its deferred commit raced the automatic rollback").
//
// The semantic contract this test pins: a WRITE is durable when its call
// returns. No sleeps — an immediate cross-connection read after every
// Create must see the row, every time.
package quark_test

import (
	"context"
	"database/sql"
	"fmt"
	"testing"
	"time"

	"github.com/jcsvwinston/quark"

	_ "github.com/jackc/pgx/v5/stdlib"
)

func TestRowLevelSecurityNativeWriteDurableOnReturn(t *testing.T) {
	dsn := resolvePostgresDSN(t)
	if dsn == "" {
		t.Skip("QUARK_TEST_POSTGRES_DSN not set (rebuild with -tags=integration to spin up a container)")
	}

	ctx := context.Background()

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

	type RLSDurabilityOrder struct {
		ID       int64  `db:"id" pk:"true"`
		TenantID string `db:"tenant_id"`
		Status   string `db:"status"`
	}

	const testRole = "quark_rls_durab"
	const testPassword = "quark_rls_durab_password"

	cleanup := func() {
		_ = adminClient.Exec(ctx, `DROP TABLE IF EXISTS rls_durability_orders CASCADE`)
		_ = adminClient.Exec(ctx, `DROP OWNED BY `+testRole+` CASCADE`)
		_ = adminClient.Exec(ctx, `DROP ROLE IF EXISTS `+testRole)
	}
	cleanup()
	t.Cleanup(cleanup)

	if err := adminClient.Migrate(ctx, &RLSDurabilityOrder{}); err != nil {
		t.Fatalf("migrate: %v", err)
	}

	nonSuperDSN, swapped := swapPGUser(dsn, testRole, testPassword)
	if !swapped {
		t.Skip("requires URL-form DSN to swap to a non-superuser role")
	}

	for _, stmt := range []string{
		`CREATE ROLE ` + testRole + ` WITH LOGIN NOSUPERUSER NOBYPASSRLS PASSWORD '` + testPassword + `'`,
		`GRANT USAGE ON SCHEMA public TO ` + testRole,
		`GRANT SELECT, INSERT, UPDATE, DELETE ON TABLE rls_durability_orders TO ` + testRole,
		`GRANT USAGE, SELECT ON ALL SEQUENCES IN SCHEMA public TO ` + testRole,
		`ALTER TABLE rls_durability_orders ENABLE ROW LEVEL SECURITY`,
		`ALTER TABLE rls_durability_orders FORCE ROW LEVEL SECURITY`,
		`CREATE POLICY rls_durability_tenant ON rls_durability_orders
		    USING (tenant_id = current_setting('app.tenant_id', true)::text)
		    WITH CHECK (tenant_id = current_setting('app.tenant_id', true)::text)`,
	} {
		if err := adminClient.Exec(ctx, stmt); err != nil {
			t.Fatalf("setup %q: %v", stmt, err)
		}
	}

	baseClient, err := quark.New("pgx", nonSuperDSN, quark.WithLimits(quark.Limits{
		MaxResults:   1000,
		QueryTimeout: 30 * time.Second,
	}))
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

	// The independent reader: a SEPARATE pool under the superuser role
	// (BYPASSRLS), exactly like the demo's verification query — so a
	// missing row cannot be RLS masking, only a missing commit.
	readerDB, err := sql.Open("pgx", dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = readerDB.Close() })

	// A long-lived request-like ctx — the natural handler shape. The write
	// must be durable the moment Create returns, so the immediate
	// cross-connection read must find it: every iteration, no sleeps.
	reqCtx := context.WithValue(ctx, testTenantKey, "ta")
	const iterations = 300
	for i := 0; i < iterations; i++ {
		row := RLSDurabilityOrder{TenantID: "ta", Status: fmt.Sprintf("s%d", i)}
		if err := quark.For[RLSDurabilityOrder](reqCtx, router).Create(&row); err != nil {
			t.Fatalf("iteration %d: Create: %v", i, err)
		}
		if row.ID == 0 {
			t.Fatalf("iteration %d: Create returned without a PK", i)
		}

		var n int
		if err := readerDB.QueryRowContext(ctx,
			`SELECT count(*) FROM rls_durability_orders WHERE id = $1`, row.ID,
		).Scan(&n); err != nil {
			t.Fatalf("iteration %d: verify read: %v", i, err)
		}
		if n != 1 {
			t.Fatalf("iteration %d: row id=%d NOT visible from another connection immediately after Create returned — the implicit-tx commit is asynchronous (context.AfterFunc goroutine) and the 2xx lies about durability (QCD-FW §1)", i, row.ID)
		}
	}
}
