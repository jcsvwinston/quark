// Copyright 2026 jcsvwinston
// SPDX-License-Identifier: Apache-2.0

package quark

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"strings"
	"testing"
	"time"
)

// QK-58 (DEP-2026-004). Every write that carries an entity of a versioned
// model is inside optimistic locking: Delete and HardDelete of an entity,
// the soft delete and Restore hold the row to the version the entity was
// loaded with — ErrStaleEntity when it moved — and the soft delete and
// Restore bump the version they write; the update branch of Upsert and
// UpsertBatch does the same, treating a zero version as new; UpdateMap has
// no entity, so it checks nothing and bumps the version of the rows it
// writes, and refuses a map that names the version column. In v1 none of
// them sent a version predicate or moved the version: a stale Delete removed
// the winner's row, a stale Upsert wrote over it, and an UpdateMap's write
// was invisible to a holder of the row.
//
// The fixtures are those of update_batch_versions_test.go (QK-45).
// internal/enginesuite (UpsertVersioned, WhereGuards/Versioned) runs the
// same on the six engines.

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

func TestWritesOfAVersionedModelJoinOptimisticLocking(t *testing.T) {
	ctx := context.Background()

	// UpdateMap bumps the version of the rows it writes, so a holder of the
	// row sees the write on their next Update instead of overwriting it.
	t.Run("UpdateMap", func(t *testing.T) {
		c := ubvClient(t)
		holder := ubvLoad(t, c, 1)[0] // 10@1
		n, err := For[ubvAccount](ctx, c).Where("id", "=", int64(1)).UpdateMap(map[string]any{"balance": int64(50)})
		if err != nil || n != 1 {
			t.Fatalf("UpdateMap = (%d, %v), want (1, nil)", n, err)
		}
		if got := ubvTable(t, c)[1]; got != "50@2" {
			t.Fatalf("after UpdateMap row 1 is %s, want 50@2: written, version bumped", got)
		}
		holder.Balance = 11
		if _, err := For[ubvAccount](ctx, c).Update(holder); !errors.Is(err, ErrStaleEntity) {
			t.Fatalf("Update after UpdateMap = %v, want ErrStaleEntity: the map's write moved the version", err)
		}
		if got := ubvTable(t, c)[1]; got != "50@2" {
			t.Fatalf("after the holder's Update row 1 is %s, want the map's 50@2 kept", got)
		}
		// A map that names the version column is refused: the column is
		// Quark's to write.
		_, err = For[ubvAccount](ctx, c).Where("id", "=", int64(1)).UpdateMap(map[string]any{"balance": int64(60), "version": int64(9)})
		if !errors.Is(err, ErrInvalidQuery) {
			t.Fatalf("UpdateMap naming the version column = %v, want ErrInvalidQuery", err)
		}
		if got := ubvTable(t, c)[1]; got != "50@2" {
			t.Fatalf("the refused UpdateMap wrote: row 1 is %s", got)
		}
		// A model without a version column: the map is written as given.
		if n, err := For[ubvPlain](ctx, c).Where("id", "=", int64(1)).UpdateMap(map[string]any{"balance": int64(50)}); err != nil || n != 1 {
			t.Fatalf("UpdateMap on a model without a version = (%d, %v), want (1, nil)", n, err)
		}
	})

	// The update branch of Upsert holds the row to the entity's version and
	// bumps it; a stale entity writes nothing.
	t.Run("UpsertUpdateBranch", func(t *testing.T) {
		c := ubvClient(t)
		stale := ubvLoad(t, c, 1)[0] // 10@1
		ubvMoveUnderneath(t, c, 1)   // row 1 → 1010@2
		stale.Balance = 77
		err := For[ubvAccount](ctx, c).Upsert(stale, []string{"id"}, []string{"balance"})
		if !errors.Is(err, ErrStaleEntity) || !strings.Contains(err.Error(), "pk=1") {
			t.Fatalf("Upsert with a stale entity = %v, want ErrStaleEntity naming pk=1", err)
		}
		if got := ubvTable(t, c)[1]; got != "1010@2" {
			t.Fatalf("after the stale Upsert row 1 is %s, want the winner's 1010@2 kept", got)
		}
		if stale.Version != 1 {
			t.Fatalf("the stale Upsert moved the in-memory version to %d", stale.Version)
		}

		fresh := ubvLoad(t, c, 1)[0] // 1010@2
		fresh.Balance = 78
		if err := For[ubvAccount](ctx, c).Upsert(fresh, []string{"id"}, []string{"balance"}); err != nil {
			t.Fatalf("Upsert with a fresh entity: %v", err)
		}
		if got := ubvTable(t, c)[1]; got != "78@3" {
			t.Fatalf("after the fresh Upsert row 1 is %s, want 78@3: written, version bumped", got)
		}
		if fresh.Version != 3 {
			t.Fatalf("the fresh Upsert left the in-memory version at %d, want the row's 3", fresh.Version)
		}
		// The same struct goes on being written.
		fresh.Balance = 79
		if _, err := For[ubvAccount](ctx, c).Update(fresh); err != nil {
			t.Fatalf("Update after Upsert = %v, want nil", err)
		}
	})

	// A zero version is treated as new: the update branch holds the row to
	// nothing, writes, bumps, and the entity learns the row's version.
	t.Run("UpsertZeroVersionIsNew", func(t *testing.T) {
		c := ubvClient(t)
		ubvMoveUnderneath(t, c, 1) // row 1 → 1010@2
		e := &ubvAccount{ID: 1, Owner: "alice", Balance: 5}
		if err := For[ubvAccount](ctx, c).Upsert(e, []string{"id"}, []string{"balance"}); err != nil {
			t.Fatalf("Upsert with a zero version = %v, want nil", err)
		}
		if got := ubvTable(t, c)[1]; got != "5@3" {
			t.Fatalf("after the zero-version Upsert row 1 is %s, want 5@3", got)
		}
		if e.Version != 3 {
			t.Fatalf("the zero-version Upsert left the in-memory version at %d, want the row's 3", e.Version)
		}
		// Inserted: the row carries the entity's version, zero or not.
		n := &ubvAccount{ID: 9, Owner: "new", Balance: 1, Version: 4}
		if err := For[ubvAccount](ctx, c).Upsert(n, []string{"id"}, []string{"balance"}); err != nil {
			t.Fatalf("Upsert of a new key = %v, want nil", err)
		}
		if got := ubvTable(t, c)[9]; got != "1@4" || n.Version != 4 {
			t.Fatalf("after the insert row 9 is %s and the entity at %d, want 1@4 and 4", got, n.Version)
		}
	})

	// Without updateCols there is no update branch, and nothing to hold.
	t.Run("UpsertWithoutUpdateColsChecksNothing", func(t *testing.T) {
		c := ubvClient(t)
		stale := ubvLoad(t, c, 1)[0]
		ubvMoveUnderneath(t, c, 1)
		stale.Balance = 77
		if err := For[ubvAccount](ctx, c).Upsert(stale, []string{"id"}, nil); err != nil {
			t.Fatalf("Upsert with a stale entity and no updateCols = %v, want nil: insert-or-ignore", err)
		}
		if got := ubvTable(t, c)[1]; got != "1010@2" {
			t.Fatalf("after the upsert row 1 is %s, want 1010@2 left alone", got)
		}
	})

	t.Run("UpsertBatchUpdateBranch", func(t *testing.T) {
		c := ubvClient(t)
		stale := ubvLoad(t, c, 1, 2) // 10@1, 20@1
		ubvMoveUnderneath(t, c, 2)   // row 2 → 1020@2
		before := ubvTable(t, c)
		stale[0].Balance, stale[1].Balance = 11, 22
		err := For[ubvAccount](ctx, c).UpsertBatch(stale, []string{"id"}, []string{"balance"})
		if !errors.Is(err, ErrStaleEntity) || !strings.Contains(err.Error(), "pk=2") {
			t.Fatalf("UpsertBatch with a stale entity = %v, want ErrStaleEntity naming pk=2", err)
		}
		if got := ubvTable(t, c); !sameTable(got, before) {
			t.Fatalf("the failed batch left %v, want %v: nothing written", got, before)
		}
		fresh := ubvLoad(t, c, 1, 2)
		fresh[0].Balance, fresh[1].Balance = 11, 22
		if err := For[ubvAccount](ctx, c).UpsertBatch(fresh, []string{"id"}, []string{"balance"}); err != nil {
			t.Fatalf("UpsertBatch with fresh entities: %v", err)
		}
		want := map[int64]string{1: "11@2", 2: "22@3", 3: "30@1"}
		if got := ubvTable(t, c); !sameTable(got, want) {
			t.Fatalf("after the batch: %v, want %v", got, want)
		}
		if fresh[0].Version != 2 || fresh[1].Version != 3 {
			t.Fatalf("in-memory versions after the batch: %d and %d, want 2 and 3", fresh[0].Version, fresh[1].Version)
		}
	})

	// A Delete with a stale entity removes nothing: the DELETE holds the row
	// to the entity's version.
	t.Run("DeleteOfAStaleEntity", func(t *testing.T) {
		c := ubvClient(t)
		stale := ubvLoad(t, c, 2)[0]
		ubvMoveUnderneath(t, c, 2) // row 2 → 1020@2
		n, err := For[ubvAccount](ctx, c).Delete(stale)
		if !errors.Is(err, ErrStaleEntity) || n != 0 || !strings.Contains(err.Error(), "pk=2") {
			t.Fatalf("Delete of a stale entity = (%d, %v), want (0, ErrStaleEntity naming pk=2)", n, err)
		}
		if _, ok := ubvTable(t, c)[2]; !ok {
			t.Fatalf("row 2 was removed by a stale Delete")
		}
		fresh := ubvLoad(t, c, 2)[0]
		if n, err := For[ubvAccount](ctx, c).Delete(fresh); err != nil || n != 1 {
			t.Fatalf("Delete of a fresh entity = (%d, %v), want (1, nil)", n, err)
		}
		if _, ok := ubvTable(t, c)[2]; ok {
			t.Fatalf("row 2 survived a fresh Delete")
		}
		// A key no row has is the conflict it is for Update.
		if _, err := For[ubvAccount](ctx, c).Delete(&ubvAccount{ID: 99, Version: 1}); !errors.Is(err, ErrStaleEntity) {
			t.Fatalf("Delete of a missing key on a versioned model = %v, want ErrStaleEntity", err)
		}
	})

	t.Run("HardDeleteOfAStaleEntity", func(t *testing.T) {
		c := ubvClient(t)
		stale := ubvLoad(t, c, 3)[0]
		ubvMoveUnderneath(t, c, 3) // row 3 → 1030@2
		n, err := For[ubvAccount](ctx, c).HardDelete(stale)
		if !errors.Is(err, ErrStaleEntity) || n != 0 {
			t.Fatalf("HardDelete of a stale entity = (%d, %v), want (0, ErrStaleEntity)", n, err)
		}
		if _, ok := ubvTable(t, c)[3]; !ok {
			t.Fatalf("row 3 was removed by a stale HardDelete")
		}
		fresh := ubvLoad(t, c, 3)[0]
		if n, err := For[ubvAccount](ctx, c).HardDelete(fresh); err != nil || n != 1 {
			t.Fatalf("HardDelete of a fresh entity = (%d, %v), want (1, nil)", n, err)
		}
	})

	// The soft delete and Restore hold the row to the entity's version and
	// bump it, in the row and in the entity.
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
		if !errors.Is(err, ErrStaleEntity) || n != 0 {
			t.Fatalf("soft Delete of a stale entity = (%d, %v), want (0, ErrStaleEntity)", n, err)
		}
		if _, err := For[olsTrashed](ctx, c).Find(1); err != nil {
			t.Fatalf("the row was trashed by a stale soft Delete: %v", err)
		}

		// The winner's entity is fresh: trashed, version bumped in the row
		// and in the entity.
		n, err = For[olsTrashed](ctx, c).Delete(&winner)
		if err != nil || n != 1 {
			t.Fatalf("soft Delete of a fresh entity = (%d, %v), want (1, nil)", n, err)
		}
		if winner.Version != 3 {
			t.Fatalf("the soft delete left the in-memory version at %d, want 3", winner.Version)
		}
		trashed, err := For[olsTrashed](ctx, c).WithTrashed().Find(1)
		if err != nil {
			t.Fatalf("read the trashed row: %v", err)
		}
		if trashed.DeletedAt == nil || trashed.Balance != 1010 || trashed.Version != 3 {
			t.Fatalf("after the soft delete the row is %+v, want 1010@3 trashed", trashed)
		}
		// Trashing it again changes nothing and is not a conflict: the
		// entity is at the row's version, the row is already trashed.
		if n, err := For[olsTrashed](ctx, c).Delete(&winner); err != nil || n != 0 {
			t.Fatalf("soft Delete of a trashed row = (%d, %v), want (0, nil)", n, err)
		}

		// Restore with the stale entity (at version 1): nothing restored.
		n, err = For[olsTrashed](ctx, c).Restore(&stale)
		if !errors.Is(err, ErrStaleEntity) || n != 0 {
			t.Fatalf("Restore with a stale entity = (%d, %v), want (0, ErrStaleEntity)", n, err)
		}
		if _, err := For[olsTrashed](ctx, c).Find(1); err == nil {
			t.Fatalf("the row was restored by a stale Restore")
		}
		// Restore with the entity at the row's version: restored, bumped.
		n, err = For[olsTrashed](ctx, c).Restore(&winner)
		if err != nil || n != 1 {
			t.Fatalf("Restore with a fresh entity = (%d, %v), want (1, nil)", n, err)
		}
		if winner.Version != 4 || winner.DeletedAt != nil {
			t.Fatalf("Restore left the entity at version %d, deleted_at %v; want 4 and nil", winner.Version, winner.DeletedAt)
		}
		restored, err := For[olsTrashed](ctx, c).Find(1)
		if err != nil {
			t.Fatalf("read the restored row: %v", err)
		}
		if restored.DeletedAt != nil || restored.Version != 4 {
			t.Fatalf("after Restore the row is %+v, want live at version 4", restored)
		}
		// Restoring a live row changes nothing and is not a conflict.
		if n, err := For[olsTrashed](ctx, c).Restore(&winner); err != nil || n != 0 {
			t.Fatalf("Restore of a live row = (%d, %v), want (0, nil)", n, err)
		}
	})
}
