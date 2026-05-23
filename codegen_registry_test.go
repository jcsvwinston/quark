// Copyright 2026 jcsvwinston
// SPDX-License-Identifier: Apache-2.0

package quark

import (
	"database/sql"
	"reflect"
	"testing"
)

// Sample models for the registry tests. Distinct types so their registry
// keys never collide across subtests.
type cgScanModel struct {
	ID   int64  `db:"id" pk:"true"`
	Name string `db:"name"`
}

type cgOtherModel struct {
	ID  int64 `db:"id" pk:"true"`
	Age int   `db:"age"`
}

func noopScanner(*sql.Rows, any) error { return nil }

func noopBinder(any, BindMode) ([]string, []any, error) { return nil, nil, nil }

func TestCodegenRegistry_RegisterAndLookup(t *testing.T) {
	rt := reflect.TypeOf(cgScanModel{})
	RegisterTypedScanner(rt, noopScanner)
	RegisterTypedBinder(rt, noopBinder)
	RegisterGeneratedMeta(rt, GeneratedMeta{ContractVersion: GenContractVersion, ModelHash: ModelHash(rt)})

	if _, ok := lookupTypedScanner(rt); !ok {
		t.Error("expected scanner hit for registered model")
	}
	if _, ok := lookupTypedBinder(rt); !ok {
		t.Error("expected binder hit for registered model")
	}

	// An unregistered model misses — the caller falls back to reflection.
	if _, ok := lookupTypedScanner(reflect.TypeOf(cgOtherModel{})); ok {
		t.Error("expected miss for unregistered model")
	}
}

func TestCodegenRegistry_PointerNormalization(t *testing.T) {
	rt := reflect.TypeOf(cgScanModel{})
	RegisterTypedScanner(rt, noopScanner)
	RegisterGeneratedMeta(rt, GeneratedMeta{ContractVersion: GenContractVersion, ModelHash: ModelHash(rt)})

	// Registration keyed on the value type; lookup starting from *T must
	// still hit.
	if _, ok := lookupTypedScanner(reflect.TypeOf(&cgScanModel{})); !ok {
		t.Error("expected scanner hit when looking up via pointer type")
	}
}

func TestCodegenRegistry_VersionMismatchFallsBack(t *testing.T) {
	type cgVersionModel struct {
		ID int64 `db:"id" pk:"true"`
	}
	rt := reflect.TypeOf(cgVersionModel{})
	// Scanner is present, but the meta declares a future contract version.
	RegisterTypedScanner(rt, noopScanner)
	RegisterGeneratedMeta(rt, GeneratedMeta{ContractVersion: GenContractVersion + 1, ModelHash: ModelHash(rt)})

	if _, ok := lookupTypedScanner(rt); ok {
		t.Error("expected miss: incompatible contract version must fall back to reflection")
	}
}

func TestModelHash_DeterministicAndShapeSensitive(t *testing.T) {
	rt := reflect.TypeOf(cgScanModel{})
	if got, want := ModelHash(rt), ModelHash(rt); got != want {
		t.Errorf("ModelHash not deterministic: %q != %q", got, want)
	}
	if ModelHash(rt) == ModelHash(reflect.TypeOf(cgOtherModel{})) {
		t.Error("expected different hashes for models with different shapes")
	}
	// Pointer and value type hash identically (modelKey normalizes).
	if ModelHash(rt) != ModelHash(reflect.TypeOf(&cgScanModel{})) {
		t.Error("pointer and value type must hash identically")
	}
}

func TestCheckGeneratedDrift(t *testing.T) {
	type cgDriftModel struct {
		ID   int64  `db:"id" pk:"true"`
		Name string `db:"name"`
	}
	rt := reflect.TypeOf(cgDriftModel{})

	// No generated code registered yet.
	if drifted, has := CheckGeneratedDrift(rt); drifted || has {
		t.Errorf("unregistered model: want (false,false), got (%v,%v)", drifted, has)
	}

	// Up-to-date hash → not drifted.
	RegisterGeneratedMeta(rt, GeneratedMeta{ContractVersion: GenContractVersion, ModelHash: ModelHash(rt)})
	if drifted, has := CheckGeneratedDrift(rt); drifted || !has {
		t.Errorf("up-to-date model: want (false,true), got (%v,%v)", drifted, has)
	}

	// Stale hash → drifted.
	RegisterGeneratedMeta(rt, GeneratedMeta{ContractVersion: GenContractVersion, ModelHash: "stale"})
	if drifted, has := CheckGeneratedDrift(rt); !drifted || !has {
		t.Errorf("stale model: want (true,true), got (%v,%v)", drifted, has)
	}
}
