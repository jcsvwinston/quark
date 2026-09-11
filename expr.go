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
// SQLGuard.ValidateQualifiedIdentifier — the AST inherits the same
// identifier safety the rest of the builder enforces, and additionally
// accepts a one-level qualified form ("orders.total") with each segment
// validated and quoted separately, so a JOIN query can disambiguate
// shared column names through the AST (AQ-01). The wildcard "*" is
// accepted as-is for use inside aggregate calls (e.g. Func("COUNT",
// Col("*"))).
func Col(name string) Expr { return colExpr{name: name} }

type colExpr struct{ name string }

func (c colExpr) ToSQL(d Dialect, g *SQLGuard) (string, []any, error) {
	if c.name == "*" {
		return "*", nil, nil
	}
	quoted, err := g.QuoteQualifiedIdentifier(d, c.name)
	if err != nil {
		return "", nil, err
	}
	return quoted, nil, nil
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

// --- Aggregate modifiers, CASE and JSON projection ---
//
// These three are the reason the AST grew constructors instead of a longer
// `astFunctionWhitelist`. None of them is a function call with a portable
// name:
//
//   - DISTINCT is a modifier on COUNT's argument, not a function. Spelling
//     it Func("DISTINCT", …) put a keyword where an identifier goes.
//   - CASE is an expression form with its own grammar; no arity of
//     Func(name, args...) renders it.
//   - the JSON accessor is named differently by every engine, so a literal
//     name in the whitelist would be portable on exactly one of them.
//
// Each emits a constant name (or asks the dialect for one), so no caller
// string reaches the SQL surface — the same contract the window-function
// leaves keep in window.go.

// CountDistinct renders `COUNT(DISTINCT <expr>)`.
//
// Oracle rejects a bare aggregate in the SELECT list of a query that does not
// group (ORA-00937, "not a single-group group function"), so pair it with
// GroupBy when the query has to run there.
func CountDistinct(e Expr) Expr { return countDistinctExpr{inner: e} }

type countDistinctExpr struct{ inner Expr }

func (c countDistinctExpr) ToSQL(d Dialect, g *SQLGuard) (string, []any, error) {
	if c.inner == nil {
		return "", nil, fmt.Errorf("%w: CountDistinct requires a non-nil expression", ErrInvalidQuery)
	}
	s, args, err := c.inner.ToSQL(d, g)
	if err != nil {
		return "", nil, err
	}
	return "COUNT(DISTINCT " + s + ")", args, nil
}

// Case builds a searched `CASE WHEN … THEN … [ELSE …] END` expression:
//
//	quark.Case().
//	    When(quark.Eq(quark.Col("status"), quark.Lit("paid")), quark.Lit(1)).
//	    Else(quark.Lit(0))
//
// It is what makes a conditional aggregate expressible —
// `SUM(CASE WHEN … THEN 1 ELSE 0 END)` — by wrapping it in Func("SUM", …).
// A CASE with no WHEN branch is rejected: it is always a mistake, and
// engines disagree on whether to reject it themselves.
//
// One portability note worth knowing before it costs an afternoon: every
// Lit() binds as a parameter, so a CASE whose branches are ALL literals has
// no branch with a known type. PostgreSQL then infers `text` for the whole
// expression, and wrapping it in SUM fails with "function sum(text) does not
// exist". Give at least one branch a typed expression — a Col(), typically —
// and the CASE takes its type. The other five engines are more forgiving,
// which is what makes this one easy to miss.
func Case() *CaseBuilder { return &CaseBuilder{} }

// CaseBuilder accumulates the branches of a CASE expression. It is an Expr
// once it has at least one WHEN, so it can be used anywhere an Expr goes;
// Else is optional and returns the builder so the call can end there.
type CaseBuilder struct {
	whens []caseBranch
	els   Expr
}

type caseBranch struct {
	cond   Expr
	result Expr
}

// When adds a `WHEN <cond> THEN <result>` branch. Branches render in the
// order they are added, which is the order the engine evaluates them.
func (c *CaseBuilder) When(cond, result Expr) *CaseBuilder {
	c.whens = append(c.whens, caseBranch{cond: cond, result: result})
	return c
}

// Else sets the `ELSE <result>` arm. Without it a CASE that matches no
// branch yields NULL, which is the SQL default and rarely what an
// aggregate wants.
func (c *CaseBuilder) Else(result Expr) *CaseBuilder {
	c.els = result
	return c
}

func (c *CaseBuilder) ToSQL(d Dialect, g *SQLGuard) (string, []any, error) {
	if len(c.whens) == 0 {
		return "", nil, fmt.Errorf("%w: Case requires at least one When branch", ErrInvalidQuery)
	}
	var b strings.Builder
	var args []any
	b.WriteString("CASE")
	for _, w := range c.whens {
		if w.cond == nil || w.result == nil {
			return "", nil, fmt.Errorf("%w: Case.When requires a non-nil condition and result", ErrInvalidQuery)
		}
		csql, cargs, err := w.cond.ToSQL(d, g)
		if err != nil {
			return "", nil, err
		}
		rsql, rargs, err := w.result.ToSQL(d, g)
		if err != nil {
			return "", nil, err
		}
		b.WriteString(" WHEN ")
		b.WriteString(csql)
		b.WriteString(" THEN ")
		b.WriteString(rsql)
		args = append(args, cargs...)
		args = append(args, rargs...)
	}
	if c.els != nil {
		esql, eargs, err := c.els.ToSQL(d, g)
		if err != nil {
			return "", nil, err
		}
		b.WriteString(" ELSE ")
		b.WriteString(esql)
		args = append(args, eargs...)
	}
	b.WriteString(" END")
	return b.String(), args, nil
}

// JSONExtract projects a member of a JSON column, so a JSON value can
// reach the SELECT list the same way WhereJSON already reaches the WHERE
// clause. The dialect renders it — `jsonb_extract_path_text` on
// PostgreSQL, `JSON_EXTRACT` on MySQL and SQLite, `JSON_VALUE` on SQL
// Server and Oracle — and validates the path, which is a dotted
// identifier chain ("user.name"), not JSONPath ("$.user.name").
func JSONExtract(column, path string) Expr {
	return jsonExtractExpr{column: column, path: path}
}

type jsonExtractExpr struct {
	column string
	path   string
}

func (j jsonExtractExpr) ToSQL(d Dialect, g *SQLGuard) (string, []any, error) {
	if d == nil {
		return "", nil, fmt.Errorf("%w: JSONExtract requires a dialect", ErrInvalidQuery)
	}
	if g != nil {
		if err := g.ValidateIdentifier(j.column); err != nil {
			return "", nil, err
		}
	}
	return d.JSONExtract(j.column, j.path)
}

// --- Arithmetic ---

// Add, Subtract, Multiply and Divide render binary arithmetic, parenthesised
// so composition does not depend on the reader (or the engine) agreeing about
// precedence.
//
// The motivating use is an UPDATE that reads the column it writes:
//
//	q.Where("id", "=", id).UpdateMap(map[string]any{
//	    "stock": quark.Subtract(quark.Col("stock"), quark.Lit(1)),
//	})
//
// which is not the same as loading the row, subtracting in Go and writing it
// back: that version loses the atomicity, and two concurrent decrements can
// leave one of them unapplied.
//
// The operator is a constant in each constructor, so nothing a caller types
// reaches the SQL surface.
func Add(lhs, rhs Expr) Expr { return arithExpr{op: "+", lhs: lhs, rhs: rhs} }

// Subtract renders `(<lhs> - <rhs>)`.
func Subtract(lhs, rhs Expr) Expr { return arithExpr{op: "-", lhs: lhs, rhs: rhs} }

// Multiply renders `(<lhs> * <rhs>)`.
func Multiply(lhs, rhs Expr) Expr { return arithExpr{op: "*", lhs: lhs, rhs: rhs} }

// Divide renders `(<lhs> / <rhs>)`. Division by zero is the engine's to
// report: engines disagree (an error on PostgreSQL, NULL on SQLite) and
// papering over that would hide a real difference.
func Divide(lhs, rhs Expr) Expr { return arithExpr{op: "/", lhs: lhs, rhs: rhs} }

type arithExpr struct {
	op       string
	lhs, rhs Expr
}

func (a arithExpr) ToSQL(d Dialect, g *SQLGuard) (string, []any, error) {
	if a.lhs == nil || a.rhs == nil {
		return "", nil, fmt.Errorf("%w: arithmetic requires two non-nil operands", ErrInvalidQuery)
	}
	lsql, largs, err := a.lhs.ToSQL(d, g)
	if err != nil {
		return "", nil, err
	}
	rsql, rargs, err := a.rhs.ToSQL(d, g)
	if err != nil {
		return "", nil, err
	}
	args := append(append([]any{}, largs...), rargs...)
	return "(" + lsql + " " + a.op + " " + rsql + ")", args, nil
}
