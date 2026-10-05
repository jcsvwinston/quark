// Copyright 2026 jcsvwinston
// SPDX-License-Identifier: Apache-2.0

package suite

import (
	"context"
	"sync"
	"testing"

	"github.com/jcsvwinston/quark"
)

// testSavepointHookUnwind is the cross-engine (engine suite) check that
// rolling back to a savepoint discards the side-effect callbacks queued
// in that scope while preserving those from before it. It uses
// Tx.OnCommit + a local counter (not the global hookRecorder) so it is
// self-contained and dialect-portable.
//
// Runs on all six engines: the savepoint statements are resolved per dialect
// (SavepointDialect — BB-9), so SQL Server uses SAVE TRANSACTION /
// ROLLBACK TRANSACTION and Oracle skips the unsupported RELEASE transparently.
func testSavepointHookUnwind(ctx context.Context, t *testing.T, client *quark.Client) {
	t.Helper()

	dropTable(client, "sp_hook_rows")
	type spHookRow struct {
		ID   int64  `db:"id" pk:"true"`
		Name string `db:"name"`
	}
	if err := client.Migrate(ctx, &spHookRow{}); err != nil {
		t.Fatalf("migrate: %v", err)
	}

	var mu sync.Mutex
	var committed []string
	track := func(label string) func(context.Context) error {
		return func(context.Context) error {
			mu.Lock()
			committed = append(committed, label)
			mu.Unlock()
			return nil
		}
	}

	err := client.Tx(ctx, func(tx *quark.Tx) error {
		if err := quark.ForTx[spHookRow](ctx, tx).Create(&spHookRow{Name: "kept"}); err != nil {
			return err
		}
		tx.OnCommit(track("kept"))
		if err := tx.Savepoint("sp"); err != nil {
			return err
		}
		if err := quark.ForTx[spHookRow](ctx, tx).Create(&spHookRow{Name: "undone"}); err != nil {
			return err
		}
		tx.OnCommit(track("undone"))
		return tx.RollbackTo("sp")
	})
	if err != nil {
		t.Fatalf("Tx: %v", err)
	}

	mu.Lock()
	got := append([]string(nil), committed...)
	mu.Unlock()
	if len(got) != 1 || got[0] != "kept" {
		t.Errorf("OnCommit fired = %v, want [kept] (rolled-back scope's callback must be discarded)", got)
	}

	rows, err := quark.For[spHookRow](ctx, client).Limit(100).List()
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(rows) != 1 || rows[0].Name != "kept" {
		t.Errorf("rows = %+v, want exactly [kept]", rows)
	}
}
