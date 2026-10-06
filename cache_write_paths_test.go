// Copyright 2026 jcsvwinston
// SPDX-License-Identifier: Apache-2.0

package quark

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"slices"
	"testing"

	_ "modernc.org/sqlite"
)

// QK-66, as the tags reach the store, on SQLite. internal/enginesuite
// (CacheWritePaths) runs every write path on the six engines against a
// cached read.

type cwpUnitRow struct {
	ID   int64  `db:"id" pk:"true"`
	Code string `db:"code" quark:"unique"`
	Name string `db:"name"`
}

func (cwpUnitRow) TableName() string { return "qk66_unit_rows" }

func cwpUnitClient(t *testing.T, name string, opts ...any) *Client {
	t.Helper()
	opts = append([]any{WithLogger(slog.New(slog.NewTextHandler(io.Discard, nil)))}, opts...)
	c, err := New("sqlite", "file:"+name+"?mode=memory&cache=shared", opts...)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { _ = c.Close() })
	if err := c.Migrate(context.Background(), &cwpUnitRow{}); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	return c
}

// Upsert through RETURNING dropped nothing: the statement goes through the
// single-row query primitive. It drops the table tag and the tag of the row
// it inserted or updated, in one call.
func TestUpsertReturningDropsTableAndRowTag(t *testing.T) {
	rec := newInvalidationRecorder()
	c := cwpUnitClient(t, "qk66_upsert", WithCacheStore(rec))
	ctx := context.Background()

	r := cwpUnitRow{Code: "a", Name: "a0"}
	if err := For[cwpUnitRow](ctx, c).Upsert(&r, []string{"code"}, []string{"name"}); err != nil {
		t.Fatalf("insert: %v", err)
	}
	want := []string{"qk66_unit_rows", fmt.Sprintf("qk66_unit_rows:%d", r.ID)}
	if got := rec.lastTags(); !slices.Equal(got, want) {
		t.Errorf("Upsert that inserted dropped %v, want %v", got, want)
	}

	rec.calls = nil
	if err := For[cwpUnitRow](ctx, c).Upsert(&cwpUnitRow{Code: "a", Name: "a1"}, []string{"code"}, []string{"name"}); err != nil {
		t.Fatalf("update: %v", err)
	}
	if got := rec.lastTags(); rec.callCount() != 1 || !slices.Equal(got, want) {
		t.Errorf("Upsert that updated dropped %v, want one call with %v", rec.calls, want)
	}

	// No update branch and a conflict: nothing written, nothing dropped.
	rec.calls = nil
	if err := For[cwpUnitRow](ctx, c).Upsert(&cwpUnitRow{Code: "a", Name: "a2"}, []string{"code"}, nil); err != nil {
		t.Fatalf("do nothing: %v", err)
	}
	if rec.callCount() != 0 {
		t.Errorf("Upsert that wrote nothing dropped %v", rec.calls)
	}
}

// A write inside a transaction drops its tags when it runs, and the commit
// drops them again, all of them in one call; a rollback drops nothing more.
func TestTxCommitDropsTheWriteTagsAgain(t *testing.T) {
	rec := newInvalidationRecorder()
	c := cwpUnitClient(t, "qk66_tx", WithCacheStore(rec))
	ctx := context.Background()

	tx, err := c.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	a := cwpUnitRow{Code: "a", Name: "a0"}
	if err := ForTx[cwpUnitRow](ctx, tx).Create(&a); err != nil {
		t.Fatal(err)
	}
	b := cwpUnitRow{Code: "b", Name: "b0"}
	if err := ForTx[cwpUnitRow](ctx, tx).Upsert(&b, []string{"code"}, []string{"name"}); err != nil {
		t.Fatal(err)
	}
	if _, err := ForTx[cwpUnitRow](ctx, tx).UpdateFields(&cwpUnitRow{ID: a.ID, Name: "a1"}, "name"); err != nil {
		t.Fatal(err)
	}
	if rec.callCount() != 3 {
		t.Fatalf("the writes dropped %v, want one call each", rec.calls)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	want := []string{"qk66_unit_rows", fmt.Sprintf("qk66_unit_rows:%d", a.ID), fmt.Sprintf("qk66_unit_rows:%d", b.ID)}
	slices.Sort(want)
	if got := rec.lastTags(); rec.callCount() != 4 || !slices.Equal(got, want) {
		t.Errorf("the commit dropped %v, want one more call with %v", rec.calls, want)
	}
	if _, ok := c.txCacheTags.Load(tx.tx); ok {
		t.Error("the committed transaction's tags are still kept")
	}

	rec.calls = nil
	tx, err = c.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := ForTx[cwpUnitRow](ctx, tx).Create(&cwpUnitRow{Code: "c", Name: "c0"}); err != nil {
		t.Fatal(err)
	}
	if err := tx.Rollback(); err != nil {
		t.Fatal(err)
	}
	if rec.callCount() != 1 {
		t.Errorf("a rolled-back transaction dropped %v, want only the write's call", rec.calls)
	}
	if _, ok := c.txCacheTags.Load(tx.tx); ok {
		t.Error("the rolled-back transaction's tags are still kept")
	}

	// UpdateBatch runs in a transaction of its own: its commit drops the
	// rows' tags again too.
	rec.calls = nil
	if err := For[cwpUnitRow](ctx, c).UpdateBatch([]*cwpUnitRow{{ID: a.ID, Name: "a2"}}); err != nil {
		t.Fatal(err)
	}
	want = []string{"qk66_unit_rows", fmt.Sprintf("qk66_unit_rows:%d", a.ID)}
	if got := rec.lastTags(); rec.callCount() != 2 || !slices.Equal(got, want) {
		t.Errorf("UpdateBatch dropped %v, want the statement's call and the commit's, both %v", rec.calls, want)
	}
}

// Without a cache store a transaction keeps nothing.
func TestTxWithoutCacheKeepsNoTags(t *testing.T) {
	c := cwpUnitClient(t, "qk66_nocache")
	tx, err := c.BeginTx(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback() }()
	if err := ForTx[cwpUnitRow](context.Background(), tx).Create(&cwpUnitRow{Code: "a"}); err != nil {
		t.Fatal(err)
	}
	if _, ok := c.txCacheTags.Load(tx.tx); ok {
		t.Error("a client without a cache store keeps the tags of its transactions")
	}
}
