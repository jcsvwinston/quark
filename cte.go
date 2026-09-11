// Copyright 2026 jcsvwinston
// SPDX-License-Identifier: Apache-2.0

package quark

import "fmt"

// cteEntry is one named WITH ... AS (...) definition attached to a query.
// The body holds the rendered SELECT (with '?' bind markers) and its
// args; buildSelect prepends the entries before the outer SELECT and
// substitutes each '?' for the dialect's placeholder at the right
// argIndex when the final SQL is composed.
type cteEntry struct {
	name      string
	sql       string
	args      []any
	recursive bool
}

// With attaches a non-recursive CTE to the query. The CTE renders as
// `WITH <name> AS (<inner>)` before the outer SELECT, and the outer
// query can reference the CTE by name in JOIN clauses.
//
// Example:
//
//	topOrders, _ := quark.For[Order](ctx, client).
//	    Where("amount", ">", 100).
//	    Select("user_id", "amount").
//	    AsSubquery()
//
//	users, err := quark.For[User](ctx, client).
//	    With("top_orders", topOrders).
//	    Join("top_orders").On("users.id", "=", "top_orders.user_id").
//	    Limit(50).
//	    List()
//
// Multiple With calls compose: the entries render comma-separated in
// the order they were added. If any entry is recursive, the prefix
// becomes `WITH RECURSIVE ...`.
func (q *Query[T]) With(name string, sub *Subquery) *Query[T] {
	c := q.clone()
	if sub == nil {
		c.err = fmt.Errorf("%w: With(%q, nil) — subquery must be non-nil", ErrInvalidQuery, name)
		return c
	}
	if err := c.guard.ValidateIdentifier(name); err != nil {
		c.err = err
		return c
	}
	c.ctes = ownedAppend(c.ctes, cteEntry{
		name:      name,
		sql:       sub.sql,
		args:      append([]any(nil), sub.args...),
		recursive: false,
	})
	return c
}

// WithRecursive is the recursive form. The prefix becomes recursive when at
// least one entry is recursive; the RECURSIVE keyword itself is dialect-aware
// (see recursiveCTEKeyword): PostgreSQL/MySQL/MariaDB/SQLite emit
// `WITH RECURSIVE`, while Oracle and SQL Server infer recursion structurally
// and reject the keyword, so Quark emits a plain `WITH` there. The inner
// Subquery is responsible for shaping the recursive body — typically a
// `UNION ALL` between a base case and a step that references the CTE name.
// Composing that body is [Query.UnionAll] plus [Query.AsSubquery]:
//
//	anchor := quark.For[Category](ctx, client).Where("id", "=", rootID)
//	step := quark.For[Category](ctx, client).
//	    Join("tree").On("categories.parent_id", "=", "tree.id")
//	body, _ := anchor.UnionAll(step).AsSubquery()
//
//	rows, err := quark.For[Category](ctx, client).
//	    WithRecursive("tree", body).FromCTE("tree").List()
//
// This comment used to say the typed Subquery surface could not model UNION,
// so recursive use was limited to single-SELECT bodies. That stopped being
// true when set operators landed, and the stale note outlived it — long
// enough that A4's own measurement session read it, wrote the case the way
// the comment implied, and recorded the capability as missing. It is not.
//
// (Oracle also requires a column-alias list on the CTE name for a genuinely
// recursive body; that must live in the Subquery's own SQL.)
func (q *Query[T]) WithRecursive(name string, sub *Subquery) *Query[T] {
	c := q.With(name, sub)
	if c.err != nil {
		return c
	}
	c.ctes[len(c.ctes)-1].recursive = true
	return c
}

// FromCTE makes the SELECT read from a CTE declared with [Query.With] or
// [Query.WithRecursive] instead of the model's own table.
//
// Without it, With() declares the CTE and the outer SELECT still reads the
// base table, so the only way to reach a CTE was to JOIN it — which works,
// and is the right shape when you want both. It is not the right shape when
// the derived rows ARE the query:
//
//	// average orders per user: the aggregate is over the derived rows, and
//	// the base table must not be in the FROM at all.
//	per, _ := quark.For[Order](ctx, client).
//	    GroupBy("user_id").
//	    SelectExpr("n", quark.Func("COUNT", quark.Col("*"))).
//	    AsSubquery()
//
//	avg, err := quark.For[Order](ctx, client).
//	    With("per_user", per).
//	    FromCTE("per_user").
//	    Avg("n")
//
// It changes the SELECT path only. UPDATE, DELETE and INSERT keep writing to
// the model's table: a CTE is not a write target, and silently redirecting a
// write would be the worst possible reading of this call.
//
// The scanned type is still T, so the CTE's projection has to carry the
// columns T expects — the same contract a JOIN already has.
//
// Two implicit scopes behave differently under FromCTE, and the difference is
// deliberate:
//
//   - The soft-delete filter is DROPPED. It is a property of the model's
//     table, and a CTE need not carry deleted_at at all; emitting it anyway
//     produced `SELECT * FROM "ids" WHERE "deleted_at" IS NULL` against a
//     one-column CTE, which SQLite answered with zero rows and no error.
//     Apply it in the subquery that builds the CTE, where it means something.
//   - The tenant filter is KEPT. Dropping it would turn a missing column into
//     cross-tenant reads, so the CTE must project the tenant column. If it
//     does not, the query fails or returns nothing — which is the safe
//     direction to be wrong in.
func (q *Query[T]) FromCTE(name string) *Query[T] {
	c := q.clone()
	if err := c.guard.ValidateIdentifier(name); err != nil {
		c.err = err
		return c
	}
	c.fromCTE = name
	return c
}

// FromTable reads from a named table instead of the one derived from T.
//
// It exists for projections. For[T] takes both the row shape and the source
// table from T, so a struct that is a result shape rather than a model — the
// columns of a join, an aggregate roll-up — sends the query at a table named
// after the DTO, which does not exist:
//
//	type OrderEmail struct {
//	    OrderID int64  `db:"order_id"`
//	    Email   string `db:"email"`
//	}
//
//	rows, err := quark.For[OrderEmail](ctx, client).
//	    FromTable("orders").
//	    Join("users").On("orders.user_id", "=", "users.id").
//	    SelectExpr("order_id", quark.Col("orders.id")).
//	    SelectExpr("email", quark.Col("users.email")).
//	    List()
//
// Like FromCTE it changes reads only, and for the same reason: a projection
// is not a write target. The soft-delete and tenant scopes follow the same
// rules as FromCTE — see its documentation, and note that a DTO has no model
// metadata to derive them from in the first place.
func (q *Query[T]) FromTable(name string) *Query[T] {
	c := q.clone()
	if err := c.guard.ValidateIdentifier(name); err != nil {
		c.err = err
		return c
	}
	c.fromCTE = name
	return c
}
