// Copyright 2026 jcsvwinston
// SPDX-License-Identifier: Apache-2.0

package quark_test

import (
	"bytes"
	"context"
	"log/slog"
	"strings"
	"testing"

	"github.com/jcsvwinston/quark"
)

type rawWarnTenantKey struct{}

func newRawWarnClient(t *testing.T, buf *bytes.Buffer) *quark.Client {
	t.Helper()
	logger := slog.New(slog.NewJSONHandler(buf, &slog.HandlerOptions{Level: slog.LevelWarn}))
	c, err := quark.New("sqlite", ":memory:",
		quark.WithLogger(logger),
		quark.WithLimits(quark.Limits{AllowRawQueries: true}),
	)
	if err != nil {
		t.Fatalf("quark.New: %v", err)
	}
	t.Cleanup(func() { _ = c.Close() })
	return c
}

func rawWarnResolver(ctx context.Context) string {
	v, _ := ctx.Value(rawWarnTenantKey{}).(string)
	return v
}

// TestRawUnderNativeRLSWarns: once a Client is the BaseClient of a
// RowLevelSecurityNative router, RawQuery/Exec emit the
// quark.tenant.raw_under_native_rls warning when the call's context
// carries a tenant — and stay silent when it does not. The warning is
// dialect-independent (it keys off the router strategy + ctx, not the
// engine), so SQLite exercises it fully; PostgreSQL enforcement is
// covered separately by the Native RLS suite.
func TestRawUnderNativeRLSWarns(t *testing.T) {
	var buf bytes.Buffer
	base := newRawWarnClient(t, &buf)

	// Constructing the Native router stamps `base` with the resolver.
	_ = quark.NewTenantRouter(
		quark.TenantConfig{Strategy: quark.RowLevelSecurityNative, BaseClient: base},
		rawWarnResolver, nil,
	)

	withTenant := context.WithValue(context.Background(), rawWarnTenantKey{}, "acme")

	if _, err := base.RawQuery(withTenant, "SELECT ?", 1); err != nil {
		t.Fatalf("RawQuery (tenant): %v", err)
	}
	if err := base.Exec(withTenant, "SELECT ?", 1); err != nil {
		t.Fatalf("Exec (tenant): %v", err)
	}
	// No tenant in context → no warning.
	if _, err := base.RawQuery(context.Background(), "SELECT ?", 1); err != nil {
		t.Fatalf("RawQuery (no tenant): %v", err)
	}

	logs := buf.String()
	if got := strings.Count(logs, "quark.tenant.raw_under_native_rls"); got != 2 {
		t.Errorf("warning count = %d, want 2 (RawQuery+Exec with tenant; none without). logs:\n%s", got, logs)
	}
	if !strings.Contains(logs, `"tenant":"acme"`) {
		t.Errorf("warning should carry the resolved tenant. logs:\n%s", logs)
	}
}

// TestRawWithoutNativeRouterIsSilent: a Client that was never stamped by
// a Native router never warns, regardless of context — the check is a
// no-op for the default (single-tenant / client-side RLS) setup.
func TestRawWithoutNativeRouterIsSilent(t *testing.T) {
	var buf bytes.Buffer
	base := newRawWarnClient(t, &buf)

	// A non-Native router must NOT stamp the client.
	_ = quark.NewTenantRouter(
		quark.TenantConfig{Strategy: quark.RowLevelSecurityClient, BaseClient: base, TenantColumn: "tenant_id"},
		rawWarnResolver, nil,
	)

	withTenant := context.WithValue(context.Background(), rawWarnTenantKey{}, "acme")
	if _, err := base.RawQuery(withTenant, "SELECT ?", 1); err != nil {
		t.Fatalf("RawQuery: %v", err)
	}
	if strings.Contains(buf.String(), "quark.tenant.raw_under_native_rls") {
		t.Errorf("client-side RLS router must not trigger the native-raw warning. logs:\n%s", buf.String())
	}
}

// TestRawNativeNilResolverDoesNotPanic: a Native router built with a nil
// resolver stamps a nil nativeTenantResolver; the warning path must
// early-return on the nil check rather than panic.
func TestRawNativeNilResolverDoesNotPanic(t *testing.T) {
	var buf bytes.Buffer
	base := newRawWarnClient(t, &buf)

	_ = quark.NewTenantRouter(
		quark.TenantConfig{Strategy: quark.RowLevelSecurityNative, BaseClient: base},
		nil, nil,
	)

	if _, err := base.RawQuery(context.Background(), "SELECT ?", 1); err != nil {
		t.Fatalf("RawQuery: %v", err)
	}
	if strings.Contains(buf.String(), "quark.tenant.raw_under_native_rls") {
		t.Errorf("nil resolver must not warn. logs:\n%s", buf.String())
	}
}
