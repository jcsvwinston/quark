// Copyright 2026 jcsvwinston
// SPDX-License-Identifier: Apache-2.0

// Regression test for QCD-CLI-2: the embed recipe printed by the CLI itself
// (errNoMigrationsRegistered) told users to write `func main() {
// commands.Execute() }`, which discards the returned error. Combined with
// SilenceErrors on every subcommand, a runner built from the documented
// recipe exited 0 with no output on ANY failure — the exact "exit 0 without
// effect" class, sitting on the path CI gates rely on.
//
// The test builds a runner whose main body is extracted verbatim from the
// recipe the CLI prints, so it exercises whatever the documentation currently
// prescribes rather than a hand-written pattern.
package commands

import (
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"testing"
)

var recipeMainRE = regexp.MustCompile(`func main\(\) \{ ([^}]+) \}`)

func TestEmbedRecipeRunnerPropagatesFailure(t *testing.T) {
	if testing.Short() {
		t.Skip("builds a full runner binary; skipped with -short")
	}

	recipe := errNoMigrationsRegistered("test").Error()
	m := recipeMainRE.FindStringSubmatch(recipe)
	if m == nil {
		t.Fatalf("embed recipe no longer contains a `func main() { ... }` line:\n%s", recipe)
	}
	mainBody := m[1]

	repoRoot, err := filepath.Abs(filepath.Join("..", "..", ".."))
	if err != nil {
		t.Fatal(err)
	}

	dir := t.TempDir()
	goMod := fmt.Sprintf("module tmprunner\n\ngo 1.25.7\n\nrequire github.com/jcsvwinston/quark v0.0.0\n\nreplace github.com/jcsvwinston/quark => %s\n", repoRoot)
	if err := os.WriteFile(filepath.Join(dir, "go.mod"), []byte(goMod), 0o644); err != nil {
		t.Fatal(err)
	}
	goSum, err := os.ReadFile(filepath.Join(repoRoot, "go.sum"))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "go.sum"), goSum, 0o644); err != nil {
		t.Fatal(err)
	}

	// The recipe's import block references the user's own migrations package
	// (a placeholder that cannot resolve here); the load-bearing part under
	// test is the main body the recipe prescribes.
	mainSrc := fmt.Sprintf(`package main

import (
	"github.com/jcsvwinston/quark/cmd/quark/commands"
)

func main() { %s }
`, mainBody)
	if err := os.WriteFile(filepath.Join(dir, "main.go"), []byte(mainSrc), 0o644); err != nil {
		t.Fatal(err)
	}

	runner := filepath.Join(dir, "runner")
	build := exec.Command("go", "build", "-o", runner, ".")
	build.Dir = dir
	build.Env = append(os.Environ(), "GOFLAGS=-mod=mod")
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("building runner from documented recipe: %v\n%s", err, out)
	}

	// The demo's repro: a tenant migrate that must fail (empty migration
	// registry — the guard fires before any DB connection).
	var stdout, stderr bytes.Buffer
	cmd := exec.Command(runner, "tenant", "migrate", "shard_a")
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "QUARK_TENANT_STRATEGY=schema_per_tenant")
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	err = cmd.Run()

	if err == nil {
		t.Errorf("runner built from the documented recipe exited 0 on a failing command\nstdout: %s\nstderr: %s",
			stdout.String(), stderr.String())
	}
	if stderr.Len() == 0 {
		t.Errorf("failing command produced no stderr output (silent failure)\nstdout: %s", stdout.String())
	}
}
