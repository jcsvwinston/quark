// Copyright 2026 jcsvwinston
// SPDX-License-Identifier: Apache-2.0

// `quark init --with nucleus` writes the Quark side of the Quark<->Nucleus
// seam as SOURCE TEXT: a nucleus.Module[struct{}] that wraps a *quark.Client.
// Quark cannot import Nucleus (Nucleus and Orbit import Quark — a cycle
// across the release train), so these tests parse the emitted file and pin
// the shape a Nucleus host expects; the only place the file is compiled
// against a real Nucleus is the suite's own integration lane.
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
	} {
		if !strings.Contains(out, want) {
			t.Errorf("next steps missing %q:\n%s", want, out)
		}
	}
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
