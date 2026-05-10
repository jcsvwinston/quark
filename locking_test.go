package quark_test

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"

	"github.com/jcsvwinston/quark"
)

// lockingObserver captures emitted SELECT SQL so the locking tests can
// inspect dialect-specific clauses without round-tripping through every
// engine.
type lockingObserver struct {
	mu   sync.Mutex
	stmt []string
}

func (o *lockingObserver) ObserveQuery(e quark.QueryEvent) {
	if e.Operation != "SELECT" {
		return
	}
	o.mu.Lock()
	o.stmt = append(o.stmt, e.SQL)
	o.mu.Unlock()
}

func (o *lockingObserver) reset() {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.stmt = nil
}

func (o *lockingObserver) latestContaining(needle string) string {
	o.mu.Lock()
	defer o.mu.Unlock()
	for i := len(o.stmt) - 1; i >= 0; i-- {
		if strings.Contains(o.stmt[i], needle) {
			return o.stmt[i]
		}
	}
	return ""
}

// testPessimisticLocking covers the Phase-2 locking deliverable. The
// dialect-specific SQL surface is exercised through the observer; the
// SQLite branch verifies that ForUpdate returns ErrUnsupportedFeature.
// Per-dialect emission is also unit-tested in dialect_lock_test.go for
// the engines that aren't reachable from SharedSuite.
func testPessimisticLocking(ctx context.Context, t *testing.T, baseClient *quark.Client) {
	t.Helper()

	type LockOrder struct {
		ID     int64  `db:"id" pk:"true"`
		Status string `db:"status"`
	}

	dropTable(baseClient, "lock_orders")
	if err := baseClient.Migrate(ctx, &LockOrder{}); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	defer dropTable(baseClient, "lock_orders")

	for _, status := range []string{"pending", "paid", "shipped"} {
		if err := quark.For[LockOrder](ctx, baseClient).Create(&LockOrder{Status: status}); err != nil {
			t.Fatalf("seed: %v", err)
		}
	}

	obs := &lockingObserver{}
	client, err := baseClient.WithOptions(quark.WithQueryObserver(obs))
	if err != nil {
		t.Fatalf("WithOptions: %v", err)
	}

	t.Run("ForUpdateOnSQLiteIsUnsupported", func(t *testing.T) {
		// SQLite has no row-level FOR UPDATE; ForUpdate must surface
		// ErrUnsupportedFeature at execution time without running any SQL.
		if client.Dialect().Name() != "sqlite" {
			t.Skip("dialect is not sqlite; this subtest pins the SQLite contract")
		}
		_, err := quark.For[LockOrder](ctx, client).
			Where("status", "=", "pending").
			ForUpdate().
			Limit(10).
			List()
		if err == nil {
			t.Fatal("expected error for ForUpdate on sqlite, got nil")
		}
		if !errors.Is(err, quark.ErrUnsupportedFeature) {
			t.Errorf("expected ErrUnsupportedFeature, got %v", err)
		}
	})

	t.Run("NoLockEmitsNoExtraClause", func(t *testing.T) {
		obs.reset()
		_, err := quark.For[LockOrder](ctx, client).
			Where("status", "=", "pending").
			Limit(10).
			List()
		if err != nil {
			t.Fatalf("baseline list: %v", err)
		}
		if got := obs.latestContaining("FOR UPDATE"); got != "" {
			t.Errorf("expected no FOR UPDATE in SQL when lock not requested: %s", got)
		}
		if got := obs.latestContaining("WITH ("); got != "" {
			t.Errorf("expected no WITH (...) hint when lock not requested: %s", got)
		}
	})
}
