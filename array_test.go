// Copyright 2026 jcsvwinston
// SPDX-License-Identifier: Apache-2.0

package quark

import (
	"testing"
)

// TestArray_ValueScanRoundTrip pins the unit-level contract: marshal V
// to JSON, scan the same bytes back, get the same slice. Covers the
// common cell types (string, int64) and the nil / empty distinctions.
func TestArray_ValueScanRoundTrip(t *testing.T) {
	t.Run("StringRoundTrip", func(t *testing.T) {
		a := Array[string]{V: []string{"go", "orm", "quark"}}
		v, err := a.Value()
		if err != nil {
			t.Fatalf("Value: %v", err)
		}
		var b Array[string]
		if err := b.Scan(v); err != nil {
			t.Fatalf("Scan: %v", err)
		}
		if len(b.V) != 3 || b.V[0] != "go" || b.V[2] != "quark" {
			t.Errorf("roundtrip lost data: %+v", b.V)
		}
	})

	t.Run("Int64RoundTrip", func(t *testing.T) {
		a := Array[int64]{V: []int64{1, 2, 9001}}
		v, _ := a.Value()
		var b Array[int64]
		if err := b.Scan(v); err != nil {
			t.Fatalf("Scan: %v", err)
		}
		if len(b.V) != 3 || b.V[2] != 9001 {
			t.Errorf("roundtrip lost data: %+v", b.V)
		}
	})

	t.Run("NilValueSerialisesAsEmptyArray", func(t *testing.T) {
		// A nil V should serialise to `[]`, not `null`. The default-
		// useful-shape for a SQL JSON column is "valid but empty"
		// rather than "absent value"; users who want NULL semantics
		// pair Array with Nullable.
		var a Array[string]
		v, err := a.Value()
		if err != nil {
			t.Fatalf("Value(nil V): %v", err)
		}
		s, ok := v.(string)
		if !ok {
			t.Fatalf("Value should return string, got %T", v)
		}
		if s != "[]" {
			t.Errorf("Value(nil V) = %q, want %q", s, "[]")
		}
	})

	t.Run("NullScanClearsToNil", func(t *testing.T) {
		// Pre-load V then scan NULL — V must reset to nil so the
		// caller can detect "no rows" without leftover state from a
		// prior scan.
		a := Array[string]{V: []string{"stale"}}
		if err := a.Scan(nil); err != nil {
			t.Fatalf("Scan(nil): %v", err)
		}
		if a.V != nil {
			t.Errorf("Scan(nil) should clear V to nil, got %+v", a.V)
		}
	})

	t.Run("EmptyBytesScanClearsToNil", func(t *testing.T) {
		// A column default of `'[]'` is the canonical zero-state for
		// the wrapper; an empty payload (zero-length []byte) means the
		// column was empty, which we treat the same as nil. JSON
		// parsing of `[]` returns an empty (non-nil) slice — that's
		// also acceptable but we normalise to nil for consistency
		// with the NULL case.
		a := Array[string]{V: []string{"stale"}}
		if err := a.Scan([]byte{}); err != nil {
			t.Fatalf("Scan(empty): %v", err)
		}
		if a.V != nil {
			t.Errorf("Scan(empty) should clear V to nil, got %+v", a.V)
		}
	})

	t.Run("ScanFromStringWorks", func(t *testing.T) {
		// Postgres and MySQL drivers return strings for JSON columns;
		// SQLite returns []byte. The wrapper must accept both forms.
		var a Array[int64]
		if err := a.Scan(`[10, 20, 30]`); err != nil {
			t.Fatalf("Scan(string): %v", err)
		}
		if len(a.V) != 3 || a.V[1] != 20 {
			t.Errorf("Scan from string lost data: %+v", a.V)
		}
	})

	t.Run("UnsupportedSourceTypeReturnsError", func(t *testing.T) {
		var a Array[string]
		err := a.Scan(int64(42))
		if err == nil {
			t.Fatal("Scan(int64) should error")
		}
	})
}

// TestArray_LenAndSlice covers the two convenience accessors. They are
// safe on zero-value Arrays (no nil deref).
func TestArray_LenAndSlice(t *testing.T) {
	var zero Array[int64]
	if zero.Len() != 0 {
		t.Errorf("zero.Len() = %d, want 0", zero.Len())
	}
	if zero.Slice() != nil {
		t.Errorf("zero.Slice() should be nil, got %+v", zero.Slice())
	}

	a := Array[int64]{V: []int64{1, 2, 3}}
	if a.Len() != 3 {
		t.Errorf("a.Len() = %d, want 3", a.Len())
	}
	if got := a.Slice(); len(got) != 3 || got[2] != 3 {
		t.Errorf("a.Slice() = %+v", got)
	}
}
