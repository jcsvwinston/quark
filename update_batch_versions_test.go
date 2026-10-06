// Copyright 2026 jcsvwinston
// SPDX-License-Identifier: Apache-2.0

package quark

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"strings"
	"testing"

	_ "modernc.org/sqlite"
)

// QK-45. Update, UpdateFields and Tracked.Save hold a versioned row to the
// version it was loaded with and return ErrStaleEntity when it moved.
// UpdateBatch sends the same version predicate and then reads nothing back:
// a stale row is not written, the call returns nil, and the rows it did write
// keep their old version in memory, so the next Update of the same struct
// reports a conflict nobody caused. CheckVersions makes the batch hold every
// row to its version, all or nothing; without it UpdateBatch stays as it was
// until Quark 2.0 makes the check the default (DEP-2026-003).
//
// internal/enginesuite (UpdateBatchVersions) runs the same on the six engines.

type ubvAccount struct {
	ID      int64  `db:"id" pk:"true"`
	Owner   string `db:"owner"`
	Balance int64  `db:"balance"`
	Version int64  `db:"version" quark:"version"`
}

func (ubvAccount) TableName() string { return "ubv_accounts" }

// ubvPlain is the same table without the version column.
type ubvPlain struct {
	ID      int64  `db:"id" pk:"true"`
	Owner   string `db:"owner"`
	Balance int64  `db:"balance"`
}

func (ubvPlain) TableName() string { return "ubv_plain" }

// ubvClient opens a fresh database with rows 1, 2 and 3 — owners alice,
// alice, bob; balance 10, 20, 30; all at version 1.
func ubvClient(t *testing.T) *Client {
	t.Helper()
	name := "qk45_" + strings.NewReplacer("/", "_", " ", "_").Replace(t.Name())
	c, err := New("sqlite", "file:"+name+"?mode=memory&cache=shared",
		WithLogger(slog.New(slog.NewTextHandler(io.Discard, nil))))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { _ = c.Close() })
	ctx := context.Background()
	if err := c.Migrate(ctx, &ubvAccount{}, &ubvPlain{}); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	for i, owner := range []string{"alice", "alice", "bob"} {
		id := int64(i + 1)
		if err := For[ubvAccount](ctx, c).Create(&ubvAccount{ID: id, Owner: owner, Balance: id * 10, Version: 1}); err != nil {
			t.Fatalf("seed account %d: %v", id, err)
		}
		if err := For[ubvPlain](ctx, c).Create(&ubvPlain{ID: id, Owner: owner, Balance: id * 10}); err != nil {
			t.Fatalf("seed plain %d: %v", id, err)
		}
	}
	return c
}

// ubvLoad reads rows by id, as a caller holding them in memory would.
func ubvLoad(t *testing.T, c *Client, ids ...int64) []*ubvAccount {
	t.Helper()
	out := make([]*ubvAccount, 0, len(ids))
	for _, id := range ids {
		a, err := For[ubvAccount](context.Background(), c).Find(id)
		if err != nil {
			t.Fatalf("load %d: %v", id, err)
		}
		out = append(out, &a)
	}
	return out
}

// ubvTable reads the table as id → "balance@version".
func ubvTable(t *testing.T, c *Client) map[int64]string {
	t.Helper()
	rows, err := For[ubvAccount](context.Background(), c).OrderBy("id", "ASC").Limit(100).List()
	if err != nil {
		t.Fatalf("read back: %v", err)
	}
	out := map[int64]string{}
	for _, r := range rows {
		out[r.ID] = fmt.Sprintf("%d@%d", r.Balance, r.Version)
	}
	return out
}

func sameTable(a, b map[int64]string) bool {
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

// ubvMoveUnderneath is the concurrent writer: it moves row id to the next
// version behind the caller's back.
func ubvMoveUnderneath(t *testing.T, c *Client, id int64) {
	t.Helper()
	other := ubvLoad(t, c, id)[0]
	other.Balance += 1000
	if _, err := For[ubvAccount](context.Background(), c).Update(other); err != nil {
		t.Fatalf("concurrent writer on %d: %v", id, err)
	}
}

// Without CheckVersions UpdateBatch is what it was, and this test pins it, so
// the change of default in 2.0 is a test that has to change with it.
func TestUpdateBatchWithoutCheckVersionsKeepsTheV1Behaviour(t *testing.T) {
	ctx := context.Background()
	c := ubvClient(t)
	rows := ubvLoad(t, c, 1, 2)
	ubvMoveUnderneath(t, c, 2) // row 2 → 1020@2
	rows[0].Balance, rows[1].Balance = 11, 22

	missing := &ubvAccount{ID: 99, Owner: "nobody", Balance: 1, Version: 1}
	if err := For[ubvAccount](ctx, c).UpdateBatch([]*ubvAccount{rows[0], rows[1], missing}); err != nil {
		t.Fatalf("UpdateBatch with a stale row and a missing key = %v, want nil (v1 behaviour)", err)
	}
	// The fresh row is written and its version moves in the database; the
	// stale one is not written; nothing reports either.
	want := map[int64]string{1: "11@2", 2: "1020@2", 3: "30@1"}
	if got := ubvTable(t, c); !sameTable(got, want) {
		t.Fatalf("after the batch: %v, want %v", got, want)
	}
	// The in-memory versions are not bumped…
	if rows[0].Version != 1 {
		t.Fatalf("UpdateBatch bumped the in-memory version to %d without CheckVersions", rows[0].Version)
	}
	// …so the next Update of the same struct reports a conflict nobody caused.
	rows[0].Balance = 12
	if _, err := For[ubvAccount](ctx, c).Update(rows[0]); !errors.Is(err, ErrStaleEntity) {
		t.Fatalf("Update after an unchecked UpdateBatch = %v, want the ErrStaleEntity v1 leaves behind", err)
	}
}

func TestUpdateBatchCheckVersionsRollsBackAMixedBatch(t *testing.T) {
	ctx := context.Background()
	c := ubvClient(t)
	rows := ubvLoad(t, c, 1, 2, 3)
	ubvMoveUnderneath(t, c, 2)
	ubvMoveUnderneath(t, c, 3)
	before := ubvTable(t, c)
	rows[0].Balance, rows[1].Balance, rows[2].Balance = 11, 22, 33

	err := For[ubvAccount](ctx, c).CheckVersions().UpdateBatch(rows)
	if !errors.Is(err, ErrStaleEntity) {
		t.Fatalf("CheckVersions().UpdateBatch with stale rows = %v, want ErrStaleEntity", err)
	}
	// Every stale key is named, the fresh one is not.
	msg := err.Error()
	for _, want := range []string{"table ubv_accounts pk=2", "table ubv_accounts pk=3"} {
		if !strings.Contains(msg, want) {
			t.Errorf("the error does not name %q: %v", want, msg)
		}
	}
	if strings.Contains(msg, "pk=1") {
		t.Errorf("the error names the fresh row 1: %v", msg)
	}
	if n := len(strings.Split(msg, "\n")); n != 2 {
		t.Errorf("the error joins %d errors, want one per stale row (2): %v", n, msg)
	}
	// All or nothing: the fresh row written before the stale ones is undone.
	if got := ubvTable(t, c); !sameTable(got, before) {
		t.Errorf("the failed batch left %v, want %v", got, before)
	}
	for _, r := range rows {
		if r.Version != 1 {
			t.Errorf("row %d: a failed batch bumped the in-memory version to %d", r.ID, r.Version)
		}
	}
}

func TestUpdateBatchCheckVersionsCommitsAndBumpsTheVersions(t *testing.T) {
	ctx := context.Background()
	c := ubvClient(t)
	rows := ubvLoad(t, c, 1, 2)
	rows[0].Balance, rows[1].Balance = 11, 22

	if err := For[ubvAccount](ctx, c).CheckVersions().UpdateBatch(rows); err != nil {
		t.Fatalf("CheckVersions().UpdateBatch of fresh rows: %v", err)
	}
	want := map[int64]string{1: "11@2", 2: "22@2", 3: "30@1"}
	if got := ubvTable(t, c); !sameTable(got, want) {
		t.Fatalf("after the batch: %v, want %v", got, want)
	}
	for _, r := range rows {
		if r.Version != 2 {
			t.Errorf("row %d: in-memory version %d after the batch, want 2", r.ID, r.Version)
		}
	}
	// The same structs go on being written without a false conflict.
	rows[0].Balance = 12
	if _, err := For[ubvAccount](ctx, c).Update(rows[0]); err != nil {
		t.Fatalf("Update after a checked UpdateBatch: %v", err)
	}
	if err := For[ubvAccount](ctx, c).CheckVersions().UpdateBatch(rows); err != nil {
		t.Fatalf("a second checked batch of the same structs: %v", err)
	}
	if rows[0].Version != 4 || rows[1].Version != 3 {
		t.Errorf("versions after the second batch: %d and %d, want 4 and 3", rows[0].Version, rows[1].Version)
	}
}

// Without conditions a key that does not exist is stale, as for Update.
func TestUpdateBatchCheckVersionsReportsAMissingKey(t *testing.T) {
	ctx := context.Background()
	c := ubvClient(t)
	rows := ubvLoad(t, c, 1)
	rows[0].Balance = 11
	before := ubvTable(t, c)

	missing := &ubvAccount{ID: 99, Owner: "nobody", Balance: 1, Version: 1}
	err := For[ubvAccount](ctx, c).CheckVersions().UpdateBatch([]*ubvAccount{rows[0], missing})
	if !errors.Is(err, ErrStaleEntity) || !strings.Contains(err.Error(), "pk=99") {
		t.Fatalf("a missing key under CheckVersions = %v, want ErrStaleEntity naming pk=99", err)
	}
	if got := ubvTable(t, c); !sameTable(got, before) {
		t.Errorf("the failed batch left %v, want %v", got, before)
	}
}

// Under conditions a row they exclude is skipped without error, stale or
// not, and a row they admit is held to its version.
func TestUpdateBatchCheckVersionsSkipsARowTheConditionsExclude(t *testing.T) {
	ctx := context.Background()
	c := ubvClient(t)
	rows := ubvLoad(t, c, 1, 2, 3) // 3 is bob's
	ubvMoveUnderneath(t, c, 3)     // and stale too
	rows[0].Balance, rows[1].Balance, rows[2].Balance = 11, 22, 33
	missing := &ubvAccount{ID: 99, Owner: "nobody", Balance: 1, Version: 1}

	err := For[ubvAccount](ctx, c).Where("owner", "=", "alice").CheckVersions().
		UpdateBatch([]*ubvAccount{rows[0], rows[1], rows[2], missing})
	if err != nil {
		t.Fatalf("rows the conditions exclude made the batch fail: %v", err)
	}
	want := map[int64]string{1: "11@2", 2: "22@2", 3: "1030@2"}
	if got := ubvTable(t, c); !sameTable(got, want) {
		t.Fatalf("after the batch: %v, want %v", got, want)
	}
	if rows[0].Version != 2 || rows[1].Version != 2 {
		t.Errorf("written rows: in-memory versions %d and %d, want 2", rows[0].Version, rows[1].Version)
	}
	if rows[2].Version != 1 || missing.Version != 1 {
		t.Errorf("skipped rows: in-memory versions %d and %d, want them left at 1", rows[2].Version, missing.Version)
	}

	// A row the conditions admit and whose version moved is still stale.
	ubvMoveUnderneath(t, c, 2)
	before := ubvTable(t, c)
	rows[0].Balance, rows[1].Balance = 12, 23
	err = For[ubvAccount](ctx, c).Where("owner", "=", "alice").CheckVersions().UpdateBatch(rows[:2])
	if !errors.Is(err, ErrStaleEntity) || !strings.Contains(err.Error(), "pk=2") {
		t.Fatalf("an admitted stale row = %v, want ErrStaleEntity naming pk=2", err)
	}
	if got := ubvTable(t, c); !sameTable(got, before) {
		t.Errorf("the failed batch left %v, want %v", got, before)
	}
}

// Inside the caller's transaction the batch is a savepoint: a stale row undoes
// the batch alone, the caller's transaction goes on and commits its own
// writes, and no version is bumped. A batch that goes through bumps its
// versions when it returns.
func TestUpdateBatchCheckVersionsInTheCallersTransaction(t *testing.T) {
	ctx := context.Background()
	c := ubvClient(t)
	rows := ubvLoad(t, c, 1, 2)
	ubvMoveUnderneath(t, c, 2)
	rows[0].Balance, rows[1].Balance = 11, 22

	err := c.Tx(ctx, func(tx *Tx) error {
		if _, err := ForTx[ubvAccount](ctx, tx).Where("id", "=", 3).UpdateMap(map[string]any{"owner": "carol"}); err != nil {
			return err
		}
		if err := ForTx[ubvAccount](ctx, tx).CheckVersions().UpdateBatch(rows); !errors.Is(err, ErrStaleEntity) {
			t.Errorf("a stale batch in the caller's transaction = %v, want ErrStaleEntity", err)
		}
		if rows[0].Version != 1 {
			t.Errorf("a failed batch bumped row 1's in-memory version to %d", rows[0].Version)
		}
		a, err := ForTx[ubvAccount](ctx, tx).Find(1)
		if err != nil {
			return err
		}
		if a.Balance != 10 {
			t.Errorf("inside the caller's transaction row 1 holds %d after the failed batch, want 10", a.Balance)
		}
		// Reload the stale row and go again: this batch goes through.
		reloaded, err := ForTx[ubvAccount](ctx, tx).Find(2)
		if err != nil {
			return err
		}
		rows[1] = &reloaded
		rows[1].Balance = 22
		if err := ForTx[ubvAccount](ctx, tx).CheckVersions().UpdateBatch(rows); err != nil {
			return fmt.Errorf("the batch after the reload: %w", err)
		}
		if rows[0].Version != 2 || rows[1].Version != 3 {
			t.Errorf("versions after the batch inside the transaction: %d and %d, want 2 and 3", rows[0].Version, rows[1].Version)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("Tx: %v", err)
	}
	want := map[int64]string{1: "11@2", 2: "22@3", 3: "30@1"}
	if got := ubvTable(t, c); !sameTable(got, want) {
		t.Errorf("after commit: %v, want %v", got, want)
	}
	if got, err := For[ubvAccount](ctx, c).Find(3); err != nil || got.Owner != "carol" {
		t.Errorf("the caller's own write did not commit: %+v, %v", got, err)
	}
}

// On a model without a version column CheckVersions changes nothing: a key
// that does not exist is not written and is not an error.
func TestCheckVersionsOnAModelWithoutAVersionColumn(t *testing.T) {
	ctx := context.Background()
	c := ubvClient(t)
	rows := []*ubvPlain{{ID: 1, Owner: "alice", Balance: 11}, {ID: 99, Owner: "nobody", Balance: 1}}
	if err := For[ubvPlain](ctx, c).CheckVersions().UpdateBatch(rows); err != nil {
		t.Fatalf("CheckVersions().UpdateBatch on a model without a version: %v", err)
	}
	got, err := For[ubvPlain](ctx, c).Find(1)
	if err != nil || got.Balance != 11 {
		t.Fatalf("row 1 after the batch: %+v, %v", got, err)
	}
}

// CheckVersions is a builder method: it leaves the query it was called on
// unchecked.
func TestCheckVersionsLeavesItsReceiverAlone(t *testing.T) {
	ctx := context.Background()
	c := ubvClient(t)
	base := For[ubvAccount](ctx, c)
	_ = base.CheckVersions()
	rows := ubvLoad(t, c, 1)
	ubvMoveUnderneath(t, c, 1)
	if err := base.UpdateBatch(rows); err != nil {
		t.Fatalf("the base query became checked: %v", err)
	}
}
