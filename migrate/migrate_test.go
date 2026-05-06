package migrate_test

import (
	"context"
	"errors"
	"testing"

	"github.com/jcsvwinston/quark"
	"github.com/jcsvwinston/quark/migrate"

	_ "modernc.org/sqlite"
)

// setupMigratorDB creates a fresh in-memory SQLite client suitable for migration tests.
// AllowRawQueries must be true because Migrator uses client.Exec internally.
func setupMigratorDB(t *testing.T) (*quark.Client, func()) {
	t.Helper()
	client, err := quark.New("sqlite", ":memory:",
		quark.WithLimits(quark.Limits{
			MaxQueryLength:     10 * 1024,
			MaxResults:         10000,
			MaxJoins:           5,
			MaxWhereConditions: 20,
			QueryTimeout:       30e9, // 30s
			AllowRawQueries:    true,
		}),
	)
	if err != nil {
		t.Fatalf("new client: %v", err)
	}
	return client, func() { client.Close() }
}

func TestMigratorInit(t *testing.T) {
	migrate.Reset()
	client, cleanup := setupMigratorDB(t)
	defer cleanup()

	m := migrate.NewMigrator(client)
	if err := m.Init(context.Background()); err != nil {
		t.Fatalf("Init: %v", err)
	}

	// Calling Init twice must be idempotent (IF NOT EXISTS)
	if err := m.Init(context.Background()); err != nil {
		t.Fatalf("Init (2nd call): %v", err)
	}
}

func TestMigratorGetApplied_Empty(t *testing.T) {
	migrate.Reset()
	client, cleanup := setupMigratorDB(t)
	defer cleanup()

	m := migrate.NewMigrator(client)
	if err := m.Init(context.Background()); err != nil {
		t.Fatal(err)
	}

	applied, err := m.GetApplied(context.Background())
	if err != nil {
		t.Fatalf("GetApplied: %v", err)
	}
	if len(applied) != 0 {
		t.Errorf("expected 0 applied migrations on fresh DB, got %d", len(applied))
	}
}

func TestMigratorUp_AppliesMigrations(t *testing.T) {
	migrate.Reset()
	client, cleanup := setupMigratorDB(t)
	defer cleanup()

	applied := false
	migrate.Register(&migrate.Migration{
		ID:   "0001_test_up",
		Name: "create test_table",
		Up: func(ctx context.Context, c *quark.Client) error {
			applied = true
			return c.Exec(ctx, "CREATE TABLE IF NOT EXISTS test_table (id INTEGER PRIMARY KEY)")
		},
		Down: func(ctx context.Context, c *quark.Client) error {
			return c.Exec(ctx, "DROP TABLE IF EXISTS test_table")
		},
	})

	m := migrate.NewMigrator(client)
	if err := m.Up(context.Background(), 0); err != nil {
		t.Fatalf("Up: %v", err)
	}

	if !applied {
		t.Error("expected Up callback to have been called")
	}

	got, err := m.GetApplied(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !got["0001_test_up"] {
		t.Error("expected 0001_test_up to be recorded as applied")
	}
}

func TestMigratorUp_SkipsAlreadyApplied(t *testing.T) {
	migrate.Reset()
	client, cleanup := setupMigratorDB(t)
	defer cleanup()

	callCount := 0
	migrate.Register(&migrate.Migration{
		ID:   "0002_idempotent",
		Name: "idempotent migration",
		Up: func(ctx context.Context, c *quark.Client) error {
			callCount++
			return c.Exec(ctx, "CREATE TABLE IF NOT EXISTS idempotent_table (id INTEGER PRIMARY KEY)")
		},
		Down: func(ctx context.Context, c *quark.Client) error {
			return c.Exec(ctx, "DROP TABLE IF EXISTS idempotent_table")
		},
	})

	m := migrate.NewMigrator(client)

	if err := m.Up(context.Background(), 0); err != nil {
		t.Fatalf("first Up: %v", err)
	}
	if err := m.Up(context.Background(), 0); err != nil {
		t.Fatalf("second Up: %v", err)
	}

	if callCount != 1 {
		t.Errorf("expected migration to run exactly once, ran %d times", callCount)
	}
}

func TestMigratorUp_StepsLimit(t *testing.T) {
	migrate.Reset()
	client, cleanup := setupMigratorDB(t)
	defer cleanup()

	appliedIDs := map[string]bool{}
	for _, id := range []string{"0010_step_a", "0011_step_b", "0012_step_c"} {
		id := id // capture loop variable
		migrate.Register(&migrate.Migration{
			ID:   id,
			Name: id,
			Up: func(ctx context.Context, c *quark.Client) error {
				appliedIDs[id] = true
				return c.Exec(ctx, "SELECT 1")
			},
			Down: func(ctx context.Context, c *quark.Client) error { return nil },
		})
	}

	m := migrate.NewMigrator(client)
	if err := m.Up(context.Background(), 1); err != nil {
		t.Fatalf("Up steps=1: %v", err)
	}

	count := 0
	for _, v := range appliedIDs {
		if v {
			count++
		}
	}
	if count != 1 {
		t.Errorf("expected exactly 1 migration applied with steps=1, got %d", count)
	}
}

func TestMigratorDown_RevertsLastMigration(t *testing.T) {
	migrate.Reset()
	client, cleanup := setupMigratorDB(t)
	defer cleanup()

	reverted := false
	migrate.Register(&migrate.Migration{
		ID:   "0020_down_test",
		Name: "down test",
		Up: func(ctx context.Context, c *quark.Client) error {
			return c.Exec(ctx, "CREATE TABLE IF NOT EXISTS down_test_tbl (id INTEGER PRIMARY KEY)")
		},
		Down: func(ctx context.Context, c *quark.Client) error {
			reverted = true
			return c.Exec(ctx, "DROP TABLE IF EXISTS down_test_tbl")
		},
	})

	m := migrate.NewMigrator(client)
	if err := m.Up(context.Background(), 0); err != nil {
		t.Fatalf("Up: %v", err)
	}
	if err := m.Down(context.Background(), 1); err != nil {
		t.Fatalf("Down: %v", err)
	}

	if !reverted {
		t.Error("expected Down callback to have been called")
	}

	got, err := m.GetApplied(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if got["0020_down_test"] {
		t.Error("expected 0020_down_test to be removed from applied after Down")
	}
}

func TestMigratorDown_NoMigrations(t *testing.T) {
	migrate.Reset()
	client, cleanup := setupMigratorDB(t)
	defer cleanup()

	m := migrate.NewMigrator(client)
	if err := m.Down(context.Background(), 1); err != nil {
		t.Fatalf("Down on empty DB: %v", err)
	}
}

func TestMigratorUp_FailingMigration(t *testing.T) {
	migrate.Reset()
	client, cleanup := setupMigratorDB(t)
	defer cleanup()

	migrate.Register(&migrate.Migration{
		ID:   "0030_fail",
		Name: "always fails",
		Up: func(ctx context.Context, c *quark.Client) error {
			return errors.New("intentional failure")
		},
		Down: func(ctx context.Context, c *quark.Client) error { return nil },
	})

	m := migrate.NewMigrator(client)
	err := m.Up(context.Background(), 0)
	if err == nil {
		t.Fatal("expected error from failing migration, got nil")
	}
	if !containsStr(err.Error(), "intentional failure") {
		t.Errorf("expected 'intentional failure' in error, got: %v", err)
	}
}

func TestMigratorDryRun_NoPendingMigrations(t *testing.T) {
	migrate.Reset()
	client, cleanup := setupMigratorDB(t)
	defer cleanup()

	// Register a simple migration and apply it first so nothing is pending
	migrate.Register(&migrate.Migration{
		ID:   "0035_predone",
		Name: "pre-done migration",
		Up:   func(ctx context.Context, c *quark.Client) error { return c.Exec(ctx, "SELECT 1") },
		Down: func(ctx context.Context, c *quark.Client) error { return nil },
	})

	m := migrate.NewMigrator(client)
	if err := m.Up(context.Background(), 0); err != nil {
		t.Fatal(err)
	}

	if err := m.UpDryRun(context.Background(), 0); err != nil {
		t.Fatalf("UpDryRun with no pending: %v", err)
	}
}

func TestMigratorDryRun_ShowsPending(t *testing.T) {
	migrate.Reset()
	client, cleanup := setupMigratorDB(t)
	defer cleanup()

	migrate.Register(&migrate.Migration{
		ID:   "0040_dryrun_check",
		Name: "dryrun pending",
		Up:   func(ctx context.Context, c *quark.Client) error { return nil },
		Down: func(ctx context.Context, c *quark.Client) error { return nil },
	})

	m := migrate.NewMigrator(client)
	if err := m.UpDryRun(context.Background(), 0); err != nil {
		t.Fatalf("UpDryRun: %v", err)
	}

	applied, err := m.GetApplied(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if applied["0040_dryrun_check"] {
		t.Error("UpDryRun must not apply migrations — 0040_dryrun_check was found in applied list")
	}
}

func containsStr(s, sub string) bool {
	return len(s) >= len(sub) && (s == sub || len(s) > 0 && stringContains(s, sub))
}

func stringContains(s, sub string) bool {
	for i := 0; i <= len(s)-len(sub); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}
