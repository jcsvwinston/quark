// Copyright 2026 jcsvwinston
// SPDX-License-Identifier: Apache-2.0

package quark

import (
	"context"
	"database/sql"
	"errors"
	"io"
	"log/slog"
	"strings"
	"testing"
)

// A8 S8 (RLS-02, RLS-04). A RowLevelSecurityNative router fails closed on
// every door when the engine has no native RLS, and, on the engine that has
// it, refuses to serve a table whose policies it cannot confirm.

func TestNativeGetClientRefusesWithoutNativeRLS(t *testing.T) {
	c, _ := txConfinementClient(t, "s8_getclient")
	router := txConfinementRouter(c, RowLevelSecurityNative)
	ctx := txConfinementCtx("acme")
	if client, err := router.GetClient(ctx); client != nil || !errors.Is(err, ErrUnsupportedFeature) {
		t.Fatalf("GetClient under Native on sqlite: client=%v err=%v — the third door has to fail closed like For and Tx", client, err)
	}
	if _, err := For[txConfinementRow](ctx, router).List(); !errors.Is(err, ErrUnsupportedFeature) {
		t.Fatalf("For under Native on sqlite: %v", err)
	}
	if _, err := For[txConfinementRow](ctx, router).First(); !errors.Is(err, ErrUnsupportedFeature) {
		t.Fatalf("First under Native on sqlite: %v", err)
	}
}

// The policy check: a PostgreSQL-shaped client whose catalog cannot be read
// is refused with ErrRLSNotEnforced; the opt-out lets the query through to
// whatever the engine says next.
func TestNativeRouterRefusesUnverifiedPolicies(t *testing.T) {
	db, err := sql.Open("sqlite", "file:s8_verify?mode=memory&cache=shared")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	c, err := NewWithDB("postgres", db, WithLogger(slog.New(slog.NewTextHandler(io.Discard, nil))))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(context.Background(), "CREATE TABLE tx_confinement_rows (id INTEGER PRIMARY KEY, tenant_id TEXT, label TEXT)"); err != nil {
		t.Fatal(err)
	}
	ctx := txConfinementCtx("acme")

	router := txConfinementRouter(c, RowLevelSecurityNative)
	_, err = For[txConfinementRow](ctx, router).List()
	if !errors.Is(err, ErrRLSNotEnforced) || !strings.Contains(err.Error(), "tx_confinement_rows") {
		t.Fatalf("a Native router over a catalog it cannot read served, or refused for another reason: %v", err)
	}
	// Inside router.Tx the check runs on ForTx too; on this SQLite double
	// the transaction's set_config fails first (no such function), so the
	// Tx door is proven on PostgreSQL in internal/enginesuite, not here.

	skipping := txConfinementRouter(c, RowLevelSecurityNative)
	skipping.config.SkipPolicyVerification = true
	if _, err := For[txConfinementRow](ctx, skipping).List(); errors.Is(err, ErrRLSNotEnforced) {
		t.Fatalf("SkipPolicyVerification did not skip: %v", err)
	}
}
