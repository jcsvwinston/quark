// Copyright 2026 jcsvwinston
// SPDX-License-Identifier: Apache-2.0

// Regression tests for QCD-CLI-1: `quark model generate --fields` emitted Go
// that did not compile (missing time/encoding/json imports) and dropped the
// primary-key declaration entirely (the computed QuarkTag was never rendered
// and the definition path never set IsPK).
package commands

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// generateWidgetFromFields runs the --fields path exactly as the CLI would for
//
//	quark model generate Widget --fields 'id:int64,label:string,due_at:time.Time' --out <dir>
//
// and returns the generated source.
func generateWidgetFromFields(t *testing.T, dir string) string {
	t.Helper()

	oldFields, oldOut, oldPkg := modelFields, modelOutDir, modelPackage
	modelFields = "id:int64,label:string,due_at:time.Time"
	modelOutDir = dir
	modelPackage = "models"
	defer func() { modelFields, modelOutDir, modelPackage = oldFields, oldOut, oldPkg }()

	if err := runModelGen([]string{"Widget"}); err != nil {
		t.Fatalf("model generate --fields: %v", err)
	}

	src, err := os.ReadFile(filepath.Join(dir, "widget.go"))
	if err != nil {
		t.Fatalf("generated file missing: %v", err)
	}
	return string(src)
}

// QCD-CLI-1 defect 1: a time.Time field must produce a compiling file. The
// output is compiled for real in a throwaway module — string-matching the
// import block is not enough evidence.
func TestModelGenerateFieldsOutputCompiles(t *testing.T) {
	dir := t.TempDir()
	modDir := filepath.Join(dir, "mod")
	outDir := filepath.Join(modDir, "models")
	if err := os.MkdirAll(outDir, 0o755); err != nil {
		t.Fatal(err)
	}

	generateWidgetFromFields(t, outDir)

	goMod := "module tmpwidget\n\ngo 1.25\n"
	if err := os.WriteFile(filepath.Join(modDir, "go.mod"), []byte(goMod), 0o644); err != nil {
		t.Fatal(err)
	}

	cmd := exec.Command("go", "build", "./...")
	cmd.Dir = modDir
	// The generated model only uses the standard library; keep the build
	// hermetic so the test cannot reach the network.
	cmd.Env = append(os.Environ(), "GOFLAGS=-mod=mod", "GOPROXY=off")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("generated model does not compile: %v\n%s", err, out)
	}
}

// QCD-CLI-1 defect 2: the definition path computed QuarkTag="pk,auto" for id
// but the template never rendered it and IsPK was never set, so the model had
// no declared primary key at all.
func TestModelGenerateFieldsDeclaresPK(t *testing.T) {
	src := generateWidgetFromFields(t, t.TempDir())

	idLine := ""
	for _, line := range strings.Split(src, "\n") {
		if strings.Contains(line, "ID ") {
			idLine = line
			break
		}
	}
	if idLine == "" {
		t.Fatalf("no ID field in generated source:\n%s", src)
	}
	if !strings.Contains(idLine, `pk:"true"`) {
		t.Errorf("id field lacks pk:\"true\" tag: %q\nfull source:\n%s", idLine, src)
	}
}
