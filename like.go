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
// text literally: `%`, `_` and the backslash itself are prefixed with a
// backslash. Every LIKE Quark renders declares that backslash as the escape
// character on the statement — `Where(col, "LIKE", …)` and
// [Query.WhereLike], [TypedStringColumn.Like] and
// [TypedStringColumn.LikeEscaped], [Cmp] with the operator and [Like] alike —
// so a pattern built around the result matches the text on every engine.
//
// The set is the portable one. SQL Server also reads `[` as the start of a
// character class, but Oracle refuses an escape character in front of
// anything that is not `%`, `_` or the escape itself (ORA-01424), so `[`
// cannot be escaped everywhere. The text searches — [Query.WhereContains]
// and friends — escape it on SQL Server and nowhere else, because they know
// the dialect; when composing a pattern by hand for SQL Server, write `\[`
// yourself.
//
//	quark.For[Doc](ctx, c).WhereLike("title", "%"+quark.EscapeLike(userText)+"%")
func EscapeLike(text string) string {
	return likeEscaper.Replace(text)
}

var likeEscaper = strings.NewReplacer(
	likeEscapeChar, likeEscapeChar+likeEscapeChar,
	"%", likeEscapeChar+"%",
	"_", likeEscapeChar+"_",
)

// likeEscaperMSSQL is EscapeLike plus the bracket: on SQL Server `[` opens a
// character class inside a LIKE pattern, so a user's `[` has to be escaped
// there — and only there, because Oracle rejects `\[` outright (ORA-01424,
// measured on the CI lane).
var likeEscaperMSSQL = strings.NewReplacer(
	likeEscapeChar, likeEscapeChar+likeEscapeChar,
	"%", likeEscapeChar+"%",
	"_", likeEscapeChar+"_",
	"[", likeEscapeChar+"[",
)

// escapeLikeFor escapes user text for the engine it will run on: the portable
// set everywhere, plus `[` on SQL Server.
func escapeLikeFor(d Dialect, text string) string {
	if d.Name() == "mssql" {
		return likeEscaperMSSQL.Replace(text)
	}
	return EscapeLike(text)
}

// likeShape says how a text search wraps the user's text. The pattern is
// composed at the last moment, once the dialect is known, because what needs
// escaping in the text depends on the engine (escapeLikeFor).
type likeShape int

const (
	likeGivenPattern likeShape = iota // the caller composed the pattern
	likeContains                      // %text%
	likeStartsWith                    // text%
	likeEndsWith                      // %text
)

// likePatternFor composes the pattern of a text search for the dialect.
func likePatternFor(d Dialect, shape likeShape, text string) string {
	esc := escapeLikeFor(d, text)
	switch shape {
	case likeContains:
		return "%" + esc + "%"
	case likeStartsWith:
		return esc + "%"
	case likeEndsWith:
		return "%" + esc
	}
	return text
}

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

// likePattern validates a LIKE pattern under the declared escape character:
// the rule is one, here, so the builder doors, the typed predicates and the
// AST refuse the same patterns.
func likePattern(g *SQLGuard, pattern string) error {
	return g.ValidateLikePattern(pattern, likeEscapeChar)
}

// isLikeOperator reports whether the operator, in any case and with
// surrounding spaces, is LIKE or NOT LIKE: the two whose statement declares
// the escape character and whose pattern is validated.
func isLikeOperator(operator string) bool {
	switch strings.ToUpper(strings.TrimSpace(operator)) {
	case "LIKE", "NOT LIKE":
		return true
	}
	return false
}

// checkLikePattern validates the value of a LIKE or NOT LIKE condition before
// it is stored on the query, and nothing for any other operator or for a
// value that is not a pattern. The builder doors that take an operator
// (Where, WhereNot, Having, HavingAggregate, WhereJSON, PreloadWhere; Or
// goes through Where) call it, so a dangling escape is refused at the call
// and never reaches the engine.
func (q *BaseQuery) checkLikePattern(operator string, value any) error {
	if !isLikeOperator(operator) {
		return nil
	}
	pattern, ok := value.(string)
	if !ok {
		return nil
	}
	return likePattern(q.guard, pattern)
}

// WhereLike adds `column LIKE pattern` with the escape character declared on
// the statement (`ESCAPE '\'`), so the pattern means the same thing on every
// engine: `%` and `_` are wildcards, and a backslash makes the next character
// literal. Compose the pattern from user text with [EscapeLike].
//
// It is Where(column, "LIKE", pattern) under a name that says what the
// statement declares: every LIKE Quark renders carries the clause (QK-25,
// QK-32), on the six engines alike, so the two forms are the same statement.
//
// A pattern that ends in a dangling escape character is refused with
// ErrInvalidQuery before it reaches the engine: PostgreSQL rejects it, other
// engines match nothing or match the backslash, and none of those is what
// the caller meant.
func (q *Query[T]) WhereLike(column, pattern string) *Query[T] {
	return q.Where(column, "LIKE", pattern)
}

// WhereNotLike is the negation of [Query.WhereLike], with the same escape
// character declared.
func (q *Query[T]) WhereNotLike(column, pattern string) *Query[T] {
	return q.Where(column, "NOT LIKE", pattern)
}

// WhereContains adds a case-sensitive "contains" search for text the user
// typed: the text is escaped with [EscapeLike], wrapped in `%`, and bound to
// a LIKE that declares its escape character. A `%` or `_` in the text is
// matched as that character, on every engine.
func (q *Query[T]) WhereContains(column, text string) *Query[T] {
	return q.Where(column, "LIKE", likePatternFor(q.dialect, likeContains, text))
}

// WhereStartsWith adds a prefix search for text the user typed; see
// [Query.WhereContains] for what happens to wildcards in it.
func (q *Query[T]) WhereStartsWith(column, text string) *Query[T] {
	return q.Where(column, "LIKE", likePatternFor(q.dialect, likeStartsWith, text))
}

// WhereEndsWith adds a suffix search for text the user typed; see
// [Query.WhereContains] for what happens to wildcards in it.
func (q *Query[T]) WhereEndsWith(column, text string) *Query[T] {
	return q.Where(column, "LIKE", likePatternFor(q.dialect, likeEndsWith, text))
}

// LikeEscaped builds `column LIKE pattern` with the escape character declared
// on the statement — the typed counterpart of [Query.WhereLike]. It is
// [TypedStringColumn.Like] under a name that says what the statement
// declares; the two are the same predicate.
func (c TypedStringColumn) LikeEscaped(pattern string) Predicate {
	return c.Like(pattern)
}

// NotLikeEscaped is the negation of [TypedStringColumn.LikeEscaped], the same
// predicate as [TypedStringColumn.NotLike].
func (c TypedStringColumn) NotLikeEscaped(pattern string) Predicate {
	return c.NotLike(pattern)
}

// Contains builds a "contains" search for text the user typed, escaped with
// [EscapeLike] — the typed counterpart of [Query.WhereContains]. This is the
// accessor a search box should call: a `%` the user typed is matched as a
// percent sign, not as a wildcard.
func (c TypedStringColumn) Contains(text string) Predicate {
	return Predicate{column: c.name, operator: "LIKE", likeShape: likeContains, likeText: text}
}

// StartsWith builds a prefix search for text the user typed; see
// [TypedStringColumn.Contains].
func (c TypedStringColumn) StartsWith(text string) Predicate {
	return Predicate{column: c.name, operator: "LIKE", likeShape: likeStartsWith, likeText: text}
}

// EndsWith builds a suffix search for text the user typed; see
// [TypedStringColumn.Contains].
func (c TypedStringColumn) EndsWith(text string) Predicate {
	return Predicate{column: c.name, operator: "LIKE", likeShape: likeEndsWith, likeText: text}
}

// Like is the AST form of [Query.WhereLike]: `lhs LIKE pattern` with the
// escape character declared, so the pattern means the same on every engine.
// Cmp(lhs, "LIKE", Lit(pattern)) renders the same statement.
func Like(lhs Expr, pattern string) Expr { return likeExpr{lhs: lhs, pattern: pattern} }

// NotLike is the negation of [Like].
func NotLike(lhs Expr, pattern string) Expr {
	return likeExpr{lhs: lhs, pattern: pattern, negate: true}
}

// Contains is the AST form of [Query.WhereContains].
func Contains(lhs Expr, text string) Expr {
	return likeExpr{lhs: lhs, shape: likeContains, pattern: text}
}

// StartsWith is the AST form of [Query.WhereStartsWith].
func StartsWith(lhs Expr, text string) Expr {
	return likeExpr{lhs: lhs, shape: likeStartsWith, pattern: text}
}

// EndsWith is the AST form of [Query.WhereEndsWith].
func EndsWith(lhs Expr, text string) Expr {
	return likeExpr{lhs: lhs, shape: likeEndsWith, pattern: text}
}

type likeExpr struct {
	lhs     Expr
	pattern string    // the pattern, or the user's text when shape is a search
	shape   likeShape // composed against the dialect in ToSQL
	negate  bool
}

func (e likeExpr) ToSQL(d Dialect, g *SQLGuard) (string, []any, error) {
	pattern := likePatternFor(d, e.shape, e.pattern)
	if err := likePattern(g, pattern); err != nil {
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
	args = append(args, pattern)
	return fmt.Sprintf("%s %s ? %s", lsql, op, likeEscapeTail(d)), args, nil
}

// likeTail is what a renderer appends after the bound pattern of a LIKE or
// NOT LIKE condition, and nothing for every other condition. One helper,
// called from every renderer that writes a condition, so a LIKE reaches
// UPDATE and DELETE with the same clause a SELECT gets, whichever door built
// it (QK-25, QK-32).
func likeTail(cond condition, d Dialect) string {
	if !isLikeOperator(cond.operator) {
		return ""
	}
	return " " + likeEscapeTail(d)
}
