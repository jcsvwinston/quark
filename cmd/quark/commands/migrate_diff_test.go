// Copyright 2026 jcsvwinston
// SPDX-License-Identifier: Apache-2.0

package commands

import (
	"bytes"
	"context"
	"os"
	"strings"
	"testing"

	"github.com/spf13/viper"

	"github.com/jcsvwinston/quark"
)

// A8 S10 (MIG-11): the binary plans from source.

func useSQLite(t *testing.T, name string) string {
	t.Helper()
	dsn := "file:" + name + "?mode=memory&cache=shared"
	prevDriver, prevDSN := viper.GetString("database.default.driver"), viper.GetString("database.default.dsn")
	viper.Set("database.default.driver", "sqlite")
	viper.Set("database.default.dsn", dsn)
	t.Cleanup(func() {
		viper.Set("database.default.driver", prevDriver)
		viper.Set("database.default.dsn", prevDSN)
	})
	// Keep one connection open so the shared in-memory database outlives the
	// clients the commands open and close.
	keeper, err := quark.New("sqlite", dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = keeper.Close() })
	return dsn
}

func runCLI(t *testing.T, args ...string) (string, error) {
	t.Helper()
	var out bytes.Buffer
	rootCmd.SetOut(&out)
	rootCmd.SetArgs(args)
	t.Cleanup(func() { rootCmd.SetArgs([]string{}); rootCmd.SetOut(nil) })
	err := rootCmd.Execute()
	return out.String(), err
}

func TestMigrateDiffPlansFromSourceAndVerifyGates(t *testing.T) {
	t.Setenv("GOWORK", "off")
	chdirBack := chdirForTest(t)
	defer chdirBack()
	dir := writeFromModelsFixture(t)
	enterDir(t, dir)
	dsn := useSQLite(t, "s10_diff")

	// Empty database: the diff proposes the two tables, verify fails.
	out, err := runCLI(t, "migrate", "diff", "--from-models", "./models")
	if err != nil {
		t.Fatalf("diff: %v\n%s", err, out)
	}
	if !strings.Contains(out, "CREATE TABLE users") || !strings.Contains(out, "CREATE TABLE articles") {
		t.Fatalf("diff output:\n%s", out)
	}
	if out, err := runCLI(t, "migrate", "verify", "--from-models", "./models"); err == nil || !strings.Contains(err.Error(), "drift") {
		t.Fatalf("verify on an empty database should fail with drift, got %v\n%s", err, out)
	}

	// Apply the same models' DDL (the reader's other output), then the plan
	// is empty and verify passes: source and runtime mapping agree.
	models, err := loadModelsForDDL("./models")
	if err != nil {
		t.Fatal(err)
	}
	up, _, err := buildDDLStatements(models, "sqlite")
	if err != nil {
		t.Fatal(err)
	}
	c, err := quark.New("sqlite", dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	for _, stmt := range up {
		if _, err := c.Raw().ExecContext(context.Background(), stmt); err != nil {
			t.Fatalf("%s: %v", stmt, err)
		}
	}
	out, err = runCLI(t, "migrate", "plan", "--from-models", "./models")
	if err != nil || !strings.Contains(out, "in sync") {
		t.Fatalf("plan after applying the DDL: %v\n%s", err, out)
	}
	if out, err := runCLI(t, "migrate", "verify", "--from-models", "./models"); err != nil {
		t.Fatalf("verify after applying the DDL: %v\n%s", err, out)
	}
}

func TestStaticReaderLearnsIndexCheckAndDefault(t *testing.T) {
	t.Setenv("GOWORK", "off")
	chdirBack := chdirForTest(t)
	defer chdirBack()
	dir := t.TempDir()
	writeFile(t, dir+"/go.mod", "module example.com/tags\n\ngo 1.25\n")
	enterDir(t, dir)
	writeFile(t, dir+"/models/m.go", "package models\n\ntype Doc struct {\n\tID     int64  `db:\"id\" pk:\"true\"`\n\tSlug   string `db:\"slug\" quark:\"index\"`\n\tStatus string `db:\"status\" quark:\"check=status IN ('draft','live'),not_null\" default:\"'draft'\"`\n\tKind   string `db:\"kind,enum=a|b\"`\n}\n")
	models, err := loadModelsForDDL("./models")
	if err != nil {
		t.Fatal(err)
	}
	if len(models) != 1 {
		t.Fatalf("models: %+v", models)
	}
	byCol := map[string]ddlField{}
	for _, f := range models[0].Fields {
		byCol[f.Column] = f
	}
	if byCol["slug"].IndexName != "idx_docs_slug" {
		t.Errorf("index: %+v", byCol["slug"])
	}
	if byCol["status"].Check != "status IN ('draft','live')" || !byCol["status"].NotNull || byCol["status"].Default != "'draft'" {
		t.Errorf("check/not_null/default: %+v", byCol["status"])
	}
	if byCol["kind"].Check != "kind IN ('a', 'b')" {
		t.Errorf("enum: %+v", byCol["kind"])
	}
	up, _, err := buildDDLStatements(models, "sqlite")
	if err != nil {
		t.Fatal(err)
	}
	joined := strings.Join(up, "\n")
	for _, want := range []string{"CONSTRAINT ck_docs_status CHECK (status IN ('draft','live'))", "CREATE INDEX IF NOT EXISTS idx_docs_slug ON docs (slug)", "DEFAULT 'draft'"} {
		if !strings.Contains(joined, want) {
			t.Errorf("DDL lacks %q:\n%s", want, joined)
		}
	}
	// And the desired schema for the plan carries the same declarations.
	desired, err := desiredSchemaFromModels(models, "sqlite")
	if err != nil {
		t.Fatal(err)
	}
	if len(desired.Tables[0].Indexes) != 1 || len(desired.Tables[0].Checks) != 2 {
		t.Errorf("desired schema: %+v", desired.Tables[0])
	}
}

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := mkdirAllForFile(path); err != nil {
		t.Fatal(err)
	}
	if err := writeFileString(path, content); err != nil {
		t.Fatal(err)
	}
}

// chdirForTest returns a func that restores the working directory; enterDir
// moves into a fixture module, because the static reader resolves "." as the
// main module the models have to belong to.
func chdirForTest(t *testing.T) func() {
	t.Helper()
	old, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	return func() { _ = os.Chdir(old) }
}

func enterDir(t *testing.T, dir string) {
	t.Helper()
	if err := os.Chdir(dir); err != nil {
		t.Fatal(err)
	}
}
