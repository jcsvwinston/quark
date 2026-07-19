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
// spelling. PostgreSQL, MSSQL, SQLite, and MariaDB use the SQL standard
// names; Oracle spells EXCEPT as MINUS. MySQL is the one holdout: it only
// gained INTERSECT/EXCEPT in 8.0.31 (2022-10), and quark cannot assume a
// minimum *minor* version without a runtime probe, so those two return
// ErrUnsupportedFeature on the mysql dialect. MariaDB has had them since
// 10.3 (ALL variants since 10.5) — within the 10.5 floor the dialect
// already assumes for RETURNING — so they are enabled there (QK-P2-2).
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
	case "mysql":
		if kind == "INTERSECT" || kind == "EXCEPT" {
			return "", fmt.Errorf("%w: %s requires MySQL 8.0.31+ for %s, which quark cannot assume without a version probe — use a JOIN-based rewrite (MariaDB 10.3+ is supported)", ErrUnsupportedFeature, name, kind)
		}
	case "sqlite":
		if all && (kind == "INTERSECT" || kind == "EXCEPT") {
			return "", fmt.Errorf("%w: SQLite does not support INTERSECT ALL / EXCEPT ALL", ErrUnsupportedFeature)
		}
	case "mssql", "sqlserver":
		// T-SQL has INTERSECT and EXCEPT but no ALL variants of either.
		// Without this guard the renderer emitted `INTERSECT ALL`, which
		// SQL Server rejects with a misleading parse error rather than a
		// clean "unsupported" — reject it here so the caller gets the same
		// ErrUnsupportedFeature every other unsupported dialect returns.
		if all && (kind == "INTERSECT" || kind == "EXCEPT") {
			return "", fmt.Errorf("%w: SQL Server does not support INTERSECT ALL / EXCEPT ALL", ErrUnsupportedFeature)
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

// Intersect renders `INTERSECT` between the base and the operand: the rows
// present in both, deduplicated.
//
// Supported on PostgreSQL, MariaDB (10.3+), MSSQL, Oracle and SQLite. MySQL
// returns ErrUnsupportedFeature at render time — INTERSECT needs 8.0.31+ and
// Quark will not assume the server version without probing it.
func (q *Query[T]) Intersect(other *Query[T]) *Query[T] {
	return q.attachSetOp("INTERSECT", false, other)
}

// IntersectAll is the multiset variant: `INTERSECT ALL` keeps duplicate rows
// rather than collapsing them, so a row appearing twice on both sides comes
// back twice.
//
// Narrower support than Intersect: PostgreSQL and MariaDB (10.5+) only.
// SQL Server, SQLite and Oracle have no INTERSECT ALL, and MySQL has no
// INTERSECT at all; every one of them returns ErrUnsupportedFeature at render
// time.
func (q *Query[T]) IntersectAll(other *Query[T]) *Query[T] {
	return q.attachSetOp("INTERSECT", true, other)
}

// Except renders `EXCEPT` (spelled `MINUS` on Oracle): the rows in the base
// that are not in the operand, deduplicated.
//
// Supported on PostgreSQL, MariaDB (10.3+), MSSQL, Oracle and SQLite. MySQL
// returns ErrUnsupportedFeature at render time, for the same version-probe
// reason as Intersect.
func (q *Query[T]) Except(other *Query[T]) *Query[T] {
	return q.attachSetOp("EXCEPT", false, other)
}

// ExceptAll is the multiset variant: `EXCEPT ALL` subtracts by multiplicity
// instead of deduplicating — a row present three times on the left and once on
// the right comes back twice.
//
// Narrower support than Except: PostgreSQL and MariaDB (10.5+) only. SQL Server
// has no EXCEPT ALL, Oracle has no MINUS ALL, SQLite has no EXCEPT ALL, and
// MySQL has no EXCEPT at all; every one of them returns ErrUnsupportedFeature
// at render time.
func (q *Query[T]) ExceptAll(other *Query[T]) *Query[T] {
	return q.attachSetOp("EXCEPT", true, other)
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
//   - The chain cannot mix operator kinds (UNION vs INTERSECT vs
//     EXCEPT) — engines disagree on the precedence of a flat mix; see
//     the mixed-kind guard below. ALL variants of the already-chained
//     kind are fine.
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
	// Mixed-kind guard (QK5-1). A chain like A.Union(B).Intersect(C)
	// renders flat, and the engines disagree on how a flat mix parses:
	// PostgreSQL, MySQL, MariaDB and SQL Server give INTERSECT higher
	// precedence than UNION/EXCEPT, while SQLite and Oracle evaluate
	// strictly left to right — the same statement silently returns
	// different rows depending on the engine. A statement may therefore
	// chain only ONE operator kind.
	//
	// "Kind" is the operator word (UNION / INTERSECT / EXCEPT); the
	// `all` flag is deliberately not part of it. UNION and UNION ALL
	// (and the other ALL variants) carry the same precedence as their
	// distinct form on every engine, so Union+UnionAll chains remain
	// legal and deterministic (equal precedence associates left).
	// Cross-kind mixes are rejected wholesale — including UNION↔EXCEPT,
	// which the SQL standard parses left-to-right at equal precedence —
	// because a single uniform rule is easier to reason about than a
	// per-pair matrix, and Oracle documents that MINUS/INTERSECT
	// precedence may change in a future release. Every earlier entry
	// passed this same guard, so checking the first one suffices.
	if len(c.setOps) > 0 && c.setOps[0].kind != kind {
		c.err = fmt.Errorf("%w: mixing %s with %s in one statement is not supported — engines disagree on set-op precedence; materialize each step into its own query instead", ErrUnsupportedFeature, c.setOps[0].kind, kind)
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
