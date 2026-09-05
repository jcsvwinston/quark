// Copyright 2026 jcsvwinston
// SPDX-License-Identifier: Apache-2.0

package migrate_test

import (
	"bytes"
	"context"
	"database/sql"
	"errors"
	"log/slog"
	"strings"
	"testing"

	"github.com/jcsvwinston/quark"
	"github.com/jcsvwinston/quark/migrate"
)

// QK-6 (maturity audit 2026-09-03): Up takes the migration lock by default
// — a no-op on SQLite, which has no distributed lock and a single writer —
// and reports through a logger instead of stdout.
func TestMigrator_LockDefaultAndLogger(t *testing.T) {
	migrate.Reset()
	client, cleanup := setupMigratorDB(t)
	defer cleanup()

	migrate.Register(&migrate.Migration{
		ID: "001", Name: "users",
		Up: func(ctx context.Context, c *quark.Client) error {
			return c.Exec(ctx, "CREATE TABLE users (id INTEGER PRIMARY KEY)")
		},
		Down: func(ctx context.Context, c *quark.Client) error { return c.Exec(ctx, "DROP TABLE users") },
	})
	var buf bytes.Buffer
	m := migrate.NewMigrator(client, migrate.WithLogger(slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug}))))
	if err := m.Up(context.Background(), 0); err != nil {
		t.Fatalf("Up with the default lock on SQLite: %v", err)
	}
	out := buf.String()
	if !strings.Contains(out, "no distributed lock") || !strings.Contains(out, "migrate: applying") || !strings.Contains(out, "id=001") {
		t.Fatalf("progress must go through the logger:\n%s", out)
	}
	if err := migrate.NewMigrator(client, migrate.WithoutLock()).Down(context.Background(), 1); err != nil {
		t.Fatalf("Down without lock: %v", err)
	}
}

// A migration in its transactional form that fails after its DDL leaves
// neither the table nor the ledger row: SQLite rolls DDL back.
func TestMigrator_UpTxIsAtomic(t *testing.T) {
	migrate.Reset()
	client, cleanup := setupMigratorDB(t)
	defer cleanup()

	boom := errors.New("second statement failed")
	migrate.Register(&migrate.Migration{
		ID: "002", Name: "orders",
		UpTx: func(ctx context.Context, tx *sql.Tx) error {
			if _, err := tx.ExecContext(ctx, "CREATE TABLE orders (id INTEGER PRIMARY KEY)"); err != nil {
				return err
			}
			return boom
		},
	})
	m := migrate.NewMigrator(client)
	err := m.Up(context.Background(), 0)
	if !errors.Is(err, boom) {
		t.Fatalf("Up: err=%v, want the migration's error", err)
	}
	var n int
	_ = client.Raw().QueryRow("SELECT count(*) FROM sqlite_master WHERE type='table' AND name='orders'").Scan(&n)
	if n != 0 {
		t.Fatalf("the table survived a failed transactional migration")
	}
	applied, _ := m.GetApplied(context.Background())
	if applied["002"] {
		t.Fatalf("the ledger recorded a migration that rolled back")
	}

	// The successful form records in the same transaction, and DownTx
	// mirrors it.
	migrate.Reset()
	migrate.Register(&migrate.Migration{
		ID: "003", Name: "items",
		UpTx: func(ctx context.Context, tx *sql.Tx) error {
			_, err := tx.ExecContext(ctx, "CREATE TABLE items (id INTEGER PRIMARY KEY)")
			return err
		},
		DownTx: func(ctx context.Context, tx *sql.Tx) error {
			_, err := tx.ExecContext(ctx, "DROP TABLE items")
			return err
		},
	})
	if err := m.Up(context.Background(), 0); err != nil {
		t.Fatalf("Up (UpTx): %v", err)
	}
	if applied, _ := m.GetApplied(context.Background()); !applied["003"] {
		t.Fatalf("UpTx not recorded")
	}
	if err := m.Down(context.Background(), 1); err != nil {
		t.Fatalf("Down (DownTx): %v", err)
	}
	if applied, _ := m.GetApplied(context.Background()); applied["003"] {
		t.Fatalf("DownTx not unrecorded")
	}
}
