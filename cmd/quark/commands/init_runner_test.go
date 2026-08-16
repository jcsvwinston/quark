// Copyright 2026 jcsvwinston
// SPDX-License-Identifier: Apache-2.0

// Regression tests for DX-10 (DX audit 2026-08-16): the CLI cycle broke at
// `migrate up` — the standalone binary cannot see project migrations
// (init() registration), every project needs a 6-line runner the CLI
// dictates in an error message but never writes, and `quark init` already
// knows the module path. Now init scaffolds the runner and the package
// stubs that make it compile on day one.
package commands

import (
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func runInitIn(t *testing.T, dir string) {
	t.Helper()
	oldDir, oldDialect := initDir, initDialect
	initDir, initDialect = dir, "sqlite"
	t.Cleanup(func() { initDir, initDialect = oldDir, oldDialect })
	if err := runInit(); err != nil {
		t.Fatalf("init: %v", err)
	}
}

// init must write the embedded runner and the package stubs it imports.
func TestInitWritesRunner(t *testing.T) {
	dir := t.TempDir()
	goMod := "module example.com/shop\n\ngo 1.25\n"
	if err := os.WriteFile(filepath.Join(dir, "go.mod"), []byte(goMod), 0o644); err != nil {
		t.Fatal(err)
	}
	runInitIn(t, dir)

	runner, err := os.ReadFile(filepath.Join(dir, "cmd", "shop", "main.go"))
	if err != nil {
		t.Fatalf("init did not write the runner: %v", err)
	}
	for _, want := range []string{
		`_ "example.com/shop/migrations"`,
		`_ "example.com/shop/seeders"`,
		"commands.Main()",
	} {
		if !strings.Contains(string(runner), want) {
			t.Errorf("runner missing %q:\n%s", want, runner)
		}
	}
	for _, stub := range []string{"migrations/doc.go", "seeders/doc.go"} {
		if _, err := os.Stat(filepath.Join(dir, stub)); err != nil {
			t.Errorf("init did not write %s (the runner cannot compile without it): %v", stub, err)
		}
	}
}

// The full documented cycle, by execution: init → migrate create → build the
// runner → runner migrate up → EXIT=0 against the configured sqlite DB.
func TestInitRunnerClosesTheCLICycle(t *testing.T) {
	if testing.Short() {
		t.Skip("compiles and runs the scaffolded runner; skipped with -short")
	}
	repoRoot, err := filepath.Abs(filepath.Join("..", "..", ".."))
	if err != nil {
		t.Fatal(err)
	}

	dir := t.TempDir()
	goMod := fmt.Sprintf("module example.com/shop\n\ngo 1.25.7\n\nrequire github.com/jcsvwinston/quark v0.0.0\n\nreplace github.com/jcsvwinston/quark => %s\n", repoRoot)
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

	runInitIn(t, dir)

	// migrate create must produce a migration the runner will register.
	oldWd, _ := os.Getwd()
	if err := os.Chdir(dir); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chdir(oldWd) })
	if err := runMigrateCreate("add_widgets"); err != nil {
		t.Fatalf("migrate create: %v", err)
	}

	build := exec.Command("go", "build", "-o", "runner", "./cmd/shop")
	build.Dir = dir
	build.Env = append(os.Environ(), "GOFLAGS=-mod=mod")
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("building the scaffolded runner: %v\n%s", err, out)
	}

	var stdout, stderr bytes.Buffer
	up := exec.Command(filepath.Join(dir, "runner"), "migrate", "up")
	up.Dir = dir
	up.Stdout = &stdout
	up.Stderr = &stderr
	if err := up.Run(); err != nil {
		t.Fatalf("runner migrate up: %v\nstdout: %s\nstderr: %s", err, stdout.String(), stderr.String())
	}
	if !strings.Contains(stdout.String(), "Applied 1 migrations") && !strings.Contains(stdout.String(), "Applying migration") {
		t.Errorf("migrate up did not apply the created migration:\nstdout: %s", stdout.String())
	}
}
