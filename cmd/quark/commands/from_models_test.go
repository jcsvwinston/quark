// Copyright 2026 jcsvwinston
// SPDX-License-Identifier: Apache-2.0

// Regression tests for DX-18 (DX audit 2026-08-16): the suite had three
// paths to a schema and none produced domain DDL usable on PostgreSQL —
// `quark migrate create` gave a ~20-line skeleton and the reference app
// wrote 8 CREATE TABLE + 8 CREATE INDEX by hand (159 lines).
//
// `quark migrate create <name> --from-models <dir> --dialect postgresql`
// now loads the model structs statically (go/packages), renders
// dialect-correct DDL through the SAME type mapping the runtime migrator
// uses (internal/migrate.SQLTypeWithOpts / PKColumnSQL), and writes a
// migration whose Up applies it and whose Down drops it in reverse
// dependency order.
package commands

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeFromModelsFixture(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	goMod := "module example.com/ddl\n\ngo 1.25\n"
	if err := os.WriteFile(filepath.Join(dir, "go.mod"), []byte(goMod), 0o644); err != nil {
		t.Fatal(err)
	}
	models := `package models

import "time"

type User struct {
	ID        int64     ` + "`db:\"id\" pk:\"true\"`" + `
	Email     string    ` + "`db:\"email,size=190\" quark:\"unique\"`" + `
	Name      string    ` + "`db:\"name\" quark:\"not_null\"`" + `
	CreatedAt time.Time ` + "`db:\"created_at\"`" + `
}

type Article struct {
	ID       int64   ` + "`db:\"id\" pk:\"true\"`" + `
	Title    string  ` + "`db:\"title\" quark:\"not_null\"`" + `
	Score    float64 ` + "`db:\"score\"`" + `
	Draft    bool    ` + "`db:\"draft\"`" + `
	AuthorID int64   ` + "`db:\"author_id\" quark:\"not_null\"`" + `
	Author   User    ` + "`rel:\"belongs_to\" join:\"author_id\"`" + `
}
`
	if err := os.MkdirAll(filepath.Join(dir, "models"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "models", "models.go"), []byte(models), 0o644); err != nil {
		t.Fatal(err)
	}
	return dir
}

func TestMigrateCreateFromModelsPostgres(t *testing.T) {
	if testing.Short() {
		t.Skip("loads packages with the go toolchain; skipped with -short")
	}
	dir := writeFromModelsFixture(t)

	oldWd, _ := os.Getwd()
	if err := os.Chdir(dir); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chdir(oldWd) })

	oldFrom, oldDialect := migrateFromModels, migrateDialect
	migrateFromModels, migrateDialect = "./models", "postgresql"
	t.Cleanup(func() { migrateFromModels, migrateDialect = oldFrom, oldDialect })

	if err := runMigrateCreate("domain_schema"); err != nil {
		t.Fatalf("migrate create --from-models: %v", err)
	}

	matches, err := filepath.Glob(filepath.Join(dir, "migrations", "*_domain_schema.go"))
	if err != nil || len(matches) != 1 {
		t.Fatalf("expected one migration file, got %v (err=%v)", matches, err)
	}
	src, err := os.ReadFile(matches[0])
	if err != nil {
		t.Fatal(err)
	}
	body := string(src)

	for _, want := range []string{
		`CREATE TABLE IF NOT EXISTS users`,
		`SERIAL PRIMARY KEY`,
		`VARCHAR(190) UNIQUE`,
		`NOT NULL`,
		`CREATE TABLE IF NOT EXISTS articles`,
		`score REAL`,
		`BOOLEAN`,
		`REFERENCES users(id)`,
		`CREATE INDEX IF NOT EXISTS idx_articles_author_id`,
		`DROP TABLE IF EXISTS articles`,
		`DROP TABLE IF EXISTS users`,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("generated migration missing %q:\n%s", want, body)
		}
	}
	// Order: users (parent) must be created before articles; dropped after.
	if strings.Index(body, "CREATE TABLE IF NOT EXISTS users") > strings.Index(body, "CREATE TABLE IF NOT EXISTS articles") {
		t.Errorf("parent table must be created first:\n%s", body)
	}
	if strings.Index(body, "DROP TABLE IF EXISTS articles") > strings.Index(body, "DROP TABLE IF EXISTS users") {
		t.Errorf("child table must be dropped first:\n%s", body)
	}
}
