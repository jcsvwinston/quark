// Copyright 2026 jcsvwinston
// SPDX-License-Identifier: Apache-2.0

package quark_test

import (
	"context"
	"strings"
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

// TestSubquery_QmarkCapture pins the dialect-agnostic capture contract:
// AsSubquery renders the inner SELECT with '?' as the bind marker
// regardless of the active dialect, and the args slice carries the WHERE
// values in the order they were enqueued. This is the precondition the
// outer AST relies on — `substitutePathMarkers` then renumbers each '?'
// to the outer dialect's placeholder syntax at the correct argIndex.
//
// SQLite is the harness here (its native placeholder is '?', so the
// captured fragment looks the same with or without the qmark wrapper),
// but the contract being asserted holds for any dialect: arg ordering
// matches the SQL fragment.
func TestSubquery_QmarkCapture(t *testing.T) {
	ctx := context.Background()
	client, err := quark.New("sqlite", ":memory:")
	if err != nil {
		t.Fatalf("client: %v", err)
	}
	defer client.Close()
	if err := client.Migrate(ctx, &subqueryUser{}, &subqueryOrder{}); err != nil {
		t.Fatalf("migrate: %v", err)
	}

	sub, err := quark.For[subqueryOrder](ctx, client).
		Select("user_id").
		Where("amount", ">", 100).
		Where("user_id", "<>", 0).
		AsSubquery()
	if err != nil {
		t.Fatalf("AsSubquery: %v", err)
	}
	sql, args := sub.SQL()

	wantArgs := []any{int64(100), int64(0)}
	if len(args) != len(wantArgs) {
		t.Fatalf("args = %v, want %v", args, wantArgs)
	}
	for i, a := range args {
		// SQLite drivers may pass int / int64 interchangeably; normalise.
		got := a
		switch v := a.(type) {
		case int:
			got = int64(v)
		case int64:
			got = v
		}
		if got != wantArgs[i] {
			t.Errorf("arg %d = %v, want %v", i, got, wantArgs[i])
		}
	}
	if !strings.Contains(sql, "WHERE") {
		t.Errorf("subquery sql missing WHERE: %q", sql)
	}
	// Native SQLite or qmark-via-AsSubquery: in both cases the fragment
	// must contain literal '?' (not '$1', '@p1', or ':1') because that
	// is the contract the outer AST consumes.
	if !strings.Contains(sql, "?") {
		t.Errorf("subquery sql missing '?' bind marker: %q", sql)
	}
	for _, bad := range []string{"$1", "@p1", ":1"} {
		if strings.Contains(sql, bad) {
			t.Errorf("subquery sql leaked dialect placeholder %q: %q", bad, sql)
		}
	}
}

// TestSubquery_RejectsLockOptions enforces the F2-subqueries decision: a
// subquery cannot carry pessimistic locks because dialect emission is
// inconsistent (MSSQL inlines `WITH (UPDLOCK)` in the FROM clause, which
// is illegal inside an `IN (SELECT ...)` context). Acquire locks on the
// outer query instead.
func TestSubquery_RejectsLockOptions(t *testing.T) {
	ctx := context.Background()
	client, err := quark.New("sqlite", ":memory:")
	if err != nil {
		t.Fatalf("client: %v", err)
	}
	defer client.Close()
	if err := client.Migrate(ctx, &subqueryOrder{}); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	// SQLite is allowed to fail at AsSubquery time too, before any dialect
	// would otherwise reject the lock — the rule is enforced uniformly in
	// `quark.AsSubquery` itself, not deferred to dialect.LockSuffix.
	_, err = quark.For[subqueryOrder](ctx, client).
		Where("amount", ">", 0).
		ForUpdate().
		AsSubquery()
	if err == nil {
		t.Fatalf("expected ErrUnsupportedFeature, got nil")
	}
	if !strings.Contains(err.Error(), "pessimistic lock") &&
		!strings.Contains(err.Error(), "Unsupported") {
		t.Errorf("error %q should mention pessimistic lock / unsupported", err)
	}
}
