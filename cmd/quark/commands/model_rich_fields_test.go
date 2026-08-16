// Copyright 2026 jcsvwinston
// SPDX-License-Identifier: Apache-2.0

// Regression tests for DX-19 (DX audit 2026-08-16): the model generator had
// no vocabulary for the rich half of quark — rel/join, JSON[T], Array[T],
// Nullable[T], quark:"version" — so the reference app wrote 415 lines of
// models by hand and the CLI's only contribution was validating them.
//
// Field spec grammar (third segment = modifiers, comma-free):
//
//	name:type                  plain column
//	name:type:not_null         quark:"not_null"
//	name:type:unique           quark:"unique"
//	name:type:version          quark:"version" (optimistic locking)
//	name:nullable<T>           quark.Nullable[T]
//	name:array<T>              quark.Array[T]
//	name:json<T>               quark.JSON[T] (T left to the user's package)
//	name:belongs_to<Model>     FK column + relation field pair
package commands

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func generateRichModel(t *testing.T, dir string) string {
	t.Helper()
	oldFields, oldOut, oldPkg := modelFields, modelOutDir, modelPackage
	modelFields = "id:int64,title:string:not_null,email:string:unique,version:int64:version,bio:nullable<string>,tags:array<string>,attrs:json<Attrs>,author:belongs_to<User>"
	modelOutDir = dir
	modelPackage = "models"
	defer func() { modelFields, modelOutDir, modelPackage = oldFields, oldOut, oldPkg }()

	if err := runModelGen([]string{"Article"}); err != nil {
		t.Fatalf("model generate rich fields: %v", err)
	}
	src, err := os.ReadFile(filepath.Join(dir, "article.go"))
	if err != nil {
		t.Fatalf("generated file missing: %v", err)
	}
	return string(src)
}

func TestModelGenerateRichVocabulary(t *testing.T) {
	src := generateRichModel(t, t.TempDir())
	for _, want := range []string{
		`Title string ` + "`" + `db:"title" quark:"not_null" json:"title"` + "`",
		`Email string ` + "`" + `db:"email" quark:"unique" json:"email"` + "`",
		`Version int64 ` + "`" + `db:"version" quark:"version" json:"version"` + "`",
		`Bio quark.Nullable[string]`,
		`Tags quark.Array[string]`,
		`Attrs quark.JSON[Attrs]`,
		`AuthorID int64 ` + "`" + `db:"author_id"`,
		`Author User ` + "`" + `rel:"belongs_to" join:"author_id"` + "`",
		`"github.com/jcsvwinston/quark"`,
	} {
		if !strings.Contains(src, want) {
			t.Errorf("generated model missing %q:\n%s", want, src)
		}
	}
}

// The generated file must COMPILE against the real quark module (the DX-19
// "hecho cuando": declarative definition → model that passes validation).
func TestModelGenerateRichVocabularyCompiles(t *testing.T) {
	if testing.Short() {
		t.Skip("compiles the generated model against the local quark module")
	}
	repoRoot, err := filepath.Abs(filepath.Join("..", "..", ".."))
	if err != nil {
		t.Fatal(err)
	}

	dir := t.TempDir()
	modDir := filepath.Join(dir, "mod")
	outDir := filepath.Join(modDir, "models")
	if err := os.MkdirAll(outDir, 0o755); err != nil {
		t.Fatal(err)
	}
	generateRichModel(t, outDir)

	// The user-owned types the spec references.
	extra := `package models

type Attrs struct {
	Kind string ` + "`json:\"kind\"`" + `
}

type User struct {
	ID   int64  ` + "`db:\"id\" pk:\"true\"`" + `
	Name string ` + "`db:\"name\"`" + `
}
`
	if err := os.WriteFile(filepath.Join(outDir, "extra.go"), []byte(extra), 0o644); err != nil {
		t.Fatal(err)
	}

	goMod := "module tmprich\n\ngo 1.25.7\n\nrequire github.com/jcsvwinston/quark v0.0.0\n\nreplace github.com/jcsvwinston/quark => " + repoRoot + "\n"
	if err := os.WriteFile(filepath.Join(modDir, "go.mod"), []byte(goMod), 0o644); err != nil {
		t.Fatal(err)
	}
	goSum, err := os.ReadFile(filepath.Join(repoRoot, "go.sum"))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(modDir, "go.sum"), goSum, 0o644); err != nil {
		t.Fatal(err)
	}

	cmd := exec.Command("go", "build", "./...")
	cmd.Dir = modDir
	cmd.Env = append(os.Environ(), "GOFLAGS=-mod=mod")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("generated rich model does not compile: %v\n%s", err, out)
	}
}
