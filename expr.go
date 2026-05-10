// Copyright 2026 jcsvwinston
// SPDX-License-Identifier: Apache-2.0

package quark

import (
	"fmt"
	"strings"
)

// Expr is the composable expression node Phase 2's query builder rests on.
// Each node implements ToSQL, returning a SQL fragment with `?` as a
// neutral bind marker plus the args that fill those markers.
//
// Callers (WhereExpr, HavingExpr) hand the rendered fragment to the
// existing buildWhereClause / substitutePathMarkers pipeline that already
// rewrites `?` to the dialect's placeholder syntax at the correct arg
// index. That keeps the AST dialect-agnostic at construction time and
// lets us compose deep trees without juggling indices.
//
// The exported constructors below (Col, Lit, And, Or, Not, Cmp, Eq, Ne,
// Lt, Gt, Lte, Gte, In, NotIn, Func) are the v0.4 AST surface. Subqueries,
// Cast, and Exists arrive in the subqueries-and-CTE PR.
type Expr interface {
	ToSQL(d Dialect, g *SQLGuard) (sql string, args []any, err error)
}

// --- Leaves ---

// Col references a column by name. The name is validated through
// SQLGuard.ValidateIdentifier — the AST inherits the same identifier
// safety the rest of the builder enforces. The wildcard "*" is accepted
// as-is for use inside aggregate calls (e.g. Func("COUNT", Col("*"))).
func Col(name string) Expr { return colExpr{name: name} }

type colExpr struct{ name string }

func (c colExpr) ToSQL(d Dialect, g *SQLGuard) (string, []any, error) {
	if c.name == "*" {
		return "*", nil, nil
	}
	if err := g.ValidateIdentifier(c.name); err != nil {
		return "", nil, err
	}
	return d.Quote(c.name), nil, nil
}

// Lit binds a Go value as a SQL parameter. The value never reaches the
// SQL surface — it always travels through args, regardless of how nested
// the expression tree is.
func Lit(v any) Expr { return litExpr{value: v} }

type litExpr struct{ value any }

func (l litExpr) ToSQL(_ Dialect, _ *SQLGuard) (string, []any, error) {
	return "?", []any{l.value}, nil
}

// --- Combinators ---

// And composes two or more expressions with logical AND. Empty And is a
// no-op (renders to ""). Single-element And renders the inner expression
// without parentheses; two or more get wrapped so precedence is explicit.
func And(parts ...Expr) Expr { return logicalExpr{op: "AND", parts: parts} }

// Or composes two or more expressions with logical OR. Same parenthesis
// rules as And.
func Or(parts ...Expr) Expr { return logicalExpr{op: "OR", parts: parts} }

type logicalExpr struct {
	op    string
	parts []Expr
}

func (e logicalExpr) ToSQL(d Dialect, g *SQLGuard) (string, []any, error) {
	if len(e.parts) == 0 {
		return "", nil, nil
	}
	if len(e.parts) == 1 {
		return e.parts[0].ToSQL(d, g)
	}
	var b strings.Builder
	var args []any
	b.WriteByte('(')
	for i, p := range e.parts {
		if i > 0 {
			b.WriteByte(' ')
			b.WriteString(e.op)
			b.WriteByte(' ')
		}
		s, pargs, err := p.ToSQL(d, g)
		if err != nil {
			return "", nil, err
		}
		b.WriteString(s)
		args = append(args, pargs...)
	}
	b.WriteByte(')')
	return b.String(), args, nil
}

// Not negates an expression. Renders as "NOT (<inner>)" so precedence is
// explicit.
func Not(e Expr) Expr { return notExpr{inner: e} }

type notExpr struct{ inner Expr }

func (n notExpr) ToSQL(d Dialect, g *SQLGuard) (string, []any, error) {
	s, args, err := n.inner.ToSQL(d, g)
	if err != nil {
		return "", nil, err
	}
	return "NOT (" + s + ")", args, nil
}

// --- Comparisons ---

// Cmp is the general comparison constructor. Operator goes through
// SQLGuard.ValidateOperator so the AST cannot smuggle arbitrary tokens
// into the SQL surface.
func Cmp(lhs Expr, op string, rhs Expr) Expr { return cmpExpr{lhs: lhs, op: op, rhs: rhs} }

// Eq, Ne, Lt, Gt, Lte, Gte are the syntactic shortcuts for Cmp with the
// most common operators. Built on top of Lit / Col for the typical
// "Col(x) = Lit(v)" shape.
func Eq(lhs, rhs Expr) Expr  { return Cmp(lhs, "=", rhs) }
func Ne(lhs, rhs Expr) Expr  { return Cmp(lhs, "<>", rhs) }
func Lt(lhs, rhs Expr) Expr  { return Cmp(lhs, "<", rhs) }
func Gt(lhs, rhs Expr) Expr  { return Cmp(lhs, ">", rhs) }
func Lte(lhs, rhs Expr) Expr { return Cmp(lhs, "<=", rhs) }
func Gte(lhs, rhs Expr) Expr { return Cmp(lhs, ">=", rhs) }

type cmpExpr struct {
	lhs, rhs Expr
	op       string
}

func (c cmpExpr) ToSQL(d Dialect, g *SQLGuard) (string, []any, error) {
	if err := g.ValidateOperator(c.op); err != nil {
		return "", nil, err
	}
	lsql, largs, err := c.lhs.ToSQL(d, g)
	if err != nil {
		return "", nil, err
	}
	rsql, rargs, err := c.rhs.ToSQL(d, g)
	if err != nil {
		return "", nil, err
	}
	args := append([]any{}, largs...)
	args = append(args, rargs...)
	// Emit the operator in upper-case so dialects with case-sensitive
	// keyword handling (Oracle, some MSSQL collations) see the canonical
	// spelling regardless of how the caller typed it.
	op := strings.ToUpper(strings.TrimSpace(c.op))
	return lsql + " " + op + " " + rsql, args, nil
}

// --- IN / NOT IN ---

// In renders "lhs IN (v1, v2, …)". Empty values list is a logic error and
// returns ErrInvalidQuery — `WHERE x IN ()` is non-portable across
// dialects (Postgres errors, MySQL silently matches nothing) so we refuse
// to emit it. Use a no-rows query instead.
func In(lhs Expr, values ...Expr) Expr { return inExpr{lhs: lhs, values: values} }

// NotIn is the negation. Same emptiness rules apply.
func NotIn(lhs Expr, values ...Expr) Expr { return inExpr{lhs: lhs, values: values, negate: true} }

type inExpr struct {
	lhs    Expr
	values []Expr
	negate bool
}

func (e inExpr) ToSQL(d Dialect, g *SQLGuard) (string, []any, error) {
	if len(e.values) == 0 {
		return "", nil, fmt.Errorf("%w: In/NotIn requires at least one value", ErrInvalidQuery)
	}
	lsql, largs, err := e.lhs.ToSQL(d, g)
	if err != nil {
		return "", nil, err
	}
	parts := make([]string, len(e.values))
	args := append([]any{}, largs...)
	for i, v := range e.values {
		s, vargs, err := v.ToSQL(d, g)
		if err != nil {
			return "", nil, err
		}
		parts[i] = s
		args = append(args, vargs...)
	}
	op := "IN"
	if e.negate {
		op = "NOT IN"
	}
	return lsql + " " + op + " (" + strings.Join(parts, ", ") + ")", args, nil
}

// --- Functions ---

// astFunctionWhitelist is the conservative roster of SQL functions the AST
// accepts in v0.4. Adding to this list without thinking through dialect
// portability is a regression risk: not every engine spells COALESCE the
// same way, NULLIF lives in different headers, etc. Add new entries one
// at a time as concrete uses surface.
var astFunctionWhitelist = map[string]struct{}{
	"COUNT":    {},
	"SUM":      {},
	"AVG":      {},
	"MIN":      {},
	"MAX":      {},
	"LOWER":    {},
	"UPPER":    {},
	"LENGTH":   {},
	"COALESCE": {},
	"ABS":      {},
}

// Func calls a SQL function. The name is normalised to upper-case and
// matched against a whitelist; unknown names return ErrInvalidQuery
// rather than reaching the SQL surface. Empty argument list is allowed —
// emit a bare "FUN()" — for COUNT(*), use Col("*") explicitly.
func Func(name string, args ...Expr) Expr { return funcExpr{name: name, args: args} }

type funcExpr struct {
	name string
	args []Expr
}

func (f funcExpr) ToSQL(d Dialect, g *SQLGuard) (string, []any, error) {
	upper := strings.ToUpper(strings.TrimSpace(f.name))
	if _, ok := astFunctionWhitelist[upper]; !ok {
		return "", nil, fmt.Errorf("%w: Func %q is not in the AST whitelist (COUNT, SUM, AVG, MIN, MAX, LOWER, UPPER, LENGTH, COALESCE, ABS)", ErrInvalidQuery, f.name)
	}
	var b strings.Builder
	var args []any
	b.WriteString(upper)
	b.WriteByte('(')
	for i, a := range f.args {
		if i > 0 {
			b.WriteString(", ")
		}
		s, aa, err := a.ToSQL(d, g)
		if err != nil {
			return "", nil, err
		}
		b.WriteString(s)
		args = append(args, aa...)
	}
	b.WriteByte(')')
	return b.String(), args, nil
}
