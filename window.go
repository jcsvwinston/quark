// Copyright 2026 jcsvwinston
// SPDX-License-Identifier: Apache-2.0

package quark

import (
	"fmt"
	"strings"
)

// Window models the `OVER (...)` clause for a window-function expression.
// Build with NewWindow() and chain PartitionBy / OrderBy. The chain is
// immutable: each method returns a fresh copy so a Window definition can
// be reused across multiple Over() calls without aliasing.
type Window struct {
	partitionBy []Expr
	orderBy     []windowOrder
}

type windowOrder struct {
	expr Expr
	desc bool
}

// NewWindow returns an empty Window. An empty Window renders as
// `OVER ()` — sometimes legitimate (e.g. `COUNT(*) OVER ()` for a
// running grand total), so it's not an error.
func NewWindow() *Window { return &Window{} }

// PartitionBy adds one or more partition expressions. Identifiers go
// through SQLGuard at render time — pass `Col("status")`, not the raw
// column string.
func (w *Window) PartitionBy(cols ...Expr) *Window {
	cp := *w
	cp.partitionBy = append(append([]Expr(nil), cp.partitionBy...), cols...)
	return &cp
}

// OrderBy adds an order entry. Set desc=true for descending; the second
// argument is the conventional bool toggle to keep the API tight (no
// "ASC"/"DESC" stringly-typed argument).
func (w *Window) OrderBy(col Expr, desc bool) *Window {
	cp := *w
	cp.orderBy = append(append([]windowOrder(nil), cp.orderBy...), windowOrder{expr: col, desc: desc})
	return &cp
}

// toSQL renders the body of the OVER clause: `PARTITION BY ... ORDER BY ...`
// without the surrounding parentheses. Either or both clauses may be
// empty.
func (w *Window) toSQL(d Dialect, g *SQLGuard) (string, []any, error) {
	var b strings.Builder
	var args []any
	if len(w.partitionBy) > 0 {
		b.WriteString("PARTITION BY ")
		for i, p := range w.partitionBy {
			if i > 0 {
				b.WriteString(", ")
			}
			s, pargs, err := p.ToSQL(d, g)
			if err != nil {
				return "", nil, err
			}
			b.WriteString(s)
			args = append(args, pargs...)
		}
	}
	if len(w.orderBy) > 0 {
		if b.Len() > 0 {
			b.WriteString(" ")
		}
		b.WriteString("ORDER BY ")
		for i, o := range w.orderBy {
			if i > 0 {
				b.WriteString(", ")
			}
			s, oargs, err := o.expr.ToSQL(d, g)
			if err != nil {
				return "", nil, err
			}
			b.WriteString(s)
			if o.desc {
				b.WriteString(" DESC")
			}
			args = append(args, oargs...)
		}
	}
	return b.String(), args, nil
}

// Over wraps an inner Expr with a Window: `<inner> OVER (<window>)`.
// Typical use is wrapping a window-function leaf (RowNumber, Rank,
// DenseRank, Lag, Lead) but any aggregate function from the AST
// whitelist (`COUNT`, `SUM`, etc.) is also valid as the inner — the
// SQL spec defines them all as windowable.
func Over(inner Expr, w *Window) Expr { return overExpr{inner: inner, w: w} }

type overExpr struct {
	inner Expr
	w     *Window
}

func (e overExpr) ToSQL(d Dialect, g *SQLGuard) (string, []any, error) {
	if e.inner == nil {
		return "", nil, fmt.Errorf("%w: Over requires a non-nil inner expression", ErrInvalidQuery)
	}
	if e.w == nil {
		return "", nil, fmt.Errorf("%w: Over requires a non-nil Window", ErrInvalidQuery)
	}
	isql, iargs, err := e.inner.ToSQL(d, g)
	if err != nil {
		return "", nil, err
	}
	wsql, wargs, err := e.w.toSQL(d, g)
	if err != nil {
		return "", nil, err
	}
	args := append([]any{}, iargs...)
	args = append(args, wargs...)
	return isql + " OVER (" + wsql + ")", args, nil
}

// --- Window function leaves ---
//
// The plain Func() AST node validates against `astFunctionWhitelist`,
// which deliberately excludes window functions because most of them
// (RANK, ROW_NUMBER, LAG, LEAD) are syntactically restricted to OVER
// (...) contexts that the whitelist doesn't model. These dedicated
// leaves emit the literal SQL function instead, so they bypass the
// whitelist while keeping the rest of the AST's safety contract intact
// (no user input reaches the SQL surface — names are constants).

type windowFuncExpr struct {
	name string
	args []Expr
}

func (w windowFuncExpr) ToSQL(d Dialect, g *SQLGuard) (string, []any, error) {
	var b strings.Builder
	b.WriteString(w.name)
	b.WriteByte('(')
	var args []any
	for i, a := range w.args {
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

// RowNumber renders `ROW_NUMBER()`. Meaningless outside Over().
func RowNumber() Expr { return windowFuncExpr{name: "ROW_NUMBER"} }

// Rank renders `RANK()`.
func Rank() Expr { return windowFuncExpr{name: "RANK"} }

// DenseRank renders `DENSE_RANK()`.
func DenseRank() Expr { return windowFuncExpr{name: "DENSE_RANK"} }

// Lag renders `LAG(<col>, <offset>)`. The offset is bound as a parameter
// so the path is uniform with the rest of the AST — no SQL-surface
// integers, no per-dialect numeric formatting concerns.
func Lag(col Expr, offset int) Expr {
	return windowFuncExpr{name: "LAG", args: []Expr{col, Lit(offset)}}
}

// Lead renders `LEAD(<col>, <offset>)`.
func Lead(col Expr, offset int) Expr {
	return windowFuncExpr{name: "LEAD", args: []Expr{col, Lit(offset)}}
}
