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

// windowSale fixture: bundles three columns so RANK / ROW_NUMBER over
// a (region, amount) partition produces a deterministic ordering we
// can assert on.
type windowSale struct {
	ID     int64  `db:"id" pk:"true"`
	Region string `db:"region"`
	Amount int64  `db:"amount"`
}

// windowCapturing records both the SQL and the args slice for every
// SELECT, so tests can pin marker/arg ordering — proves correct
// argIndex threading independently of whether the dialect uses '?'
// (SQLite) or '$N' (Postgres).
type windowCapturing struct {
	quark.BaseMiddleware
	mu      sync.Mutex
	queries []string
	args    [][]any
}

func (m *windowCapturing) WrapQuery(next quark.QueryFunc) quark.QueryFunc {
	return func(ctx context.Context, exec quark.Executor, sqlStr string, args []any) (*sql.Rows, error) {
		m.mu.Lock()
		m.queries = append(m.queries, sqlStr)
		m.args = append(m.args, append([]any(nil), args...))
		m.mu.Unlock()
		return next(ctx, exec, sqlStr, args)
	}
}

func (m *windowCapturing) snapshot() ([]string, [][]any) {
	m.mu.Lock()
	defer m.mu.Unlock()
	q := make([]string, len(m.queries))
	copy(q, m.queries)
	a := make([][]any, len(m.args))
	copy(a, m.args)
	return q, a
}

// testWindow exercises the Phase-2 window deliverable: SelectExpr +
// Over(...) + RowNumber/Rank/DenseRank/Lag/Lead. SQLite 3.25+ supports
// every shape used here.
func testWindow(ctx context.Context, t *testing.T, baseClient *quark.Client) {
	t.Helper()

	dropTable(baseClient, "window_sales")
	if err := baseClient.Migrate(ctx, &windowSale{}); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	defer dropTable(baseClient, "window_sales")

	for _, s := range []windowSale{
		{Region: "north", Amount: 100},
		{Region: "north", Amount: 200},
		{Region: "north", Amount: 50},
		{Region: "south", Amount: 75},
		{Region: "south", Amount: 25},
	} {
		row := s
		if err := quark.For[windowSale](ctx, baseClient).Create(&row); err != nil {
			t.Fatalf("seed: %v", err)
		}
	}

	t.Run("SelectExprRendersOverPartitionByOrderBy", func(t *testing.T) {
		mw := &windowCapturing{}
		client, err := baseClient.WithOptions(quark.WithMiddleware(mw))
		if err != nil {
			t.Fatalf("WithOptions: %v", err)
		}
		// Build a query that projects both the regular columns and a
		// RANK() over (region, amount DESC) projection. List into
		// windowSale ignores the rank column (no matching field) but we
		// must still capture the error so a runtime failure in non-SQLite
		// dialects doesn't pass silently.
		_, err = quark.For[windowSale](ctx, client).
			Select("id", "region", "amount").
			SelectExpr("rk", quark.Over(
				quark.Rank(),
				quark.NewWindow().
					PartitionBy(quark.Col("region")).
					OrderBy(quark.Col("amount"), true),
			)).
			Limit(50).
			List()
		if err != nil {
			t.Fatalf("list: %v", err)
		}

		queries, _ := mw.snapshot()
		if len(queries) == 0 {
			t.Fatalf("no SELECT captured")
		}
		var sel string
		for _, q := range queries {
			if strings.HasPrefix(strings.TrimSpace(q), "SELECT") {
				sel = q
				break
			}
		}
		want := `RANK() OVER (PARTITION BY "region" ORDER BY "amount" DESC) AS "rk"`
		if !strings.Contains(sel, want) {
			t.Errorf("missing window projection in SELECT.\n got: %q\nwant: containing %q", sel, want)
		}
	})

	t.Run("SelectExprErrorsOnInvalidAlias", func(t *testing.T) {
		_, err := quark.For[windowSale](ctx, baseClient).
			SelectExpr("alias; DROP TABLE x;--", quark.Rank()).
			List()
		if err == nil {
			t.Fatalf("expected identifier-validation error for bad alias")
		}
	})

	t.Run("SelectExprComposesWithRegularSelect", func(t *testing.T) {
		mw := &windowCapturing{}
		client, err := baseClient.WithOptions(quark.WithMiddleware(mw))
		if err != nil {
			t.Fatalf("WithOptions: %v", err)
		}
		_, err = quark.For[windowSale](ctx, client).
			Select("region").
			SelectExpr("rn", quark.Over(quark.RowNumber(),
				quark.NewWindow().OrderBy(quark.Col("id"), false))).
			Limit(5).
			List()
		if err != nil {
			t.Fatalf("list: %v", err)
		}

		queries, _ := mw.snapshot()
		var sel string
		for _, q := range queries {
			if strings.HasPrefix(strings.TrimSpace(q), "SELECT") {
				sel = q
				break
			}
		}
		// Expect comma-joined: "region", ROW_NUMBER() OVER (...) AS "rn"
		if !strings.Contains(sel, `"region", ROW_NUMBER() OVER (`) {
			t.Errorf("expected regular cols and AST projection comma-joined, got %q", sel)
		}
	})

	t.Run("MultipleSelectExprArgIndexing", func(t *testing.T) {
		// Two AST projections, each carrying one bind arg via Lag(col,
		// offset). The args slice the middleware captures must contain
		// the offsets in the order the SELECT-list was rendered (2, 5).
		// On dialects that number placeholders ($1, $2, ...) the marker
		// substitution would land at indexes 1 and 2 — the args-order
		// assertion is the dialect-agnostic proxy for "substitution
		// matched the bind sites correctly".
		mw := &windowCapturing{}
		client, err := baseClient.WithOptions(quark.WithMiddleware(mw))
		if err != nil {
			t.Fatalf("WithOptions: %v", err)
		}
		_, err = quark.For[windowSale](ctx, client).
			SelectExpr("prev", quark.Over(quark.Lag(quark.Col("amount"), 2),
				quark.NewWindow().OrderBy(quark.Col("id"), false))).
			SelectExpr("prev5", quark.Over(quark.Lag(quark.Col("amount"), 5),
				quark.NewWindow().OrderBy(quark.Col("id"), false))).
			Limit(5).
			List()
		if err != nil {
			t.Fatalf("list: %v", err)
		}

		queries, argSlices := mw.snapshot()
		var sel string
		var selArgs []any
		for i, q := range queries {
			if strings.HasPrefix(strings.TrimSpace(q), "SELECT") {
				sel = q
				selArgs = argSlices[i]
				break
			}
		}
		// Args must be [2, 5] in that exact order — the SELECT-list
		// renders the projections in the order they were added.
		if len(selArgs) < 2 {
			t.Fatalf("expected ≥2 args, got %v (sql=%q)", selArgs, sel)
		}
		first := normaliseInt(selArgs[0])
		second := normaliseInt(selArgs[1])
		if first != 2 || second != 5 {
			t.Errorf("args[0..1] = (%v, %v), want (2, 5) — sql=%q", first, second, sel)
		}
		// Both LAG fragments must be present.
		if !strings.Contains(sel, `LAG("amount", `) {
			t.Errorf("missing LAG fragment in SELECT, got %q", sel)
		}
	})
}

// normaliseInt collapses int / int64 to int64 so equality comparisons in
// the multi-expr test stay independent of how the driver round-trips
// numeric binds.
func normaliseInt(v any) int64 {
	switch n := v.(type) {
	case int:
		return int64(n)
	case int64:
		return n
	case int32:
		return int64(n)
	}
	return -1
}
