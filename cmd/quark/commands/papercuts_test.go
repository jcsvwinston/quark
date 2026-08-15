// Copyright 2026 jcsvwinston
// SPDX-License-Identifier: Apache-2.0

// Regression tests for the v1.4.1 CLI papercuts surfaced by
// quantum-coverage-demo alongside QCD-CLI-1/2/3:
//
//   - `migrate status` before the first `up` failed with a raw
//     "relation quark_migrations does not exist" instead of reporting zero
//     applied migrations, and never listed pending migrations despite
//     "Show migration status".
//   - `seed run` (all seeders) iterated a Go map — random order — while
//     claiming "registration order".
//   - `quark init` ignored the directory's go.mod and wrote the
//     github.com/user/myapp placeholder as project.module.
package commands

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/jcsvwinston/quark"
	"github.com/jcsvwinston/quark/migrate"
	"github.com/spf13/viper"

	"context"
)

// withSQLiteConfig points the CLI config at a throwaway on-disk SQLite
// database and restores the previous values on cleanup.
func withSQLiteConfig(t *testing.T) {
	t.Helper()
	dsn := filepath.Join(t.TempDir(), "cli.db")
	for _, kv := range [][2]string{
		{"database.default.driver", "sqlite"},
		{"database.default.dsn", dsn},
	} {
		key, old := kv[0], viper.GetString(kv[0])
		viper.Set(key, kv[1])
		t.Cleanup(func() { viper.Set(key, old) })
	}
}

// captureStdout runs fn with os.Stdout redirected and returns what it wrote.
func captureStdout(t *testing.T, fn func() error) (string, error) {
	t.Helper()
	old := os.Stdout
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	os.Stdout = w
	defer func() { os.Stdout = old }()

	var buf strings.Builder
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		b := make([]byte, 4096)
		for {
			n, readErr := r.Read(b)
			buf.Write(b[:n])
			if readErr != nil {
				return
			}
		}
	}()

	fnErr := fn()
	w.Close()
	wg.Wait()
	return buf.String(), fnErr
}

// `migrate status` on a fresh database must report zero applied migrations,
// not a missing-table error, and must list registered-but-unapplied
// migrations as pending.
func TestMigrateStatusFreshDatabaseListsPending(t *testing.T) {
	withSQLiteConfig(t)

	migrate.Reset()
	t.Cleanup(migrate.Reset)
	noop := func(ctx context.Context, client *quark.Client) error { return nil }
	migrate.Register(&migrate.Migration{ID: "0002_add_orders", Up: noop, Down: noop})
	migrate.Register(&migrate.Migration{ID: "0001_add_users", Up: noop, Down: noop})

	out, err := captureStdout(t, runMigrateStatus)
	if err != nil {
		t.Fatalf("migrate status on a fresh database must not fail, got: %v", err)
	}
	if !strings.Contains(out, "[ ] 0001_add_users") || !strings.Contains(out, "[ ] 0002_add_orders") {
		t.Errorf("status does not list registered migrations as pending:\n%s", out)
	}
	if strings.Index(out, "0001_add_users") > strings.Index(out, "0002_add_orders") {
		t.Errorf("pending migrations not listed in ID order:\n%s", out)
	}
}

// `seed run` without --name must run seeders in registration order, as its
// own output comment promises — not in Go map iteration order.
func TestSeedRunHonorsRegistrationOrder(t *testing.T) {
	withSQLiteConfig(t)

	oldRegistry, oldOrder := seederRegistry, seederOrder
	seederRegistry, seederOrder = map[string]SeederFunc{}, nil
	t.Cleanup(func() { seederRegistry, seederOrder = oldRegistry, oldOrder })

	var got []string
	names := []string{"zeta", "alpha", "mike", "juliet", "bravo", "yankee", "echo", "quebec"}
	for _, name := range names {
		name := name
		RegisterSeeder(name, func(ctx context.Context, client *quark.Client) error {
			got = append(got, name)
			return nil
		})
	}

	if _, err := captureStdout(t, runSeedRun); err != nil {
		t.Fatalf("seed run: %v", err)
	}
	if fmt.Sprint(got) != fmt.Sprint(names) {
		t.Errorf("seeders ran out of registration order:\n got  %v\n want %v", got, names)
	}
}

// `quark init` in a directory with a go.mod must adopt the real module path
// and derive the project name from it, instead of writing the
// github.com/user/myapp placeholder.
func TestInitReadsGoModule(t *testing.T) {
	dir := t.TempDir()
	goMod := "module github.com/acme/shop\n\ngo 1.25\n"
	if err := os.WriteFile(filepath.Join(dir, "go.mod"), []byte(goMod), 0o644); err != nil {
		t.Fatal(err)
	}

	oldDir, oldDialect := initDir, initDialect
	initDir, initDialect = dir, "sqlite"
	t.Cleanup(func() { initDir, initDialect = oldDir, oldDialect })

	if _, err := captureStdout(t, runInit); err != nil {
		t.Fatalf("init: %v", err)
	}

	cfg, err := os.ReadFile(filepath.Join(dir, ".quark.yml"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(cfg), "github.com/acme/shop") {
		t.Errorf("project.module ignores the directory's go.mod:\n%s", cfg)
	}
	if strings.Contains(string(cfg), "github.com/user/myapp") {
		t.Errorf("placeholder module survived despite a real go.mod:\n%s", cfg)
	}
	if !strings.Contains(string(cfg), "name: shop") {
		t.Errorf("project.name not derived from the module path:\n%s", cfg)
	}
}
