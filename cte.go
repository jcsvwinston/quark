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
//	    Join("top_orders", "users.id = top_orders.user_id").
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

// WithRecursive is the recursive form. Emits `WITH RECURSIVE` (or just
// promotes the prefix when at least one of the previously-added entries
// is recursive). The inner Subquery is responsible for shaping the
// recursive body — typically a `UNION ALL` between a base case and a
// step that references the CTE name. quark's typed Subquery surface
// doesn't yet model UNION (F2-set), so practical recursive use today
// is limited to engines/cases where the Subquery body can be
// constructed from a single SELECT — full recursive support is the
// motivating use case for F2-set.
func (q *Query[T]) WithRecursive(name string, sub *Subquery) *Query[T] {
	c := q.With(name, sub)
	if c.err != nil {
		return c
	}
	c.ctes[len(c.ctes)-1].recursive = true
	return c
}
