// Copyright 2026 jcsvwinston
// SPDX-License-Identifier: Apache-2.0

package quark_test

import (
	"testing"

	"github.com/jcsvwinston/quark"
)

// TestPlan_Hash_DeterministicAndStable: same plan value produces
// the same hash across multiple calls and across distinct Plan
// values that carry equivalent ops. Different plans must produce
// different hashes — this is the safety boundary for resume.
func TestPlan_Hash_DeterministicAndStable(t *testing.T) {
	p1 := quark.Plan{Ops: []quark.Operation{
		quark.OpCreateTable{Table: quark.Table{Name: "users"}},
		quark.OpAddColumn{Table: "users", Column: quark.Column{Name: "email", Type: "TEXT"}},
	}}
	p2 := quark.Plan{Ops: []quark.Operation{
		quark.OpCreateTable{Table: quark.Table{Name: "users"}},
		quark.OpAddColumn{Table: "users", Column: quark.Column{Name: "email", Type: "TEXT"}},
	}}
	p3 := quark.Plan{Ops: []quark.Operation{
		quark.OpCreateTable{Table: quark.Table{Name: "orders"}}, // different table
		quark.OpAddColumn{Table: "users", Column: quark.Column{Name: "email", Type: "TEXT"}},
	}}

	h1 := p1.Hash()
	if h1 != p1.Hash() {
		t.Errorf("Hash() should be stable across calls on the same Plan")
	}
	if h1 != p2.Hash() {
		t.Errorf("Equivalent plans should hash to the same value; p1=%q p2=%q", h1, p2.Hash())
	}
	if h1 == p3.Hash() {
		t.Errorf("Different plans should hash differently; both produced %q", h1)
	}
	// Empty plan has a stable, well-known hash too — used by the
	// applyPlanNoTx fast-skip for IsEmpty().
	if (quark.Plan{}).Hash() == "" {
		t.Errorf("empty plan should produce a non-empty hash (sha256 of empty string)")
	}
	if (quark.Plan{}).Hash() != (quark.Plan{}).Hash() {
		t.Errorf("two empty plans should hash equal")
	}
}

// TestPlan_Hash_OrderSensitive: two plans with the same OPS but
// different ORDER must hash differently. The diff layer's
// dependency-aware ordering means op order matters for correctness
// — the hash must too.
func TestPlan_Hash_OrderSensitive(t *testing.T) {
	a := quark.Plan{Ops: []quark.Operation{
		quark.OpAddColumn{Table: "t", Column: quark.Column{Name: "a", Type: "TEXT"}},
		quark.OpAddColumn{Table: "t", Column: quark.Column{Name: "b", Type: "TEXT"}},
	}}
	b := quark.Plan{Ops: []quark.Operation{
		quark.OpAddColumn{Table: "t", Column: quark.Column{Name: "b", Type: "TEXT"}},
		quark.OpAddColumn{Table: "t", Column: quark.Column{Name: "a", Type: "TEXT"}},
	}}
	if a.Hash() == b.Hash() {
		t.Errorf("plans with same ops in different order should hash differently")
	}
}

// TestPlan_Hash_Length: the hash is always 64 hex chars (sha256).
// Pins the expected format so a caller storing this in a fixed-
// width column (or as we do, CHAR(64) in quark_migration_state)
// can rely on it.
func TestPlan_Hash_Length(t *testing.T) {
	p := quark.Plan{Ops: []quark.Operation{
		quark.OpDropTable{Table: "x"},
	}}
	if got := len(p.Hash()); got != 64 {
		t.Errorf("Plan.Hash() length: want 64 hex chars, got %d (%q)", got, p.Hash())
	}
}
