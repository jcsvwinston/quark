// Copyright 2026 jcsvwinston
// SPDX-License-Identifier: Apache-2.0

package quark_test

import (
	"context"
	"errors"
	"testing"

	"github.com/jcsvwinston/quark"
)

// qcUser / qcOrder share the column names `id` and `created_tag`, so any
// unqualified filter on them under a JOIN is ambiguous for the engine.
// This is the AQ-01 fixture: before the fix, Where/OrderBy/GroupBy and
// Col() rejected the qualified "table.column" form outright, so a join
// query could not filter on ANY shared-name column (the PK included).
type qcUser struct {
	ID   int64  `db:"id" pk:"true"`
	Name string `db:"name"`
}

type qcOrder struct {
	ID     int64 `db:"id" pk:"true"`
	UserID int64 `db:"user_id"`
	Total  int64 `db:"total"`
}

// testQualifiedColumns pins that a query WITH joins accepts one-level
// qualified identifiers (table.column) in Where, OrderBy, GroupBy, Select
// and the Col() AST leaf — each segment validated and quoted separately —
// while a query WITHOUT joins keeps the historical strict single-identifier
// rule, and injection shapes stay rejected.
func testQualifiedColumns(ctx context.Context, t *testing.T, client *quark.Client) {
	t.Helper()

	dropTable(client, "qc_orders")
	dropTable(client, "qc_users")
	if err := client.Migrate(ctx, &qcUser{}, &qcOrder{}); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	defer dropTable(client, "qc_orders")
	defer dropTable(client, "qc_users")

	alice := qcUser{Name: "alice"}
	bob := qcUser{Name: "bob"}
	for _, u := range []*qcUser{&alice, &bob} {
		if err := quark.For[qcUser](ctx, client).Create(u); err != nil {
			t.Fatalf("seed user: %v", err)
		}
	}
	for _, o := range []qcOrder{
		{UserID: alice.ID, Total: 100},
		{UserID: alice.ID, Total: 50},
		{UserID: bob.ID, Total: 70},
	} {
		row := o
		if err := quark.For[qcOrder](ctx, client).Create(&row); err != nil {
			t.Fatalf("seed order: %v", err)
		}
	}

	join := func() *quark.Query[qcOrder] {
		return quark.For[qcOrder](ctx, client).
			Join("qc_users").On("qc_users.id", "=", "qc_orders.user_id")
	}

	t.Run("WhereQualifiedPK", func(t *testing.T) {
		// Filtering by the shared `id` column, qualified with the joined
		// table: exactly alice's orders.
		got, err := join().Where("qc_users.id", "=", alice.ID).List()
		if err != nil {
			t.Fatalf("qualified Where: %v", err)
		}
		if len(got) != 2 {
			t.Fatalf("expected 2 orders for alice, got %d: %+v", len(got), got)
		}
		for _, o := range got {
			if o.UserID != alice.ID {
				t.Errorf("row leaked from another user: %+v", o)
			}
		}
	})

	t.Run("WhereQualifiedBaseTable", func(t *testing.T) {
		got, err := join().Where("qc_orders.id", ">", int64(0)).Count()
		if err != nil {
			t.Fatalf("qualified Where on base table: %v", err)
		}
		if got != 3 {
			t.Errorf("expected 3, got %d", got)
		}
	})

	t.Run("OrderByQualified", func(t *testing.T) {
		got, err := join().
			Where("qc_users.id", "=", alice.ID).
			OrderBy("qc_orders.total", "DESC").
			List()
		if err != nil {
			t.Fatalf("qualified OrderBy: %v", err)
		}
		if len(got) != 2 || got[0].Total != 100 || got[1].Total != 50 {
			t.Errorf("expected totals [100 50], got %+v", got)
		}
	})

	t.Run("WhereExprColQualified", func(t *testing.T) {
		got, err := join().
			WhereExpr(quark.Gt(quark.Col("qc_orders.total"), quark.Lit(60))).
			Count()
		if err != nil {
			t.Fatalf("qualified Col: %v", err)
		}
		if got != 2 {
			t.Errorf("expected 2 orders with total>60, got %d", got)
		}
	})

	t.Run("SelectGroupByQualified", func(t *testing.T) {
		got, err := join().
			Select("qc_orders.user_id").
			GroupBy("qc_orders.user_id").
			List()
		if err != nil {
			t.Fatalf("qualified Select/GroupBy: %v", err)
		}
		if len(got) != 2 {
			t.Errorf("expected 2 groups, got %d: %+v", len(got), got)
		}
	})

	t.Run("QualifiedRejectedWithoutJoin", func(t *testing.T) {
		// Without a JOIN the historical strict rule stands: a dotted
		// name is not a valid single identifier.
		_, err := quark.For[qcOrder](ctx, client).Where("qc_orders.total", ">", 1).List()
		if !errors.Is(err, quark.ErrInvalidIdentifier) {
			t.Errorf("expected ErrInvalidIdentifier without joins, got %v", err)
		}
	})

	t.Run("InjectionShapesStillRejected", func(t *testing.T) {
		cases := []string{
			"qc_users.id; DROP TABLE qc_users",
			"a.b.c",
			"qc_users..id",
			".id",
			"id.",
			`qc_users."id"`,
		}
		for _, col := range cases {
			if _, err := join().Where(col, "=", 1).List(); err == nil {
				t.Errorf("column %q must be rejected under a join", col)
			}
		}
	})
}
