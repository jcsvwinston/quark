// Copyright 2026 jcsvwinston
// SPDX-License-Identifier: Apache-2.0

package codegen

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/jcsvwinston/quark"
	"github.com/jcsvwinston/quark/cmd/quark/internal/codegen/sample"
)

const samplePkg = "github.com/jcsvwinston/quark/cmd/quark/internal/codegen/sample"

func loadSample(t *testing.T) ModelDef {
	t.Helper()
	pkgs, err := Load(samplePkg)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	for _, pm := range pkgs {
		for _, m := range pm.Models {
			if m.Name == "Account" {
				return m
			}
		}
	}
	t.Fatalf("model Account not found in %s", samplePkg)
	return ModelDef{}
}

// TestConformance_ASTvsReflection is the ADR-0014 mitigation for the
// two-tag-interpreters drift risk: the metadata the generator derives from
// the AST must match what the runtime derives from reflection. We assert the
// hashes match (the authoritative check, since the hash folds in field
// names, columns, PK flags, and canonicalized types) and, for a better
// failure message, that the per-field types line up.
func TestConformance_ASTvsReflection(t *testing.T) {
	astModel := loadSample(t)
	rt := reflect.TypeOf(sample.Account{})

	if got, want := astModel.Hash, quark.ModelHash(rt); got != want {
		t.Fatalf("AST hash %q != reflection hash %q — generator and runtime disagree on model shape", got, want)
	}

	// Per-field type-rendering agreement (the one attribute each side
	// derives independently). The sample uses simple db tags, so the raw
	// tag value is the column name.
	astTypes := map[string]string{}
	for _, f := range astModel.Fields {
		astTypes[f.Name] = quark.CanonicalType(f.GoType)
	}
	persisted := 0
	for i := 0; i < rt.NumField(); i++ {
		f := rt.Field(i)
		if f.PkgPath != "" {
			continue
		}
		if _, ok := f.Tag.Lookup("db"); !ok {
			continue
		}
		persisted++
		want := quark.CanonicalType(f.Type.String())
		if got := astTypes[f.Name]; got != want {
			t.Errorf("field %s: AST type %q != reflection type %q", f.Name, got, want)
		}
	}
	if persisted != len(astModel.Fields) {
		t.Errorf("field count mismatch: reflection %d persisted, AST %d", persisted, len(astModel.Fields))
	}
}

// TestGoldenStable asserts that regenerating the sample reproduces the
// checked-in quark_gen.go byte-for-byte, so the generator is deterministic
// and the committed file is not stale.
func TestGoldenStable(t *testing.T) {
	pkgs, err := Load(samplePkg)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if len(pkgs) != 1 {
		t.Fatalf("expected 1 package with models, got %d", len(pkgs))
	}
	got, err := Render(pkgs[0])
	if err != nil {
		t.Fatalf("Render: %v", err)
	}
	goldenPath := filepath.Join("sample", GeneratedFileName)
	want, err := os.ReadFile(goldenPath)
	if err != nil {
		t.Fatalf("read golden %s: %v", goldenPath, err)
	}
	if string(got) != string(want) {
		t.Errorf("regenerated %s differs from the checked-in file; run `quark gen` and commit.\n--- got ---\n%s", GeneratedFileName, got)
	}
}

// TestGeneratedRegistration confirms the checked-in generated file's init()
// registered the model with the runtime and that the recorded hash matches
// the live model (no drift) — i.e. conformance verified end-to-end through
// the actual registry, not just the extractor.
func TestGeneratedRegistration(t *testing.T) {
	rt := reflect.TypeOf(sample.Account{})
	drifted, hasGenerated := quark.CheckGeneratedDrift(rt)
	if !hasGenerated {
		t.Fatal("sample.Account has no generated registration; did the generated init() run?")
	}
	if drifted {
		t.Error("sample.Account generated hash drifted from the live model; regenerate quark_gen.go")
	}
}

// TestRenderHeader sanity-checks the contract-version stamp and the
// do-not-edit marker that downstream tooling and humans rely on.
func TestRenderHeader(t *testing.T) {
	pkgs, err := Load(samplePkg)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	src, err := Render(pkgs[0])
	if err != nil {
		t.Fatalf("Render: %v", err)
	}
	got := string(src)
	if !strings.Contains(got, "DO NOT EDIT") {
		t.Error("generated file missing DO NOT EDIT marker")
	}
	if !strings.Contains(got, "//quark:gen v") {
		t.Error("generated file missing //quark:gen version header")
	}
}
