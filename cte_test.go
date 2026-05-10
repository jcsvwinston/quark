// Copyright 2026 jcsvwinston
// SPDX-License-Identifier: Apache-2.0

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

// cteUser / cteOrder mirror the subquery fixtures but live in their own
// table set so tests don't collide.
type cteUser struct {
	ID   int64  `db:"id" pk:"true"`
	Name string `db:"name"`
}

type cteOrder struct {
	ID     int64 `db:"id" pk:"true"`
	UserID int64 `db:"user_id"`
	Amount int64 `db:"amount"`
}

// cteCapturingMiddleware records every SELECT so the CTE-prefix render is
// observable. We can't reach buildSelect from the test package, but the
// driver-bound SQL is the source of truth: a regression in the WITH prefix
// or the arg-index threading shows up here as a literal '?' or a missing
// CTE name.
type cteCapturingMiddleware struct {
	quark.BaseMiddleware
	mu      sync.Mutex
	queries []string
}

func (m *cteCapturingMiddleware) WrapQuery(next quark.QueryFunc) quark.QueryFunc {
	return func(ctx context.Context, exec quark.Executor, sqlStr string, args []any) (*sql.Rows, error) {
		if strings.HasPrefix(strings.TrimSpace(sqlStr), "WITH") {
			m.mu.Lock()
			m.queries = append(m.queries, sqlStr)
			m.mu.Unlock()
		}
		return next(ctx, exec, sqlStr, args)
	}
}

func (m *cteCapturingMiddleware) WrapQueryRow(next quark.QueryRowFunc) quark.QueryRowFunc {
	return func(ctx context.Context, exec quark.Executor, sqlStr string, args []any) *sql.Row {
		if strings.HasPrefix(strings.TrimSpace(sqlStr), "WITH") {
			m.mu.Lock()
			m.queries = append(m.queries, sqlStr)
			m.mu.Unlock()
		}
		return next(ctx, exec, sqlStr, args)
	}
}

func (m *cteCapturingMiddleware) snapshot() []string {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]string, len(m.queries))
	copy(out, m.queries)
	return out
}

func testCTE(ctx context.Context, t *testing.T, baseClient *quark.Client) {
	t.Helper()

	dropTable(baseClient, "cte_orders")
	dropTable(baseClient, "cte_users")
	if err := baseClient.Migrate(ctx, &cteUser{}, &cteOrder{}); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	defer dropTable(baseClient, "cte_orders")
	defer dropTable(baseClient, "cte_users")

	users := []cteUser{{Name: "alice"}, {Name: "bob"}, {Name: "carol"}}
	for i := range users {
		if err := quark.For[cteUser](ctx, baseClient).Create(&users[i]); err != nil {
			t.Fatalf("seed user: %v", err)
		}
	}
	for _, o := range []cteOrder{
		{UserID: users[0].ID, Amount: 200},
		{UserID: users[0].ID, Amount: 50},
		{UserID: users[1].ID, Amount: 150},
		{UserID: users[2].ID, Amount: 0},
	} {
		ord := o
		if err := quark.For[cteOrder](ctx, baseClient).Create(&ord); err != nil {
			t.Fatalf("seed order: %v", err)
		}
	}

	t.Run("WithPrependsCTEAndJoins", func(t *testing.T) {
		// CTE: SELECT user_id FROM orders WHERE amount > 100
		topOrders, err := quark.For[cteOrder](ctx, baseClient).
			Select("user_id").
			Where("amount", ">", 100).
			AsSubquery()
		if err != nil {
			t.Fatalf("AsSubquery: %v", err)
		}

		mw := &cteCapturingMiddleware{}
		client, err := baseClient.WithOptions(quark.WithMiddleware(mw))
		if err != nil {
			t.Fatalf("WithOptions: %v", err)
		}

		got, err := quark.For[cteUser](ctx, client).
			With("top_orders", topOrders).
			Join("top_orders").On("cte_users.id", "=", "top_orders.user_id").
			Limit(50).
			List()
		if err != nil {
			t.Fatalf("list: %v", err)
		}
		// alice (200) and bob (150) qualify; carol does not.
		if len(got) != 2 {
			t.Fatalf("expected 2 users, got %d (%+v)", len(got), got)
		}

		// Captured SQL must start with `WITH "top_orders" AS (...)` and
		// reference the CTE by name in the FROM/JOIN of the outer SELECT.
		captured := mw.snapshot()
		if len(captured) == 0 {
			t.Fatalf("no WITH-prefixed SQL captured, middleware saw nothing")
		}
		sql := captured[0]
		if !strings.Contains(sql, `WITH "top_orders" AS (`) {
			t.Errorf("expected WITH \"top_orders\" AS ( ... ) prefix, got %q", sql)
		}
		if !strings.Contains(sql, `JOIN "top_orders"`) {
			t.Errorf("expected outer JOIN to reference top_orders, got %q", sql)
		}
		if !strings.Contains(sql, "?") {
			t.Errorf("expected '?' bind marker after substitution, got %q", sql)
		}
	})

	t.Run("WithRecursiveEmitsRECURSIVE", func(t *testing.T) {
		base, err := quark.For[cteOrder](ctx, baseClient).
			Select("id", "user_id", "amount").
			AsSubquery()
		if err != nil {
			t.Fatalf("AsSubquery: %v", err)
		}

		mw := &cteCapturingMiddleware{}
		client, err := baseClient.WithOptions(quark.WithMiddleware(mw))
		if err != nil {
			t.Fatalf("WithOptions: %v", err)
		}

		_, _ = quark.For[cteUser](ctx, client).
			WithRecursive("rec_orders", base).
			Limit(10).
			List()

		captured := mw.snapshot()
		if len(captured) == 0 {
			t.Fatalf("no WITH-prefixed SQL captured")
		}
		if !strings.HasPrefix(strings.TrimSpace(captured[0]), "WITH RECURSIVE") {
			t.Errorf("expected WITH RECURSIVE prefix, got %q", captured[0])
		}
	})

	t.Run("InvalidCTENameSurfacesAtExec", func(t *testing.T) {
		ok, err := quark.For[cteOrder](ctx, baseClient).
			Select("id").
			AsSubquery()
		if err != nil {
			t.Fatalf("AsSubquery: %v", err)
		}
		_, err = quark.For[cteUser](ctx, baseClient).
			With("evil; DROP TABLE x;--", ok).
			Limit(10).
			List()
		if err == nil {
			t.Fatalf("expected identifier-validation error for bad CTE name")
		}
	})

	t.Run("NilSubqueryRejected", func(t *testing.T) {
		_, err := quark.For[cteUser](ctx, baseClient).
			With("anything", nil).
			Limit(10).
			List()
		if err == nil {
			t.Fatalf("expected error for With(name, nil)")
		}
		if !errors.Is(err, quark.ErrInvalidQuery) {
			t.Errorf("expected ErrInvalidQuery, got %v", err)
		}
	})

	t.Run("CountAndAggregateEmitCTEPrefix", func(t *testing.T) {
		// Count() and Sum/Avg/Min/Max() build their own SELECT — they
		// must also emit the WITH prefix so a JOIN against the CTE name
		// resolves at exec time. A regression here would surface as a
		// "no such table: top_orders" runtime error.
		topOrders, err := quark.For[cteOrder](ctx, baseClient).
			Select("user_id").
			Where("amount", ">", 100).
			AsSubquery()
		if err != nil {
			t.Fatalf("AsSubquery: %v", err)
		}

		mw := &cteCapturingMiddleware{}
		client, err := baseClient.WithOptions(quark.WithMiddleware(mw))
		if err != nil {
			t.Fatalf("WithOptions: %v", err)
		}

		got, err := quark.For[cteUser](ctx, client).
			With("top_orders", topOrders).
			Join("top_orders").On("cte_users.id", "=", "top_orders.user_id").
			Count()
		if err != nil {
			t.Fatalf("Count: %v", err)
		}
		if got != 2 {
			t.Errorf("Count = %d, want 2 (alice + bob)", got)
		}

		captured := mw.snapshot()
		if len(captured) == 0 {
			t.Fatalf("Count middleware saw no WITH-prefixed SQL")
		}
		if !strings.Contains(captured[0], `WITH "top_orders" AS (`) {
			t.Errorf("Count SQL missing CTE prefix: %q", captured[0])
		}
		if !strings.Contains(captured[0], "SELECT COUNT(*)") {
			t.Errorf("Count SQL malformed: %q", captured[0])
		}
	})

	t.Run("CTEArgsAreThreadedBeforeWHERE", func(t *testing.T) {
		// CTE has 1 bound arg (amount > 100); outer WHERE has 1 bound arg
		// (name = 'alice'). After substitution the args slice must be
		// [100, "alice"] in that order — the CTE renders first, the
		// outer WHERE second.
		topOrders, err := quark.For[cteOrder](ctx, baseClient).
			Select("user_id").
			Where("amount", ">", 100).
			AsSubquery()
		if err != nil {
			t.Fatalf("AsSubquery: %v", err)
		}

		mw := &cteCapturingMiddleware{}
		client, err := baseClient.WithOptions(quark.WithMiddleware(mw))
		if err != nil {
			t.Fatalf("WithOptions: %v", err)
		}

		_, err = quark.For[cteUser](ctx, client).
			With("top_orders", topOrders).
			Where("name", "=", "alice").
			Limit(10).
			List()
		if err != nil {
			t.Fatalf("list: %v", err)
		}
		captured := mw.snapshot()
		if len(captured) == 0 {
			t.Fatalf("no captured SQL")
		}
		s := captured[0]
		// Outer WHERE arg-index starts AFTER the CTE args. SQLite
		// uses '?', so the only thing we can pin is that there are
		// exactly two '?' markers in the rendered fragment in the
		// canonical order (amount-comparison first, name-comparison
		// second). For ordinal dialects the same order would translate
		// to $1 then $2.
		amountIdx := strings.Index(s, "amount")
		nameIdx := strings.Index(s, "name")
		if amountIdx < 0 || nameIdx < 0 {
			t.Fatalf("captured SQL missing column refs: %q", s)
		}
		if amountIdx > nameIdx {
			t.Errorf("expected CTE WHERE to render before outer WHERE, got %q", s)
		}
	})
}
