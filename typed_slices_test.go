// Copyright 2026 jcsvwinston
// SPDX-License-Identifier: Apache-2.0

package quark_test

import (
	"context"
	"testing"

	"github.com/jcsvwinston/quark"
	_ "modernc.org/sqlite"
)

type tsItem struct {
	ID   int64  `db:"id" pk:"true"`
	Name string `db:"name"`
}

// TestTypedSliceHelpers covers AQ-07: WhereIn/DeleteBatch demand []any, so
// the most common shape — ids []int64 from a previous query — forced a
// hand-written conversion loop. WhereInOf / DeleteBatchOf accept the typed
// slice directly.
func TestTypedSliceHelpers(t *testing.T) {
	ctx := context.Background()
	client, err := quark.New("sqlite", "file:typedslices?mode=memory&cache=shared")
	if err != nil {
		t.Fatalf("new: %v", err)
	}
	defer client.Close()

	if err := client.Migrate(ctx, &tsItem{}); err != nil {
		t.Fatalf("migrate: %v", err)
	}

	var ids []int64
	for _, name := range []string{"a", "b", "c", "d"} {
		it := tsItem{Name: name}
		if err := quark.For[tsItem](ctx, client).Create(&it); err != nil {
			t.Fatalf("seed: %v", err)
		}
		ids = append(ids, it.ID)
	}

	t.Run("WhereInOfInt64", func(t *testing.T) {
		got, err := quark.WhereInOf(quark.For[tsItem](ctx, client), "id", ids[:3]).
			Limit(10).List()
		if err != nil {
			t.Fatalf("WhereInOf: %v", err)
		}
		if len(got) != 3 {
			t.Errorf("expected 3 rows, got %d", len(got))
		}
	})

	t.Run("WhereInOfString", func(t *testing.T) {
		n, err := quark.WhereInOf(quark.For[tsItem](ctx, client), "name", []string{"a", "d"}).Count()
		if err != nil {
			t.Fatalf("WhereInOf strings: %v", err)
		}
		if n != 2 {
			t.Errorf("expected 2, got %d", n)
		}
	})

	t.Run("DeleteBatchOfInt64", func(t *testing.T) {
		n, err := quark.DeleteBatchOf(quark.For[tsItem](ctx, client), ids[:2])
		if err != nil {
			t.Fatalf("DeleteBatchOf: %v", err)
		}
		if n != 2 {
			t.Errorf("expected 2 deleted, got %d", n)
		}
		left, err := quark.For[tsItem](ctx, client).Count()
		if err != nil {
			t.Fatalf("count: %v", err)
		}
		if left != 2 {
			t.Errorf("expected 2 remaining, got %d", left)
		}
	})
}
