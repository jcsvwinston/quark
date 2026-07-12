// Copyright 2026 jcsvwinston
// SPDX-License-Identifier: Apache-2.0

package commands

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jcsvwinston/quark"
	"github.com/spf13/viper"
)

// QK-P1-2 end-to-end: `quark validate` now loads real Go structs and compares
// them against the introspected table — in both directions.
func TestValidateComparesModelAgainstTable(t *testing.T) {
	dir := t.TempDir()

	// A tiny self-contained module with one model package. The model maps
	// users.id/email but NOT the extra_col column, and declares a
	// missing_col the table doesn't have.
	if err := os.WriteFile(filepath.Join(dir, "go.mod"), []byte("module tmpmodels\n\ngo 1.21\n"), 0644); err != nil {
		t.Fatal(err)
	}
	model := `package models

type User struct {
	ID      int64  ` + "`db:\"id\" pk:\"true\"`" + `
	Email   string ` + "`db:\"email\"`" + `
	Missing string ` + "`db:\"missing_col\"`" + `
}
`
	if err := os.MkdirAll(filepath.Join(dir, "models"), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "models", "user.go"), []byte(model), 0644); err != nil {
		t.Fatal(err)
	}

	// Matching SQLite database with an extra unmapped column.
	dbPath := filepath.Join(dir, "app.db")
	client, err := quark.New("sqlite", "file:"+dbPath, quark.WithLimits(quark.Limits{AllowRawQueries: true}))
	if err != nil {
		t.Fatal(err)
	}
	if err := client.Exec(context.Background(), `CREATE TABLE users (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		email TEXT NOT NULL,
		extra_col TEXT
	)`); err != nil {
		t.Fatal(err)
	}
	client.Close()

	viper.Set("database.default.driver", "sqlite")
	viper.Set("database.default.dsn", "file:"+dbPath)
	validateTable, validateModels, validateModel = "users", filepath.Join(dir, "models"), ""
	t.Cleanup(func() {
		viper.Set("database.default.driver", "")
		viper.Set("database.default.dsn", "")
		validateTable, validateModels, validateModel, validateStrict = "", "", "", false
	})

	// missing_col is declared in Go but absent in the table → hard error.
	err = runValidate()
	if err == nil || !strings.Contains(err.Error(), "no matching column") {
		t.Fatalf("expected 'no matching column' error for missing_col, got: %v", err)
	}

	// Drop the phantom field: now only extra_col (unmapped in Go) remains —
	// OK by default, non-zero under --strict.
	model = strings.ReplaceAll(model, "\tMissing string `db:\"missing_col\"`\n", "")
	if err := os.WriteFile(filepath.Join(dir, "models", "user.go"), []byte(model), 0644); err != nil {
		t.Fatal(err)
	}
	if err := runValidate(); err != nil {
		t.Fatalf("default mode should tolerate unmapped DB columns, got: %v", err)
	}
	validateStrict = true
	err = runValidate()
	if err == nil || !strings.Contains(err.Error(), "--strict") {
		t.Fatalf("strict mode should fail on unmapped extra_col, got: %v", err)
	}
}

// A table with no matching struct must name the discovered models instead of
// pretending everything is fine.
func TestValidateUnknownModel(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "go.mod"), []byte("module tmpmodels2\n\ngo 1.21\n"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(dir, "models"), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "models", "user.go"),
		[]byte("package models\n\ntype User struct {\n\tID int64 `db:\"id\"`\n}\n"), 0644); err != nil {
		t.Fatal(err)
	}

	validateTable, validateModels, validateModel = "orders", filepath.Join(dir, "models"), ""
	t.Cleanup(func() { validateTable, validateModels, validateModel = "", "", "" })

	err := runValidate()
	if err == nil || !strings.Contains(err.Error(), "User") {
		t.Fatalf("expected the discovered models to be listed, got: %v", err)
	}
}
