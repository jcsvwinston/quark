// Copyright 2026 jcsvwinston
// SPDX-License-Identifier: Apache-2.0

package enginesuite

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/jcsvwinston/quark"
)

// bctxRow is the fixture of testBatchInCallerTx. Rows are told apart by
// code: the engines assign the ids.
type bctxRow struct {
	ID    int64  `db:"id" pk:"true"`
	Code  string `db:"code" quark:"unique"`
	Score int64  `db:"score"`
	Note  string `db:"note"`
}

func (bctxRow) TableName() string { return "batch_caller_tx_rows" }

// bctxTimeout bounds each transaction of testBatchInCallerTx. A batch that
// opens a connection of its own waits there for the row the caller locked;
// the bound turns that wait into a failure instead of a lane timeout.
const bctxTimeout = 20 * time.Second

var errBctxRollback = errors.New("the caller rolls back")

// testBatchInCallerTx proves QK-57 on the engine this lane runs: every batch
// writer on a query bound with ForTx runs in the caller's transaction. A
// writer that opened a transaction of its own — UpdateBatch did — ran on
// another connection: its commit survived the caller's rollback, and when
// the caller held a lock on a row it wrote (SQLite: the writer lock), it
// waited for that lock until the timeout and the caller's transaction
// failed.
//
// For each writer: inside the transaction the caller sees the writer's rows;
// the caller's rollback undoes them, with and without a lock of the caller's
// on row a, which every writer touches (the lock writes a's note, which no
// assertion reads); the caller's commit keeps them. And
// UpdateBatch, which is all or nothing, is undone on its own when a row
// fails inside the caller's transaction — by a savepoint, so the caller's
// transaction stays usable (on PostgreSQL an error aborts the whole
// transaction otherwise) and the caller's own writes commit.
func testBatchInCallerTx(ctx context.Context, t *testing.T, client *quark.Client) {
	engine := client.Dialect().Name()
	dropTable(client, "batch_caller_tx_rows")
	if err := client.Migrate(ctx, &bctxRow{}); err != nil {
		t.Fatalf("migrate on %s: %v", engine, err)
	}
	t.Cleanup(func() { dropTable(client, "batch_caller_tx_rows") })

	reseed := func(t *testing.T) map[string]bctxRow {
		t.Helper()
		if _, err := client.Raw().Exec("DELETE FROM " + client.Dialect().Quote("batch_caller_tx_rows")); err != nil {
			t.Fatalf("clear on %s: %v", engine, err)
		}
		for i, code := range []string{"a", "b", "c"} {
			if err := quark.For[bctxRow](ctx, client).Create(&bctxRow{Code: code, Score: int64(i + 1)}); err != nil {
				t.Fatalf("seed %s on %s: %v", code, engine, err)
			}
		}
		return bctxRows(t, quark.For[bctxRow](ctx, client))
	}

	type writer struct {
		name string
		// run writes with the query ForTx gives it; seed is the table as
		// reseed left it, for the keys.
		run func(q func() *quark.Query[bctxRow], seed map[string]bctxRow) error
		// want is the table, code → score, after run.
		want map[string]int64
	}
	writers := []writer{
		{"UpdateBatch", func(q func() *quark.Query[bctxRow], seed map[string]bctxRow) error {
			a, b := seed["a"], seed["b"]
			a.Score, b.Score = 10, 20
			return q().UpdateBatch([]*bctxRow{&a, &b})
		}, map[string]int64{"a": 10, "b": 20, "c": 3}},
		{"CreateBatch", func(q func() *quark.Query[bctxRow], _ map[string]bctxRow) error {
			return q().CreateBatch([]*bctxRow{{Code: "d", Score: 4}, {Code: "e", Score: 5}})
		}, map[string]int64{"a": 1, "b": 2, "c": 3, "d": 4, "e": 5}},
		{"UpsertBatch", func(q func() *quark.Query[bctxRow], _ map[string]bctxRow) error {
			return q().UpsertBatch([]*bctxRow{{Code: "a", Score: 11}, {Code: "f", Score: 6}}, []string{"code"}, []string{"score"})
		}, map[string]int64{"a": 11, "b": 2, "c": 3, "f": 6}},
		{"DeleteBatch", func(q func() *quark.Query[bctxRow], seed map[string]bctxRow) error {
			_, err := q().DeleteBatch([]any{seed["a"].ID})
			return err
		}, map[string]int64{"b": 2, "c": 3}},
		{"DeleteBy", func(q func() *quark.Query[bctxRow], _ map[string]bctxRow) error {
			_, err := q().Where("code", "=", "a").DeleteBy()
			return err
		}, map[string]int64{"b": 2, "c": 3}},
	}

	// inCallerTx runs w inside a transaction — after locking row a when
	// lock is set — checks what the caller sees, and ends the transaction
	// with end's answer.
	inCallerTx := func(t *testing.T, w writer, seed map[string]bctxRow, lock bool, end error) error {
		t.Helper()
		txCtx, cancel := context.WithTimeout(ctx, bctxTimeout)
		defer cancel()
		start := time.Now()
		err := client.Tx(txCtx, func(tx *quark.Tx) error {
			q := func() *quark.Query[bctxRow] { return quark.ForTx[bctxRow](txCtx, tx) }
			if lock {
				if _, err := q().Where("code", "=", "a").UpdateMap(map[string]any{"note": "held"}); err != nil {
					return fmt.Errorf("the caller's lock on a: %w", err)
				}
			}
			if err := w.run(q, seed); err != nil {
				return fmt.Errorf("%s: %w", w.name, err)
			}
			if got := bctxScores(bctxRows(t, q())); !sameBctxScores(got, w.want) {
				t.Errorf("%s on %s: inside the caller's transaction the table is %v, want %v", w.name, engine, got, w.want)
			}
			return end
		})
		if took := time.Since(start); took > bctxTimeout/2 {
			t.Errorf("%s on %s: the caller's transaction took %v — the writer waited for a lock the caller holds", w.name, engine, took.Round(time.Millisecond))
		}
		return err
	}

	for _, w := range writers {
		for _, lock := range []bool{false, true} {
			name := w.name + "/RollbackUndoesIt"
			if lock {
				name += "UnderTheCallersLock"
			}
			t.Run(name, func(t *testing.T) {
				before := reseed(t)
				err := inCallerTx(t, w, before, lock, errBctxRollback)
				if !errors.Is(err, errBctxRollback) {
					t.Fatalf("%s on %s: the transaction ended with %v, want the caller's own error", w.name, engine, err)
				}
				if got := bctxScores(bctxRows(t, quark.For[bctxRow](ctx, client))); !sameBctxScores(got, bctxScores(before)) {
					t.Errorf("%s on %s: the caller's rollback left %v, want %v", w.name, engine, got, bctxScores(before))
				}
			})
		}
		t.Run(w.name+"/CommitKeepsIt", func(t *testing.T) {
			seed := reseed(t)
			if err := inCallerTx(t, w, seed, true, nil); err != nil {
				t.Fatalf("%s on %s: %v", w.name, engine, err)
			}
			if got := bctxScores(bctxRows(t, quark.For[bctxRow](ctx, client))); !sameBctxScores(got, w.want) {
				t.Errorf("%s on %s: after the caller's commit the table is %v, want %v", w.name, engine, got, w.want)
			}
		})
	}

	t.Run("UpdateBatch/FailingRowUndoesOnlyTheBatch", func(t *testing.T) {
		seed := reseed(t)
		txCtx, cancel := context.WithTimeout(ctx, bctxTimeout)
		defer cancel()
		err := client.Tx(txCtx, func(tx *quark.Tx) error {
			q := func() *quark.Query[bctxRow] { return quark.ForTx[bctxRow](txCtx, tx) }
			if err := q().Create(&bctxRow{Code: "d", Score: 4}); err != nil {
				return fmt.Errorf("the caller's write: %w", err)
			}
			a, b := seed["a"], seed["b"]
			a.Score = 10
			b.Code = "c" // c's unique code: the second UPDATE fails
			if err := q().UpdateBatch([]*bctxRow{&a, &b}); err == nil {
				t.Errorf("UpdateBatch onto c's unique code on %s succeeded", engine)
			}
			if got := bctxScores(bctxRows(t, q())); got["a"] != 1 {
				t.Errorf("on %s the failed UpdateBatch left its first row in the caller's transaction: %v", engine, got)
			}
			if err := q().Create(&bctxRow{Code: "e", Score: 5}); err != nil {
				return fmt.Errorf("the caller's write after the failed batch: %w", err)
			}
			return nil
		})
		if err != nil {
			t.Fatalf("on %s: %v", engine, err)
		}
		want := map[string]int64{"a": 1, "b": 2, "c": 3, "d": 4, "e": 5}
		if got := bctxScores(bctxRows(t, quark.For[bctxRow](ctx, client))); !sameBctxScores(got, want) {
			t.Errorf("on %s after the caller's commit the table is %v, want %v", engine, got, want)
		}
	})
}

func bctxRows(t *testing.T, q *quark.Query[bctxRow]) map[string]bctxRow {
	t.Helper()
	rows, err := q.Limit(100).List()
	if err != nil {
		t.Fatalf("read back: %v", err)
	}
	out := map[string]bctxRow{}
	for _, r := range rows {
		out[r.Code] = r
	}
	return out
}

func bctxScores(rows map[string]bctxRow) map[string]int64 {
	out := make(map[string]int64, len(rows))
	for code, r := range rows {
		out[code] = r.Score
	}
	return out
}

func sameBctxScores(a, b map[string]int64) bool {
	if len(a) != len(b) {
		return false
	}
	for k, v := range a {
		if w, ok := b[k]; !ok || w != v {
			return false
		}
	}
	return true
}
