// Copyright 2026 jcsvwinston
// SPDX-License-Identifier: Apache-2.0

package quark

import "fmt"

// Subquery is a rendered SELECT that can be embedded inside another query
// through the Expr AST (Sub, Exists, NotExists, InSub, NotInSub).
//
// A subquery is built like any other query — `For[T](...).Where(...)` — and
// then captured with `AsSubquery()`. The capture eagerly renders the SELECT
// using the active dialect's identifier quoting but with `?` as the bind
// marker, so the outer query's `buildWhereClause` can swap each `?` for the
// dialect's placeholder syntax at the correct arg index when the AST is
// rendered.
//
// This contract matches the rest of the AST: leaves emit `?`,
// `substitutePathMarkers` does the placeholder rewrite, args are threaded
// through `condition.extraArgs`. So a subquery is just another Expr leaf.
type Subquery struct {
	sql  string
	args []any
}

// qmarkDialect wraps a real Dialect and overrides Placeholder to always
// return '?'. Used by AsSubquery to render the inner SELECT in a
// dialect-agnostic shape that the outer AST can finish substituting.
//
// All other Dialect methods delegate via the embedded interface — Quote,
// LimitOffset, JSONExtract, LockSuffix, etc. — so identifier quoting and
// dialect-specific clauses stay correct.
type qmarkDialect struct{ Dialect }

func (qmarkDialect) Placeholder(int) string { return "?" }

// AsSubquery captures the current Query[T] as a renderable Subquery. The
// SELECT is rendered eagerly: identifier validation, soft-delete and tenant
// predicates, JOINs, GROUP BY, HAVING, ORDER BY, LIMIT all run at
// AsSubquery time.
//
// The captured SQL uses '?' as the bind marker; the outer query swaps it
// for the dialect's placeholder when the wrapping Expr is rendered. The
// SELECT cols can be customised via the standard Select() before capture
// — typical use is `Select("id")` for an `IN (subquery)` shape.
//
// Pessimistic lock options (`ForUpdate` / `ForShare` / `SkipLocked` /
// `NoWait`) are explicitly rejected on the inner query — MSSQL emits
// table hints (`WITH (UPDLOCK, ROWLOCK)`) inline in the FROM clause that
// are not legal inside a subquery context, and PG / MySQL / Oracle's
// `FOR UPDATE` suffix on a subquery is technically valid but misleading
// when the outer caller already drives row locking. Acquire locks on the
// outer query instead.
func (q *Query[T]) AsSubquery() (*Subquery, error) {
	if !q.lock.IsZero() {
		return nil, fmt.Errorf("%w: pessimistic lock options on a subquery are not supported — apply locks on the outer query instead", ErrUnsupportedFeature)
	}
	c := q.clone()
	c.dialect = qmarkDialect{Dialect: c.dialect}
	sql, args, err := c.buildSelect()
	if err != nil {
		return nil, err
	}
	return &Subquery{sql: sql, args: args}, nil
}

// MustAsSubquery is the panic-on-error variant of AsSubquery for use in
// expression composition where errors would otherwise have to be threaded
// through the AST. The error is realistic (invalid identifier, etc.) only
// when the inner query is malformed; for well-formed inputs it never
// triggers.
func (q *Query[T]) MustAsSubquery() *Subquery {
	s, err := q.AsSubquery()
	if err != nil {
		panic(fmt.Sprintf("MustAsSubquery: %v", err))
	}
	return s
}

// SQL returns the captured SELECT fragment with '?' bind markers and the
// args slice. Exposed for test introspection — production code should use
// the Expr wrappers (Sub, Exists, etc.) which compose through the AST.
func (s *Subquery) SQL() (string, []any) {
	if s == nil {
		return "", nil
	}
	args := append([]any{}, s.args...)
	return s.sql, args
}

// --- Expr wrappers ---

// Sub wraps a Subquery as an Expr leaf so it can take the place of a Lit
// or Col anywhere an Expr is accepted (e.g. `Eq(Col("user_id"), Sub(maxID))`,
// `Cmp(Col("price"), ">", Sub(avgPrice))`). Renders as `(<inner-sql>)`.
func Sub(s *Subquery) Expr { return subExpr{s: s} }

type subExpr struct{ s *Subquery }

func (e subExpr) ToSQL(_ Dialect, _ *SQLGuard) (string, []any, error) {
	if e.s == nil {
		return "", nil, fmt.Errorf("%w: Sub(nil)", ErrInvalidQuery)
	}
	args := append([]any{}, e.s.args...)
	return "(" + e.s.sql + ")", args, nil
}

// Exists renders `EXISTS (<subquery>)`. Typically used as a top-level
// WhereExpr predicate.
//
// Example:
//
//	hasOrders, _ := quark.For[Order](ctx, client).
//	    Where("user_id", "=", quark.Col("users.id")).
//	    Select("1").
//	    AsSubquery()
//	q := quark.For[User](ctx, client).WhereExpr(quark.Exists(hasOrders))
//
// (Correlated subqueries require the inner query to reference the outer
// table by qualified name; see the JoinOn grammar for what's accepted.)
func Exists(s *Subquery) Expr { return existsExpr{s: s, negate: false} }

// NotExists is the negated form: `NOT EXISTS (<subquery>)`.
func NotExists(s *Subquery) Expr { return existsExpr{s: s, negate: true} }

type existsExpr struct {
	s      *Subquery
	negate bool
}

func (e existsExpr) ToSQL(_ Dialect, _ *SQLGuard) (string, []any, error) {
	if e.s == nil {
		return "", nil, fmt.Errorf("%w: Exists/NotExists(nil)", ErrInvalidQuery)
	}
	args := append([]any{}, e.s.args...)
	if e.negate {
		return "NOT EXISTS (" + e.s.sql + ")", args, nil
	}
	return "EXISTS (" + e.s.sql + ")", args, nil
}

// InSub renders `lhs IN (<subquery>)`. Useful for the common
// `WHERE x IN (SELECT id FROM ...)` shape; the inner Subquery should
// `Select("…")` exactly one column for the comparison.
func InSub(lhs Expr, s *Subquery) Expr { return inSubExpr{lhs: lhs, s: s, negate: false} }

// NotInSub is the negated form: `lhs NOT IN (<subquery>)`.
func NotInSub(lhs Expr, s *Subquery) Expr { return inSubExpr{lhs: lhs, s: s, negate: true} }

type inSubExpr struct {
	lhs    Expr
	s      *Subquery
	negate bool
}

func (e inSubExpr) ToSQL(d Dialect, g *SQLGuard) (string, []any, error) {
	if e.s == nil {
		return "", nil, fmt.Errorf("%w: In/NotIn subquery is nil", ErrInvalidQuery)
	}
	if e.lhs == nil {
		return "", nil, fmt.Errorf("%w: In/NotIn subquery requires a left-hand expression", ErrInvalidQuery)
	}
	lsql, largs, err := e.lhs.ToSQL(d, g)
	if err != nil {
		return "", nil, err
	}
	op := "IN"
	if e.negate {
		op = "NOT IN"
	}
	args := append([]any{}, largs...)
	args = append(args, e.s.args...)
	return lsql + " " + op + " (" + e.s.sql + ")", args, nil
}
