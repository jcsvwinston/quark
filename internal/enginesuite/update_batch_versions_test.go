// Copyright 2026 jcsvwinston
// SPDX-License-Identifier: Apache-2.0

package enginesuite

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/jcsvwinston/quark"
)

// ubvRow is the fixture of testUpdateBatchVersions: a versioned model whose
// ids the engine assigns.
type ubvRow struct {
	ID      int64  `db:"id" pk:"true"`
	Owner   string `db:"owner"`
	Balance int64  `db:"balance"`
	Version int64  `db:"version" quark:"version"`
}

func (ubvRow) TableName() string { return "ubv_rows" }

// testUpdateBatchVersions proves QK-45 on the engine this lane runs.
// CheckVersions makes UpdateBatch on a versioned model all or nothing over
// the versions: a row its version predicate does not match — moved by
// another writer, or a key that does not exist — rolls the batch back with
// one ErrStaleEntity per such row; a row the query's conditions exclude is
// skipped; the in-memory versions of the written rows are bumped once the
// batch is through. Inside the caller's transaction the batch is a savepoint,
// so a stale batch leaves the caller's transaction usable — on PostgreSQL it
// is not aborted — and the caller's own writes commit.
//
// Without CheckVersions UpdateBatch keeps its v1 behaviour, pinned here too:
// the stale row is not written and nothing reports it, and the written rows
// keep their old version in memory. Quark 2.0 makes the check the default
// (DEP-2026-003), and the pinned subtest is the one that changes with it.
func testUpdateBatchVersions(ctx context.Context, t *testing.T, client *quark.Client) {
	engine := client.Dialect().Name()
	dropTable(client, "ubv_rows")
	if err := client.Migrate(ctx, &ubvRow{}); err != nil {
		t.Fatalf("migrate on %s: %v", engine, err)
	}
	t.Cleanup(func() { dropTable(client, "ubv_rows") })

	// reseed empties the table and writes three rows at version 1 — owners
	// alice, alice, bob; balance 10, 20, 30 — returned in that order.
	reseed := func(t *testing.T) []int64 {
		t.Helper()
		if _, err := client.Raw().Exec("DELETE FROM " + client.Dialect().Quote("ubv_rows")); err != nil {
			t.Fatalf("clear on %s: %v", engine, err)
		}
		var ids []int64
		for i, owner := range []string{"alice", "alice", "bob"} {
			r := ubvRow{Owner: owner, Balance: int64(i+1) * 10, Version: 1}
			if err := quark.For[ubvRow](ctx, client).Create(&r); err != nil {
				t.Fatalf("seed on %s: %v", engine, err)
			}
			ids = append(ids, r.ID)
		}
		return ids
	}
	load := func(t *testing.T, q *quark.Query[ubvRow], ids ...int64) []*ubvRow {
		t.Helper()
		out := make([]*ubvRow, 0, len(ids))
		for _, id := range ids {
			r, err := q.Find(id)
			if err != nil {
				t.Fatalf("load %d on %s: %v", id, engine, err)
			}
			out = append(out, &r)
		}
		return out
	}
	// table reads id → "balance@version".
	table := func(t *testing.T, q *quark.Query[ubvRow]) map[int64]string {
		t.Helper()
		rows, err := q.OrderBy("id", "ASC").Limit(100).List()
		if err != nil {
			t.Fatalf("read back on %s: %v", engine, err)
		}
		out := map[int64]string{}
		for _, r := range rows {
			out[r.ID] = fmt.Sprintf("%d@%d", r.Balance, r.Version)
		}
		return out
	}
	same := func(a, b map[int64]string) bool {
		if len(a) != len(b) {
			return false
		}
		for k, v := range a {
			if b[k] != v {
				return false
			}
		}
		return true
	}
	// moveUnderneath is the concurrent writer: row id goes to the next
	// version, its balance up by 1000.
	moveUnderneath := func(t *testing.T, id int64) {
		t.Helper()
		other := load(t, quark.For[ubvRow](ctx, client), id)[0]
		other.Balance += 1000
		if _, err := quark.For[ubvRow](ctx, client).Update(other); err != nil {
			t.Fatalf("concurrent writer on %s: %v", engine, err)
		}
	}
	pkIn := func(id int64) string { return fmt.Sprintf("table ubv_rows pk=%d", id) }
	// missingID is a key no row holds.
	missingID := func(ids []int64) int64 { return ids[len(ids)-1] + 1000 }

	t.Run("WithoutCheckVersionsKeepsTheV1Behaviour", func(t *testing.T) {
		ids := reseed(t)
		rows := load(t, quark.For[ubvRow](ctx, client), ids[0], ids[1])
		moveUnderneath(t, ids[1])
		rows[0].Balance, rows[1].Balance = 11, 22
		missing := &ubvRow{ID: missingID(ids), Owner: "nobody", Balance: 1, Version: 1}

		if err := quark.For[ubvRow](ctx, client).UpdateBatch([]*ubvRow{rows[0], rows[1], missing}); err != nil {
			t.Fatalf("UpdateBatch with a stale row and a missing key on %s = %v, want nil (v1 behaviour)", engine, err)
		}
		want := map[int64]string{ids[0]: "11@2", ids[1]: "1020@2", ids[2]: "30@1"}
		if got := table(t, quark.For[ubvRow](ctx, client)); !same(got, want) {
			t.Fatalf("after the batch on %s: %v, want %v", engine, got, want)
		}
		if rows[0].Version != 1 {
			t.Fatalf("UpdateBatch on %s bumped the in-memory version to %d without CheckVersions", engine, rows[0].Version)
		}
		rows[0].Balance = 12
		if _, err := quark.For[ubvRow](ctx, client).Update(rows[0]); !errors.Is(err, quark.ErrStaleEntity) {
			t.Fatalf("Update after an unchecked UpdateBatch on %s = %v, want the ErrStaleEntity v1 leaves behind", engine, err)
		}
	})

	t.Run("MixedBatchRollsBackWithEveryStaleKey", func(t *testing.T) {
		ids := reseed(t)
		rows := load(t, quark.For[ubvRow](ctx, client), ids...)
		moveUnderneath(t, ids[1])
		before := table(t, quark.For[ubvRow](ctx, client))
		rows[0].Balance, rows[1].Balance, rows[2].Balance = 11, 22, 33
		missing := &ubvRow{ID: missingID(ids), Owner: "nobody", Balance: 1, Version: 1}

		err := quark.For[ubvRow](ctx, client).CheckVersions().UpdateBatch([]*ubvRow{rows[0], rows[1], rows[2], missing})
		if !errors.Is(err, quark.ErrStaleEntity) {
			t.Fatalf("a mixed batch on %s = %v, want ErrStaleEntity", engine, err)
		}
		msg := err.Error()
		for _, id := range []int64{ids[1], missing.ID} {
			if !strings.Contains(msg, pkIn(id)) {
				t.Errorf("the error on %s does not name %q: %v", engine, pkIn(id), msg)
			}
		}
		for _, id := range []int64{ids[0], ids[2]} {
			if strings.Contains(msg, pkIn(id)) {
				t.Errorf("the error on %s names the fresh row %d: %v", engine, id, msg)
			}
		}
		if got := table(t, quark.For[ubvRow](ctx, client)); !same(got, before) {
			t.Errorf("the failed batch on %s left %v, want %v", engine, got, before)
		}
		for _, r := range rows {
			if r.Version != 1 {
				t.Errorf("row %d on %s: a failed batch bumped the in-memory version to %d", r.ID, engine, r.Version)
			}
		}
	})

	t.Run("FreshBatchCommitsAndBumps", func(t *testing.T) {
		ids := reseed(t)
		rows := load(t, quark.For[ubvRow](ctx, client), ids[0], ids[1])
		rows[0].Balance, rows[1].Balance = 11, 22
		if err := quark.For[ubvRow](ctx, client).CheckVersions().UpdateBatch(rows); err != nil {
			t.Fatalf("a fresh batch on %s: %v", engine, err)
		}
		want := map[int64]string{ids[0]: "11@2", ids[1]: "22@2", ids[2]: "30@1"}
		if got := table(t, quark.For[ubvRow](ctx, client)); !same(got, want) {
			t.Fatalf("after the batch on %s: %v, want %v", engine, got, want)
		}
		if rows[0].Version != 2 || rows[1].Version != 2 {
			t.Fatalf("in-memory versions on %s: %d and %d, want 2", engine, rows[0].Version, rows[1].Version)
		}
		rows[0].Balance = 12
		if _, err := quark.For[ubvRow](ctx, client).Update(rows[0]); err != nil {
			t.Fatalf("Update after a checked UpdateBatch on %s: %v", engine, err)
		}
	})

	t.Run("RowTheConditionsExcludeIsSkipped", func(t *testing.T) {
		ids := reseed(t)
		rows := load(t, quark.For[ubvRow](ctx, client), ids...) // ids[2] is bob's
		moveUnderneath(t, ids[2])                               // and stale too
		rows[0].Balance, rows[1].Balance, rows[2].Balance = 11, 22, 33
		missing := &ubvRow{ID: missingID(ids), Owner: "nobody", Balance: 1, Version: 1}

		err := quark.For[ubvRow](ctx, client).Where("owner", "=", "alice").CheckVersions().
			UpdateBatch([]*ubvRow{rows[0], rows[1], rows[2], missing})
		if err != nil {
			t.Fatalf("rows the conditions exclude failed the batch on %s: %v", engine, err)
		}
		want := map[int64]string{ids[0]: "11@2", ids[1]: "22@2", ids[2]: "1030@2"}
		if got := table(t, quark.For[ubvRow](ctx, client)); !same(got, want) {
			t.Fatalf("after the batch on %s: %v, want %v", engine, got, want)
		}
		if rows[2].Version != 1 {
			t.Errorf("the excluded row's in-memory version on %s moved to %d", engine, rows[2].Version)
		}

		// Admitted by the conditions and stale: still a conflict.
		moveUnderneath(t, ids[1])
		before := table(t, quark.For[ubvRow](ctx, client))
		rows[0].Balance, rows[1].Balance = 12, 23
		err = quark.For[ubvRow](ctx, client).Where("owner", "=", "alice").CheckVersions().UpdateBatch(rows[:2])
		if !errors.Is(err, quark.ErrStaleEntity) || !strings.Contains(err.Error(), pkIn(ids[1])) {
			t.Fatalf("an admitted stale row on %s = %v, want ErrStaleEntity naming %q", engine, err, pkIn(ids[1]))
		}
		if got := table(t, quark.For[ubvRow](ctx, client)); !same(got, before) {
			t.Errorf("the failed batch on %s left %v, want %v", engine, got, before)
		}
	})

	t.Run("InTheCallersTransaction", func(t *testing.T) {
		ids := reseed(t)
		rows := load(t, quark.For[ubvRow](ctx, client), ids[0], ids[1])
		moveUnderneath(t, ids[1])
		rows[0].Balance, rows[1].Balance = 11, 22

		err := client.Tx(ctx, func(tx *quark.Tx) error {
			q := func() *quark.Query[ubvRow] { return quark.ForTx[ubvRow](ctx, tx) }
			if _, err := q().Where("id", "=", ids[2]).UpdateMap(map[string]any{"owner": "carol"}); err != nil {
				return fmt.Errorf("the caller's own write: %w", err)
			}
			if err := q().CheckVersions().UpdateBatch(rows); !errors.Is(err, quark.ErrStaleEntity) {
				t.Errorf("a stale batch in the caller's transaction on %s = %v, want ErrStaleEntity", engine, err)
			}
			if rows[0].Version != 1 {
				t.Errorf("a failed batch on %s bumped the in-memory version to %d", engine, rows[0].Version)
			}
			// The savepoint undid the fresh row, and the transaction is
			// still usable: this read would fail on an aborted PostgreSQL
			// transaction.
			if got := load(t, q(), ids[0])[0]; got.Balance != 10 {
				t.Errorf("inside the caller's transaction on %s row %d holds %d after the failed batch, want 10", engine, ids[0], got.Balance)
			}
			rows[1] = load(t, q(), ids[1])[0]
			rows[1].Balance = 22
			if err := q().CheckVersions().UpdateBatch(rows); err != nil {
				return fmt.Errorf("the batch after the reload: %w", err)
			}
			if rows[0].Version != 2 || rows[1].Version != 3 {
				t.Errorf("versions after the batch inside the transaction on %s: %d and %d, want 2 and 3", engine, rows[0].Version, rows[1].Version)
			}
			return nil
		})
		if err != nil {
			t.Fatalf("Tx on %s: %v", engine, err)
		}
		want := map[int64]string{ids[0]: "11@2", ids[1]: "22@3", ids[2]: "30@1"}
		if got := table(t, quark.For[ubvRow](ctx, client)); !same(got, want) {
			t.Errorf("after commit on %s: %v, want %v", engine, got, want)
		}
		if got := load(t, quark.For[ubvRow](ctx, client), ids[2])[0]; got.Owner != "carol" {
			t.Errorf("the caller's own write did not commit on %s: %+v", engine, got)
		}
	})
}
