// Copyright 2026 jcsvwinston
// SPDX-License-Identifier: Apache-2.0

package quark_test

import (
	"context"
	"testing"
	"time"

	"github.com/jcsvwinston/quark"
)

// bb2Customer and bb2Order are the BB-2 regression fixture. Both tables carry
// `id` and `deleted_at`, so a typed JOIN with a bare `SELECT *` and an
// unqualified soft-delete predicate collides on both column names — exactly
// the cross-engine failure BB-2 reported (ambiguous `id`/`deleted_at` on the
// strict engines, silent column mis-bind on the lax ones).
type bb2Customer struct {
	ID        int64      `db:"id" pk:"true"`
	Name      string     `db:"name"`
	DeletedAt *time.Time `db:"deleted_at"`
}

type bb2Order struct {
	ID         int64      `db:"id" pk:"true"`
	CustomerID int64      `db:"customer_id"`
	Status     string     `db:"status"`
	DeletedAt  *time.Time `db:"deleted_at"`
}

// testBB2JoinProjection pins the BB-2 fix. A typed query with a JOIN and no
// explicit Select must:
//
//	(a) project only the base table's columns (`SELECT bb2_orders.*`) so the
//	    scanner never binds a joined table's column into T, and
//	(b) qualify the injected soft-delete predicate with the base table
//	    (`bb2_orders.deleted_at IS NULL`) so a joined table that also exposes
//	    `deleted_at` doesn't make the column ambiguous.
//
// Before the fix this whole path was un-runnable via List() — join_builder_test.go
// reaches for Count() specifically to dodge the `SELECT *` ambiguity.
func testBB2JoinProjection(ctx context.Context, t *testing.T, baseClient *quark.Client) {
	t.Helper()

	dropTable(baseClient, "bb2_orders")
	dropTable(baseClient, "bb2_customers")
	if err := baseClient.Migrate(ctx, &bb2Customer{}, &bb2Order{}); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	defer dropTable(baseClient, "bb2_orders")
	defer dropTable(baseClient, "bb2_customers")

	acme := bb2Customer{Name: "acme"}
	if err := quark.For[bb2Customer](ctx, baseClient).Create(&acme); err != nil {
		t.Fatalf("seed customer: %v", err)
	}
	// A soft-deleted customer: proves the join doesn't drag the joined
	// table's deleted_at into the base table's scope (the base scope is
	// keyed on bb2_orders.deleted_at, not bb2_customers.deleted_at).
	gone := bb2Customer{Name: "gone"}
	if err := quark.For[bb2Customer](ctx, baseClient).Create(&gone); err != nil {
		t.Fatalf("seed customer: %v", err)
	}
	if _, err := quark.For[bb2Customer](ctx, baseClient).Delete(&gone); err != nil {
		t.Fatalf("soft-delete customer: %v", err)
	}

	orders := []bb2Order{
		{CustomerID: acme.ID, Status: "paid"},
		{CustomerID: acme.ID, Status: "pending"},
	}
	for i := range orders {
		if err := quark.For[bb2Order](ctx, baseClient).Create(&orders[i]); err != nil {
			t.Fatalf("seed order: %v", err)
		}
	}
	// One trashed order: must NOT come back under the default scope, even
	// across a join.
	trashed := bb2Order{CustomerID: acme.ID, Status: "void"}
	if err := quark.For[bb2Order](ctx, baseClient).Create(&trashed); err != nil {
		t.Fatalf("seed trashed order: %v", err)
	}
	if _, err := quark.For[bb2Order](ctx, baseClient).Delete(&trashed); err != nil {
		t.Fatalf("soft-delete order: %v", err)
	}

	t.Run("InnerJoinListProjectsBaseTable", func(t *testing.T) {
		// Pre-fix: `SELECT *` over the join → ambiguous `id`/`deleted_at`
		// (hard error on strict engines, silent mis-bind on lax ones).
		got, err := quark.For[bb2Order](ctx, baseClient).
			Join("bb2_customers").On("bb2_customers.id", "=", "bb2_orders.customer_id").
			List()
		if err != nil {
			t.Fatalf("Join().List(): %v", err)
		}
		// 2 live orders (the trashed one stays hidden); both belong to acme.
		if len(got) != 2 {
			t.Fatalf("expected 2 live orders across the join, got %d: %+v", len(got), got)
		}
		seen := map[int64]bool{orders[0].ID: false, orders[1].ID: false}
		for _, o := range got {
			// id must be the order's own id, never the customer's — the
			// silent-corruption symptom BB-2 warned about.
			if _, ok := seen[o.ID]; !ok {
				t.Errorf("order scanned with foreign/zero id %d (base-table projection broken): %+v", o.ID, o)
			}
			seen[o.ID] = true
			if o.CustomerID != acme.ID {
				t.Errorf("expected customer_id %d, got %d", acme.ID, o.CustomerID)
			}
			if o.Status == "void" {
				t.Errorf("trashed order leaked through the join: %+v", o)
			}
		}
		for id, ok := range seen {
			if !ok {
				t.Errorf("live order id %d missing from join result", id)
			}
		}
	})

	t.Run("LeftJoinListNoNullCorruption", func(t *testing.T) {
		// An order referencing a non-existent customer: a LEFT JOIN keeps the
		// order row with NULL customer columns. Pre-fix the bare `*` scanned
		// the NULL bb2_customers.id into bb2Order.ID → "converting NULL to
		// int64". With base-table projection the customer columns never enter
		// the result set, so the order scans cleanly.
		orphan := bb2Order{CustomerID: 999999, Status: "orphan"}
		if err := quark.For[bb2Order](ctx, baseClient).Create(&orphan); err != nil {
			t.Fatalf("seed orphan order: %v", err)
		}
		got, err := quark.For[bb2Order](ctx, baseClient).
			LeftJoin("bb2_customers").On("bb2_customers.id", "=", "bb2_orders.customer_id").
			List()
		if err != nil {
			t.Fatalf("LeftJoin().List(): %v", err)
		}
		var sawOrphan bool
		for _, o := range got {
			if o.ID == orphan.ID {
				sawOrphan = true
				if o.Status != "orphan" || o.CustomerID != 999999 {
					t.Errorf("orphan order mis-scanned: %+v", o)
				}
			}
		}
		if !sawOrphan {
			t.Errorf("expected the orphan order (NULL-matched customer) in the LEFT JOIN result")
		}
	})

	t.Run("CountUnderJoinStillScopes", func(t *testing.T) {
		// Count shares the soft-delete predicate path; the base-table
		// qualification must keep it unambiguous and scoped to live rows.
		// The orphan seeded by the previous subtest is live but has no
		// matching customer, so the INNER JOIN drops it — only the 2 live
		// acme orders survive the join.
		n, err := quark.For[bb2Order](ctx, baseClient).
			Join("bb2_customers").On("bb2_customers.id", "=", "bb2_orders.customer_id").
			Count()
		if err != nil {
			t.Fatalf("Join().Count(): %v", err)
		}
		// INNER JOIN drops the orphan (no matching customer); 2 live acme orders.
		if n != 2 {
			t.Errorf("expected 2 live joined orders, got %d", n)
		}
	})
}
