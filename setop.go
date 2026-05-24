// Copyright 2026 jcsvwinston
// SPDX-License-Identifier: Apache-2.0

package quark

import (
	"fmt"
	"strings"
)

// setOpEntry captures one set-operator branch attached to a query. The
// sql carries '?' bind markers (rendered by Union/Intersect/Except via
// qmarkDialect at attach time); buildSelect substitutes them for the
// dialect's placeholder syntax at outer render time.
type setOpEntry struct {
	kind string // UNION | INTERSECT | EXCEPT
	all  bool
	sql  string
	args []any
}

// setOpKeyword maps the canonical SQL set-op keyword to the dialect's
// spelling. PostgreSQL, MSSQL, SQLite, and MySQL/MariaDB use the SQL
// standard names; Oracle spells EXCEPT as MINUS. MySQL and MariaDB
// don't support INTERSECT or EXCEPT at all (returns ErrUnsupportedFeature).
//
// Kept as a package-level helper rather than a Dialect method so adding
// set-op support doesn't break custom Dialect implementations sitting
// downstream — adding a method to the interface would force them to
// implement it.
func setOpKeyword(d Dialect, kind string, all bool) (string, error) {
	name := strings.ToLower(d.Name())
	switch name {
	case "oracle":
		switch kind {
		case "EXCEPT":
			if all {
				return "", fmt.Errorf("%w: Oracle does not support MINUS ALL — use distinct EXCEPT instead", ErrUnsupportedFeature)
			}
			return "MINUS", nil
		case "INTERSECT":
			if all {
				return "", fmt.Errorf("%w: Oracle does not support INTERSECT ALL", ErrUnsupportedFeature)
			}
		}
	case "mysql", "mariadb":
		if kind == "INTERSECT" || kind == "EXCEPT" {
			return "", fmt.Errorf("%w: %s does not support %s — use a JOIN-based rewrite", ErrUnsupportedFeature, name, kind)
		}
	case "sqlite":
		if all && (kind == "INTERSECT" || kind == "EXCEPT") {
			return "", fmt.Errorf("%w: SQLite does not support INTERSECT ALL / EXCEPT ALL", ErrUnsupportedFeature)
		}
	}
	if all {
		return kind + " ALL", nil
	}
	return kind, nil
}

// Union appends a UNION (DISTINCT) operand to the query. The combined
// statement renders flat — `SELECT ... UNION SELECT ...` — without
// parentheses around the operands, since SQLite's compound-select
// grammar rejects parenthesised operands. The flat form is the
// portable shape across all six target dialects.
//
// Identifier validation runs eagerly on the operand so a malformed
// other-query surfaces at attach time, not at the outer query's exec
// time. Outer-query `OrderBy` / `Limit` apply to the combined result
// (the SQL standard binding); the operand cannot have its own ORDER
// BY / LIMIT (rejected with `ErrUnsupportedFeature`). See attachSetOp
// for the full operand restriction list.
func (q *Query[T]) Union(other *Query[T]) *Query[T] {
	return q.attachSetOp("UNION", false, other)
}

// UnionAll is the multiset variant: `UNION ALL` keeps duplicate rows.
func (q *Query[T]) UnionAll(other *Query[T]) *Query[T] {
	return q.attachSetOp("UNION", true, other)
}

// Intersect renders `INTERSECT` between the base and the operand. Not
// supported on MySQL / MariaDB — those return ErrUnsupportedFeature
// from setOpKeyword at render time.
func (q *Query[T]) Intersect(other *Query[T]) *Query[T] {
	return q.attachSetOp("INTERSECT", false, other)
}

// Except renders `EXCEPT` (or `MINUS` on Oracle). Not supported on
// MySQL / MariaDB.
func (q *Query[T]) Except(other *Query[T]) *Query[T] {
	return q.attachSetOp("EXCEPT", false, other)
}

// attachSetOp captures `other` as a qmark-rendered core (SELECT through
// HAVING — no ORDER BY / LIMIT / lock / nested set-ops) and stashes it
// on the cloned query. The operand is rendered "flat" (without parens)
// because SQLite's compound-select grammar doesn't accept parentheses
// around individual operands, and the SQL standard form
// `SELECT ... <op> SELECT ... ORDER BY ... LIMIT ...` is portable across
// all six target dialects.
//
// Restrictions enforced here (any violation surfaces as
// `ErrUnsupportedFeature`):
//   - The base cannot carry pessimistic locks — the lock suffix would
//     bind to the combined result and most engines' locking semantics
//     don't model that.
//   - The operand cannot carry ORDER BY, LIMIT, OFFSET, lock options,
//     CTEs, or its own set-ops. ORDER BY and LIMIT on the combined
//     result come from the outer (base) query instead.
func (q *Query[T]) attachSetOp(kind string, all bool, other *Query[T]) *Query[T] {
	c := q.clone()
	if other == nil {
		c.err = fmt.Errorf("%w: %s operand is nil", ErrInvalidQuery, kind)
		return c
	}
	if !c.lock.IsZero() {
		c.err = fmt.Errorf("%w: pessimistic lock options on a set-op base are not supported — apply locks on each operand individually", ErrUnsupportedFeature)
		return c
	}
	if len(other.orderBy) > 0 || other.hasLimit || other.offset > 0 {
		c.err = fmt.Errorf("%w: %s operand cannot have ORDER BY / LIMIT / OFFSET — apply on the outer (combined) query instead", ErrUnsupportedFeature, kind)
		return c
	}
	if !other.lock.IsZero() {
		c.err = fmt.Errorf("%w: %s operand cannot have pessimistic-lock options", ErrUnsupportedFeature, kind)
		return c
	}
	if len(other.ctes) > 0 {
		c.err = fmt.Errorf("%w: %s operand cannot define its own CTEs — define them on the outer query", ErrUnsupportedFeature, kind)
		return c
	}
	if len(other.setOps) > 0 {
		c.err = fmt.Errorf("%w: nested set-ops on a %s operand are not supported", ErrUnsupportedFeature, kind)
		return c
	}
	// Render the operand using the qmarkDialect so its `?` markers can be
	// reindexed by the outer buildSelect at the right argIndex. With the
	// restrictions above, the rendered SQL is a SELECT/FROM/JOIN/WHERE/
	// GROUP/HAVING fragment with no trailing clauses to splice around.
	o := other.clone()
	o.dialect = qmarkDialect{Dialect: o.dialect}
	sql, args, err := o.buildSelect()
	if err != nil {
		c.err = err
		return c
	}
	c.setOps = ownedAppend(c.setOps, setOpEntry{kind: kind, all: all, sql: sql, args: args})
	return c
}
