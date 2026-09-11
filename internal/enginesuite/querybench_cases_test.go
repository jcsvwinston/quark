// Copyright 2026 jcsvwinston
// SPDX-License-Identifier: Apache-2.0

package enginesuite

import (
	"context"
	"fmt"
	"time"

	"github.com/jcsvwinston/quark"
)

// qbVerdict is what the measurement found for one case.
type qbVerdict int

const (
	// qbTyped: the typed API expresses it and the emitted SQL does what the
	// case asked for.
	qbTyped qbVerdict = iota
	// qbWrong: it compiles and runs, but the SQL does something else. These
	// are the dangerous ones — no error tells the caller.
	qbWrong
	// qbNoAPI: there is no way to express it. Either it does not compile, or
	// it is rejected at run time on every engine.
	qbNoAPI
)

func (v qbVerdict) String() string {
	switch v {
	case qbTyped:
		return "typed"
	case qbWrong:
		return "wrong-sql"
	default:
		return "no-api"
	}
}

// qbCase is one query of the bench.
//
// Verdict records the CURRENT measurement. The test asserts the verdict
// rather than asserting success, so closing a gap is a failing test that says
// "this one passes now, update the verdict" instead of a silent improvement
// nobody records.
//
// Run is nil when the API has no spelling for the case at all — that is the
// measurement, and there is nothing to execute.
type qbCase struct {
	ID      string
	Family  string
	Want    string // the SQL shape the case asks for
	Verdict qbVerdict
	Note    string // why not, or what the emitted SQL does instead
	Run     func(ctx context.Context, c *quark.Client) error
}

func qbCases() []qbCase {
	return concatCases(qbFamilyA(), qbFamilyB(), qbFamilyC(), qbFamilyD(), qbFamilyE(),
		qbFamilyF(), qbFamilyG(), qbFamilyH(), qbFamilyI(), qbFamilyJ())
}

func concatCases(groups ...[]qbCase) []qbCase {
	var out []qbCase
	for _, g := range groups {
		out = append(out, g...)
	}
	return out
}

func qbFamilyA() []qbCase {
	return []qbCase{
		{"Q01", "filtering", "WHERE col = ?", qbTyped, "", func(ctx context.Context, c *quark.Client) error {
			_, err := quark.For[qbUser](ctx, c).Where("country", "=", "ES").Limit(10).List()
			return err
		}},
		{"Q02", "filtering", "WHERE col BETWEEN ? AND ? ORDER BY ... LIMIT", qbTyped, "", func(ctx context.Context, c *quark.Client) error {
			_, err := quark.For[qbProduct](ctx, c).WhereBetween("price", 10, 100).OrderBy("price", "DESC").Limit(20).List()
			return err
		}},
		{"Q03", "filtering", "WHERE col IN (...)", qbTyped, "", func(ctx context.Context, c *quark.Client) error {
			_, err := quark.For[qbUser](ctx, c).WhereIn("country", []any{"ES", "FR"}).Limit(10).List()
			return err
		}},
		{"Q04", "filtering", "WHERE a = ? OR b > ?", qbTyped, "", func(ctx context.Context, c *quark.Client) error {
			_, err := quark.For[qbOrder](ctx, c).Where("status", "=", "paid").
				Or(func(q *quark.Query[qbOrder]) *quark.Query[qbOrder] { return q.Where("total", ">", 1000) }).Limit(10).List()
			return err
		}},
		{"Q05", "filtering", "WHERE a AND (b OR c) — nested boolean tree", qbTyped, "", func(ctx context.Context, c *quark.Client) error {
			_, err := quark.For[qbProduct](ctx, c).WhereExpr(quark.And(
				quark.Eq(quark.Col("active"), quark.Lit(true)),
				quark.Or(quark.Gt(quark.Col("stock"), quark.Lit(0)), quark.Lt(quark.Col("price"), quark.Lit(5.0))),
			)).Limit(10).List()
			return err
		}},
		{"Q06", "filtering", "WHERE col IS NULL", qbTyped,
			`the operator is the string "IS NULL"; passing "IS" with a nil value emits "IS ?", which is a syntax error outside SQLite`,
			func(ctx context.Context, c *quark.Client) error {
				_, err := quark.For[qbUser](ctx, c).Where("country", "IS NULL", nil).Limit(10).List()
				return err
			}},
		{"Q07", "filtering", "LIMIT/OFFSET pagination with total count", qbTyped, "", func(ctx context.Context, c *quark.Client) error {
			_, err := quark.For[qbOrder](ctx, c).OrderBy("placed_at", "DESC").Paginate(50, 1)
			return err
		}},
		{"Q08", "filtering", "keyset (cursor) pagination", qbTyped, "", func(ctx context.Context, c *quark.Client) error {
			_, err := quark.For[qbOrder](ctx, c).OrderBy("id", "ASC").Cursor()
			return err
		}},
		{"Q09", "filtering", "SELECT DISTINCT col", qbTyped, "", func(ctx context.Context, c *quark.Client) error {
			_, err := quark.For[qbProduct](ctx, c).Select("category_id").Distinct().Limit(10).List()
			return err
		}},
		{"Q10", "filtering", "soft-delete scope (only trashed)", qbTyped, "", func(ctx context.Context, c *quark.Client) error {
			_, err := quark.For[qbUser](ctx, c).OnlyTrashed().Where("created_at", "<", time.Now()).Limit(10).List()
			return err
		}},
	}
}

func qbFamilyB() []qbCase {
	return []qbCase{
		{"Q11", "joins", "INNER JOIN with a predicate on the joined table", qbTyped, "", func(ctx context.Context, c *quark.Client) error {
			_, err := quark.For[qbOrder](ctx, c).Join("qb_users").On("qb_orders.user_id", "=", "qb_users.id").
				Where("qb_users.country", "=", "ES").Limit(10).List()
			return err
		}},
		{"Q12", "joins", "LEFT JOIN", qbTyped, "", func(ctx context.Context, c *quark.Client) error {
			_, err := quark.For[qbOrder](ctx, c).LeftJoin("qb_order_items").On("qb_orders.id", "=", "qb_order_items.order_id").Limit(10).List()
			return err
		}},
		{"Q13", "joins", "three-table join chain", qbTyped, "", func(ctx context.Context, c *quark.Client) error {
			_, err := quark.For[qbOrder](ctx, c).
				Join("qb_order_items").On("qb_orders.id", "=", "qb_order_items.order_id").
				Join("qb_products").On("qb_order_items.product_id", "=", "qb_products.id").
				Where("qb_products.active", "=", true).Limit(10).List()
			return err
		}},
		{"Q14", "joins", "JOIN ... ON a = b AND c > 1 (literal in the ON clause)", qbTyped,
			`OnExpr, added by S3. On and OnRaw take caller strings, so they are held to an identifier-only grammar; an AST clause binds its literals and is not subject to it`,
			func(ctx context.Context, c *quark.Client) error {
				_, err := quark.For[qbOrder](ctx, c).LeftJoin("qb_order_items").
					OnExpr(quark.And(
						quark.Eq(quark.Col("qb_orders.id"), quark.Col("qb_order_items.order_id")),
						quark.Gt(quark.Col("qb_order_items.qty"), quark.Lit(1)),
					)).Limit(10).List()
				return err
			}},
		{"Q15", "joins", "eager-load a has_many relation", qbTyped, "", func(ctx context.Context, c *quark.Client) error {
			_, err := quark.For[qbUser](ctx, c).Preload("Orders").Limit(10).List()
			return err
		}},
		{"Q16", "joins", "eager-load a relation FILTERED (only paid orders)", qbTyped,
			`PreloadWhere, added by S3. It narrows which children load without dropping parents that end up with none`,
			func(ctx context.Context, c *quark.Client) error {
				users, err := quark.For[qbUser](ctx, c).
					PreloadWhere("Orders", "status", "=", "paid").Limit(10).List()
				if err != nil {
					return err
				}
				for _, u := range users {
					for _, o := range u.Orders {
						if o.Status != "paid" {
							return fmt.Errorf("preload filter ignored: loaded order %d with status %q", o.ID, o.Status)
						}
					}
				}
				return nil
			}},
		{"Q17", "joins", "project a join onto a DTO that is not a model", qbTyped,
			`FromTable, added by S3: For[T] takes both the row shape and the source table from T, so a DTO needs to name its source`,
			func(ctx context.Context, c *quark.Client) error {
				rows, err := quark.For[qbOrderEmail](ctx, c).
					FromTable("qb_orders").
					Join("qb_users").On("qb_orders.user_id", "=", "qb_users.id").
					SelectExpr("order_id", quark.Col("qb_orders.id")).
					SelectExpr("email", quark.Col("qb_users.email")).Limit(10).List()
				if err != nil {
					return err
				}
				if len(rows) == 0 || rows[0].Email == "" {
					return fmt.Errorf("projection returned %d rows with empty email — the DTO did not scan", len(rows))
				}
				return nil
			}},
		{"Q18", "joins", "FULL OUTER JOIN / CROSS JOIN", qbTyped,
			`FullJoin and CrossJoin, added by S3. FULL OUTER is not implemented by MySQL or MariaDB and Quark does not rewrite it silently, so the bench exercises CrossJoin, which all six have`,
			func(ctx context.Context, c *quark.Client) error {
				_, err := quark.For[qbOrder](ctx, c).CrossJoin("qb_users").Limit(10).List()
				return err
			}},
	}
}

func qbFamilyC() []qbCase {
	return []qbCase{
		{"Q19", "aggregation", "COUNT(*)", qbTyped, "", func(ctx context.Context, c *quark.Client) error {
			_, err := quark.For[qbOrder](ctx, c).Count()
			return err
		}},
		{"Q20", "aggregation", "SUM / AVG / MIN / MAX over a column", qbTyped, "", func(ctx context.Context, c *quark.Client) error {
			if _, err := quark.For[qbOrder](ctx, c).Sum("total"); err != nil {
				return err
			}
			if _, err := quark.For[qbOrder](ctx, c).Avg("total"); err != nil {
				return err
			}
			_, err := quark.For[qbOrder](ctx, c).Max("total")
			return err
		}},
		{"Q21", "aggregation", "GROUP BY col with an aggregate in the projection", qbTyped, "", func(ctx context.Context, c *quark.Client) error {
			_, err := quark.For[qbOrder](ctx, c).GroupBy("status").
				SelectExpr("n", quark.Func("COUNT", quark.Col("*"))).Limit(10).List()
			return err
		}},
		{"Q22", "aggregation", "GROUP BY ... HAVING COUNT(*) > ?", qbWrong,
			`the HAVING is right, but no Select() means the projection stays "SELECT *", which is invalid with GROUP BY on every engine except SQLite and MySQL in its permissive mode. S3 made it WARN; making it an ERROR breaks callers on the permissive engines, so it waits for the major QADR-0010 collects breaking changes into. Adding Select() makes the query correct today`,
			func(ctx context.Context, c *quark.Client) error {
				_, err := quark.For[qbOrder](ctx, c).GroupBy("user_id").
					HavingAggregate("COUNT", "*", ">", 3).Limit(10).List()
				return err
			}},
		{"Q23", "aggregation", "GROUP BY ... HAVING SUM(col) > ?", qbWrong,
			`same "SELECT *" with GROUP BY as Q22, and the same WARN`,
			func(ctx context.Context, c *quark.Client) error {
				_, err := quark.For[qbOrder](ctx, c).GroupBy("user_id").
					HavingExpr(quark.Gt(quark.Func("SUM", quark.Col("total")), quark.Lit(1000))).Limit(10).List()
				return err
			}},
		{"Q24", "aggregation", "COUNT(DISTINCT col)", qbTyped,
			`CountDistinct, added by S1. DISTINCT is a modifier on the argument, not a function, so it got a constructor rather than a whitelist entry`,
			func(ctx context.Context, c *quark.Client) error {
				_, err := quark.For[qbOrder](ctx, c).
					SelectExpr("n", quark.CountDistinct(quark.Col("user_id"))).Limit(10).List()
				return err
			}},
		{"Q25", "aggregation", "conditional aggregate: SUM(CASE WHEN ... THEN 1 ELSE 0 END)", qbTyped,
			`Case(), added by S1. CASE is an expression form with its own grammar, so no arity of Func(name, args...) could render it`,
			func(ctx context.Context, c *quark.Client) error {
				_, err := quark.For[qbOrder](ctx, c).GroupBy("user_id").SelectExpr("paid",
					quark.Func("SUM", quark.Case().
						When(quark.Eq(quark.Col("status"), quark.Lit("paid")), quark.Lit(1)).
						Else(quark.Lit(0)))).Limit(10).List()
				return err
			}},
		{"Q26", "aggregation", "GROUP BY two columns, ordered by the aggregate", qbTyped, "", func(ctx context.Context, c *quark.Client) error {
			_, err := quark.For[qbOrder](ctx, c).GroupBy("user_id", "status").
				SelectExpr("n", quark.Func("COUNT", quark.Col("*"))).OrderBy("n", "DESC").Limit(10).List()
			return err
		}},
	}
}

func qbFamilyD() []qbCase {
	return []qbCase{
		{"Q27", "subquery", "WHERE col IN (SELECT ...)", qbTyped, "", func(ctx context.Context, c *quark.Client) error {
			sub, err := quark.For[qbOrder](ctx, c).Where("total", ">", 500).Select("user_id").AsSubquery()
			if err != nil {
				return err
			}
			_, err = quark.For[qbUser](ctx, c).WhereExpr(quark.InSub(quark.Col("id"), sub)).Limit(10).List()
			return err
		}},
		{"Q28", "subquery", "WHERE col NOT IN (SELECT ...)", qbTyped, "", func(ctx context.Context, c *quark.Client) error {
			sub, err := quark.For[qbOrder](ctx, c).Select("user_id").AsSubquery()
			if err != nil {
				return err
			}
			_, err = quark.For[qbUser](ctx, c).WhereExpr(quark.NotInSub(quark.Col("id"), sub)).Limit(10).List()
			return err
		}},
		{"Q29", "subquery", "WHERE EXISTS (correlated subquery)", qbTyped,
			`the correlation has to be written WhereExpr(Eq(Col, Col)); Where("a", "=", Col("b")) is rejected because the value side is validated as a literal`,
			func(ctx context.Context, c *quark.Client) error {
				sub, err := quark.For[qbOrder](ctx, c).
					WhereExpr(quark.Eq(quark.Col("qb_orders.user_id"), quark.Col("qb_users.id"))).AsSubquery()
				if err != nil {
					return err
				}
				_, err = quark.For[qbUser](ctx, c).WhereExpr(quark.Exists(sub)).Limit(10).List()
				return err
			}},
		{"Q30", "subquery", "WHERE NOT EXISTS (correlated)", qbTyped, "", func(ctx context.Context, c *quark.Client) error {
			sub, err := quark.For[qbOrder](ctx, c).
				WhereExpr(quark.Eq(quark.Col("qb_orders.user_id"), quark.Col("qb_users.id"))).AsSubquery()
			if err != nil {
				return err
			}
			_, err = quark.For[qbUser](ctx, c).WhereExpr(quark.NotExists(sub)).Limit(10).List()
			return err
		}},
		{"Q31", "subquery", "correlated scalar subquery in the projection", qbTyped, "", func(ctx context.Context, c *quark.Client) error {
			sub, err := quark.For[qbOrder](ctx, c).SelectExpr("n", quark.Func("COUNT", quark.Col("*"))).
				WhereExpr(quark.Eq(quark.Col("qb_orders.user_id"), quark.Col("qb_users.id"))).AsSubquery()
			if err != nil {
				return err
			}
			_, err = quark.For[qbUser](ctx, c).SelectExpr("order_count", quark.Sub(sub)).Limit(10).List()
			return err
		}},
		{"Q32", "subquery", "compare against a scalar subquery (col > (SELECT AVG(...)))", qbTyped, "", func(ctx context.Context, c *quark.Client) error {
			sub, err := quark.For[qbOrder](ctx, c).SelectExpr("a", quark.Func("AVG", quark.Col("total"))).AsSubquery()
			if err != nil {
				return err
			}
			_, err = quark.For[qbOrder](ctx, c).WhereExpr(quark.Gt(quark.Col("total"), quark.Sub(sub))).Limit(10).List()
			return err
		}},
		{"Q33", "subquery", "derived table in FROM: SELECT ... FROM (SELECT ...) t", qbTyped,
			`FromCTE, added by S2. With() alone declares the CTE and leaves the FROM on the base table — reachable by joining it, but not usable as the only source until FromCTE`,
			func(ctx context.Context, c *quark.Client) error {
				sub, err := quark.For[qbOrder](ctx, c).Where("status", "=", "paid").AsSubquery()
				if err != nil {
					return err
				}
				_, err = quark.For[qbOrder](ctx, c).With("paid", sub).FromCTE("paid").Limit(10).List()
				return err
			}},
	}
}

func qbFamilyE() []qbCase {
	return []qbCase{
		{"Q34", "cte", "WITH t AS (SELECT ...) — one CTE", qbTyped,
			`the CTE is emitted; reaching it needs an explicit Join (Q38), see Q33`,
			func(ctx context.Context, c *quark.Client) error {
				sub, err := quark.For[qbOrder](ctx, c).Where("status", "=", "paid").AsSubquery()
				if err != nil {
					return err
				}
				_, err = quark.For[qbUser](ctx, c).With("paid", sub).
					Join("paid").On("qb_users.id", "=", "paid.user_id").Limit(10).List()
				return err
			}},
		{"Q35", "cte", "two CTEs in one statement", qbTyped, "", func(ctx context.Context, c *quark.Client) error {
			a, err := quark.For[qbOrder](ctx, c).Where("status", "=", "paid").AsSubquery()
			if err != nil {
				return err
			}
			b, err := quark.For[qbOrder](ctx, c).Where("status", "=", "shipped").AsSubquery()
			if err != nil {
				return err
			}
			_, err = quark.For[qbOrder](ctx, c).With("paid", a).With("shipped", b).Limit(10).List()
			return err
		}},
		{"Q36", "cte", "WITH RECURSIVE — walk a category tree", qbTyped,
			`the body is anchor UNION ALL step, composed with UnionAll and captured by AsSubquery. S0 recorded this as impossible by writing the case wrong — with a single-SELECT body — and the comment in cte.go said the same; both were stale`,
			func(ctx context.Context, c *quark.Client) error {
				anchor := quark.For[qbCategory](ctx, c).Where("parent_id", "IS NULL", nil)
				step := quark.For[qbCategory](ctx, c).
					Join("tree").On("qb_categories.parent_id", "=", "tree.id")
				body, err := anchor.UnionAll(step).AsSubquery()
				if err != nil {
					return err
				}
				_, err = quark.For[qbCategory](ctx, c).WithRecursive("tree", body).
					FromCTE("tree").Limit(10).List()
				return err
			}},
		{"Q37", "cte", "recursive CTE with an explicit UNION ALL body", qbTyped,
			`same composition as Q36, verified to actually recurse: a four-level tree comes back with all four rows`,
			func(ctx context.Context, c *quark.Client) error {
				anchor := quark.For[qbCategory](ctx, c).Where("id", "=", 1)
				step := quark.For[qbCategory](ctx, c).
					Join("tree").On("qb_categories.parent_id", "=", "tree.id")
				body, err := anchor.UnionAll(step).AsSubquery()
				if err != nil {
					return err
				}
				rows, err := quark.For[qbCategory](ctx, c).WithRecursive("tree", body).
					FromCTE("tree").Limit(50).List()
				if err != nil {
					return err
				}
				if len(rows) < 4 {
					return fmt.Errorf("recursive CTE walked %d rows, want the whole 4-level tree", len(rows))
				}
				return nil
			}},
		{"Q38", "cte", "CTE referenced from a JOIN", qbTyped, "", func(ctx context.Context, c *quark.Client) error {
			sub, err := quark.For[qbOrder](ctx, c).Where("status", "=", "paid").AsSubquery()
			if err != nil {
				return err
			}
			_, err = quark.For[qbUser](ctx, c).With("paid", sub).
				Join("paid").On("qb_users.id", "=", "paid.user_id").Limit(10).List()
			return err
		}},
	}
}

func qbFamilyF() []qbCase {
	return []qbCase{
		{"Q39", "window", "ROW_NUMBER() OVER (PARTITION BY ... ORDER BY ...)", qbTyped, "", func(ctx context.Context, c *quark.Client) error {
			w := quark.NewWindow().PartitionBy(quark.Col("user_id")).OrderBy(quark.Col("placed_at"), true)
			_, err := quark.For[qbOrder](ctx, c).SelectExpr("rn", quark.Over(quark.RowNumber(), w)).Limit(10).List()
			return err
		}},
		{"Q40", "window", "RANK() and DENSE_RANK()", qbTyped, "", func(ctx context.Context, c *quark.Client) error {
			w := quark.NewWindow().PartitionBy(quark.Col("status")).OrderBy(quark.Col("total"), true)
			_, err := quark.For[qbOrder](ctx, c).
				SelectExpr("r", quark.Over(quark.Rank(), w)).
				SelectExpr("dr", quark.Over(quark.DenseRank(), w)).Limit(10).List()
			return err
		}},
		{"Q41", "window", "LAG / LEAD over an ordered partition", qbTyped, "", func(ctx context.Context, c *quark.Client) error {
			w := quark.NewWindow().PartitionBy(quark.Col("user_id")).OrderBy(quark.Col("placed_at"), false)
			_, err := quark.For[qbOrder](ctx, c).
				SelectExpr("prev", quark.Over(quark.Lag(quark.Col("total"), 1), w)).
				SelectExpr("next", quark.Over(quark.Lead(quark.Col("total"), 1), w)).Limit(10).List()
			return err
		}},
		{"Q42", "window", "running total: SUM(x) OVER (ORDER BY y)", qbTyped, "", func(ctx context.Context, c *quark.Client) error {
			w := quark.NewWindow().OrderBy(quark.Col("placed_at"), false)
			_, err := quark.For[qbOrder](ctx, c).
				SelectExpr("running", quark.Over(quark.Func("SUM", quark.Col("total")), w)).Limit(10).List()
			return err
		}},
		{"Q43", "window", "windowed aggregate with an explicit frame (ROWS BETWEEN 2 PRECEDING AND CURRENT ROW)", qbTyped,
			`Window.Rows/Range with typed frame bounds, added by S3. Omitting the frame was never a smaller version of the query: the default runs from the start of the partition, so a moving average was silently a running one`,
			func(ctx context.Context, c *quark.Client) error {
				w := quark.NewWindow().OrderBy(quark.Col("placed_at"), false).
					Rows(quark.Preceding(2), quark.CurrentRow())
				_, err := quark.For[qbOrder](ctx, c).
					SelectExpr("avg3", quark.Over(quark.Func("AVG", quark.Col("total")), w)).Limit(10).List()
				return err
			}},
		{"Q44", "window", "top-N per group (filter on the window result)", qbTyped,
			`joining the ranked CTE already worked; FromCTE makes the shorter form available too`,
			func(ctx context.Context, c *quark.Client) error {
				w := quark.NewWindow().PartitionBy(quark.Col("user_id")).OrderBy(quark.Col("total"), true)
				sub, err := quark.For[qbOrder](ctx, c).
					Select("id").SelectExpr("rn", quark.Over(quark.RowNumber(), w)).AsSubquery()
				if err != nil {
					return err
				}
				_, err = quark.For[qbOrder](ctx, c).With("ranked", sub).
					Join("ranked").On("qb_orders.id", "=", "ranked.id").
					WhereExpr(quark.Lte(quark.Col("ranked.rn"), quark.Lit(1))).Limit(10).List()
				return err
			}},
		{"Q45", "window", "NTILE / PERCENT_RANK", qbTyped,
			`NTile and PercentRank, added by S1 as window-function leaves with constant names — the same contract RowNumber and Rank already kept`,
			func(ctx context.Context, c *quark.Client) error {
				w := quark.NewWindow().OrderBy(quark.Col("total"), false)
				_, err := quark.For[qbOrder](ctx, c).
					SelectExpr("q", quark.Over(quark.NTile(4), w)).
					SelectExpr("pr", quark.Over(quark.PercentRank(), w)).Limit(10).List()
				return err
			}},
	}
}

func qbFamilyG() []qbCase {
	return []qbCase{
		{"Q46", "setop", "UNION", qbTyped, "", func(ctx context.Context, c *quark.Client) error {
			other := quark.For[qbOrder](ctx, c).Where("status", "=", "shipped")
			_, err := quark.For[qbOrder](ctx, c).Where("status", "=", "paid").Union(other).Limit(10).List()
			return err
		}},
		{"Q47", "setop", "UNION ALL", qbTyped, "", func(ctx context.Context, c *quark.Client) error {
			other := quark.For[qbOrder](ctx, c).Where("status", "=", "shipped")
			_, err := quark.For[qbOrder](ctx, c).Where("status", "=", "paid").UnionAll(other).Limit(10).List()
			return err
		}},
		{"Q48", "setop", "INTERSECT", qbTyped, "", func(ctx context.Context, c *quark.Client) error {
			other := quark.For[qbOrder](ctx, c).Where("total", ">", 100)
			_, err := quark.For[qbOrder](ctx, c).Where("status", "=", "paid").Intersect(other).Limit(10).List()
			return err
		}},
		{"Q49", "setop", "EXCEPT", qbTyped, "", func(ctx context.Context, c *quark.Client) error {
			other := quark.For[qbOrder](ctx, c).Where("total", "<", 10)
			_, err := quark.For[qbOrder](ctx, c).Where("status", "=", "paid").Except(other).Limit(10).List()
			return err
		}},
	}
}

func qbFamilyH() []qbCase {
	return []qbCase{
		{"Q50", "json", "filter on a JSON member", qbTyped,
			`the path is a dotted identifier chain ("tier"), not JSONPath ("$.tier"), which is rejected`,
			func(ctx context.Context, c *quark.Client) error {
				_, err := quark.For[qbUser](ctx, c).WhereJSON("profile", "tier", "=", "gold").Limit(10).List()
				return err
			}},
		{"Q51", "json", "filter on a nested JSON member", qbTyped, "", func(ctx context.Context, c *quark.Client) error {
			_, err := quark.For[qbProduct](ctx, c).WhereJSON("attrs", "dims.width", ">", 10).Limit(10).List()
			return err
		}},
		{"Q52", "json", "project a JSON member as a column", qbTyped,
			`JSONExtract, added by S1. The accessor is named differently by every engine, so the dialect renders it — a literal name in the whitelist would have been portable on one engine only`,
			func(ctx context.Context, c *quark.Client) error {
				_, err := quark.For[qbUser](ctx, c).
					SelectExpr("tier", quark.JSONExtract("profile", "tier")).Limit(10).List()
				return err
			}},
	}
}

// Family I is engine-dependent by nature: SQLite has no row-level locking and
// says so explicitly, which is the right answer rather than a gap. The cases
// are marked typed because the API expresses them and locking_test.go
// exercises them against PostgreSQL, MySQL and Oracle.
func qbFamilyI() []qbCase {
	return []qbCase{
		{"Q53", "locking", "SELECT ... FOR UPDATE", qbTyped,
			`SQLite rejects it with ErrUnsupportedFeature naming BEGIN IMMEDIATE; see locking_test.go for the engines that have it`,
			nil},
		{"Q54", "locking", "SELECT ... FOR SHARE", qbTyped, `as Q53`, nil},
		{"Q55", "locking", "FOR UPDATE SKIP LOCKED (queue pattern)", qbTyped, `as Q53`, nil},
		{"Q56", "locking", "FOR UPDATE NOWAIT", qbTyped, `as Q53`, nil},
	}
}

func qbFamilyJ() []qbCase {
	return []qbCase{
		{"Q57", "writes", "INSERT ... ON CONFLICT DO UPDATE (upsert)", qbTyped, "", func(ctx context.Context, c *quark.Client) error {
			return quark.For[qbProduct](ctx, c).Upsert(
				&qbProduct{ID: 900, Name: "up", Price: 1, CategoryID: 1},
				[]string{"id"}, []string{"name", "price"})
		}},
		{"Q58", "writes", "bulk INSERT of N rows in one statement", qbTyped, "", func(ctx context.Context, c *quark.Client) error {
			return quark.For[qbProduct](ctx, c).CreateBatch([]*qbProduct{
				{ID: 901, Name: "a", CategoryID: 1}, {ID: 902, Name: "b", CategoryID: 1},
			})
		}},
		{"Q59", "writes", "UPDATE ... WHERE (set-based, no entity load)", qbTyped, "", func(ctx context.Context, c *quark.Client) error {
			_, err := quark.For[qbProduct](ctx, c).Where("category_id", "=", 1).
				UpdateMap(map[string]any{"active": false})
			return err
		}},
		{"Q60", "writes", "UPDATE with an expression over the column (stock = stock - 1)", qbTyped,
			`UpdateMap recognises an Expr value as SQL rather than data, and Subtract gives it something to say. Read-modify-write is not equivalent: it loses the atomicity that is the reason to write it in SQL`,
			func(ctx context.Context, c *quark.Client) error {
				before, err := quark.For[qbProduct](ctx, c).Find(int64(1))
				if err != nil {
					return err
				}
				if _, err := quark.For[qbProduct](ctx, c).Where("id", "=", 1).
					UpdateMap(map[string]any{
						"stock": quark.Subtract(quark.Col("stock"), quark.Lit(1)),
					}); err != nil {
					return err
				}
				after, err := quark.For[qbProduct](ctx, c).Find(int64(1))
				if err != nil {
					return err
				}
				if after.Stock != before.Stock-1 {
					return fmt.Errorf("stock went %d -> %d, want a decrement of 1", before.Stock, after.Stock)
				}
				return nil
			}},
	}
}
