package quark_test

import (
	"context"
	"errors"
	"testing"

	"github.com/jcsvwinston/quark"
)

// testOptimisticLocking exercises the quark:"version" tag across the three
// update entry points: Update, UpdateFields, and Tracked.Save. The contract:
//   - SET clause includes "version = version + 1".
//   - WHERE clause includes "AND version = <loaded_or_snapshotted>".
//   - On conflict (zero rows-affected), the call returns ErrStaleEntity.
//   - On success, the entity's version field is bumped in memory.
func testOptimisticLocking(ctx context.Context, t *testing.T, baseClient *quark.Client) {
	t.Helper()

	type Account struct {
		ID      int64  `db:"id" pk:"true"`
		Owner   string `db:"owner"`
		Balance int64  `db:"balance"`
		Version int64  `db:"version" quark:"version"`
	}

	dropTable(baseClient, "accounts")
	if err := baseClient.Migrate(ctx, &Account{}); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	defer dropTable(baseClient, "accounts")

	t.Run("UpdateBumpsVersion", func(t *testing.T) {
		a := &Account{Owner: "alice", Balance: 100, Version: 1}
		if err := quark.For[Account](ctx, baseClient).Create(a); err != nil {
			t.Fatalf("create: %v", err)
		}

		a.Balance = 150
		rows, err := quark.For[Account](ctx, baseClient).Update(a)
		if err != nil {
			t.Fatalf("update: %v", err)
		}
		if rows != 1 {
			t.Errorf("expected 1 row, got %d", rows)
		}
		if a.Version != 2 {
			t.Errorf("expected entity.Version bumped to 2 in memory, got %d", a.Version)
		}

		got, _ := quark.For[Account](ctx, baseClient).Find(a.ID)
		if got.Version != 2 {
			t.Errorf("expected DB version=2, got %d", got.Version)
		}
		if got.Balance != 150 {
			t.Errorf("expected balance=150, got %d", got.Balance)
		}
	})

	t.Run("StaleUpdateReturnsErrStaleEntity", func(t *testing.T) {
		a := &Account{Owner: "bob", Balance: 200, Version: 1}
		if err := quark.For[Account](ctx, baseClient).Create(a); err != nil {
			t.Fatalf("create: %v", err)
		}

		// Simulate two readers loading the same row.
		readerA, _ := quark.For[Account](ctx, baseClient).Find(a.ID)
		readerB, _ := quark.For[Account](ctx, baseClient).Find(a.ID)

		// Reader A writes first. Version goes 1 → 2.
		readerA.Balance = 250
		if _, err := quark.For[Account](ctx, baseClient).Update(&readerA); err != nil {
			t.Fatalf("readerA update: %v", err)
		}
		if readerA.Version != 2 {
			t.Errorf("readerA.Version should be 2 after first update, got %d", readerA.Version)
		}

		// Reader B tries to write with the stale version=1 — must fail.
		readerB.Balance = 99999
		_, err := quark.For[Account](ctx, baseClient).Update(&readerB)
		if err == nil {
			t.Fatal("expected ErrStaleEntity, got nil")
		}
		if !errors.Is(err, quark.ErrStaleEntity) {
			t.Errorf("expected ErrStaleEntity, got %v", err)
		}

		// Reader B's malicious write must NOT have landed.
		got, _ := quark.For[Account](ctx, baseClient).Find(a.ID)
		if got.Balance != 250 {
			t.Errorf("readerB stale write should not have landed; balance=%d (expected 250)", got.Balance)
		}
		if got.Version != 2 {
			t.Errorf("expected version still 2, got %d", got.Version)
		}
	})

	t.Run("UpdateFieldsBumpsVersion", func(t *testing.T) {
		a := &Account{Owner: "carol", Balance: 300, Version: 1}
		if err := quark.For[Account](ctx, baseClient).Create(a); err != nil {
			t.Fatalf("create: %v", err)
		}
		a.Balance = 400
		rows, err := quark.For[Account](ctx, baseClient).UpdateFields(a, "balance")
		if err != nil {
			t.Fatalf("UpdateFields: %v", err)
		}
		if rows != 1 {
			t.Errorf("expected 1 row, got %d", rows)
		}
		if a.Version != 2 {
			t.Errorf("UpdateFields should bump version in memory, got %d", a.Version)
		}

		got, _ := quark.For[Account](ctx, baseClient).Find(a.ID)
		if got.Version != 2 || got.Balance != 400 {
			t.Errorf("expected version=2 balance=400, got version=%d balance=%d", got.Version, got.Balance)
		}
	})

	t.Run("UpdateFieldsStaleReturnsErrStaleEntity", func(t *testing.T) {
		a := &Account{Owner: "dave", Balance: 500, Version: 1}
		if err := quark.For[Account](ctx, baseClient).Create(a); err != nil {
			t.Fatalf("create: %v", err)
		}
		// Bump the row out from under the caller via Update.
		bump, _ := quark.For[Account](ctx, baseClient).Find(a.ID)
		bump.Balance = 555
		if _, err := quark.For[Account](ctx, baseClient).Update(&bump); err != nil {
			t.Fatalf("bump update: %v", err)
		}
		// Now try UpdateFields with the stale `a` (still at version=1).
		a.Balance = 999
		_, err := quark.For[Account](ctx, baseClient).UpdateFields(a, "balance")
		if err == nil {
			t.Fatal("expected ErrStaleEntity, got nil")
		}
		if !errors.Is(err, quark.ErrStaleEntity) {
			t.Errorf("expected ErrStaleEntity, got %v", err)
		}
	})

	t.Run("TrackedSaveBumpsVersion", func(t *testing.T) {
		a := &Account{Owner: "eve", Balance: 600, Version: 1}
		if err := quark.For[Account](ctx, baseClient).Create(a); err != nil {
			t.Fatalf("create: %v", err)
		}

		tracked, err := quark.For[Account](ctx, baseClient).Track().Find(a.ID)
		if err != nil {
			t.Fatalf("track find: %v", err)
		}
		tracked.Entity.Balance = 700
		rows, err := tracked.Save(ctx)
		if err != nil {
			t.Fatalf("save: %v", err)
		}
		if rows != 1 {
			t.Errorf("expected 1 row, got %d", rows)
		}
		if tracked.Entity.Version != 2 {
			t.Errorf("Tracked.Save should bump version in memory, got %d", tracked.Entity.Version)
		}

		// Re-saving the same Tracked without further mutation should still
		// be a no-op (no SQL): the snapshot got refreshed.
		rows, err = tracked.Save(ctx)
		if err != nil {
			t.Fatalf("re-save: %v", err)
		}
		if rows != 0 {
			t.Errorf("expected 0 rows on no-op re-save, got %d", rows)
		}

		got, _ := quark.For[Account](ctx, baseClient).Find(a.ID)
		if got.Version != 2 {
			t.Errorf("expected DB version=2, got %d", got.Version)
		}
	})

	t.Run("TrackedSaveStaleReturnsErrStaleEntity", func(t *testing.T) {
		a := &Account{Owner: "frank", Balance: 800, Version: 1}
		if err := quark.For[Account](ctx, baseClient).Create(a); err != nil {
			t.Fatalf("create: %v", err)
		}

		// Two readers get a Tracked handle on the same row.
		trackedA, _ := quark.For[Account](ctx, baseClient).Track().Find(a.ID)
		trackedB, _ := quark.For[Account](ctx, baseClient).Track().Find(a.ID)

		trackedA.Entity.Balance = 850
		if _, err := trackedA.Save(ctx); err != nil {
			t.Fatalf("trackedA save: %v", err)
		}

		trackedB.Entity.Balance = 9999
		_, err := trackedB.Save(ctx)
		if err == nil {
			t.Fatal("expected ErrStaleEntity, got nil")
		}
		if !errors.Is(err, quark.ErrStaleEntity) {
			t.Errorf("expected ErrStaleEntity, got %v", err)
		}
	})
}
