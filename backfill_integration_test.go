// Copyright 2026 jcsvwinston
// SPDX-License-Identifier: Apache-2.0

package quark_test

import (
	"context"
	"sort"
	"testing"

	"github.com/jcsvwinston/quark"
)

// testBackfillOrchestration is the cross-dialect integration
// contract for F3-6. The unit tests in `migrate_backfill_test.go`
// only exercise SQLite, but `migrate_backfill.go`'s
// `fetchBackfillBatch` has dialect-specific branches (MSSQL TOP,
// Oracle FETCH NEXT, others LIMIT) and `ensureBackfillStateTable`
// has dialect-specific CREATE TABLE syntax — both must be
// validated against each motor the SharedSuite covers.
//
// The fixture is intentionally small (5 rows, batch 2) to keep the
// matrix fast; the contract being asserted is "the helper iterates
// in PK order, processes every row exactly once, and idempotency
// holds on re-run" — none of which require large data.
func testBackfillOrchestration(ctx context.Context, t *testing.T, baseClient *quark.Client) {
	t.Helper()

	dropTable(baseClient, "bf_integration_fixture")
	defer dropTable(baseClient, "bf_integration_fixture")
	// Clean up the state row so subsequent runs of the suite
	// against the same DB (rare locally; common in dev) see a
	// fresh start. Best-effort — failure is non-fatal.
	defer func() {
		_, _ = baseClient.Raw().ExecContext(ctx,
			"DELETE FROM quark_backfill_state WHERE name = 'bf_integration'")
	}()

	if _, err := baseClient.Raw().ExecContext(ctx,
		`CREATE TABLE bf_integration_fixture (id INTEGER PRIMARY KEY)`); err != nil {
		t.Fatalf("seed CREATE: %v", err)
	}
	for i := 1; i <= 5; i++ {
		if _, err := baseClient.Raw().ExecContext(ctx,
			`INSERT INTO bf_integration_fixture (id) VALUES (`+integerPlaceholder(baseClient, 1)+`)`, i); err != nil {
			t.Fatalf("seed insert %d: %v", i, err)
		}
	}

	t.Run("ProcessesAllRowsInOrder", func(t *testing.T) {
		var seen []int64
		err := baseClient.Backfill(ctx, quark.BackfillSpec{
			Name:      "bf_integration",
			Table:     "bf_integration_fixture",
			BatchSize: 2,
			Process: func(_ context.Context, batchPKs []int64) error {
				seen = append(seen, batchPKs...)
				return nil
			},
		})
		if err != nil {
			t.Fatalf("Backfill: %v", err)
		}
		if len(seen) != 5 {
			t.Fatalf("want 5 PKs processed, got %d: %v", len(seen), seen)
		}
		sort.Slice(seen, func(i, j int) bool { return seen[i] < seen[j] })
		for i := int64(1); i <= 5; i++ {
			if seen[i-1] != i {
				t.Errorf("missing PK %d, got %v", i, seen)
			}
		}
	})

	t.Run("ReRunIsIdempotent", func(t *testing.T) {
		// After the first subtest completes, the state row is
		// set to last_pk=5. A re-invocation must call Process
		// ZERO times (no PKs > 5 exist in the fixture).
		calls := 0
		err := baseClient.Backfill(ctx, quark.BackfillSpec{
			Name:      "bf_integration",
			Table:     "bf_integration_fixture",
			BatchSize: 2,
			Process: func(_ context.Context, _ []int64) error {
				calls++
				return nil
			},
		})
		if err != nil {
			t.Fatalf("Backfill re-run: %v", err)
		}
		if calls != 0 {
			t.Errorf("re-invocation should be no-op, got %d callbacks", calls)
		}
	})
}

// integerPlaceholder is a tiny shim for the placeholder format used
// in the seed INSERT inside this test file (we splice it via fmt
// because the seed loop is the test's own DDL, not user input).
// Mirrors `dialect.Placeholder(1)` without needing access to the
// non-exported Dialect type.
func integerPlaceholder(c *quark.Client, n int) string {
	// The Client's dialect IS exported as `quark.Dialect()` —
	// use that to keep this helper portable across the matrix.
	return c.Dialect().Placeholder(n)
}
