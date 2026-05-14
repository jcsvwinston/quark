// Copyright 2026 jcsvwinston
// SPDX-License-Identifier: Apache-2.0

package quark_test

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/jcsvwinston/quark"

	_ "modernc.org/sqlite"
)

func newBackfillClient(t *testing.T) *quark.Client {
	t.Helper()
	dsn := fmt.Sprintf("file:%s_%d?mode=memory&cache=shared", t.Name(), time.Now().UnixNano())
	c, err := quark.New("sqlite", dsn)
	if err != nil {
		t.Fatalf("quark.New: %v", err)
	}
	t.Cleanup(func() { _ = c.Close() })
	return c
}

// seedBackfillFixture creates a `bf_users` table with the given
// number of rows (PKs 1..n) and returns the client + a "label"
// column that the tests can later UPDATE in their backfill
// callbacks to verify the callback fired on the expected PKs.
func seedBackfillFixture(t *testing.T, c *quark.Client, n int) {
	t.Helper()
	ctx := context.Background()
	if _, err := c.Raw().ExecContext(ctx,
		`CREATE TABLE bf_users (id INTEGER PRIMARY KEY, label TEXT)`); err != nil {
		t.Fatalf("seed CREATE: %v", err)
	}
	for i := 1; i <= n; i++ {
		if _, err := c.Raw().ExecContext(ctx,
			`INSERT INTO bf_users (id) VALUES (?)`, i); err != nil {
			t.Fatalf("seed insert %d: %v", i, err)
		}
	}
}

// TestBackfill_HappyPath: 10 rows, batch size 4 → 3 batches
// (4 + 4 + 2). The callback records every PK it sees; after the
// backfill, we assert the full set 1..10 was processed in order.
func TestBackfill_HappyPath(t *testing.T) {
	ctx := context.Background()
	c := newBackfillClient(t)
	seedBackfillFixture(t, c, 10)

	var seen []int64
	err := c.Backfill(ctx, quark.BackfillSpec{
		Name:      "bf_happy",
		Table:     "bf_users",
		BatchSize: 4,
		Process: func(_ context.Context, batchPKs []int64) error {
			seen = append(seen, batchPKs...)
			return nil
		},
	})
	if err != nil {
		t.Fatalf("Backfill: %v", err)
	}
	if len(seen) != 10 {
		t.Fatalf("want 10 PKs processed, got %d: %v", len(seen), seen)
	}
	// Verify ascending order — the spec promises it.
	for i := 1; i < len(seen); i++ {
		if seen[i] <= seen[i-1] {
			t.Errorf("PKs should be ascending; got %v", seen)
			break
		}
	}
	// Verify the complete set.
	sort.Slice(seen, func(i, j int) bool { return seen[i] < seen[j] })
	for i := int64(1); i <= 10; i++ {
		if seen[i-1] != i {
			t.Errorf("missing PK %d in seen=%v", i, seen)
		}
	}
}

// TestBackfill_ResumesAfterCallbackError: callback fails on the
// second batch. The first batch is recorded as applied; on
// re-invocation the helper should start AFTER the first batch's
// max PK, not from scratch.
func TestBackfill_ResumesAfterCallbackError(t *testing.T) {
	ctx := context.Background()
	c := newBackfillClient(t)
	seedBackfillFixture(t, c, 10)

	calls := 0
	var seen []int64
	cb := func(_ context.Context, batchPKs []int64) error {
		calls++
		if calls == 2 {
			return fmt.Errorf("boom on batch 2")
		}
		seen = append(seen, batchPKs...)
		return nil
	}
	err := c.Backfill(ctx, quark.BackfillSpec{
		Name:      "bf_resume",
		Table:     "bf_users",
		BatchSize: 4,
		Process:   cb,
	})
	if err == nil {
		t.Fatalf("first Backfill: want error, got nil")
	}
	if len(seen) != 4 {
		t.Fatalf("after first error, want 4 PKs seen (batch 1 only), got %d: %v", len(seen), seen)
	}

	// Re-invoke with a working callback — should resume after PK 4.
	calls = 0
	var resumeSeen []int64
	cb2 := func(_ context.Context, batchPKs []int64) error {
		resumeSeen = append(resumeSeen, batchPKs...)
		return nil
	}
	if err := c.Backfill(ctx, quark.BackfillSpec{
		Name:      "bf_resume", // SAME name → reads existing state
		Table:     "bf_users",
		BatchSize: 4,
		Process:   cb2,
	}); err != nil {
		t.Fatalf("second Backfill (resume): %v", err)
	}
	if len(resumeSeen) != 6 {
		t.Fatalf("after resume, want 6 PKs (5..10), got %d: %v", len(resumeSeen), resumeSeen)
	}
	if resumeSeen[0] != 5 {
		t.Errorf("resume should start at PK 5, got %d", resumeSeen[0])
	}
}

// TestBackfill_CompletedIsIdempotent: after a successful backfill,
// re-invoking with the same Name should be a fast no-op (no
// callback invocations).
func TestBackfill_CompletedIsIdempotent(t *testing.T) {
	ctx := context.Background()
	c := newBackfillClient(t)
	seedBackfillFixture(t, c, 5)

	calls := 0
	cb := func(_ context.Context, batchPKs []int64) error {
		calls++
		return nil
	}
	spec := quark.BackfillSpec{
		Name:      "bf_idempotent",
		Table:     "bf_users",
		BatchSize: 10,
		Process:   cb,
	}
	if err := c.Backfill(ctx, spec); err != nil {
		t.Fatalf("first Backfill: %v", err)
	}
	if calls != 1 {
		t.Fatalf("first run: want 1 batch, got %d", calls)
	}
	// Second run — should see 0 callbacks.
	calls = 0
	if err := c.Backfill(ctx, spec); err != nil {
		t.Fatalf("second Backfill: %v", err)
	}
	if calls != 0 {
		t.Errorf("re-invocation of completed backfill should NOT call callback, got %d calls", calls)
	}
}

// TestBackfill_ValidatesInputs: blank Name / Table / nil Process
// → descriptive error, no DDL executed.
func TestBackfill_ValidatesInputs(t *testing.T) {
	ctx := context.Background()
	c := newBackfillClient(t)
	cb := func(_ context.Context, _ []int64) error { return nil }

	cases := []struct {
		name string
		spec quark.BackfillSpec
		want string // substring expected in error
	}{
		{"empty Name", quark.BackfillSpec{Table: "x", Process: cb}, "Name"},
		{"empty Table", quark.BackfillSpec{Name: "n", Process: cb}, "Table"},
		{"nil Process", quark.BackfillSpec{Name: "n", Table: "x"}, "Process"},
		{"bad table identifier", quark.BackfillSpec{Name: "n", Table: "t; DROP", Process: cb}, "table name"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := c.Backfill(ctx, tc.spec)
			if err == nil {
				t.Fatalf("want error containing %q, got nil", tc.want)
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Errorf("error should mention %q, got %q", tc.want, err)
			}
		})
	}
}

// TestBackfill_EmptyTable: zero rows → zero callbacks, returns
// nil. Pins the no-op contract so a future refactor that
// accidentally calls Process(empty-slice) is caught.
func TestBackfill_EmptyTable(t *testing.T) {
	ctx := context.Background()
	c := newBackfillClient(t)
	seedBackfillFixture(t, c, 0)

	calls := 0
	err := c.Backfill(ctx, quark.BackfillSpec{
		Name:      "bf_empty",
		Table:     "bf_users",
		BatchSize: 10,
		Process: func(_ context.Context, _ []int64) error {
			calls++
			return nil
		},
	})
	if err != nil {
		t.Fatalf("Backfill on empty table: %v", err)
	}
	if calls != 0 {
		t.Errorf("empty table should yield 0 callbacks, got %d", calls)
	}
}

// TestBackfill_CustomPKColumn: PKColumn defaults to "id" but a
// caller can pass any sortable integer column.
func TestBackfill_CustomPKColumn(t *testing.T) {
	ctx := context.Background()
	c := newBackfillClient(t)
	// Schema with a non-"id" PK to ensure the helper uses
	// spec.PKColumn rather than always assuming "id".
	if _, err := c.Raw().ExecContext(ctx,
		`CREATE TABLE bf_custom (rowid_alt INTEGER PRIMARY KEY, payload TEXT)`); err != nil {
		t.Fatalf("seed: %v", err)
	}
	for i := 1; i <= 3; i++ {
		if _, err := c.Raw().ExecContext(ctx,
			`INSERT INTO bf_custom (rowid_alt) VALUES (?)`, i); err != nil {
			t.Fatalf("insert: %v", err)
		}
	}

	var seen []int64
	err := c.Backfill(ctx, quark.BackfillSpec{
		Name:      "bf_custom_pk",
		Table:     "bf_custom",
		PKColumn:  "rowid_alt",
		BatchSize: 2,
		Process: func(_ context.Context, batchPKs []int64) error {
			seen = append(seen, batchPKs...)
			return nil
		},
	})
	if err != nil {
		t.Fatalf("Backfill: %v", err)
	}
	if len(seen) != 3 || seen[0] != 1 || seen[2] != 3 {
		t.Errorf("want [1 2 3], got %v", seen)
	}
}
