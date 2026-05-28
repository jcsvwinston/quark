// Copyright 2026 jcsvwinston
// SPDX-License-Identifier: Apache-2.0

// Package reporter is the structured-failure sink every bug-bash phase
// writes to. A phase records a Failure via Fail; the failure is both
// surfaced loudly through testing.TB and, when BUGBASH_REPORT_DIR is set,
// appended as one JSON object per line to failures.jsonl for the
// bugbash-reporter subagent to classify.
//
// JSONL (not a JSON array) is deliberate: it is append-safe across the
// sequential subtests a phase runs, and a partial run still yields a
// parseable file.
package reporter

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// EnvReportDir is the env var a phase run sets to the directory where
// failures.jsonl is written. Unset (e.g. a local `go test` without the
// /bugbash harness) means "report to the test log only".
const EnvReportDir = "BUGBASH_REPORT_DIR"

// Category buckets a failure by root cause. Mirrors the taxonomy the
// bugbash-reporter subagent classifies against.
type Category string

const (
	CategoryRegression      Category = "regression"
	CategoryDialectSpecific Category = "dialect-specific"
	CategoryGap             Category = "gap"
	CategoryDocDrift        Category = "doc-drift"
	CategoryTestOnly        Category = "test-only"
)

// Severity is the blocking weight of a failure. P0 blocks a patch release.
type Severity string

const (
	SeverityP0 Severity = "P0"
	SeverityP1 Severity = "P1"
	SeverityP2 Severity = "P2"
	SeverityP3 Severity = "P3"
)

// Reproducer carries everything a human needs to reproduce a failure by
// hand: the seed, the exact `go test` command, and the suspected files.
type Reproducer struct {
	Seed    int64    `json:"seed,omitempty"`
	Command string   `json:"command,omitempty"`
	Files   []string `json:"files,omitempty"`
}

// Failure is one structured bug-bash finding.
type Failure struct {
	Phase      string     `json:"phase"`
	Test       string     `json:"test"`
	Engine     string     `json:"engine"`
	Category   Category   `json:"category"`
	Severity   Severity   `json:"severity"`
	Error      string     `json:"error"`
	Reproducer Reproducer `json:"reproducer,omitzero"`
	Time       time.Time  `json:"time"`
}

// TB is the subset of testing.TB the reporter needs. Declaring it locally
// keeps the package importable from non-test code and trivially fakeable
// in reporter's own unit test.
type TB interface {
	Helper()
	Errorf(format string, args ...any)
}

var mu sync.Mutex

// Fail records f: it always reports through t (loud, non-aborting via
// Errorf) and, when BUGBASH_REPORT_DIR is set, appends f to
// failures.jsonl. A phase must not abort on the first failure — the value
// of the bug-bash is the aggregate, so Fail never calls Fatal.
func Fail(t TB, f Failure) {
	t.Helper()
	if f.Time.IsZero() {
		f.Time = time.Now().UTC()
	}
	t.Errorf("[%s/%s] %s on %s: %s", f.Severity, f.Category, f.Test, f.Engine, f.Error)
	if dir := os.Getenv(EnvReportDir); dir != "" {
		if err := appendJSONL(dir, f); err != nil {
			t.Errorf("reporter: could not persist failure to %s: %v", dir, err)
		}
	}
}

// appendJSONL appends f as a single line to <dir>/failures.jsonl, creating
// the directory if needed. Guarded by a package mutex so concurrent
// subtests cannot interleave a line.
func appendJSONL(dir string, f Failure) error {
	mu.Lock()
	defer mu.Unlock()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	line, err := json.Marshal(f)
	if err != nil {
		return err
	}
	file, err := os.OpenFile(filepath.Join(dir, "failures.jsonl"), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return err
	}
	defer file.Close()
	_, err = file.Write(append(line, '\n'))
	return err
}
