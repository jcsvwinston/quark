// Copyright 2026 jcsvwinston
// SPDX-License-Identifier: Apache-2.0

package quark

import "testing"

// TestOwnedAppendDoesNotShareBacking proves the copy-on-write primitive: given
// a slice with spare capacity, two ownedAppend calls from the same base must
// each allocate their own backing array, so neither sees the other's write.
// A plain append would write both into the base's spare capacity (index 3 of a
// cap-8 array) and the second would clobber the first.
func TestOwnedAppendDoesNotShareBacking(t *testing.T) {
	base := make([]int, 3, 8) // len 3, cap 8 — deliberate spare capacity
	base[0], base[1], base[2] = 1, 2, 3

	a := ownedAppend(base, 4)
	b := ownedAppend(base, 5)

	if a[3] != 4 {
		t.Errorf("a[3] = %d, want 4 (b's append corrupted a — backing shared)", a[3])
	}
	if b[3] != 5 {
		t.Errorf("b[3] = %d, want 5", b[3])
	}
	if len(base) != 3 || cap(base) != 8 {
		t.Errorf("base mutated: len=%d cap=%d, want len=3 cap=8", len(base), cap(base))
	}
	if &a[0] == &base[0] {
		t.Error("ownedAppend result shares the base backing array")
	}

	// nil base: ownedAppend allocates from scratch and leaves the nil intact.
	var nilBase []int
	n := ownedAppend(nilBase, 9)
	if len(n) != 1 || n[0] != 9 {
		t.Errorf("ownedAppend(nil, 9) = %v, want [9]", n)
	}
	if nilBase != nil {
		t.Error("nil base was mutated")
	}
}

// TestCloneCOWIsolation is the builder-level guarantee that the existing
// immutability tests (TestQueryClone, TestPaginate*) do not cover: they only
// vary scalar fields (Limit) on a shared base. Here a base accumulates enough
// WHERE conditions to leave spare capacity in the backing array (Go grows
// 1→2→4, so three conditions give len 3, cap 4), then TWO children each append
// a distinct condition. Without ownedAppend both would write into the shared
// spare slot and corrupt each other; with it, each reallocates.
func TestCloneCOWIsolation(t *testing.T) {
	base := (&Query[struct{}]{}).
		Where("a", "=", 1).
		Where("b", "=", 2).
		Where("c", "=", 3)

	child1 := base.Where("d", "=", 4)
	child2 := base.Where("e", "=", 5)

	// The base must be untouched by either child.
	if got := whereCols(base); !equalStrs(got, []string{"a", "b", "c"}) {
		t.Errorf("base.where mutated by a child: %v", got)
	}
	// Each child sees its own appended condition — not the sibling's.
	if got := whereCols(child1); !equalStrs(got, []string{"a", "b", "c", "d"}) {
		t.Errorf("child1.where = %v, want [a b c d]", got)
	}
	if got := whereCols(child2); !equalStrs(got, []string{"a", "b", "c", "e"}) {
		t.Errorf("child2.where = %v, want [a b c e] (child2's append clobbered child1 if this is [a b c d])", got)
	}

	// Same guarantee for a second slice type (orderBy) to exercise a
	// different element type through ownedAppend.
	ob := base.OrderBy("x", "ASC").OrderBy("y", "ASC") // len 2 backing
	o1 := ob.OrderBy("p", "ASC")
	o2 := ob.OrderBy("q", "ASC")
	if got := orderCols(o1); !equalStrs(got, []string{"x", "y", "p"}) {
		t.Errorf("o1.orderBy = %v, want [x y p]", got)
	}
	if got := orderCols(o2); !equalStrs(got, []string{"x", "y", "q"}) {
		t.Errorf("o2.orderBy = %v, want [x y q] (sibling corruption)", got)
	}

	// groupBy (variadic append path) and preloads (string slice) cover the
	// other common element types routed through ownedAppend.
	gb := base.GroupBy("g1").GroupBy("g2")
	g1 := gb.GroupBy("p")
	g2 := gb.GroupBy("q")
	if !equalStrs(g1.groupBy, []string{"g1", "g2", "p"}) || !equalStrs(g2.groupBy, []string{"g1", "g2", "q"}) {
		t.Errorf("groupBy COW broken: g1=%v g2=%v", g1.groupBy, g2.groupBy)
	}

	pl := base.Preload("A").Preload("B")
	p1 := pl.Preload("C")
	p2 := pl.Preload("D")
	if !equalStrs(p1.preloads, []string{"A", "B", "C"}) || !equalStrs(p2.preloads, []string{"A", "B", "D"}) {
		t.Errorf("preloads COW broken: p1=%v p2=%v", p1.preloads, p2.preloads)
	}
}

// BenchmarkBuilderDeriveFatBase measures deriving a child query from a base
// that already has several non-empty slices (where, orderBy, groupBy,
// preloads). Pre-COW, clone() deep-copied all four on every derive even
// though .Where touches only `where`; with copy-on-write the clone is a plain
// struct copy and ownedAppend reallocates just the `where` slice — so the
// per-op allocation count drops to the one slice the method actually mutates.
func BenchmarkBuilderDeriveFatBase(b *testing.B) {
	base := (&Query[struct{}]{}).
		Where("a", "=", 1).
		Where("b", "=", 2).
		OrderBy("created_at", "DESC").
		GroupBy("status").
		Preload("Rel")

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = base.Where("c", "=", 3)
	}
}

func whereCols[T any](q *Query[T]) []string {
	out := make([]string, len(q.where))
	for i, c := range q.where {
		out[i] = c.column
	}
	return out
}

func orderCols[T any](q *Query[T]) []string {
	out := make([]string, len(q.orderBy))
	for i, o := range q.orderBy {
		out[i] = o.column
	}
	return out
}

func equalStrs(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
