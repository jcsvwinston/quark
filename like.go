// Copyright 2026 jcsvwinston
// SPDX-License-Identifier: Apache-2.0

package quark

import (
	"fmt"
	"strings"
)

// likeEscapeChar is the escape character every escaped LIKE Quark emits
// declares. A backslash, because it is what every engine's users already
// write — and declared explicitly on every statement, because what a bare
// backslash means in a LIKE pattern is where the engines disagree (QK-25):
// PostgreSQL, MySQL and MariaDB read it as an escape with no ESCAPE clause,
// SQLite, SQL Server and Oracle read it as an ordinary character.
const likeEscapeChar = `\`

// EscapeLike escapes text so that a LIKE pattern built around it matches the
// text literally: `%`, `_`, `[` and the backslash itself are prefixed with a
// backslash. The result is meant for the escaped LIKE surfaces —
// [Query.WhereLike], [TypedStringColumn.LikeEscaped], [Like] — which declare
// the escape character on the statement; in a plain
// `Where(col, "LIKE", …)` the backslash keeps whatever meaning the engine
// gives it by default.
//
// `[` is escaped on every engine, not only on SQL Server where it opens a
// character class: with the escape character declared, `\[` means a literal
// bracket everywhere, so the escaped value is the same on all six engines.
//
// [Query.WhereContains], [Query.WhereStartsWith] and [Query.WhereEndsWith]
// call this for you; reach for it when composing a pattern by hand:
//
//	quark.For[Doc](ctx, c).WhereLike("title", "%"+quark.EscapeLike(userText)+"%")
func EscapeLike(text string) string {
	return likeEscaper.Replace(text)
}

var likeEscaper = strings.NewReplacer(
	likeEscapeChar, likeEscapeChar+likeEscapeChar,
	"%", likeEscapeChar+"%",
	"_", likeEscapeChar+"_",
	"[", likeEscapeChar+"[",
)

// likeEscapeTail is the `ESCAPE '<c>'` clause for the dialect, spelled the way
// that engine's string-literal rules require. MySQL and MariaDB read a
// backslash inside a string literal as an escape, so a lone one leaves the
// literal unterminated and the clause needs `'\\'`; every other engine takes
// `'\'` as a one-character literal — and SQLite REJECTS the doubled form as
// "more than one character". The tail therefore cannot be one string for
// six engines, which is why it is spelled here and proven per engine in
// internal/enginesuite (control LIKE-07).
//
// Under MySQL's NO_BACKSLASH_ESCAPES mode the doubled form would itself be
// two characters; that mode is not one Quark's drivers set, and the
// limitation is documented rather than probed at run time.
func likeEscapeTail(d Dialect) string {
	switch d.Name() {
	case "mysql", "mariadb":
		return `ESCAPE '\\'`
	}
	return `ESCAPE '\'`
}

// likePattern is the operator + value pair the escaped surfaces lower into a
// condition: the pattern is validated once, here, so the five renderers and
// the AST share one rule.
func likePattern(g *SQLGuard, pattern string) error {
	return g.ValidateLikePattern(pattern, likeEscapeChar)
}

// WhereLike adds `column LIKE pattern` with the escape character declared on
// the statement (`ESCAPE '\'`), so the pattern means the same thing on every
// engine: `%` and `_` are wildcards, and a backslash makes the next character
// literal. Compose the pattern from user text with [EscapeLike].
//
// It differs from Where(column, "LIKE", pattern) in exactly that: the plain
// form hands the engine an opaque pattern and lets the engine's own default
// decide what a backslash means, which is not the same on SQLite, SQL Server
// and Oracle as it is on PostgreSQL and MySQL (QK-25). The plain form is
// unchanged — its meaning is published — and this one is the portable form.
//
// A pattern that ends in a dangling escape character is refused with
// ErrInvalidQuery before it reaches the engine: PostgreSQL rejects it, other
// engines match nothing or match the backslash, and none of those is what
// the caller meant.
func (q *Query[T]) WhereLike(column, pattern string) *Query[T] {
	return q.whereLike(column, "LIKE", pattern)
}

// WhereNotLike is the negation of [Query.WhereLike], with the same escape
// character declared.
func (q *Query[T]) WhereNotLike(column, pattern string) *Query[T] {
	return q.whereLike(column, "NOT LIKE", pattern)
}

// WhereContains adds a case-sensitive "contains" search for text the user
// typed: the text is escaped with [EscapeLike], wrapped in `%`, and bound to
// a LIKE that declares its escape character. A `%` or `_` in the text is
// matched as that character, on every engine.
func (q *Query[T]) WhereContains(column, text string) *Query[T] {
	return q.whereLike(column, "LIKE", "%"+EscapeLike(text)+"%")
}

// WhereStartsWith adds a prefix search for text the user typed; see
// [Query.WhereContains] for what happens to wildcards in it.
func (q *Query[T]) WhereStartsWith(column, text string) *Query[T] {
	return q.whereLike(column, "LIKE", EscapeLike(text)+"%")
}

// WhereEndsWith adds a suffix search for text the user typed; see
// [Query.WhereContains] for what happens to wildcards in it.
func (q *Query[T]) WhereEndsWith(column, text string) *Query[T] {
	return q.whereLike(column, "LIKE", "%"+EscapeLike(text))
}

func (q *Query[T]) whereLike(column, operator, pattern string) *Query[T] {
	c := q.clone()
	if err := likePattern(c.guard, pattern); err != nil {
		c.err = err
		return c
	}
	c.where = ownedAppend(c.where, condition{
		column:   column,
		operator: operator,
		value:    pattern,
		logic:    "AND",
		escape:   true,
	})
	return c
}

// LikeEscaped builds `column LIKE pattern` with the escape character declared
// on the statement — the typed counterpart of [Query.WhereLike]. [TypedStringColumn.Like]
// keeps the plain, engine-default form.
func (c TypedStringColumn) LikeEscaped(pattern string) Predicate {
	return Predicate{column: c.name, operator: "LIKE", value: pattern, escape: true}
}

// NotLikeEscaped is the negation of [TypedStringColumn.LikeEscaped].
func (c TypedStringColumn) NotLikeEscaped(pattern string) Predicate {
	return Predicate{column: c.name, operator: "NOT LIKE", value: pattern, escape: true}
}

// Contains builds a "contains" search for text the user typed, escaped with
// [EscapeLike] — the typed counterpart of [Query.WhereContains]. This is the
// accessor a search box should call: a `%` the user typed is matched as a
// percent sign, not as a wildcard.
func (c TypedStringColumn) Contains(text string) Predicate {
	return c.LikeEscaped("%" + EscapeLike(text) + "%")
}

// StartsWith builds a prefix search for text the user typed; see
// [TypedStringColumn.Contains].
func (c TypedStringColumn) StartsWith(text string) Predicate {
	return c.LikeEscaped(EscapeLike(text) + "%")
}

// EndsWith builds a suffix search for text the user typed; see
// [TypedStringColumn.Contains].
func (c TypedStringColumn) EndsWith(text string) Predicate {
	return c.LikeEscaped("%" + EscapeLike(text))
}

// Like is the AST form of [Query.WhereLike]: `lhs LIKE pattern` with the
// escape character declared, so the pattern means the same on every engine.
// The plain, engine-default form remains Cmp(lhs, "LIKE", Lit(pattern)).
func Like(lhs Expr, pattern string) Expr { return likeExpr{lhs: lhs, pattern: pattern} }

// NotLike is the negation of [Like].
func NotLike(lhs Expr, pattern string) Expr {
	return likeExpr{lhs: lhs, pattern: pattern, negate: true}
}

// Contains is the AST form of [Query.WhereContains].
func Contains(lhs Expr, text string) Expr { return Like(lhs, "%"+EscapeLike(text)+"%") }

// StartsWith is the AST form of [Query.WhereStartsWith].
func StartsWith(lhs Expr, text string) Expr { return Like(lhs, EscapeLike(text)+"%") }

// EndsWith is the AST form of [Query.WhereEndsWith].
func EndsWith(lhs Expr, text string) Expr { return Like(lhs, "%"+EscapeLike(text)) }

type likeExpr struct {
	lhs     Expr
	pattern string
	negate  bool
}

func (e likeExpr) ToSQL(d Dialect, g *SQLGuard) (string, []any, error) {
	if err := likePattern(g, e.pattern); err != nil {
		return "", nil, err
	}
	lsql, largs, err := e.lhs.ToSQL(d, g)
	if err != nil {
		return "", nil, err
	}
	op := "LIKE"
	if e.negate {
		op = "NOT LIKE"
	}
	args := append([]any{}, largs...)
	args = append(args, e.pattern)
	return fmt.Sprintf("%s %s ? %s", lsql, op, likeEscapeTail(d)), args, nil
}

// likeTail is what a renderer appends after the bound pattern of an escaped
// LIKE condition, and nothing for every other condition. One helper, called
// from every renderer that writes a condition, so a condition built through
// WhereLike reaches UPDATE and DELETE with the same clause a SELECT gets.
func likeTail(cond condition, d Dialect) string {
	if !cond.escape {
		return ""
	}
	return " " + likeEscapeTail(d)
}
