// Copyright 2026 jcsvwinston
// SPDX-License-Identifier: Apache-2.0

package quarktenant_test

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/jcsvwinston/quark"
	"github.com/jcsvwinston/quark/quarktenant"
)

// Preflight guards of VerifyRLSPolicies that need no database: nil client,
// non-PostgreSQL dialect, and the empty registry (verifying nothing proves
// nothing — same contract as InstallRLSPolicies).
func TestVerifyRLSPolicies_Preflight(t *testing.T) {
	ctx := context.Background()

	if _, err := quarktenant.VerifyRLSPolicies(ctx, nil, quarktenant.DefaultInstallOptions()); err == nil {
		t.Fatal("nil client must error")
	}

	sqlite, err := quark.New("sqlite", ":memory:")
	if err != nil {
		t.Fatalf("sqlite client: %v", err)
	}
	t.Cleanup(func() { _ = sqlite.Close() })

	_, err = quarktenant.VerifyRLSPolicies(ctx, sqlite, quarktenant.DefaultInstallOptions())
	if !errors.Is(err, quark.ErrUnsupportedFeature) {
		t.Fatalf("non-postgres dialect: want ErrUnsupportedFeature, got %v", err)
	}
	if !strings.Contains(err.Error(), "sqlite") {
		t.Errorf("error must name the dialect, got %v", err)
	}
}

// ParseAction accepts the new verify action and its unknown-action message
// lists both actions.
func TestParseAction_Verify(t *testing.T) {
	a, err := quarktenant.ParseAction("verify-rls-policies")
	if err != nil || a != quarktenant.ActionVerifyRLSPolicies {
		t.Fatalf("parse verify: got %v, %v", a, err)
	}
	_, err = quarktenant.ParseAction("bogus")
	if err == nil || !strings.Contains(err.Error(), "verify-rls-policies") {
		t.Fatalf("unknown-action message must list verify-rls-policies, got %v", err)
	}
}
