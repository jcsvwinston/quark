package quark_test

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"sync"
	"testing"

	"github.com/jcsvwinston/quark"
)

// havingMiddleware captures SELECT statements that contain "HAVING " so the
// HavingAggregate tests can inspect dialect-specific output without
// depending on Count() semantics across GROUP BY (Count returns total
// rows in Quark, not group count, which is the correct standard behaviour
// for SELECT COUNT(*)).
type havingMiddleware struct {
	quark.BaseMiddleware
	mu   sync.Mutex
	stmt []string
}

func (m *havingMiddleware) WrapQuery(next quark.QueryFunc) quark.QueryFunc {
	return func(ctx context.Context, exec quark.Executor, sqlStr string, args []any) (*sql.Rows, error) {
		if strings.Contains(sqlStr, "HAVING ") {
			m.mu.Lock()
			m.stmt = append(m.stmt, sqlStr)
			m.mu.Unlock()
		}
		return next(ctx, exec, sqlStr, args)
	}
}

func (m *havingMiddleware) reset() {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.stmt = nil
}

func (m *havingMiddleware) latest() string {
	m.mu.Lock()
	defer m.mu.Unlock()
	if len(m.stmt) == 0 {
		return ""
	}
	return m.stmt[len(m.stmt)-1]
}

// testHavingAggregate covers the Phase-2 deliverable: HAVING over the
// COUNT/SUM/AVG/MIN/MAX aggregates without falling back to RawQuery.
// Closes the historical limitation where Having() validated the column
// through SQLGuard.ValidateIdentifier and thus rejected anything with
// parentheses (i.e. every aggregate).
func testHavingAggregate(ctx context.Context, t *testing.T, baseClient *quark.Client) {
	t.Helper()

	type HAOrder struct {
		ID     int64  `db:"id" pk:"true"`
		Status string `db:"status"`
		Amount int64  `db:"amount"`
	}

	dropTable(baseClient, "ha_orders")
	if err := baseClient.Migrate(ctx, &HAOrder{}); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	defer dropTable(baseClient, "ha_orders")

	seed := []HAOrder{
		{Status: "pending", Amount: 10},
		{Status: "pending", Amount: 20},
		{Status: "pending", Amount: 30},
		{Status: "paid", Amount: 100},
		{Status: "shipped", Amount: 5},
	}
	for i := range seed {
		if err := quark.For[HAOrder](ctx, baseClient).Create(&seed[i]); err != nil {
			t.Fatalf("seed %d: %v", i, err)
		}
	}

	// We can't List grouped rows back into HAOrder (rows are aggregate
	// projections, not full models) and Count() over a GROUP BY returns
	// the total of COUNT(*), not the group count. We therefore verify the
	// emitted SQL through a middleware: HAVING <agg> <op> <bind> appears
	// in the right shape for the active dialect.
	mw := &havingMiddleware{}
	client, err := baseClient.WithOptions(quark.WithMiddleware(mw))
	if err != nil {
		t.Fatalf("WithOptions: %v", err)
	}

	t.Run("CountStarGreaterThan", func(t *testing.T) {
		mw.reset()
		_, err := quark.For[HAOrder](ctx, client).
			GroupBy("status").
			HavingAggregate("COUNT", "*", ">", 1).
			Limit(10).
			List()
		if err != nil {
			t.Fatalf("list grouped: %v", err)
		}
		got := mw.latest()
		if got == "" {
			t.Fatal("no HAVING SELECT captured")
		}
		if !strings.Contains(got, "HAVING COUNT(*) > ") {
			t.Errorf("expected HAVING COUNT(*) > <bind> in SQL, got: %s", got)
		}
	})

	t.Run("SumGreaterEqual", func(t *testing.T) {
		mw.reset()
		_, err := quark.For[HAOrder](ctx, client).
			GroupBy("status").
			HavingAggregate("SUM", "amount", ">=", 60).
			Limit(10).
			List()
		if err != nil {
			t.Fatalf("list grouped sum: %v", err)
		}
		got := mw.latest()
		if got == "" {
			t.Fatal("no HAVING SELECT captured")
		}
		// SQL must include SUM(<quoted amount>) >= <bind>, with the column
		// quoted per dialect ("amount" / `amount` / [amount]).
		if !strings.Contains(strings.ToLower(got), "sum(") {
			t.Errorf("expected SUM(...) in HAVING SQL, got: %s", got)
		}
		if !strings.Contains(got, ">= ") {
			t.Errorf("expected >= operator in HAVING SQL, got: %s", got)
		}
	})

	t.Run("CaseInsensitiveFn", func(t *testing.T) {
		// "count" lowercase should be normalised to COUNT.
		_, err := quark.For[HAOrder](ctx, baseClient).
			GroupBy("status").
			HavingAggregate("count", "*", ">", 0).
			Count()
		if err != nil {
			t.Fatalf("lowercase count: %v", err)
		}
	})

	t.Run("RejectsUnknownFn", func(t *testing.T) {
		_, err := quark.For[HAOrder](ctx, baseClient).
			GroupBy("status").
			HavingAggregate("DROP", "amount", ">", 0).
			Count()
		if err == nil {
			t.Fatal("expected error for unknown aggregate fn, got nil")
		}
		if !errors.Is(err, quark.ErrInvalidQuery) {
			t.Errorf("expected ErrInvalidQuery, got %v", err)
		}
	})

	t.Run("RejectsStarOnNonCount", func(t *testing.T) {
		_, err := quark.For[HAOrder](ctx, baseClient).
			GroupBy("status").
			HavingAggregate("SUM", "*", ">", 0).
			Count()
		if err == nil {
			t.Fatal("expected error for SUM(*), got nil")
		}
		if !errors.Is(err, quark.ErrInvalidQuery) {
			t.Errorf("expected ErrInvalidQuery, got %v", err)
		}
	})

	t.Run("RejectsInvalidColumn", func(t *testing.T) {
		_, err := quark.For[HAOrder](ctx, baseClient).
			GroupBy("status").
			HavingAggregate("SUM", "amount; DROP TABLE x", ">", 0).
			Count()
		if err == nil {
			t.Fatal("expected error for injectable column, got nil")
		}
		// The guard returns its own ErrInvalidIdentifier-prefixed message —
		// confirm it surfaces (not silently swallowed).
		if err.Error() == "" {
			t.Error("expected non-empty error from guard")
		}
	})
}
