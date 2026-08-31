// Copyright 2026 jcsvwinston
// SPDX-License-Identifier: Apache-2.0

package seed

import (
	"context"
	"testing"

	"github.com/jcsvwinston/quark"
)

func noop(context.Context, *quark.Client) error { return nil }

// Register preserves registration order (the order Run executes) rather than
// Go map iteration order — symmetric to migrate's sorted IDs, but seeders have
// no natural ordering key, so insertion order is the contract.
func TestRegisterPreservesOrder(t *testing.T) {
	Reset()
	t.Cleanup(Reset)

	want := []string{"zeta", "alpha", "mike", "bravo"}
	for _, n := range want {
		Register(n, noop)
	}
	got := Names()
	if len(got) != len(want) {
		t.Fatalf("Names() = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("order not preserved: got %v, want %v", got, want)
		}
	}
	if Count() != len(want) {
		t.Errorf("Count() = %d, want %d", Count(), len(want))
	}
}

// Re-registering a name replaces the function but keeps its position.
func TestReregisterKeepsPosition(t *testing.T) {
	Reset()
	t.Cleanup(Reset)

	Register("a", noop)
	Register("b", noop)
	replaced := false
	Register("a", func(context.Context, *quark.Client) error { replaced = true; return nil })

	if got := Names(); len(got) != 2 || got[0] != "a" || got[1] != "b" {
		t.Fatalf("re-register changed order/count: %v", got)
	}
	fn, ok := Get("a")
	if !ok {
		t.Fatal("Get(a) missing after re-register")
	}
	_ = fn(context.Background(), nil)
	if !replaced {
		t.Error("re-register did not replace the function")
	}
}

// Names returns a copy — mutating it must not corrupt the registry order.
func TestNamesReturnsCopy(t *testing.T) {
	Reset()
	t.Cleanup(Reset)
	Register("a", noop)
	Register("b", noop)
	got := Names()
	got[0] = "mutated"
	if Names()[0] != "a" {
		t.Error("Names() exposed the internal slice")
	}
}
