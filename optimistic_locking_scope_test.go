// Copyright 2026 jcsvwinston
// SPDX-License-Identifier: Apache-2.0

package quark

import (
	"context"
	"io"
	"log/slog"
	"strings"
	"testing"
	"time"
)

// QK-58. Update, UpdateFields and Tracked.Save hold a versioned row to the
// version the entity was loaded with — ErrStaleEntity when it moved — and
// bump the version they write. The other writes of a versioned model do
// neither: UpdateMap, the update branch of Upsert and UpsertBatch, Delete and
// HardDelete of an entity, the soft delete and Restore send no version
// predicate and leave the column where it was. Quark 2.0 brings them into
// optimistic locking (DEP-2026-004); this file pins what they do until then,
// so the change at the major is a diff to these tests.
//
// The fixtures are those of update_batch_versions_test.go (QK-45).

// olsTrashed is the versioned model with a soft-delete column.
type olsTrashed struct {
	ID        int64      `db:"id" pk:"true"`
	Owner     string     `db:"owner"`
	Balance   int64      `db:"balance"`
	Version   int64      `db:"version" quark:"version"`
	DeletedAt *time.Time `db:"deleted_at"`
}

func (olsTrashed) TableName() string { return "ols_trashed" }

// olsClient opens a fresh database with one olsTrashed row: id 1, alice,
// balance 10, version 1, not trashed.
func olsClient(t *testing.T) *Client {
	t.Helper()
	name := "qk58_" + strings.NewReplacer("/", "_", " ", "_").Replace(t.Name())
	c, err := New("sqlite", "file:"+name+"?mode=memory&cache=shared",
		WithLogger(slog.New(slog.NewTextHandler(io.Discard, nil))))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { _ = c.Close() })
	ctx := context.Background()
	if err := c.Migrate(ctx, &olsTrashed{}); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	if err := For[olsTrashed](ctx, c).Create(&olsTrashed{ID: 1, Owner: "alice", Balance: 10, Version: 1}); err != nil {
		t.Fatalf("seed: %v", err)
	}
	return c
}

func TestWritesOutsideOptimisticLockingKeepTheV1Behaviour(t *testing.T) {
	ctx := context.Background()

	// UpdateMap sends no version predicate and does not move the version,
	// so its write is invisible to a holder of the row: the holder's next
	// Update sees no conflict and overwrites it.
	t.Run("UpdateMap", func(t *testing.T) {
		c := ubvClient(t)
		holder := ubvLoad(t, c, 1)[0] // 10@1
		n, err := For[ubvAccount](ctx, c).Where("id", "=", int64(1)).UpdateMap(map[string]any{"balance": int64(50)})
		if err != nil || n != 1 {
			t.Fatalf("UpdateMap = (%d, %v), want (1, nil)", n, err)
		}
		if got := ubvTable(t, c)[1]; got != "50@1" {
			t.Fatalf("after UpdateMap row 1 is %s, want 50@1: written, version not bumped (v1 behaviour)", got)
		}
		holder.Balance = 11
		if _, err := For[ubvAccount](ctx, c).Update(holder); err != nil {
			t.Fatalf("Update after UpdateMap = %v, want nil: the map's write moved no version, so the holder sees no conflict (v1 behaviour)", err)
		}
		if got := ubvTable(t, c)[1]; got != "11@2" {
			t.Fatalf("after the holder's Update row 1 is %s, want 11@2: the map's write overwritten", got)
		}
	})

	// The update branch of Upsert writes a stale entity's columns over the
	// winner's and leaves the version where the winner put it.
	t.Run("UpsertUpdateBranch", func(t *testing.T) {
		c := ubvClient(t)
		stale := ubvLoad(t, c, 1)[0] // 10@1
		ubvMoveUnderneath(t, c, 1)   // row 1 → 1010@2
		stale.Balance = 77
		if err := For[ubvAccount](ctx, c).Upsert(stale, []string{"id"}, []string{"balance"}); err != nil {
			t.Fatalf("Upsert with a stale entity = %v, want nil: no version predicate (v1 behaviour)", err)
		}
		if got := ubvTable(t, c)[1]; got != "77@2" {
			t.Fatalf("after Upsert row 1 is %s, want 77@2: the stale balance written over the winner's, version not bumped", got)
		}
		if stale.Version != 1 {
			t.Fatalf("Upsert moved the in-memory version to %d", stale.Version)
		}
	})

	t.Run("UpsertBatchUpdateBranch", func(t *testing.T) {
		c := ubvClient(t)
		stale := ubvLoad(t, c, 1, 2) // 10@1, 20@1
		ubvMoveUnderneath(t, c, 2)   // row 2 → 1020@2
		stale[0].Balance, stale[1].Balance = 11, 22
		if err := For[ubvAccount](ctx, c).UpsertBatch(stale, []string{"id"}, []string{"balance"}); err != nil {
			t.Fatalf("UpsertBatch with a stale entity = %v, want nil: no version predicate (v1 behaviour)", err)
		}
		want := map[int64]string{1: "11@1", 2: "22@2", 3: "30@1"}
		if got := ubvTable(t, c); !sameTable(got, want) {
			t.Fatalf("after UpsertBatch: %v, want %v: both rows written, no version bumped", got, want)
		}
	})

	// A Delete with a stale entity removes the winner's row: the DELETE
	// matches on the key alone (what the enterprise bench records as the
	// open half of OPS-04).
	t.Run("DeleteOfAStaleEntity", func(t *testing.T) {
		c := ubvClient(t)
		stale := ubvLoad(t, c, 2)[0]
		ubvMoveUnderneath(t, c, 2) // row 2 → 1020@2
		n, err := For[ubvAccount](ctx, c).Delete(stale)
		if err != nil || n != 1 {
			t.Fatalf("Delete of a stale entity = (%d, %v), want (1, nil): the DELETE matches the key alone (v1 behaviour)", n, err)
		}
		if _, ok := ubvTable(t, c)[2]; ok {
			t.Fatalf("row 2 survived a Delete that returned 1")
		}
	})

	t.Run("HardDeleteOfAStaleEntity", func(t *testing.T) {
		c := ubvClient(t)
		stale := ubvLoad(t, c, 3)[0]
		ubvMoveUnderneath(t, c, 3) // row 3 → 1030@2
		n, err := For[ubvAccount](ctx, c).HardDelete(stale)
		if err != nil || n != 1 {
			t.Fatalf("HardDelete of a stale entity = (%d, %v), want (1, nil): the DELETE matches the key alone (v1 behaviour)", n, err)
		}
		if _, ok := ubvTable(t, c)[3]; ok {
			t.Fatalf("row 3 survived a HardDelete that returned 1")
		}
	})

	// The soft delete trashes the winner's row without moving its version,
	// and Restore brings it back the same way.
	t.Run("SoftDeleteAndRestoreOfAStaleEntity", func(t *testing.T) {
		c := olsClient(t)
		stale, err := For[olsTrashed](ctx, c).Find(1) // 10@1
		if err != nil {
			t.Fatalf("load: %v", err)
		}
		winner, err := For[olsTrashed](ctx, c).Find(1)
		if err != nil {
			t.Fatalf("load: %v", err)
		}
		winner.Balance += 1000
		if _, err := For[olsTrashed](ctx, c).Update(&winner); err != nil { // → 1010@2
			t.Fatalf("concurrent writer: %v", err)
		}

		n, err := For[olsTrashed](ctx, c).Delete(&stale)
		if err != nil || n != 1 {
			t.Fatalf("soft Delete of a stale entity = (%d, %v), want (1, nil): no version predicate (v1 behaviour)", n, err)
		}
		if _, err := For[olsTrashed](ctx, c).Find(1); err == nil {
			t.Fatalf("the row is still visible after the soft delete")
		}
		trashed, err := For[olsTrashed](ctx, c).WithTrashed().Find(1)
		if err != nil {
			t.Fatalf("read the trashed row: %v", err)
		}
		if trashed.DeletedAt == nil || trashed.Balance != 1010 || trashed.Version != 2 {
			t.Fatalf("after the soft delete the row is %+v, want the winner's 1010@2 trashed: version not bumped", trashed)
		}

		n, err = For[olsTrashed](ctx, c).Restore(&stale)
		if err != nil || n != 1 {
			t.Fatalf("Restore with a stale entity = (%d, %v), want (1, nil): no version predicate (v1 behaviour)", n, err)
		}
		restored, err := For[olsTrashed](ctx, c).Find(1)
		if err != nil {
			t.Fatalf("read the restored row: %v", err)
		}
		if restored.DeletedAt != nil || restored.Version != 2 {
			t.Fatalf("after Restore the row is %+v, want live at version 2: version not bumped", restored)
		}
	})
}
