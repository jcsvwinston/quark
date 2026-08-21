// Copyright 2026 jcsvwinston
// SPDX-License-Identifier: Apache-2.0

// Regression test for the crossed --steps defaults: `migrate up` (default 0)
// and `migrate down` (default 1) used to register --steps on the SAME package
// variable, and pflag writes each default into the bound variable at
// registration time — after init() the shared variable held down's 1, so a
// bare `quark migrate up` applied only the FIRST pending migration and
// exited 0 ("exit 0 with no effect" class). Every earlier test ran with a
// single pending migration, where the truncation is invisible.
package commands

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/jcsvwinston/quark"
	"github.com/jcsvwinston/quark/migrate"
	"github.com/spf13/viper"
)

// The bug lived in the flag defaults, so the test must drive the real command
// line without --steps — calling migrate.Up directly would never see it.
func TestBareMigrateUpAppliesEveryPendingMigration(t *testing.T) {
	dsn := filepath.Join(t.TempDir(), "steps.db")
	for _, kv := range [][2]string{
		{"database.default.driver", "sqlite"},
		{"database.default.dsn", dsn},
	} {
		key, old := kv[0], viper.GetString(kv[0])
		viper.Set(key, kv[1])
		t.Cleanup(func() { viper.Set(key, old) })
	}

	var upIDs, downIDs []string
	for _, id := range []string{"20260820000001", "20260820000002"} {
		id := id
		migrate.Register(&migrate.Migration{
			ID:   id,
			Name: "noop_" + id,
			Up: func(ctx context.Context, client *quark.Client) error {
				upIDs = append(upIDs, id)
				return nil
			},
			Down: func(ctx context.Context, client *quark.Client) error {
				downIDs = append(downIDs, id)
				return nil
			},
		})
	}
	t.Cleanup(migrate.Reset)
	t.Cleanup(func() { rootCmd.SetArgs([]string{}) })

	rootCmd.SetArgs([]string{"migrate", "up"})
	if err := rootCmd.Execute(); err != nil {
		t.Fatalf("migrate up: %v", err)
	}
	if len(upIDs) != 2 {
		t.Fatalf("bare `migrate up` applied %d of 2 pending migrations (%v) — the up/down --steps defaults are crossed", len(upIDs), upIDs)
	}

	// The inverse contract must survive the fix: a bare `migrate down` still
	// reverts exactly the latest migration, not all of them.
	rootCmd.SetArgs([]string{"migrate", "down"})
	if err := rootCmd.Execute(); err != nil {
		t.Fatalf("migrate down: %v", err)
	}
	if len(downIDs) != 1 || downIDs[0] != "20260820000002" {
		t.Fatalf("bare `migrate down` must revert only the latest migration, reverted %v", downIDs)
	}
}
