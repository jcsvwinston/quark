// Copyright 2026 jcsvwinston
// SPDX-License-Identifier: Apache-2.0

package reporter

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

type fakeTB struct{ errors int }

func (f *fakeTB) Helper()               {}
func (f *fakeTB) Errorf(string, ...any) { f.errors++ }

func TestFailWritesJSONL(t *testing.T) {
	dir := t.TempDir()
	t.Setenv(EnvReportDir, dir)

	ft := &fakeTB{}
	Fail(ft, Failure{
		Phase:    "f01_smoke",
		Test:     "TestRoundTrip",
		Engine:   "sqlite",
		Category: CategoryDialectSpecific,
		Severity: SeverityP1,
		Error:    "boom",
	})
	if ft.errors != 1 {
		t.Fatalf("Fail reported through TB.Errorf %d times, want 1", ft.errors)
	}

	b, err := os.ReadFile(filepath.Join(dir, "failures.jsonl"))
	if err != nil {
		t.Fatalf("read failures.jsonl: %v", err)
	}
	var got Failure
	if err := json.Unmarshal(b, &got); err != nil {
		t.Fatalf("unmarshal recorded failure: %v", err)
	}
	if got.Engine != "sqlite" || got.Severity != SeverityP1 || got.Category != CategoryDialectSpecific {
		t.Errorf("round-trip mismatch: %+v", got)
	}
	if got.Time.IsZero() {
		t.Error("Fail did not stamp Time")
	}
}

func TestFailWithoutReportDirOnlyLogs(t *testing.T) {
	t.Setenv(EnvReportDir, "") // empty == disabled

	ft := &fakeTB{}
	Fail(ft, Failure{Test: "T", Engine: "sqlite", Error: "x"})
	if ft.errors != 1 {
		t.Fatalf("expected exactly one Errorf with no report dir, got %d", ft.errors)
	}
}
