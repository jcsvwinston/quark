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

	_ "modernc.org/sqlite"
)

// QK-57. UpdateBatch on a query bound to a transaction with ForTx opened a
// transaction of its own on another connection of the pool. On SQLite that
// connection waited for the caller's write lock until the query timeout, and
// the caller's transaction failed; on the server engines the batch committed
// on its own, so the caller's rollback did not undo it, or it waited for the
// row locks the caller held. The batch now runs in the caller's transaction,
// inside a savepoint, so it is still all or nothing on its own.
//
// internal/enginesuite (BatchInCallerTx) runs the same on the six engines,
// with every batch writer.

type bctxRow struct {
	ID    int64  `db:"id" pk:"true"`
	Code  string `db:"code" quark:"unique"`
	Score int64  `db:"score"`
}

func (bctxRow) TableName() string { return "bctx_rows" }

// bctxTimeout bounds every test here. The batch used to wait for the
// caller's lock until it ran out: a short timeout turns that wait into a
// failure in seconds instead of the 30 s default.
const bctxTimeout = 3 * time.Second

// bctxClient opens a shared-cache in-memory database — more than one
// connection on the same data, as a file or a server gives — and seeds the
// rows a, b and c with scores 1, 2 and 3.
func bctxClient(t *testing.T, name string) (*Client, *txStatementRecorder) {
	t.Helper()
	l := DefaultLimits()
	l.QueryTimeout = bctxTimeout
	rec := &txStatementRecorder{}
	c, err := New("sqlite", "file:"+name+"?mode=memory&cache=shared",
		WithLimits(l), WithLogger(slog.New(slog.NewTextHandler(io.Discard, nil))), WithQueryObserver(rec))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { _ = c.Close() })
	ctx := context.Background()
	if err := c.Migrate(ctx, &bctxRow{}); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	for i, code := range []string{"a", "b", "c"} {
		if err := For[bctxRow](ctx, c).Create(&bctxRow{Code: code, Score: int64(i + 1)}); err != nil {
			t.Fatalf("seed %s: %v", code, err)
		}
	}
	rec.reset()
	return c, rec
}

// bctxScores reads every row as code → score, through the
// caller's transaction when tx is non-nil, the pool otherwise.
func bctxScores(t *testing.T, ctx context.Context, c *Client, tx *Tx) map[string]int64 {
	t.Helper()
	q := For[bctxRow](ctx, c)
	if tx != nil {
		q = ForTx[bctxRow](ctx, tx)
	}
	rows, err := q.OrderBy("code", "ASC").Limit(100).List()
	if err != nil {
		t.Fatalf("read back: %v", err)
	}
	out := map[string]int64{}
	for _, r := range rows {
		out[r.Code] = r.Score
	}
	return out
}

// bctxByCode reads the rows with the given codes, keys included, for UpdateBatch.
func bctxByCode(t *testing.T, ctx context.Context, c *Client, codes ...string) []*bctxRow {
	t.Helper()
	var out []*bctxRow
	for _, code := range codes {
		r, err := For[bctxRow](ctx, c).Where("code", "=", code).First()
		if err != nil {
			t.Fatalf("read %s: %v", code, err)
		}
		out = append(out, &r)
	}
	return out
}

func sameScores(a, b map[string]int64) bool {
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

var errBctxRollback = errors.New("caller rolls back")

func TestUpdateBatchRunsInTheCallersTransaction(t *testing.T) {
	c, rec := bctxClient(t, "qk57_rollback")
	ctx, cancel := context.WithTimeout(context.Background(), bctxTimeout)
	defer cancel()
	before := bctxScores(t, ctx, c, nil)
	rows := bctxByCode(t, ctx, c, "a", "b")
	rows[0].Score, rows[1].Score = 10, 20

	start := time.Now()
	err := c.Tx(ctx, func(tx *Tx) error {
		// A write first, so the caller's transaction holds the write lock a
		// second connection would wait for.
		if err := ForTx[bctxRow](ctx, tx).Create(&bctxRow{Code: "d", Score: 4}); err != nil {
			return err
		}
		if err := ForTx[bctxRow](ctx, tx).UpdateBatch(rows); err != nil {
			return err
		}
		// The caller sees the batch inside its own transaction.
		if got := bctxScores(t, ctx, c, tx); got["a"] != 10 || got["b"] != 20 || got["d"] != 4 {
			t.Errorf("inside the transaction after UpdateBatch: %v", got)
		}
		return errBctxRollback
	})
	if !errors.Is(err, errBctxRollback) {
		t.Fatalf("Tx = %v after %v, want the caller's own error: UpdateBatch did not run in the caller's transaction\n%s",
			err, time.Since(start).Round(time.Millisecond), strings.Join(rec.all, "\n"))
	}
	if got := bctxScores(t, context.Background(), c, nil); !sameScores(got, before) {
		t.Errorf("the caller's rollback did not undo UpdateBatch:\nbefore %v\nafter  %v", before, got)
	}
}

func TestUpdateBatchCommitsWithTheCallersTransaction(t *testing.T) {
	c, _ := bctxClient(t, "qk57_commit")
	ctx, cancel := context.WithTimeout(context.Background(), bctxTimeout)
	defer cancel()
	rows := bctxByCode(t, ctx, c, "a", "b")
	rows[0].Score, rows[1].Score = 10, 20

	err := c.Tx(ctx, func(tx *Tx) error {
		if err := ForTx[bctxRow](ctx, tx).Create(&bctxRow{Code: "d", Score: 4}); err != nil {
			return err
		}
		return ForTx[bctxRow](ctx, tx).UpdateBatch(rows)
	})
	if err != nil {
		t.Fatalf("Tx: %v", err)
	}
	want := map[string]int64{"a": 10, "b": 20, "c": 3, "d": 4}
	if got := bctxScores(t, context.Background(), c, nil); !sameScores(got, want) {
		t.Errorf("after commit: %v, want %v", got, want)
	}
}

// A batch that fails inside the caller's transaction is undone on its own —
// the rows it wrote before the failing one go with it — and leaves the
// caller's transaction usable, with the caller's own writes in place.
func TestUpdateBatchFailingInTheCallersTransactionUndoesOnlyItself(t *testing.T) {
	c, rec := bctxClient(t, "qk57_atomic")
	ctx, cancel := context.WithTimeout(context.Background(), bctxTimeout)
	defer cancel()
	rows := bctxByCode(t, ctx, c, "a", "b")
	rows[0].Score = 10
	rows[1].Code = "c" // the unique key of another row: the second UPDATE fails

	err := c.Tx(ctx, func(tx *Tx) error {
		if err := ForTx[bctxRow](ctx, tx).Create(&bctxRow{Code: "d", Score: 4}); err != nil {
			return err
		}
		if err := ForTx[bctxRow](ctx, tx).UpdateBatch(rows); err == nil {
			t.Error("UpdateBatch onto another row's unique key succeeded")
		}
		if got := bctxScores(t, ctx, c, tx); got["a"] != 1 {
			t.Errorf("the failed batch left its first row behind in the caller's transaction: %v", got)
		}
		// Still the caller's transaction, and still usable.
		return ForTx[bctxRow](ctx, tx).Create(&bctxRow{Code: "e", Score: 5})
	})
	if err != nil {
		t.Fatalf("Tx: %v\n%s", err, strings.Join(rec.all, "\n"))
	}
	want := map[string]int64{"a": 1, "b": 2, "c": 3, "d": 4, "e": 5}
	if got := bctxScores(t, context.Background(), c, nil); !sameScores(got, want) {
		t.Errorf("after commit: %v, want %v", got, want)
	}
}

// Outside a transaction UpdateBatch still opens its own, and is all or
// nothing.
func TestUpdateBatchWithoutTransactionIsAllOrNothing(t *testing.T) {
	c, _ := bctxClient(t, "qk57_own")
	ctx, cancel := context.WithTimeout(context.Background(), bctxTimeout)
	defer cancel()
	before := bctxScores(t, ctx, c, nil)
	rows := bctxByCode(t, ctx, c, "a", "b")
	rows[0].Score = 10
	rows[1].Code = "c"

	if err := For[bctxRow](ctx, c).UpdateBatch(rows); err == nil {
		t.Fatal("UpdateBatch onto another row's unique key succeeded")
	}
	if got := bctxScores(t, ctx, c, nil); !sameScores(got, before) {
		t.Errorf("a failed UpdateBatch left writes behind:\nbefore %v\nafter  %v", before, got)
	}
}
