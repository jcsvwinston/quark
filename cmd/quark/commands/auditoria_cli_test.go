// Copyright 2026 jcsvwinston
// SPDX-License-Identifier: Apache-2.0

// Regression tests for the CLI findings of the integral audit 2026-08-30:
//
//   - QC-1: `quark init` in a directory with no go.mod left a runner whose
//     imports pointed at a placeholder module and a final message ordering a
//     command that failed instantly. init now scaffolds a coherent go.mod.
//   - QC-2: `quark seed create` produced a seeder that never registered; the
//     generated file now self-registers via seed.Register in an init().
//   - QC-3/AQ-09: identifiers from --table reached introspection SQL raw; they
//     now pass SQLGuard on every command that interpolates them.
//   - QC-4: `migrate create --from-models` reported "no model structs" when the
//     real cause was a package that did not load/compile.
package commands

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jcsvwinston/quark"
	"github.com/jcsvwinston/quark/seed"
	"github.com/spf13/viper"
)

// QC-1: init in a directory outside any Go module scaffolds a go.mod so the
// runner's imports resolve, and the runner imports the SAME module path.
func TestInitScaffoldsGoModuleWhenAbsent(t *testing.T) {
	dir := t.TempDir()
	oldDir, oldDialect, oldModule := initDir, initDialect, initModule
	initDir, initDialect, initModule = dir, "sqlite", "example.com/scaffolded"
	t.Cleanup(func() { initDir, initDialect, initModule = oldDir, oldDialect, oldModule })

	if _, err := captureStdout(t, runInit); err != nil {
		t.Fatalf("init: %v", err)
	}

	gomod, err := os.ReadFile(filepath.Join(dir, "go.mod"))
	if err != nil {
		t.Fatalf("init did not scaffold go.mod: %v", err)
	}
	if !strings.Contains(string(gomod), "module example.com/scaffolded") {
		t.Errorf("go.mod does not use --module path:\n%s", gomod)
	}
	// The runner must import the scaffolded module, not the old placeholder.
	runner, err := os.ReadFile(filepath.Join(dir, "cmd", "scaffolded", "main.go"))
	if err != nil {
		t.Fatalf("runner not written: %v", err)
	}
	if !strings.Contains(string(runner), `_ "example.com/scaffolded/migrations"`) {
		t.Errorf("runner does not import the scaffolded module:\n%s", runner)
	}
	if strings.Contains(string(runner), "github.com/user/myapp") {
		t.Errorf("placeholder module leaked into the runner:\n%s", runner)
	}
	// The config must agree with the module path.
	cfg, err := os.ReadFile(filepath.Join(dir, ".quark.yml"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(cfg), "example.com/scaffolded") {
		t.Errorf("config module ignores the scaffolded go.mod:\n%s", cfg)
	}
}

// QC-1: when the directory is already inside a module, init must NOT scaffold a
// second go.mod, and the runner must use the existing module path (extended by
// the subdirectory when init runs in a subpackage).
func TestInitDoesNotScaffoldInsideExistingModule(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "go.mod"), []byte("module example.com/app\n\ngo 1.25\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	sub := filepath.Join(root, "service")
	if err := os.MkdirAll(sub, 0o755); err != nil {
		t.Fatal(err)
	}

	oldDir, oldDialect, oldModule := initDir, initDialect, initModule
	initDir, initDialect, initModule = sub, "sqlite", ""
	t.Cleanup(func() { initDir, initDialect, initModule = oldDir, oldDialect, oldModule })

	if _, err := captureStdout(t, runInit); err != nil {
		t.Fatalf("init: %v", err)
	}
	if _, err := os.Stat(filepath.Join(sub, "go.mod")); !os.IsNotExist(err) {
		t.Error("init scaffolded a second go.mod inside an existing module")
	}
	runner, err := os.ReadFile(filepath.Join(sub, "cmd", "service", "main.go"))
	if err != nil {
		t.Fatalf("runner not written: %v", err)
	}
	if !strings.Contains(string(runner), `_ "example.com/app/service/migrations"`) {
		t.Errorf("runner import does not extend the module path with the subpackage:\n%s", runner)
	}
}

// QC-2: the generated seeder self-registers via seed.Register in an init(), so
// blank-importing the seeders package is enough for `seed run` to see it.
func TestSeedCreateWritesSelfRegisteringSeeder(t *testing.T) {
	dir := t.TempDir()
	// runSeedCreate writes to paths.seeders (default ./seeders); pin it and
	// chdir so it lands under the temp dir.
	oldSeeders := viper.GetString("paths.seeders")
	viper.Set("paths.seeders", "seeders")
	t.Cleanup(func() { viper.Set("paths.seeders", oldSeeders) })
	oldWd, _ := os.Getwd()
	if err := os.Chdir(dir); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chdir(oldWd) })

	if _, err := captureStdout(t, func() error { return runSeedCreate("demo_users") }); err != nil {
		t.Fatalf("seed create: %v", err)
	}
	body, err := os.ReadFile(filepath.Join("seeders", "demo_users_seeder.go"))
	if err != nil {
		t.Fatalf("seeder not written: %v", err)
	}
	for _, want := range []string{
		`"github.com/jcsvwinston/quark/seed"`,
		"func init() {",
		`seed.Register("demo_users", SeedDemoUsers)`,
		"func SeedDemoUsers(",
	} {
		if !strings.Contains(string(body), want) {
			t.Errorf("generated seeder missing %q:\n%s", want, body)
		}
	}
}

// QC-2: seed run without --name executes registered seeders and reports
// success; the registration path is the seed package the template uses.
func TestSeedRunExecutesRegisteredSeeder(t *testing.T) {
	withSQLiteConfig(t)
	seed.Reset()
	t.Cleanup(seed.Reset)

	ran := false
	seed.Register("demo", func(context.Context, *quark.Client) error { ran = true; return nil })
	if _, err := captureStdout(t, runSeedRun); err != nil {
		t.Fatalf("seed run: %v", err)
	}
	if !ran {
		t.Error("registered seeder was not executed")
	}
}

// QC-3/AQ-09: table identifiers from the CLI are validated before any SQL is
// built. validateTableName rejects injection payloads and accepts real names.
func TestValidateTableNameGuardsIdentifiers(t *testing.T) {
	bad := []string{
		"users) UNION SELECT 1 --",
		"users;DROP TABLE secrets",
		"users--x",
		"public.users", // schema-qualified is not a bare identifier
		"",
	}
	for _, name := range bad {
		err := validateTableName(name)
		if err == nil {
			t.Errorf("validateTableName(%q) = nil, want rejection", name)
			continue
		}
		if !errors.Is(err, quark.ErrInvalidIdentifier) {
			t.Errorf("validateTableName(%q) error not ErrInvalidIdentifier: %v", name, err)
		}
	}
	for _, name := range []string{"users", "order_items", "_tmp", "T42"} {
		if err := validateTableName(name); err != nil {
			t.Errorf("validateTableName(%q) rejected a valid identifier: %v", name, err)
		}
	}
}

// QC-3: the guard fires from the command RunE before any DB connection, so a
// hostile --table needs no configured database to be rejected.
func TestInspectTableRejectsHostileIdentifier(t *testing.T) {
	err := runInspectTable("users);DROP TABLE t--")
	if err == nil || !errors.Is(err, quark.ErrInvalidIdentifier) {
		t.Fatalf("inspect table with hostile name: got %v, want ErrInvalidIdentifier", err)
	}
}

// QC-4: a directory that is not a loadable Go package reports THAT, not the
// misleading "no model structs".
func TestFromModelsReportsUnloadablePackage(t *testing.T) {
	dir := t.TempDir() // no go.mod, no Go files → packages.Load finds nothing
	_, err := loadModelsForDDL(dir)
	if err == nil {
		t.Fatal("expected an error for an unloadable package")
	}
	if strings.Contains(err.Error(), "no model structs") {
		t.Errorf("unloadable package still disguised as 'no model structs': %v", err)
	}
}

// QC-4: a package that compiles but defines no db-tagged struct keeps the
// honest "no model structs" wording, distinct from the load-failure path.
func TestFromModelsReportsNoModelsWhenPackageCompiles(t *testing.T) {
	// The loader anchors module resolution at the process cwd, so the probe
	// package must BE the current module for it to load cleanly. chdir into a
	// throwaway module whose only file compiles but carries no db tags.
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "go.mod"), []byte("module models_probe\n\ngo 1.25\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "x.go"), []byte("package models_probe\n\ntype NotAModel struct{ Name string }\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	oldWd, _ := os.Getwd()
	if err := os.Chdir(dir); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chdir(oldWd) })

	_, err := loadModelsForDDL(".")
	if err == nil {
		t.Fatal("expected an error when no db-tagged struct is present")
	}
	if !strings.Contains(err.Error(), "no model structs") {
		t.Errorf("compiling-but-empty package should say 'no model structs', got: %v", err)
	}
}
