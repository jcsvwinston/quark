// Copyright 2026 jcsvwinston
// SPDX-License-Identifier: Apache-2.0

package quarkmigrate_test

import (
	"bytes"
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/jcsvwinston/quark"
	"github.com/jcsvwinston/quark/quarkmigrate"

	_ "modernc.org/sqlite"
)

// runFixture is the canonical model for the F3-5 CLI tests. Single
// struct kept tiny so the rendered plan is deterministic for
// regex / substring assertions.
type runFixture struct {
	ID   int64  `db:"id" pk:"true"`
	Name string `db:"name" quark:"not_null"`
}

func newClient(t *testing.T) *quark.Client {
	t.Helper()
	// Per-test in-memory DSN so multiple test functions don't share
	// state via `cache=shared`. Reviewer S1: if these tests ever
	// gain `t.Parallel()`, shared cache would let one test see
	// another's tables. Pure `:memory:` is private to each Go
	// `database/sql` connection, but `quark.New` opens a pool —
	// using `file:<unique>?mode=memory&cache=shared` keeps the
	// pool consistent within ONE test while still isolating across
	// tests.
	dsn := fmt.Sprintf("file:%s_%d?mode=memory&cache=shared", t.Name(), time.Now().UnixNano())
	c, err := quark.New("sqlite", dsn)
	if err != nil {
		t.Fatalf("quark.New: %v", err)
	}
	t.Cleanup(func() { _ = c.Close() })
	return c
}

// TestParseAction pins the four shapes ParseAction accepts. A
// failure here means someone changed the action grammar without
// updating the docs — the CHANGELOG / migrations.mdx promises
// these exact strings.
func TestParseAction(t *testing.T) {
	cases := []struct {
		in      string
		want    quarkmigrate.Action
		wantErr bool
	}{
		{"plan", quarkmigrate.ActionPlan, false},
		{"verify", quarkmigrate.ActionVerify, false},
		{"apply", quarkmigrate.ActionApply, false},
		{"", quarkmigrate.ActionPlan, false}, // default
		{"rollback", "", true},               // unknown
		{"PLAN", "", true},                   // case-sensitive
		{" plan", "", true},                  // no trim
	}
	for _, tc := range cases {
		t.Run(tc.in, func(t *testing.T) {
			got, err := quarkmigrate.ParseAction(tc.in)
			if (err != nil) != tc.wantErr {
				t.Fatalf("err: got %v, wantErr %v", err, tc.wantErr)
			}
			if got != tc.want {
				t.Errorf("action: got %q, want %q", got, tc.want)
			}
		})
	}
}

// TestRun_Plan_EmptyDB_ExitsSuccessAndPrintsPlan: empty DB + plan
// action → exit 0, stdout shows the "operation count" header.
// Pins the contract that `plan` is informational and never fails.
func TestRun_Plan_EmptyDB_ExitsSuccessAndPrintsPlan(t *testing.T) {
	ctx := context.Background()
	c := newClient(t)
	var stdout, stderr bytes.Buffer

	code := quarkmigrate.RunWithOutput(ctx, quarkmigrate.ActionPlan, c, &stdout, &stderr, &runFixture{})

	if code != quarkmigrate.ExitSuccess {
		t.Fatalf("exit code: want 0, got %d (stderr=%q)", code, stderr.String())
	}
	out := stdout.String()
	if !strings.Contains(out, "operation(s)") {
		t.Errorf("plan output should mention operation count, got %q", out)
	}
	if !strings.Contains(out, "CREATE TABLE") {
		t.Errorf("plan output should describe the CREATE TABLE op, got %q", out)
	}
}

// TestRun_Verify_NonEmpty_ExitsDriftDetected: verify with a dirty
// schema returns exit 1 — the CI gate use case.
func TestRun_Verify_NonEmpty_ExitsDriftDetected(t *testing.T) {
	ctx := context.Background()
	c := newClient(t)
	var stdout, stderr bytes.Buffer

	code := quarkmigrate.RunWithOutput(ctx, quarkmigrate.ActionVerify, c, &stdout, &stderr, &runFixture{})

	if code != quarkmigrate.ExitDriftDetected {
		t.Fatalf("exit code: want 1 (drift), got %d", code)
	}
	if !strings.Contains(stderr.String(), "drifted") {
		t.Errorf("verify should mention 'drifted' on stderr, got %q", stderr.String())
	}
}

// TestRun_Verify_Empty_ExitsSuccess: after Migrate, verify returns
// 0. This pins the post-apply "schema is clean" signal.
func TestRun_Verify_Empty_ExitsSuccess(t *testing.T) {
	ctx := context.Background()
	c := newClient(t)
	if err := c.Migrate(ctx, &runFixture{}); err != nil {
		t.Fatalf("Migrate: %v", err)
	}
	var stdout, stderr bytes.Buffer

	code := quarkmigrate.RunWithOutput(ctx, quarkmigrate.ActionVerify, c, &stdout, &stderr, &runFixture{})

	if code != quarkmigrate.ExitSuccess {
		t.Fatalf("exit code: want 0 (clean), got %d (stderr=%q)", code, stderr.String())
	}
	if !strings.Contains(stdout.String(), "in sync") {
		t.Errorf("verify-clean should mention 'in sync', got %q", stdout.String())
	}
}

// TestRun_Apply_RoundTrip: apply against an empty DB creates the
// table; a subsequent plan run shows the schema in sync. The
// end-to-end happy path.
func TestRun_Apply_RoundTrip(t *testing.T) {
	ctx := context.Background()
	c := newClient(t)
	var stdout, stderr bytes.Buffer

	// First call: apply. Should exit 0 and create the table.
	code := quarkmigrate.RunWithOutput(ctx, quarkmigrate.ActionApply, c, &stdout, &stderr, &runFixture{})
	if code != quarkmigrate.ExitSuccess {
		t.Fatalf("apply exit code: want 0, got %d (stderr=%q)", code, stderr.String())
	}
	if !strings.Contains(stdout.String(), "applied successfully") {
		t.Errorf("apply success should print 'applied successfully', got %q", stdout.String())
	}

	// Second call: plan. Should now report sync.
	stdout.Reset()
	stderr.Reset()
	code = quarkmigrate.RunWithOutput(ctx, quarkmigrate.ActionPlan, c, &stdout, &stderr, &runFixture{})
	if code != quarkmigrate.ExitSuccess {
		t.Fatalf("post-apply plan exit code: want 0, got %d", code)
	}
	if !strings.Contains(stdout.String(), "in sync") {
		t.Errorf("post-apply plan should report sync, got %q", stdout.String())
	}
}

// TestRun_Apply_EmptyPlan_DoesNotCallApplyPlan: when the plan is
// empty (schema already in sync), `apply` is a no-op — it shouldn't
// call ApplyPlan, just print sync and exit 0.
func TestRun_Apply_EmptyPlan_DoesNotCallApplyPlan(t *testing.T) {
	ctx := context.Background()
	c := newClient(t)
	if err := c.Migrate(ctx, &runFixture{}); err != nil {
		t.Fatalf("Migrate: %v", err)
	}
	var stdout, stderr bytes.Buffer

	code := quarkmigrate.RunWithOutput(ctx, quarkmigrate.ActionApply, c, &stdout, &stderr, &runFixture{})

	if code != quarkmigrate.ExitSuccess {
		t.Fatalf("apply-empty exit code: want 0, got %d", code)
	}
	// "applied successfully" only prints on a non-empty apply; an
	// empty plan should not produce it (we'd be lying about doing
	// work).
	if strings.Contains(stdout.String(), "applied successfully") {
		t.Errorf("apply on empty plan should NOT print 'applied successfully', got %q", stdout.String())
	}
}

// TestRun_PlanMigrationError_ReturnsExitError: when PlanMigration
// itself fails (e.g., the user passes a non-struct model), Run
// returns exit 2 and writes the error to stderr.
func TestRun_PlanMigrationError_ReturnsExitError(t *testing.T) {
	ctx := context.Background()
	c := newClient(t)
	var stdout, stderr bytes.Buffer

	// 42 is an int, not a struct — PlanMigration will reject it.
	code := quarkmigrate.RunWithOutput(ctx, quarkmigrate.ActionPlan, c, &stdout, &stderr, 42)

	if code != quarkmigrate.ExitError {
		t.Fatalf("exit code: want 2 (error), got %d", code)
	}
	if !strings.Contains(stderr.String(), "PlanMigration failed") {
		t.Errorf("stderr should mention 'PlanMigration failed', got %q", stderr.String())
	}
}

// TestRun_UnknownAction_ReturnsExitError: an Action value that
// isn't one of the three constants returns exit 2. This is mostly
// a guard against future code that constructs an Action from a
// string without going through ParseAction.
func TestRun_UnknownAction_ReturnsExitError(t *testing.T) {
	ctx := context.Background()
	c := newClient(t)
	var stdout, stderr bytes.Buffer

	code := quarkmigrate.RunWithOutput(ctx, quarkmigrate.Action("nonsense"), c, &stdout, &stderr, &runFixture{})

	if code != quarkmigrate.ExitError {
		t.Fatalf("exit code: want 2 (error), got %d", code)
	}
	if !strings.Contains(stderr.String(), "unknown action") {
		t.Errorf("stderr should mention 'unknown action', got %q", stderr.String())
	}
}
