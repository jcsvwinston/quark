// Copyright 2026 jcsvwinston
// SPDX-License-Identifier: Apache-2.0

// Tests for DX-N3: `migrate create --from-models <dir> --emit json` prints
// the rendered DDL as one JSON document for another tool to write (nucleus's
// makemigrations is the first), and writes no file. The expectations come
// from buildDDLStatements itself, so the document can never drift from what
// `--emit go` would have embedded in the migration file.
package commands

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

// chdirFixture moves the test into a fresh --from-models fixture module and
// back when it ends; the static loader resolves ./models from the cwd.
func chdirFixture(t *testing.T) string {
	t.Helper()
	dir := writeFromModelsFixture(t)
	oldWd, _ := os.Getwd()
	if err := os.Chdir(dir); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chdir(oldWd) })
	return dir
}

// setMigrateCreateFlags sets the package-level flag variables for one run
// and restores them afterwards.
func setMigrateCreateFlags(t *testing.T, fromModels, dialect, emit string) {
	t.Helper()
	oldFrom, oldDialect, oldEmit := migrateFromModels, migrateDialect, migrateEmit
	migrateFromModels, migrateDialect, migrateEmit = fromModels, dialect, emit
	t.Cleanup(func() { migrateFromModels, migrateDialect, migrateEmit = oldFrom, oldDialect, oldEmit })
}

func TestMigrateCreateEmitJSONPrintsTheRenderedDDL(t *testing.T) {
	if testing.Short() {
		t.Skip("loads packages with the go toolchain; skipped with -short")
	}
	dir := chdirFixture(t)

	models, err := loadModelsForDDL("./models")
	if err != nil {
		t.Fatalf("loading the fixture models: %v", err)
	}

	for _, spelling := range []string{"postgresql", "mysql", "sqlite", "mssql", "oracle"} {
		t.Run(spelling, func(t *testing.T) {
			setMigrateCreateFlags(t, "./models", spelling, "json")

			out, err := captureStdout(t, func() error { return runMigrateCreate("domain_schema") })
			if err != nil {
				t.Fatalf("migrate create --emit json: %v", err)
			}

			// One document and nothing else on stdout.
			dec := json.NewDecoder(strings.NewReader(out))
			var doc DDLDocument
			if err := dec.Decode(&doc); err != nil {
				t.Fatalf("stdout is not one JSON document: %v\n%s", err, out)
			}
			if dec.More() {
				t.Fatalf("stdout carries more than the JSON document:\n%s", out)
			}
			if strings.Contains(out, "Rendered DDL") || strings.Contains(out, "Created migration") {
				t.Fatalf("progress lines leaked into stdout in json mode:\n%s", out)
			}

			wantDialect := normalizeDDLDialect(spelling)
			wantUp, wantDown, err := buildDDLStatements(models, wantDialect)
			if err != nil {
				t.Fatal(err)
			}
			// The contract version is pinned here on purpose: moving it is
			// the incompatible-change signal, and this test must move with it.
			if doc.Schema != 1 {
				t.Errorf("schema = %d, want 1", doc.Schema)
			}
			if doc.Tool != "quark" {
				t.Errorf("tool = %q, want quark", doc.Tool)
			}
			if doc.Version != cliVersion() {
				t.Errorf("version = %q, want %q (what `quark version` prints)", doc.Version, cliVersion())
			}
			if doc.Dialect != wantDialect {
				t.Errorf("dialect = %q, want %q", doc.Dialect, wantDialect)
			}
			wantModels := []DDLDocumentModel{{Name: "Article", Table: "articles"}, {Name: "User", Table: "users"}}
			if !reflect.DeepEqual(doc.Models, wantModels) {
				t.Errorf("models = %+v, want %+v", doc.Models, wantModels)
			}
			if !reflect.DeepEqual(doc.Up, wantUp) {
				t.Errorf("up differs from buildDDLStatements:\n got %q\nwant %q", doc.Up, wantUp)
			}
			if !reflect.DeepEqual(doc.Down, wantDown) {
				t.Errorf("down differs from buildDDLStatements:\n got %q\nwant %q", doc.Down, wantDown)
			}
			// The statements are data, not Go: no Exec wrapper, no semicolon added.
			for _, stmt := range append(append([]string{}, doc.Up...), doc.Down...) {
				if strings.Contains(stmt, "client.Exec") || strings.HasSuffix(stmt, ";") {
					t.Errorf("statement is not the bare DDL: %q", stmt)
				}
			}
		})
	}

	// json mode writes nothing: not even the migrations directory.
	if _, err := os.Stat(filepath.Join(dir, "migrations")); !os.IsNotExist(err) {
		matches, _ := filepath.Glob(filepath.Join(dir, "migrations", "*"))
		t.Fatalf("--emit json must write no file, found %v (stat err=%v)", matches, err)
	}
}

func TestMigrateCreateEmitJSONNeedsFromModels(t *testing.T) {
	chdirFixture(t)
	setMigrateCreateFlags(t, "", "postgresql", "json")

	out, err := captureStdout(t, func() error { return runMigrateCreate("domain_schema") })
	if err == nil {
		t.Fatal("--emit json without --from-models must fail")
	}
	for _, want := range []string{"--emit json", "--from-models"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q does not name %q", err, want)
		}
	}
	if out != "" {
		t.Errorf("nothing may reach stdout on failure, got %q", out)
	}
	if _, statErr := os.Stat("migrations"); !os.IsNotExist(statErr) {
		t.Errorf("a failed --emit json run must not create the migrations directory")
	}
}

func TestMigrateCreateEmitRejectsUnknownMode(t *testing.T) {
	chdirFixture(t)
	setMigrateCreateFlags(t, "./models", "postgresql", "yaml")

	_, err := captureStdout(t, func() error { return runMigrateCreate("domain_schema") })
	if err == nil || !strings.Contains(err.Error(), "--emit go") || !strings.Contains(err.Error(), "--emit json") {
		t.Fatalf("an unknown --emit value must name the two modes, got %v", err)
	}
}

// The default is --emit go and it still writes the file: the flag's zero
// value and its registered default both mean the migration file.
func TestMigrateCreateEmitDefaultIsGo(t *testing.T) {
	if f := migrateCreateCmd.Flags().Lookup("emit"); f == nil || f.DefValue != "go" {
		t.Fatalf("--emit must exist with default go, got %+v", f)
	}
	dir := chdirFixture(t)
	setMigrateCreateFlags(t, "", "", "")

	if _, err := captureStdout(t, func() error { return runMigrateCreate("empty_skeleton") }); err != nil {
		t.Fatalf("migrate create with an empty --emit: %v", err)
	}
	matches, _ := filepath.Glob(filepath.Join(dir, "migrations", "*_empty_skeleton.go"))
	if len(matches) != 1 {
		t.Fatalf("expected one migration file, got %v", matches)
	}
}

// Every spelling the --dialect help lists, plus the one nucleus reports its
// engine as (postgresql), normalizes to the name the type mapping knows.
func TestNormalizeDDLDialectSpellings(t *testing.T) {
	cases := map[string]string{
		"postgresql": "postgres",
		"postgres":   "postgres",
		"PostgreSQL": "postgres",
		"mysql":      "mysql",
		"mariadb":    "mariadb",
		"sqlite":     "sqlite",
		"mssql":      "mssql",
		"sqlserver":  "mssql",
		"oracle":     "oracle",
		" oracle ":   "oracle",
		"bogus":      "",
		"":           "",
	}
	for in, want := range cases {
		if got := normalizeDDLDialect(in); got != want {
			t.Errorf("normalizeDDLDialect(%q) = %q, want %q", in, got, want)
		}
	}
}
