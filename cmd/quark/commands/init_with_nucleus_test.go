// Copyright 2026 jcsvwinston
// SPDX-License-Identifier: Apache-2.0

// `quark init --with nucleus` writes the Quark side of the Quark<->Nucleus
// seam as SOURCE TEXT: a nucleus.Module[struct{}] that wraps a *quark.Client.
// Quark is the autonomous data layer of the suite and carries no framework
// dependency (QADR-0001/0006), so this repo never compiles the emitted file
// against Nucleus: these tests parse it and pin the shape a Nucleus host
// expects; the only place the file is compiled against a real Nucleus is the
// suite's own integration lane.
package commands

import (
	"bytes"
	"go/parser"
	"go/token"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

// runInitArgs drives the real command line (`quark init <args>`) so a flag
// the command does not know fails the way it fails for a person: cobra's
// "unknown flag". Stdout is captured for the next-steps assertions.
func runInitArgs(t *testing.T, args ...string) (stdout string, err error) {
	t.Helper()
	oldDir, oldDialect, oldModule, oldWith := initDir, initDialect, initModule, initWith
	t.Cleanup(func() {
		initDir, initDialect, initModule, initWith = oldDir, oldDialect, oldModule, oldWith
		rootCmd.SetArgs([]string{})
	})

	r, w, pipeErr := os.Pipe()
	if pipeErr != nil {
		t.Fatal(pipeErr)
	}
	realStdout := os.Stdout
	os.Stdout = w
	rootCmd.SetArgs(append([]string{"init"}, args...))
	err = rootCmd.Execute()
	os.Stdout = realStdout
	_ = w.Close()
	var buf bytes.Buffer
	_, _ = io.Copy(&buf, r)
	return buf.String(), err
}

func TestInitWithNucleusWritesModule(t *testing.T) {
	dir := t.TempDir()
	goMod := "module example.com/shop\n\ngo 1.25\n"
	if err := os.WriteFile(filepath.Join(dir, "go.mod"), []byte(goMod), 0o644); err != nil {
		t.Fatal(err)
	}

	out, err := runInitArgs(t, "--dir", dir, "--dialect", "sqlite", "--with", "nucleus")
	if err != nil {
		t.Fatalf("init --with nucleus: %v", err)
	}

	modulePath := filepath.Join(dir, "internal", "shop", "module.go")
	src, err := os.ReadFile(modulePath)
	if err != nil {
		t.Fatalf("init --with nucleus did not write internal/shop/module.go: %v", err)
	}

	// The file must be valid Go: it is the one artefact this repo can never
	// compile against its consumer, so at least its syntax is pinned here.
	if _, err := parser.ParseFile(token.NewFileSet(), modulePath, src, parser.AllErrors); err != nil {
		t.Fatalf("emitted module does not parse: %v\n%s", err, src)
	}

	for _, want := range []string{
		"package shop",
		`"github.com/jcsvwinston/nucleus/pkg/nucleus"`,
		`"github.com/jcsvwinston/quark"`,
		`_ "github.com/jcsvwinston/quark/drivers/sqlite"`,
		"func Module(client *quark.Client) nucleus.ModuleSpec",
		"nucleus.Module[struct{}]{",
		"Routes: func(r nucleus.Router, _ struct{})",
		"OnStart: func(ctx context.Context, rt nucleus.Runtime, _ struct{}) error",
		"Policies: []nucleus.PolicyRule{",
		"CSRFExempt:",
		"quark.For[Note](c.Request.Context(), m.client)",
		"quark.IsUniqueViolation(err)",
	} {
		if !strings.Contains(string(src), want) {
			t.Errorf("emitted module missing %q:\n%s", want, src)
		}
	}

	// The runner scaffold is unchanged: --with adds a file, it does not
	// replace the migrate/seed runner the plain init writes.
	if _, err := os.Stat(filepath.Join(dir, "cmd", "shop", "main.go")); err != nil {
		t.Errorf("--with nucleus must keep writing the runner: %v", err)
	}

	// Next steps name the mount line and the full-app path on the Nucleus
	// side, so the reader knows which of the two generators owns main.go.
	for _, want := range []string{
		"go get github.com/jcsvwinston/nucleus@latest",
		"Mount(shop.Module(client))",
		"nucleus new shop --with quark",
		"Created nucleus.yml",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("next steps missing %q:\n%s", want, out)
		}
	}

	// The next steps say FromConfigFile("nucleus.yml"), so the file must
	// exist: without it the recipe compiles and dies at boot with "open
	// nucleus.yml: no such file or directory". The minimum Nucleus needs is
	// the default database (the one .quark.yml names, as a URL), and env.
	cfg := readNucleusConfig(t, dir)
	if got := cfg["database_default"]; got != "default" {
		t.Errorf("nucleus.yml database_default = %v, want \"default\"", got)
	}
	if got := nucleusDefaultURL(t, cfg); got != "sqlite://myapp.db" {
		t.Errorf("nucleus.yml databases.default.url = %q, want the .quark.yml database as a sqlite:// URL", got)
	}
	if got := cfg["env"]; got != "development" {
		t.Errorf("nucleus.yml env = %v, want \"development\"", got)
	}
	if _, ok := cfg["port"].(int); !ok {
		t.Errorf("nucleus.yml port = %v (%T), want an integer", cfg["port"], cfg["port"])
	}
}

// readNucleusConfig parses the nucleus.yml init wrote under dir.
func readNucleusConfig(t *testing.T, dir string) map[string]any {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(dir, "nucleus.yml"))
	if err != nil {
		t.Fatalf("init --with nucleus did not write nucleus.yml: %v", err)
	}
	var cfg map[string]any
	if err := yaml.Unmarshal(raw, &cfg); err != nil {
		t.Fatalf("nucleus.yml is not valid YAML: %v\n%s", err, raw)
	}
	return cfg
}

// nucleusDefaultURL returns databases.default.url from a parsed nucleus.yml.
func nucleusDefaultURL(t *testing.T, cfg map[string]any) string {
	t.Helper()
	dbs, _ := cfg["databases"].(map[string]any)
	def, _ := dbs["default"].(map[string]any)
	url, _ := def["url"].(string)
	return url
}

// The driver module the emitted file blank-imports follows the dialect, and
// the package name is a valid identifier even when the directory is not.
func TestInitWithNucleusFollowsDialectAndPackageName(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "my-shop.api")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if _, err := runInitArgs(t, "--dir", dir, "--dialect", "postgresql", "--with", "nucleus"); err != nil {
		t.Fatalf("init: %v", err)
	}
	src, err := os.ReadFile(filepath.Join(dir, "internal", "myshopapi", "module.go"))
	if err != nil {
		t.Fatalf("module not written under a sanitized package dir: %v", err)
	}
	for _, want := range []string{
		"package myshopapi",
		`_ "github.com/jcsvwinston/quark/drivers/postgres"`,
	} {
		if !strings.Contains(string(src), want) {
			t.Errorf("emitted module missing %q:\n%s", want, src)
		}
	}
	// nucleus.yml follows the dialect too, as the URL form Nucleus parses.
	if got := nucleusDefaultURL(t, readNucleusConfig(t, dir)); got != "postgres://user:pass@localhost/myapp?sslmode=disable" {
		t.Errorf("nucleus.yml databases.default.url = %q, want the postgres:// placeholder", got)
	}
}

// Every dialect init accepts maps to a URL Nucleus resolves to a driver
// (postgres://, mysql://, sqlite://, sqlserver://, oracle://) and names the
// same database as the .quark.yml placeholder.
func TestNucleusDatabaseURLCoversEveryDialect(t *testing.T) {
	for dialect, want := range map[string]string{
		"postgresql": "postgres://user:pass@localhost/myapp?sslmode=disable",
		"postgres":   "postgres://user:pass@localhost/myapp?sslmode=disable",
		"mysql":      "mysql://user:pass@localhost:3306/myapp",
		"mariadb":    "mysql://user:pass@localhost:3306/myapp",
		"sqlite":     "sqlite://myapp.db",
		"mssql":      "sqlserver://user:pass@localhost:1433?database=myapp",
		"sqlserver":  "sqlserver://user:pass@localhost:1433?database=myapp",
		"oracle":     "oracle://user:pass@localhost:1521/xe",
	} {
		if got := nucleusDatabaseURL(dialect); got != want {
			t.Errorf("nucleusDatabaseURL(%q) = %q, want %q", dialect, got, want)
		}
	}
	if got := nucleusDatabaseURL("bogus"); got != "" {
		t.Errorf("nucleusDatabaseURL(bogus) = %q, want empty", got)
	}
}

// An unknown --with target fails before anything is written, the way an
// unknown dialect does.
func TestInitWithRejectsUnknownTarget(t *testing.T) {
	dir := t.TempDir()
	_, err := runInitArgs(t, "--dir", dir, "--dialect", "sqlite", "--with", "rails")
	if err == nil || !strings.Contains(err.Error(), `unknown --with target "rails"`) || !strings.Contains(err.Error(), "nucleus") {
		t.Fatalf("expected an error naming the target and the accepted values, got %v", err)
	}
	entries, _ := os.ReadDir(dir)
	if len(entries) != 0 {
		t.Fatalf("files were written despite the invalid --with target: %v", entries)
	}
}

// Without --with nothing changes: no internal/ package appears.
func TestInitWithoutWithWritesNoModule(t *testing.T) {
	dir := t.TempDir()
	if _, err := runInitArgs(t, "--dir", dir, "--dialect", "sqlite"); err != nil {
		t.Fatalf("init: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "internal")); !os.IsNotExist(err) {
		t.Fatalf("plain init must not write internal/: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "nucleus.yml")); !os.IsNotExist(err) {
		t.Fatalf("plain init must not write nucleus.yml: %v", err)
	}
}

// An existing module.go is never overwritten (same contract as the runner).
func TestInitWithNucleusKeepsExistingModule(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "go.mod"), []byte("module example.com/shop\n\ngo 1.25\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	modDir := filepath.Join(dir, "internal", "shop")
	if err := os.MkdirAll(modDir, 0o755); err != nil {
		t.Fatal(err)
	}
	mine := "package shop // hand-written\n"
	if err := os.WriteFile(filepath.Join(modDir, "module.go"), []byte(mine), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := runInitArgs(t, "--dir", dir, "--dialect", "sqlite", "--with", "nucleus"); err != nil {
		t.Fatalf("init: %v", err)
	}
	got, _ := os.ReadFile(filepath.Join(modDir, "module.go"))
	if string(got) != mine {
		t.Fatalf("init overwrote a hand-written module.go:\n%s", got)
	}
}

// An existing nucleus.yml (the fuller one `nucleus new` writes, or a
// hand-tuned one) is never overwritten.
func TestInitWithNucleusKeepsExistingConfig(t *testing.T) {
	dir := t.TempDir()
	mine := "database_default: default\ndatabases:\n  default:\n    url: sqlite://mine.db\nport: 9090\nenv: production\n"
	if err := os.WriteFile(filepath.Join(dir, "nucleus.yml"), []byte(mine), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := runInitArgs(t, "--dir", dir, "--dialect", "sqlite", "--with", "nucleus"); err != nil {
		t.Fatalf("init: %v", err)
	}
	got, _ := os.ReadFile(filepath.Join(dir, "nucleus.yml"))
	if string(got) != mine {
		t.Fatalf("init overwrote an existing nucleus.yml:\n%s", got)
	}
}
