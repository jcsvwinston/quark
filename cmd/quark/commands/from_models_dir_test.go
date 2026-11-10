// Copyright 2026 jcsvwinston
// SPDX-License-Identifier: Apache-2.0

package commands

import (
	"os"
	"path/filepath"
	"testing"
)

// QK-100: --from-models used to be loaded with the package pattern resolved
// in the CURRENT directory, so an absolute path to a models package that
// lives in another module (the case when another tool — nucleus
// makemigrations — runs the CLI) found "no Go package" unless the caller
// happened to be inside that module. The loader now runs in the models
// directory itself, so an absolute path works from anywhere and a relative
// one keeps meaning "relative to where the command runs", like
// paths.migrations.
func TestLoadModelsForDDLFromOutsideTheModule(t *testing.T) {
	dir := writeFromModelsFixture(t)
	elsewhere := t.TempDir() // not a Go module, not inside the fixture's module
	cwd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chdir(elsewhere); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chdir(cwd) })

	models, err := loadModelsForDDL(filepath.Join(dir, "models"))
	if err != nil {
		t.Fatalf("absolute --from-models from outside the module: %v", err)
	}
	if len(models) == 0 {
		t.Fatal("no models extracted from an absolute models path")
	}

	// A relative path is still relative to the current directory.
	if _, err := loadModelsForDDL("./models"); err == nil {
		t.Fatal("a relative path resolved somewhere other than the current directory")
	}
}
