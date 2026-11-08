// Copyright 2026 jcsvwinston
// SPDX-License-Identifier: Apache-2.0

package render

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/jcsvwinston/quark"
)

// A preset is a fixed query the form cannot express with its fields: one
// per family of the query bench that the fluent API renders the same way on
// every engine. Each preset is a generic function over the model types it
// reads, so the WebAssembly build can run it through the row shape (see
// fromTable) while TestPresetsRenderTheSameSQLAsTheModelTypes runs the same
// function over the real types and compares the SQL.
//
// The Go text is written by hand next to the function that runs. Keep the
// two in step: the text is what the reader learns from.
type preset struct {
	PresetID
	code string
	run  func(ctx context.Context, c *quark.Client) error
}

var since = time.Date(2026, time.January, 1, 0, 0, 0, 0, time.UTC)

func joinPreset[O any](ctx context.Context, c *quark.Client, orders base[O]) error {
	_, err := orders(ctx, c).
		Select("orders.id", "orders.total", "users.email").
		Join("users").On("users.id", "=", "orders.user_id").
		Where("users.country", "=", "ES").
		OrderBy("orders.placed_at", "DESC").
		Limit(10).
		List()
	return err
}

func groupPreset[O any](ctx context.Context, c *quark.Client, orders base[O]) error {
	_, err := orders(ctx, c).
		Select("status").
		SelectExpr("orders", quark.Func("COUNT", quark.Col("*"))).
		SelectExpr("revenue", quark.Func("SUM", quark.Col("total"))).
		GroupBy("status").
		HavingAggregate("COUNT", "*", ">", 5).
		OrderBy("status", "ASC").
		Limit(10).
		List()
	return err
}

func countPreset[U any](ctx context.Context, c *quark.Client, users base[U]) error {
	_, err := users(ctx, c).
		Where("country", "=", "ES").
		Where("created_at", ">=", since).
		Count()
	return err
}

func existsPreset[O, U any](ctx context.Context, c *quark.Client, orders base[O], users base[U]) error {
	paid, err := orders(ctx, c).
		Select("id").
		WhereExpr(quark.Eq(quark.Col("orders.user_id"), quark.Col("users.id"))).
		Where("status", "=", "paid").
		AsSubquery()
	if err != nil {
		return err
	}
	_, err = users(ctx, c).
		WhereExpr(quark.Exists(paid)).
		OrderBy("id", "ASC").
		Limit(10).
		List()
	return err
}

func windowPreset[O any](ctx context.Context, c *quark.Client, orders base[O]) error {
	_, err := orders(ctx, c).
		Select("id", "status", "total").
		SelectExpr("rank_in_status", quark.Over(quark.Rank(),
			quark.NewWindow().
				PartitionBy(quark.Col("status")).
				OrderBy(quark.Col("total"), true))).
		Limit(10).
		List()
	return err
}

func unionPreset[U any](ctx context.Context, c *quark.Client, users base[U]) error {
	es := users(ctx, c).Where("country", "=", "ES")
	pt := users(ctx, c).Where("country", "=", "PT")
	_, err := es.Union(pt).
		OrderBy("id", "ASC").
		Limit(10).
		List()
	return err
}

func ctePreset[O any](ctx context.Context, c *quark.Client, orders base[O]) error {
	big, err := orders(ctx, c).
		Where("total", ">", 100.0).
		AsSubquery()
	if err != nil {
		return err
	}
	_, err = orders(ctx, c).
		With("big_orders", big).
		FromCTE("big_orders").
		OrderBy("placed_at", "DESC").
		Limit(10).
		List()
	return err
}

// The bases the WebAssembly build renders the presets through.
var (
	ordersRow = fromTable[row]("orders")
	usersReal = model[User]()
)

var presets = []preset{
	{
		PresetID: PresetID{
			ID: "join", Label: "Join: orders with their buyer's e-mail", Family: "joins",
			Description: "An INNER JOIN whose ON clause is three identifiers. The filter on the joined table uses a qualified column.",
		},
		code: `quark.For[Order](ctx, client).
	Select("orders.id", "orders.total", "users.email").
	Join("users").On("users.id", "=", "orders.user_id").
	Where("users.country", "=", "ES").
	OrderBy("orders.placed_at", "DESC").
	Limit(10).
	List()`,
		run: func(ctx context.Context, c *quark.Client) error { return joinPreset(ctx, c, ordersRow) },
	},
	{
		PresetID: PresetID{
			ID: "group", Label: "Group: orders and revenue per status", Family: "aggregation",
			Description: "GROUP BY with a projection, two aggregates and a HAVING over COUNT(*). The projection is what makes it run on every engine.",
		},
		code: `quark.For[Order](ctx, client).
	Select("status").
	SelectExpr("orders", quark.Func("COUNT", quark.Col("*"))).
	SelectExpr("revenue", quark.Func("SUM", quark.Col("total"))).
	GroupBy("status").
	HavingAggregate("COUNT", "*", ">", 5).
	OrderBy("status", "ASC").
	Limit(10).
	List()`,
		run: func(ctx context.Context, c *quark.Client) error { return groupPreset(ctx, c, ordersRow) },
	},
	{
		PresetID: PresetID{
			ID: "count", Label: "Count: users from a country since a date", Family: "filtering",
			Description: "Count() renders SELECT COUNT(*) with the WHERE a List() would use, soft-delete predicate included.",
		},
		code: `quark.For[User](ctx, client).
	Where("country", "=", "ES").
	Where("created_at", ">=", time.Date(2026, time.January, 1, 0, 0, 0, 0, time.UTC)).
	Count()`,
		run: func(ctx context.Context, c *quark.Client) error { return countPreset(ctx, c, usersReal) },
	},
	{
		PresetID: PresetID{
			ID: "exists", Label: "Subquery: users with at least one paid order", Family: "subquery",
			Description: "A correlated EXISTS. The inner query is captured with AsSubquery and embedded through the expression API; its argument is renumbered for the outer statement.",
		},
		code: `paid, err := quark.For[Order](ctx, client).
	Select("id").
	WhereExpr(quark.Eq(quark.Col("orders.user_id"), quark.Col("users.id"))).
	Where("status", "=", "paid").
	AsSubquery()

quark.For[User](ctx, client).
	WhereExpr(quark.Exists(paid)).
	OrderBy("id", "ASC").
	Limit(10).
	List()`,
		run: func(ctx context.Context, c *quark.Client) error { return existsPreset(ctx, c, ordersRow, usersReal) },
	},
	{
		PresetID: PresetID{
			ID: "window", Label: "Window: rank of each order inside its status", Family: "window",
			Description: "RANK() OVER (PARTITION BY … ORDER BY …) projected next to plain columns.",
		},
		code: `quark.For[Order](ctx, client).
	Select("id", "status", "total").
	SelectExpr("rank_in_status", quark.Over(quark.Rank(),
		quark.NewWindow().
			PartitionBy(quark.Col("status")).
			OrderBy(quark.Col("total"), true))).
	Limit(10).
	List()`,
		run: func(ctx context.Context, c *quark.Client) error { return windowPreset(ctx, c, ordersRow) },
	},
	{
		PresetID: PresetID{
			ID: "union", Label: "Set operation: users from two countries", Family: "setop",
			Description: "UNION of two queries over the same model; ORDER BY and LIMIT apply to the combined result, and the soft-delete predicate to each side.",
		},
		code: `es := quark.For[User](ctx, client).Where("country", "=", "ES")
pt := quark.For[User](ctx, client).Where("country", "=", "PT")

es.Union(pt).
	OrderBy("id", "ASC").
	Limit(10).
	List()`,
		run: func(ctx context.Context, c *quark.Client) error { return unionPreset(ctx, c, usersReal) },
	},
	{
		PresetID: PresetID{
			ID: "cte", Label: "CTE: orders read through a named subquery", Family: "cte",
			Description: "WITH … AS (…) followed by a read from the common table expression instead of the model's table.",
		},
		code: `big, err := quark.For[Order](ctx, client).
	Where("total", ">", 100.0).
	AsSubquery()

quark.For[Order](ctx, client).
	With("big_orders", big).
	FromCTE("big_orders").
	OrderBy("placed_at", "DESC").
	Limit(10).
	List()`,
		run: func(ctx context.Context, c *quark.Client) error { return ctePreset(ctx, c, ordersRow) },
	},
}

func findPreset(id string) (preset, bool) {
	for _, p := range presets {
		if p.ID == id {
			return p, true
		}
	}
	return preset{}, false
}

func presetIDs() []string {
	ids := make([]string, len(presets))
	for i, p := range presets {
		ids[i] = p.ID
	}
	return ids
}

func (r *Renderer) renderPreset(id string) (string, []DialectSQL, error) {
	p, ok := findPreset(id)
	if !ok {
		return "", nil, fmt.Errorf("unknown preset %q; the catalog has %s", id, strings.Join(presetIDs(), ", "))
	}
	out, err := r.capture(p.run)
	return p.code, out, err
}

// capture runs fn against every dialect's recording client and collects
// the statement each one received.
func (r *Renderer) capture(fn func(ctx context.Context, c *quark.Client) error) ([]DialectSQL, error) {
	out := make([]DialectSQL, 0, len(r.targets))
	for _, t := range r.targets {
		t.rec.reset()
		if err := fn(context.Background(), t.client); err != nil {
			return nil, fmt.Errorf("%s: %w", t.dialect.Name(), err)
		}
		sqlText, args := t.rec.last()
		out = append(out, DialectSQL{Name: t.dialect.Name(), SQL: sqlText, Args: jsonArgs(args)})
	}
	return out, nil
}
