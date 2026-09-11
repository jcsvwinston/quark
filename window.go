// Copyright 2026 jcsvwinston
// SPDX-License-Identifier: Apache-2.0

package quark

import (
	"fmt"
	"strconv"
	"strings"
)

// Window models the `OVER (...)` clause for a window-function expression.
// Build with NewWindow() and chain PartitionBy / OrderBy. The chain is
// immutable: each method returns a fresh copy so a Window definition can
// be reused across multiple Over() calls without aliasing.
type Window struct {
	partitionBy []Expr
	orderBy     []windowOrder
	frame       string // rendered frame clause, empty when the caller set none
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

// Rows sets a ROWS frame: which rows of the partition the window function
// actually sees, counted in physical rows.
//
// Without a frame, a window with an ORDER BY defaults to every row from the
// start of the partition through the current one. That default is what makes
// SUM() OVER (ORDER BY …) a running total — and it is also why a "moving
// average" written without a frame is silently a running average instead.
//
//	// three-row moving average
//	quark.NewWindow().OrderBy(quark.Col("day"), false).Rows(quark.Preceding(2), quark.CurrentRow())
//
// Bounds come from [Preceding], [Following], [CurrentRow], [UnboundedPreceding]
// and [UnboundedFollowing]. The offsets render as literals, not bind
// parameters: every engine requires a constant there.
func (w *Window) Rows(start, end FrameBound) *Window {
	cp := *w
	cp.frame = "ROWS BETWEEN " + string(start) + " AND " + string(end)
	return &cp
}

// Range sets a RANGE frame. It counts in VALUES of the ORDER BY expression
// rather than in rows, so peer rows — those comparing equal — enter or leave
// the frame together.
func (w *Window) Range(start, end FrameBound) *Window {
	cp := *w
	cp.frame = "RANGE BETWEEN " + string(start) + " AND " + string(end)
	return &cp
}

// FrameBound is one edge of a window frame. The constructors below are the
// only way to make one, so no caller string reaches the frame clause.
type FrameBound string

// UnboundedPreceding is the start of the partition.
func UnboundedPreceding() FrameBound { return "UNBOUNDED PRECEDING" }

// UnboundedFollowing is the end of the partition.
func UnboundedFollowing() FrameBound { return "UNBOUNDED FOLLOWING" }

// CurrentRow is the row being computed.
func CurrentRow() FrameBound { return "CURRENT ROW" }

// Preceding is n rows (ROWS) or n units of the order value (RANGE) back.
func Preceding(n int) FrameBound { return FrameBound(itoaFrame(n) + " PRECEDING") }

// Following is n rows or units forward.
func Following(n int) FrameBound { return FrameBound(itoaFrame(n) + " FOLLOWING") }

// itoaFrame renders a non-negative bound offset. A negative offset is
// clamped to 0: every engine rejects it, and "0 PRECEDING" fails loudly at
// the engine instead of emitting malformed SQL here.
func itoaFrame(n int) string {
	if n < 0 {
		n = 0
	}
	return strconv.Itoa(n)
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
	if w.frame != "" {
		if b.Len() > 0 {
			b.WriteString(" ")
		}
		b.WriteString(w.frame)
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

// NTile renders `NTILE(<buckets>)`: the bucket a row falls in when the
// partition is split into n roughly equal groups. The bucket count is
// bound as a parameter, same as Lag/Lead's offset.
func NTile(buckets int) Expr {
	return windowFuncExpr{name: "NTILE", args: []Expr{Lit(buckets)}}
}

// PercentRank renders `PERCENT_RANK()`: the row's rank as a fraction of
// the partition, from 0 to 1.
func PercentRank() Expr { return windowFuncExpr{name: "PERCENT_RANK"} }

// CumeDist renders `CUME_DIST()`: the cumulative distribution — the
// fraction of partition rows at or before this one.
func CumeDist() Expr { return windowFuncExpr{name: "CUME_DIST"} }

// FirstValue renders `FIRST_VALUE(<col>)`, the column's value in the
// first row of the window frame.
func FirstValue(col Expr) Expr {
	return windowFuncExpr{name: "FIRST_VALUE", args: []Expr{col}}
}

// LastValue renders `LAST_VALUE(<col>)`. Read the frame note on Window
// before reaching for it: with the default frame the "last" row is the
// current one, which is rarely what the caller means.
func LastValue(col Expr) Expr {
	return windowFuncExpr{name: "LAST_VALUE", args: []Expr{col}}
}

// NthValue renders `NTH_VALUE(<col>, <n>)`, 1-based.
//
// Five of the six engines have it. SQL Server does NOT: it fails with
// "'NTH_VALUE' is not a recognized built-in function name". Quark does not
// emulate it there — an emulation would have different NULL and frame
// behaviour — so a query using it is not portable to SQL Server. Reach for
// Lag/Lead or a ranked subquery if you need that engine.
func NthValue(col Expr, n int) Expr {
	return windowFuncExpr{name: "NTH_VALUE", args: []Expr{col, Lit(n)}}
}
