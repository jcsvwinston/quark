// Copyright 2026 jcsvwinston
// SPDX-License-Identifier: Apache-2.0

package suite

import (
	"context"
	"testing"

	"github.com/jcsvwinston/quark"
)

// subqueryUser / subqueryOrder are the canonical fixture for the subquery
// integration tests. Orders with a non-zero amount drive the EXISTS / IN
// subquery shapes; zero-amount orders are filtered out so the negated
// shapes (NotExists / NotInSub) have something to assert against.
type subqueryUser struct {
	ID   int64  `db:"id" pk:"true"`
	Name string `db:"name"`
}

type subqueryOrder struct {
	ID     int64 `db:"id" pk:"true"`
	UserID int64 `db:"user_id"`
	Amount int64 `db:"amount"`
}

func testSubquery(ctx context.Context, t *testing.T, baseClient *quark.Client) {
	t.Helper()

	dropTable(baseClient, "subquery_orders")
	dropTable(baseClient, "subquery_users")
	if err := baseClient.Migrate(ctx, &subqueryUser{}, &subqueryOrder{}); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	defer dropTable(baseClient, "subquery_orders")
	defer dropTable(baseClient, "subquery_users")

	users := []subqueryUser{
		{Name: "alice"},
		{Name: "bob"},
		{Name: "carol"},
	}
	for i := range users {
		if err := quark.For[subqueryUser](ctx, baseClient).Create(&users[i]); err != nil {
			t.Fatalf("seed user: %v", err)
		}
	}
	// alice → 2 orders > 0; bob → 1 order = 0; carol → no orders at all.
	for _, o := range []subqueryOrder{
		{UserID: users[0].ID, Amount: 100},
		{UserID: users[0].ID, Amount: 50},
		{UserID: users[1].ID, Amount: 0},
	} {
		ord := o
		if err := quark.For[subqueryOrder](ctx, baseClient).Create(&ord); err != nil {
			t.Fatalf("seed order: %v", err)
		}
	}

	t.Run("InSubFiltersUsersWithPositiveOrders", func(t *testing.T) {
		// SELECT user_id FROM orders WHERE amount > 0
		sub, err := quark.For[subqueryOrder](ctx, baseClient).
			Select("user_id").
			Where("amount", ">", 0).
			AsSubquery()
		if err != nil {
			t.Fatalf("AsSubquery: %v", err)
		}
		got, err := quark.For[subqueryUser](ctx, baseClient).WhereExpr(
			quark.InSub(quark.Col("id"), sub),
		).List()
		if err != nil {
			t.Fatalf("list: %v", err)
		}
		// Only alice has orders with amount > 0.
		if len(got) != 1 || got[0].Name != "alice" {
			t.Errorf("got %+v, want [alice]", got)
		}
	})

	t.Run("NotInSubFiltersUsersWithoutPositiveOrders", func(t *testing.T) {
		sub, err := quark.For[subqueryOrder](ctx, baseClient).
			Select("user_id").
			Where("amount", ">", 0).
			AsSubquery()
		if err != nil {
			t.Fatalf("AsSubquery: %v", err)
		}
		got, err := quark.For[subqueryUser](ctx, baseClient).WhereExpr(
			quark.NotInSub(quark.Col("id"), sub),
		).List()
		if err != nil {
			t.Fatalf("list: %v", err)
		}
		// bob and carol — bob's order has amount=0, carol has none.
		if len(got) != 2 {
			t.Errorf("got %d users, want 2", len(got))
		}
		names := map[string]bool{}
		for _, u := range got {
			names[u.Name] = true
		}
		if !names["bob"] || !names["carol"] {
			t.Errorf("got %v, want bob+carol", names)
		}
	})

	t.Run("SubAsScalarComparison", func(t *testing.T) {
		// Find the user matching the id of the order with the largest amount.
		// Subquery: SELECT user_id FROM orders ORDER BY amount DESC LIMIT 1
		sub, err := quark.For[subqueryOrder](ctx, baseClient).
			Select("user_id").
			OrderBy("amount", "DESC").
			Limit(1).
			AsSubquery()
		if err != nil {
			t.Fatalf("AsSubquery: %v", err)
		}
		got, err := quark.For[subqueryUser](ctx, baseClient).WhereExpr(
			quark.Eq(quark.Col("id"), quark.Sub(sub)),
		).List()
		if err != nil {
			t.Fatalf("list: %v", err)
		}
		if len(got) != 1 || got[0].Name != "alice" {
			t.Errorf("got %+v, want [alice]", got)
		}
	})

	t.Run("InvalidInnerIdentifierSurfacesAtCapture", func(t *testing.T) {
		_, err := quark.For[subqueryOrder](ctx, baseClient).
			Where("amount; DROP TABLE x;--", ">", 0).
			AsSubquery()
		if err == nil {
			t.Fatalf("expected identifier-validation error, got nil")
		}
	})
}
