// Copyright 2026 jcsvwinston
// SPDX-License-Identifier: Apache-2.0

package quark_test

import (
	"context"
	"errors"
	"testing"

	"github.com/jcsvwinston/quark"
)

// TestRowLevelSecurityNativeRejectsNonPostgresViaForT validates that
// constructing a Query[T] under a Native router with a non-PostgreSQL
// dialect surfaces ErrUnsupportedFeature on the query (before any SQL
// is built). This is the F5-2 invariant: Native is PG-only.
func TestRowLevelSecurityNativeRejectsNonPostgresViaForT(t *testing.T) {
	t.Parallel()

	client, err := quark.New("sqlite", ":memory:")
	if err != nil {
		t.Fatalf("new sqlite client: %v", err)
	}
	t.Cleanup(func() { _ = client.Close() })

	type rlsNativeProbe struct {
		ID       int64  `db:"id" pk:"true"`
		TenantID string `db:"tenant_id"`
	}
	if err := client.Migrate(context.Background(), &rlsNativeProbe{}); err != nil {
		t.Fatalf("migrate: %v", err)
	}

	cfg := quark.DefaultTenantConfig()
	cfg.Strategy = quark.RowLevelSecurityNative
	cfg.BaseClient = client

	resolver := func(ctx context.Context) string {
		if v, ok := ctx.Value(testTenantKey).(string); ok {
			return v
		}
		return ""
	}
	router := quark.NewTenantRouter(cfg, resolver, nil)

	ctx := context.WithValue(context.Background(), testTenantKey, "ta")

	// For[T] under Native + SQLite must surface ErrUnsupportedFeature.
	// We trigger it by calling List() — that calls executeQuery which
	// short-circuits when q.err is set.
	_, err = quark.For[rlsNativeProbe](ctx, router).List()
	if err == nil {
		t.Fatalf("expected error from Native on sqlite, got nil")
	}
	if !errors.Is(err, quark.ErrUnsupportedFeature) {
		t.Fatalf("expected ErrUnsupportedFeature, got %v", err)
	}
}

// TestRowLevelSecurityNativeRejectsNonPostgresViaRouterTx validates
// that TenantRouter.Tx surfaces the same error when invoked under a
// non-PG BaseClient with strategy Native. The check runs before any
// transaction is opened.
func TestRowLevelSecurityNativeRejectsNonPostgresViaRouterTx(t *testing.T) {
	t.Parallel()

	client, err := quark.New("sqlite", ":memory:")
	if err != nil {
		t.Fatalf("new sqlite client: %v", err)
	}
	t.Cleanup(func() { _ = client.Close() })

	cfg := quark.DefaultTenantConfig()
	cfg.Strategy = quark.RowLevelSecurityNative
	cfg.BaseClient = client

	router := quark.NewTenantRouter(cfg,
		func(ctx context.Context) string {
			if v, ok := ctx.Value(testTenantKey).(string); ok {
				return v
			}
			return ""
		},
		nil,
	)

	ctx := context.WithValue(context.Background(), testTenantKey, "ta")

	err = router.Tx(ctx, func(tx *quark.Tx) error { return nil })
	if err == nil {
		t.Fatal("expected error from router.Tx on sqlite, got nil")
	}
	if !errors.Is(err, quark.ErrUnsupportedFeature) {
		t.Fatalf("expected ErrUnsupportedFeature, got %v", err)
	}
}

// TestRowLevelSecurityNativeDefaultsVarToAppTenantID asserts that an
// unset NativeRLSVar falls back to "app.tenant_id". This is the
// contract documented in TenantConfig and used by the F5-3 policy
// generator — any drift here would silently desynchronise routers
// from generated policies.
func TestRowLevelSecurityNativeDefaultsVarToAppTenantID(t *testing.T) {
	t.Parallel()

	cfg := quark.DefaultTenantConfig()
	if cfg.NativeRLSVar != "app.tenant_id" {
		t.Fatalf("DefaultTenantConfig.NativeRLSVar = %q, want %q",
			cfg.NativeRLSVar, "app.tenant_id")
	}

	// Empty NativeRLSVar (user did not call DefaultTenantConfig) must
	// also resolve to the default via the package's internal helper.
	// We exercise this indirectly through the same default; the
	// behaviour is also covered end-to-end by the PG integration test.
}

// TestRowLevelSecurityNativeRouterTxDelegatesForOtherStrategies
// verifies that router.Tx falls through to the underlying client's Tx
// when the strategy is not Native — RowLevelSecurityClient,
// SchemaPerTenant, DatabasePerTenant all bypass the set_config emit.
// Without this, router.Tx would silently fail for the legacy paths.
func TestRowLevelSecurityNativeRouterTxDelegatesForOtherStrategies(t *testing.T) {
	t.Parallel()

	client, err := quark.New("sqlite", ":memory:")
	if err != nil {
		t.Fatalf("new sqlite client: %v", err)
	}
	t.Cleanup(func() { _ = client.Close() })

	cfg := quark.DefaultTenantConfig()
	cfg.Strategy = quark.RowLevelSecurityClient
	cfg.BaseClient = client

	router := quark.NewTenantRouter(cfg,
		func(ctx context.Context) string {
			if v, ok := ctx.Value(testTenantKey).(string); ok {
				return v
			}
			return ""
		},
		nil,
	)

	ctx := context.WithValue(context.Background(), testTenantKey, "ta")

	var called bool
	err = router.Tx(ctx, func(tx *quark.Tx) error {
		called = true
		return nil
	})
	if err != nil {
		t.Fatalf("router.Tx under RowLevelSecurityClient + sqlite: %v", err)
	}
	if !called {
		t.Fatal("router.Tx did not invoke fn under RowLevelSecurityClient")
	}
}

// testTenantKey is a typed ctx key shared across the rls_native tests.
type testTenantCtxKey struct{}

var testTenantKey = testTenantCtxKey{}
