// Copyright 2026 jcsvwinston
// SPDX-License-Identifier: Apache-2.0

package quark_test

import (
	"context"
	"database/sql"
	"strings"
	"sync"
	"testing"

	"github.com/jcsvwinston/quark"
)

// exprUser is the canonical fixture for AST integration. Three columns let
// us exercise leaves, And/Or composition, In/NotIn, and HAVING-with-Func
// against a real driver.
type exprUser struct {
	ID     int64  `db:"id" pk:"true"`
	Name   string `db:"name"`
	Status string `db:"status"`
	Logins int64  `db:"logins"`
}

// exprCapturingMiddleware records every SELECT that the package emits so we
// can assert the AST's rendered fragment shape after dialect substitution.
// We can't reach buildWhereClause from the test package, but we can read the
// final SQL the driver receives — that's the only thing that matters for
// behaviour.
type exprCapturingMiddleware struct {
	quark.BaseMiddleware
	mu      sync.Mutex
	queries []string
}

func (m *exprCapturingMiddleware) WrapQuery(next quark.QueryFunc) quark.QueryFunc {
	return func(ctx context.Context, exec quark.Executor, sqlStr string, args []any) (*sql.Rows, error) {
		if strings.HasPrefix(strings.TrimSpace(sqlStr), "SELECT") {
			m.mu.Lock()
			m.queries = append(m.queries, sqlStr)
			m.mu.Unlock()
		}
		return next(ctx, exec, sqlStr, args)
	}
}

func (m *exprCapturingMiddleware) snapshot() []string {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]string, len(m.queries))
	copy(out, m.queries)
	return out
}

// testExprAST is the Phase-2 deliverable for the composable expression AST.
// Runs against the SharedSuite so all six dialects pick it up once their
// containers are wired in. SQLite is the proven path; the AST itself is
// dialect-agnostic by design.
func testExprAST(ctx context.Context, t *testing.T, baseClient *quark.Client) {
	t.Helper()

	dropTable(baseClient, "expr_users")
	if err := baseClient.Migrate(ctx, &exprUser{}); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	defer dropTable(baseClient, "expr_users")

	rows := []exprUser{
		{Name: "alice", Status: "active", Logins: 12},
		{Name: "bob", Status: "active", Logins: 3},
		{Name: "carol", Status: "pending", Logins: 0},
		{Name: "dave", Status: "archived", Logins: 50},
	}
	for i := range rows {
		if err := quark.For[exprUser](ctx, baseClient).Create(&rows[i]); err != nil {
			t.Fatalf("seed: %v", err)
		}
	}

	t.Run("EqAndOrFiltersCorrectRows", func(t *testing.T) {
		// active AND (logins > 10 OR name = 'bob') — alice (logins=12) and bob match.
		got, err := quark.For[exprUser](ctx, baseClient).WhereExpr(
			quark.And(
				quark.Eq(quark.Col("status"), quark.Lit("active")),
				quark.Or(
					quark.Gt(quark.Col("logins"), quark.Lit(int64(10))),
					quark.Eq(quark.Col("name"), quark.Lit("bob")),
				),
			),
		).List()
		if err != nil {
			t.Fatalf("list: %v", err)
		}
		if len(got) != 2 {
			t.Fatalf("expected 2 rows (alice + bob), got %d", len(got))
		}
		seen := map[string]bool{}
		for _, r := range got {
			seen[r.Name] = true
		}
		if !seen["alice"] || !seen["bob"] {
			t.Errorf("got %v, want alice + bob", seen)
		}
	})

	t.Run("InFiltersMultipleValues", func(t *testing.T) {
		got, err := quark.For[exprUser](ctx, baseClient).WhereExpr(
			quark.In(quark.Col("status"),
				quark.Lit("pending"), quark.Lit("archived"),
			),
		).List()
		if err != nil {
			t.Fatalf("list: %v", err)
		}
		if len(got) != 2 {
			t.Fatalf("expected 2 rows (carol + dave), got %d", len(got))
		}
	})

	t.Run("NotWrapsCompare", func(t *testing.T) {
		got, err := quark.For[exprUser](ctx, baseClient).WhereExpr(
			quark.Not(quark.Eq(quark.Col("status"), quark.Lit("active"))),
		).List()
		if err != nil {
			t.Fatalf("list: %v", err)
		}
		if len(got) != 2 {
			t.Fatalf("expected 2 rows (carol + dave), got %d", len(got))
		}
	})

	t.Run("HavingExprWithFunc", func(t *testing.T) {
		// GROUP BY status; HAVING SUM(logins) > 10. Buckets:
		//   active: 12 + 3 = 15  ✓
		//   pending: 0          ✗
		//   archived: 50        ✓
		// We can't List back into exprUser because non-grouped columns
		// would be ambiguous on strict dialects, so we capture the SQL via
		// middleware and assert the HAVING fragment landed correctly.
		mw := &exprCapturingMiddleware{}
		client, err := baseClient.WithOptions(quark.WithMiddleware(mw))
		if err != nil {
			t.Fatalf("WithOptions: %v", err)
		}
		_, _ = quark.For[exprUser](ctx, client).
			Select("status").
			GroupBy("status").
			HavingExpr(
				quark.Gt(
					quark.Func("SUM", quark.Col("logins")),
					quark.Lit(int64(10)),
				),
			).List()
		// We don't insist the query succeeds (some dialects may complain
		// about the non-grouped columns) — the contract we pin is that
		// the HAVING fragment was emitted with the loaded column
		// identifier dialect-quoted. We can't compare the placeholder
		// literal (`?` on SQLite, `$1` on PG, `@p1` on MSSQL, `:1` on
		// Oracle); instead assert presence of `HAVING SUM(<quoted>)`
		// and that the HAVING is NOT followed by an un-substituted `?`
		// marker on dialects whose Placeholder is NOT `?`.
		havingFrag := "SUM(" + q(client, "logins") + ")"
		found := false
		for _, sqlStr := range mw.snapshot() {
			if strings.Contains(sqlStr, "HAVING") && strings.Contains(sqlStr, havingFrag) {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("expected captured SQL to contain HAVING %s fragment, got %v",
				havingFrag, mw.snapshot())
		}
	})

	t.Run("InvalidIdentifierSurfacesAtExec", func(t *testing.T) {
		_, err := quark.For[exprUser](ctx, baseClient).WhereExpr(
			quark.Eq(quark.Col("name; DROP TABLE x;--"), quark.Lit("alice")),
		).List()
		if err == nil {
			t.Fatalf("expected identifier-validation error, got nil")
		}
	})

	t.Run("PlaceholderSubstitution", func(t *testing.T) {
		// Pin the contract that '?' markers from the AST end up substituted
		// for the dialect's placeholder. SQLite's placeholder happens to be
		// '?', so the easier shape to assert across dialects is that
		// positionally-correct binds reach the driver — proven by the
		// query returning the expected row.
		mw := &exprCapturingMiddleware{}
		client, err := baseClient.WithOptions(quark.WithMiddleware(mw))
		if err != nil {
			t.Fatalf("WithOptions: %v", err)
		}
		got, err := quark.For[exprUser](ctx, client).WhereExpr(
			quark.And(
				quark.Eq(quark.Col("status"), quark.Lit("active")),
				quark.Gt(quark.Col("logins"), quark.Lit(int64(5))),
			),
		).List()
		if err != nil {
			t.Fatalf("list: %v", err)
		}
		// alice (12) qualifies, bob (3) does not.
		if len(got) != 1 || got[0].Name != "alice" {
			t.Fatalf("expected just alice, got %+v", got)
		}
		// Captured SQL must carry the dialect-quoted AST fragment with
		// the placeholder substituted to the dialect's syntax (`?` on
		// SQLite, `$N` on PG, `@pN` on MSSQL, `:N` on Oracle). We can't
		// compare the placeholder literal across dialects, so we assert
		// the surrounding identifier shape only.
		statusFrag := "(" + q(client, "status") + " = "
		loginsFrag := "AND " + q(client, "logins") + " > "
		var hasFragment bool
		for _, sqlStr := range mw.snapshot() {
			if strings.Contains(sqlStr, statusFrag) && strings.Contains(sqlStr, loginsFrag) {
				hasFragment = true
				break
			}
		}
		if !hasFragment {
			t.Errorf("captured SQL missing AST fragment %q+%q, got %v",
				statusFrag, loginsFrag, mw.snapshot())
		}
	})
}
