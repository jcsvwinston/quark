// Copyright 2026 jcsvwinston
// SPDX-License-Identifier: Apache-2.0

package quark_test

import (
	"context"
	"errors"
	"testing"

	"github.com/jcsvwinston/quark"
)

// jbUser / jbOrder are the canonical fixture for the structured JOIN
// builder. The tables and column shapes mirror the join_on_security_test
// fixtures so the new builder exercises the same ValidateJoinOn surface
// the legacy free-form path did.
type jbUser struct {
	ID    int64  `db:"id" pk:"true"`
	Email string `db:"email"`
}

type jbOrder struct {
	ID     int64 `db:"id" pk:"true"`
	UserID int64 `db:"user_id"`
	Amount int64 `db:"amount"`
}

// testJoinBuilder is the SharedSuite registration for F2-join-builder.
// It cross-checks the typed `Join(table).On(left, op, right)` form and
// the `OnRaw(onClause)` escape hatch against the existing JOIN-rendering
// pipeline.
func testJoinBuilder(ctx context.Context, t *testing.T, baseClient *quark.Client) {
	t.Helper()

	dropTable(baseClient, "jb_orders")
	dropTable(baseClient, "jb_users")
	if err := baseClient.Migrate(ctx, &jbUser{}, &jbOrder{}); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	defer dropTable(baseClient, "jb_orders")
	defer dropTable(baseClient, "jb_users")

	alice := jbUser{Email: "alice@x"}
	if err := quark.For[jbUser](ctx, baseClient).Create(&alice); err != nil {
		t.Fatalf("seed user: %v", err)
	}
	var orderIDs []int64
	for _, o := range []jbOrder{
		{UserID: alice.ID, Amount: 100},
		{UserID: alice.ID, Amount: 50},
	} {
		row := o
		if err := quark.For[jbOrder](ctx, baseClient).Create(&row); err != nil {
			t.Fatalf("seed order: %v", err)
		}
		orderIDs = append(orderIDs, row.ID)
	}

	t.Run("OnTypedFormExecutes", func(t *testing.T) {
		// `.On(left, op, right)` is the typed shape. Each side is a
		// qualified identifier that goes through the existing
		// ValidateJoinOn grammar.
		//
		// Count() pins "ON clause is accepted and the JOIN executes". The
		// List() projection contract — that a bare SELECT under a JOIN
		// projects only the base table's columns — is pinned separately in
		// OnTypedFormListsBaseColumns below (the BB-2 fix); cross-engine
		// coverage of both projection paths lives in testBB2JoinProjection.
		got, err := quark.For[jbOrder](ctx, baseClient).
			Join("jb_users").On("jb_users.id", "=", "jb_orders.user_id").
			Count()
		if err != nil {
			t.Fatalf("On count: %v", err)
		}
		if got != 2 {
			t.Errorf("expected 2 orders, got %d", got)
		}
	})

	t.Run("OnTypedFormListsBaseColumns", func(t *testing.T) {
		// Both tables expose `id`. Pre-BB-2 the default `SELECT *` over the
		// JOIN was ambiguous (hard error on MSSQL, silent mis-bind elsewhere),
		// which is why the suite used to route every join assertion through
		// Count(). With base-table projection (`SELECT jb_orders.*`) List()
		// returns the base rows cleanly — each with its own id, not the
		// joined user's.
		got, err := quark.For[jbOrder](ctx, baseClient).
			Join("jb_users").On("jb_users.id", "=", "jb_orders.user_id").
			List()
		if err != nil {
			t.Fatalf("On list: %v", err)
		}
		if len(got) != 2 {
			t.Fatalf("expected 2 orders, got %d: %+v", len(got), got)
		}
		for _, o := range got {
			if o.UserID != alice.ID {
				t.Errorf("expected user_id %d, got %d", alice.ID, o.UserID)
			}
			// id must be one of the seeded order ids — never the joined
			// user's id (the mis-bind symptom a bare `SELECT *` produced).
			var isOrderID bool
			for _, id := range orderIDs {
				if o.ID == id {
					isOrderID = true
					break
				}
			}
			if !isOrderID {
				t.Errorf("order.ID %d is not a seeded order id — possible mis-bind to the joined table: %+v", o.ID, o)
			}
		}
	})

	t.Run("OnRawAcceptsCompoundClause", func(t *testing.T) {
		// `OnRaw` is the escape hatch for AND-chained ON clauses; the
		// validator still rejects everything outside the
		// identifier-only grammar. `Count()` for the same MSSQL
		// ambiguous-id reason as OnTypedFormExecutes.
		got, err := quark.For[jbOrder](ctx, baseClient).
			Join("jb_users").OnRaw("jb_users.id = jb_orders.user_id AND jb_users.email = jb_users.email").
			Count()
		if err != nil {
			t.Fatalf("OnRaw count: %v", err)
		}
		if got != 2 {
			t.Errorf("expected 2 orders, got %d", got)
		}
	})

	t.Run("OnRawRejectsInjection", func(t *testing.T) {
		_, err := quark.For[jbOrder](ctx, baseClient).
			Join("jb_users").OnRaw("jb_users.id = jb_orders.user_id; DROP TABLE jb_users").
			Limit(50).
			List()
		if err == nil {
			t.Fatalf("expected ErrInvalidJoin for injection-laden OnRaw")
		}
		if !errors.Is(err, quark.ErrInvalidJoin) {
			t.Errorf("expected ErrInvalidJoin, got %v", err)
		}
	})

	t.Run("OnTypedFormRejectsInjection", func(t *testing.T) {
		// `.On(left, op, right)` concatenates into a single ON clause
		// before validation, so injection in any of the three arguments
		// must still be rejected. Cover the three positions to pin the
		// regression contract.
		cases := []struct {
			name            string
			left, op, right string
		}{
			{"InjectionInLeft",
				"jb_users.id; DROP TABLE jb_users", "=", "jb_orders.user_id"},
			{"InjectionInRight",
				"jb_users.id", "=", "jb_orders.user_id; DROP TABLE jb_orders"},
			{"BogusOperator",
				"jb_users.id", "OR", "jb_orders.user_id"},
		}
		for _, tc := range cases {
			t.Run(tc.name, func(t *testing.T) {
				_, err := quark.For[jbOrder](ctx, baseClient).
					Join("jb_users").On(tc.left, tc.op, tc.right).
					Limit(10).
					List()
				if err == nil {
					t.Fatalf("expected ErrInvalidJoin for %q %q %q", tc.left, tc.op, tc.right)
				}
				if !errors.Is(err, quark.ErrInvalidJoin) {
					t.Errorf("%s: expected ErrInvalidJoin, got %v", tc.name, err)
				}
			})
		}
	})

	t.Run("LeftJoinReturnsBuilder", func(t *testing.T) {
		// Sanity check that LeftJoin also returns *JoinBuilder[T].
		// SQLite supports LEFT JOIN; RightJoin coverage lives in
		// p0_fixes_test.go's TestRightJoin since SQLite's RIGHT JOIN
		// support is version-dependent and requires a tolerant assert.
		//
		// `Count()` for the same MSSQL ambiguous-id reason as the typed
		// On test above.
		_, err := quark.For[jbOrder](ctx, baseClient).
			LeftJoin("jb_users").On("jb_users.id", "=", "jb_orders.user_id").
			Count()
		if err != nil {
			t.Fatalf("LeftJoin On: %v", err)
		}
	})
}
