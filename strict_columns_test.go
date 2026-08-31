// Copyright 2026 jcsvwinston
// SPDX-License-Identifier: Apache-2.0

package quark_test

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/jcsvwinston/quark"
	_ "modernc.org/sqlite"
)

type scUser struct {
	ID   int64  `db:"id" pk:"true"`
	Name string `db:"name"`
	Age  int    `db:"age"`
}

type scOrder struct {
	ID     int64 `db:"id" pk:"true"`
	UserID int64 `db:"user_id"`
	Total  int64 `db:"total"`
}

// TestStrictColumns covers AQ-02: SQLGuard's charset check let a typo'd
// column through, and on SQLite the double-quoted unknown degraded to a
// string literal — Where("agee", ">", 1) returned EVERY row with err ==
// nil. WithStrictColumns turns that into ErrInvalidQuery listing the valid
// columns. The default (no option) keeps the historical behaviour.
func TestStrictColumns(t *testing.T) {
	ctx := context.Background()

	seed := func(t *testing.T, client *quark.Client) {
		t.Helper()
		if err := client.Migrate(ctx, &scUser{}, &scOrder{}); err != nil {
			t.Fatalf("migrate: %v", err)
		}
		for _, u := range []scUser{{Name: "a", Age: 30}, {Name: "b", Age: 40}} {
			row := u
			if err := quark.For[scUser](ctx, client).Create(&row); err != nil {
				t.Fatalf("seed: %v", err)
			}
		}
	}

	t.Run("OffByDefaultKeepsHistoricalBehaviour", func(t *testing.T) {
		client, err := quark.New("sqlite", "file:strictoff?mode=memory&cache=shared")
		if err != nil {
			t.Fatalf("new: %v", err)
		}
		defer client.Close()
		seed(t, client)

		// The measured AQ-02 trap, pinned as-is for compatibility: the typo
		// passes the guard and SQLite returns rows with no error.
		if _, err := quark.For[scUser](ctx, client).Where("agee", ">", 1).Limit(10).List(); err != nil {
			t.Errorf("without WithStrictColumns the typo must keep passing (compat): %v", err)
		}
	})

	t.Run("On", func(t *testing.T) {
		client, err := quark.New("sqlite", "file:stricton?mode=memory&cache=shared",
			quark.WithStrictColumns())
		if err != nil {
			t.Fatalf("new: %v", err)
		}
		defer client.Close()
		seed(t, client)

		t.Run("WhereTypoRejectedListingColumns", func(t *testing.T) {
			_, err := quark.For[scUser](ctx, client).Where("agee", ">", 1).Limit(10).List()
			if !errors.Is(err, quark.ErrInvalidQuery) {
				t.Fatalf("expected ErrInvalidQuery for typo'd column, got %v", err)
			}
			for _, want := range []string{"agee", "id", "name", "age"} {
				if !strings.Contains(err.Error(), want) {
					t.Errorf("error must name %q; got %q", want, err.Error())
				}
			}
		})

		t.Run("KnownColumnsPass", func(t *testing.T) {
			got, err := quark.For[scUser](ctx, client).
				Select("name").
				Where("age", ">", 1).
				GroupBy("name").
				OrderBy("name", "ASC").
				Limit(10).
				List()
			if err != nil {
				t.Fatalf("known columns must pass: %v", err)
			}
			if len(got) != 2 {
				t.Errorf("expected 2 rows, got %d", len(got))
			}
		})

		t.Run("OrderByTypoRejected", func(t *testing.T) {
			_, err := quark.For[scUser](ctx, client).OrderBy("agee", "ASC").Limit(1).List()
			if !errors.Is(err, quark.ErrInvalidQuery) {
				t.Errorf("expected ErrInvalidQuery, got %v", err)
			}
		})

		t.Run("SelectTypoRejected", func(t *testing.T) {
			_, err := quark.For[scUser](ctx, client).Select("agee").Limit(1).List()
			if !errors.Is(err, quark.ErrInvalidQuery) {
				t.Errorf("expected ErrInvalidQuery, got %v", err)
			}
		})

		t.Run("AggregateTypoRejected", func(t *testing.T) {
			_, err := quark.For[scUser](ctx, client).Sum("agee")
			if !errors.Is(err, quark.ErrInvalidQuery) {
				t.Errorf("expected ErrInvalidQuery, got %v", err)
			}
		})

		t.Run("SelectExprAliasOrderable", func(t *testing.T) {
			// ORDER BY / GROUP BY on a SelectExpr alias stays legal.
			got, err := quark.For[scUser](ctx, client).
				Select("id").
				SelectExpr("age_twice", quark.Func("MAX", quark.Col("age"))).
				GroupBy("id").
				OrderBy("age_twice", "DESC").
				Limit(10).
				List()
			if err != nil {
				t.Fatalf("alias in OrderBy must pass under strict columns: %v", err)
			}
			if len(got) != 2 {
				t.Errorf("expected 2 rows, got %d", len(got))
			}
		})

		t.Run("JoinsAreExempt", func(t *testing.T) {
			// A joined query legitimately references the other table's
			// columns — the documented escape hatch.
			if _, err := quark.For[scOrder](ctx, client).
				Join("sc_users").On("sc_users.id", "=", "sc_orders.user_id").
				Where("sc_users.age", ">", 1).
				Count(); err != nil {
				t.Errorf("joined query must be exempt from strict columns: %v", err)
			}
		})

		t.Run("WhereExprIsEscapeHatch", func(t *testing.T) {
			// The AST path is raw for the membership check (still guard-
			// validated for identifier safety).
			if _, err := quark.For[scUser](ctx, client).
				WhereExpr(quark.Gt(quark.Col("age"), quark.Lit(1))).
				Limit(10).
				List(); err != nil {
				t.Errorf("WhereExpr must stay usable under strict columns: %v", err)
			}
		})
	})
}
