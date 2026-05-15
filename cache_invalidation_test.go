// Copyright 2026 jcsvwinston
// SPDX-License-Identifier: Apache-2.0

package quark

import (
	"context"
	"database/sql"
	"sync"
	"testing"
	"time"
)

// invalidationRecorder is a CacheStore that records every InvalidateTags
// call so tests can assert which tags landed. Get/Set/Delete are no-ops
// returning errors — we only exercise the invalidation path.
type invalidationRecorder struct {
	mu    sync.Mutex
	calls [][]string
}

func newInvalidationRecorder() *invalidationRecorder {
	return &invalidationRecorder{}
}

func (r *invalidationRecorder) Get(context.Context, string) ([]byte, error) {
	return nil, errNotImplementedForTest
}
func (r *invalidationRecorder) Set(context.Context, string, []byte, time.Duration, ...string) error {
	return errNotImplementedForTest
}
func (r *invalidationRecorder) Delete(context.Context, string) error {
	return errNotImplementedForTest
}
func (r *invalidationRecorder) InvalidateTags(_ context.Context, tags ...string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	cp := make([]string, len(tags))
	copy(cp, tags)
	r.calls = append(r.calls, cp)
	return nil
}
func (r *invalidationRecorder) lastTags() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	if len(r.calls) == 0 {
		return nil
	}
	return r.calls[len(r.calls)-1]
}
func (r *invalidationRecorder) callCount() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.calls)
}

var errNotImplementedForTest = errPlaceholder("invalidationRecorder: only InvalidateTags is implemented")

type errPlaceholder string

func (e errPlaceholder) Error() string { return string(e) }

// TestRowTag_Format pins the row-tag shape: `<table>:<pk>` with %v
// formatting for the PK value.
func TestRowTag_Format(t *testing.T) {
	cases := []struct {
		name    string
		table   string
		hasComp bool
		pk      any
		want    string
	}{
		{"int64", "users", false, int64(42), "users:42"},
		{"string", "orders", false, "abc-123", "orders:abc-123"},
		// String PK containing the separator: the tag IS ambiguous if
		// a consumer naïvely splits on ":", but the contract is that
		// the tag is OPAQUE — consumers compare equality, they don't
		// parse. Pin the format so a future change to the formatter
		// doesn't quietly break round-tripping.
		{"string pk containing separator is preserved verbatim", "orders", false, "abc:def", "orders:abc:def"},
		{"empty table is rejected", "", false, int64(1), ""},
		{"nil pk is rejected", "users", false, nil, ""},
		{"composite pk yields empty (gap)", "ledger", true, []any{1, 2}, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			q := &BaseQuery{table: tc.table}
			if tc.hasComp {
				q.meta = &ModelMeta{HasCompositePK: true}
			}
			if got := q.rowTag(tc.pk); got != tc.want {
				t.Errorf("rowTag = %q, want %q", got, tc.want)
			}
		})
	}
}

// TestInvalidateRowTag_EmitsRowTagOnly checks the helper that runs
// post-Create: it emits the row tag alone, leaving the table tag for
// executeExec's own InvalidateTags call.
func TestInvalidateRowTag_EmitsRowTagOnly(t *testing.T) {
	rec := newInvalidationRecorder()
	c := &Client{cacheStore: rec}
	q := &BaseQuery{client: c, table: "users"}

	q.invalidateRowTag(context.Background(), int64(7))

	if rec.callCount() != 1 {
		t.Fatalf("want 1 InvalidateTags call, got %d", rec.callCount())
	}
	got := rec.lastTags()
	if len(got) != 1 || got[0] != "users:7" {
		t.Errorf("tags = %v, want [users:7]", got)
	}
}

// TestInvalidateRowTag_NoopWhenTagEmpty: no client / no table / nil pk /
// composite PK all skip the InvalidateTags call entirely.
func TestInvalidateRowTag_NoopWhenTagEmpty(t *testing.T) {
	rec := newInvalidationRecorder()
	c := &Client{cacheStore: rec}

	t.Run("composite pk skips", func(t *testing.T) {
		rec.calls = nil
		q := &BaseQuery{client: c, table: "users", meta: &ModelMeta{HasCompositePK: true}}
		q.invalidateRowTag(context.Background(), []any{1, 2})
		if rec.callCount() != 0 {
			t.Errorf("composite PK should skip; got %v", rec.calls)
		}
	})
	t.Run("nil pk skips", func(t *testing.T) {
		rec.calls = nil
		q := &BaseQuery{client: c, table: "users"}
		q.invalidateRowTag(context.Background(), nil)
		if rec.callCount() != 0 {
			t.Errorf("nil pk should skip; got %v", rec.calls)
		}
	})
	t.Run("no cache store skips", func(t *testing.T) {
		q := &BaseQuery{client: &Client{}, table: "users"}
		q.invalidateRowTag(context.Background(), int64(1))
		// Doesn't panic, doesn't emit anywhere.
	})
}

// TestExecuteExec_PassesRowTagAlongTable proves the F4-6 wire: when a
// CRUD method passes a row tag to executeExec, the resulting
// InvalidateTags call carries BOTH the table tag (historical default)
// AND the row tag (new). Mutations whose callers know nothing about
// the affected PK still get the table-only fallback.
//
// We can't run a real DB here, but executeExec calls back into a
// QueryFunc-style ExecContext. We swap in a no-op exec via the
// BaseQuery.exec field of an Executor that returns a fake result.
//
// Dialect-independence: the wired-up behaviour under test (building the
// tag slice before passing it to CacheStore.InvalidateTags) lives in
// pure-Go logic that does not touch SQL generation. SQLite is fixed as
// the dialect to keep the fixture simple; the integration suite
// (testcontainers, CI matrix on 4 motors + SQLite) covers the actual
// SQL emit paths.
func TestExecuteExec_PassesRowTagAlongTable(t *testing.T) {
	rec := newInvalidationRecorder()
	exec := &noopExec{}
	c := &Client{
		cacheStore: rec,
		limits:     DefaultLimits(),
		dialect:    SQLite(),
		guard:      NewSQLGuard(),
	}
	q := &BaseQuery{
		client:  c,
		ctx:     context.Background(),
		dialect: SQLite(),
		guard:   c.guard,
		table:   "users",
		exec:    exec,
	}

	t.Run("with row tag", func(t *testing.T) {
		rec.calls = nil
		if _, err := q.executeExec(context.Background(), "UPDATE users SET name=? WHERE id=?", []any{"Alice", 1}, "users:1"); err != nil {
			t.Fatalf("executeExec: %v", err)
		}
		got := rec.lastTags()
		if len(got) != 2 || got[0] != "users" || got[1] != "users:1" {
			t.Errorf("tags = %v, want [users, users:1]", got)
		}
	})

	t.Run("without row tag falls back to table", func(t *testing.T) {
		rec.calls = nil
		if _, err := q.executeExec(context.Background(), "DELETE FROM users WHERE deleted=?", []any{true}); err != nil {
			t.Fatalf("executeExec: %v", err)
		}
		got := rec.lastTags()
		if len(got) != 1 || got[0] != "users" {
			t.Errorf("tags = %v, want [users]", got)
		}
	})

	t.Run("empty row tag is dropped (composite-PK callers)", func(t *testing.T) {
		rec.calls = nil
		if _, err := q.executeExec(context.Background(), "UPDATE users SET x=?", []any{1}, ""); err != nil {
			t.Fatalf("executeExec: %v", err)
		}
		got := rec.lastTags()
		if len(got) != 1 || got[0] != "users" {
			t.Errorf("empty tag should be dropped; tags = %v, want [users]", got)
		}
	})

}

// noopExec is an Executor that ignores SQL entirely — Exec returns a
// fixed-rows fakeExecResult so executeExec's post-success branches
// (which is where InvalidateTags lives) are reached. Query / QueryRow
// are unused by this test file.
//
// CAUTION: this mock is ONLY valid for paths that go through Exec.
// QueryRowContext returns a nil *sql.Row — a caller that hits the
// Query path and tries to .Scan() the result will panic. The tests in
// this file deliberately stay on Exec; expanding them to cover the
// Query path requires a real *sql.DB (the database/sql package gives
// no public constructor for *sql.Row).
type noopExec struct{}

func (n *noopExec) ExecContext(context.Context, string, ...any) (sql.Result, error) {
	return fakeExecResult{}, nil
}
func (n *noopExec) QueryContext(context.Context, string, ...any) (*sql.Rows, error) {
	return nil, nil
}
func (n *noopExec) QueryRowContext(context.Context, string, ...any) *sql.Row {
	return nil
}

type fakeExecResult struct{}

func (fakeExecResult) LastInsertId() (int64, error) { return 0, nil }
func (fakeExecResult) RowsAffected() (int64, error) { return 1, nil }
