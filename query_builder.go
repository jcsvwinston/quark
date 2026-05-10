// Copyright 2026 jcsvwinston
// SPDX-License-Identifier: Apache-2.0

package quark

import (
	"context"
	"fmt"
	"time"
)

// Scope is a reusable query modifier — a function that receives and returns a *Query[T].
// Scopes can be composed via Apply().
type Scope[T any] func(*Query[T]) *Query[T]

// condition represents a WHERE clause condition.
type condition struct {
	column    string
	operator  string
	value     any
	logic     string      // "AND" or "OR" (default "AND")
	group     []condition // sub-conditions for grouping
	isRaw     bool        // if true, column is not quoted (used for JSON/Expressions)
	extraArgs []any       // additional bind args carried by the column SQL fragment
	// (e.g. JSON path components). The fragment uses '?' as a neutral
	// bind marker that buildWhereClause substitutes for the dialect's
	// placeholder syntax at the correct argIndex.
}

// order represents an ORDER BY clause.
type order struct {
	column string
	desc   bool
}

// join represents a JOIN clause.
type join struct {
	joinType string // "INNER JOIN", "LEFT JOIN", "RIGHT JOIN"
	table    string
	onClause string
	args     []any
}

// BaseQuery holds the non-generic state of a database query.
type BaseQuery struct {
	client  *Client
	ctx     context.Context
	table   string
	schema  string // optional schema prefix for multi-tenant isolation
	dialect Dialect
	guard   *SQLGuard
	pk      pkMeta
	exec    Executor // *sql.DB or *sql.Tx
	meta    *ModelMeta

	// Query state (cloned on each builder method)
	selectCols  []string
	where       []condition
	orderBy     []order
	joins       []join
	preloads    []string
	limit       int
	offset      int
	hasLimit    bool   // tracks if Limit() was explicitly called
	unscoped    bool   // if true, soft-delete filter is dropped (WithTrashed semantics)
	onlyTrashed bool   // if true, the soft-delete filter is inverted to IS NOT NULL
	tenantID    string // for RowLevelSecurity isolation
	tenantCol   string // column name for tenant isolation
	cache       CacheConfig
	groupBy     []string    // GROUP BY columns
	having      []condition // HAVING conditions
	distinct    bool        // SELECT DISTINCT
	err         error       // stores initialization error from ClientProvider
}

// Query represents a type-safe database query builder for model T.
// All builder methods return a new Query (immutable/clone pattern) for thread-safety.
// Execution methods are in query_exec.go and query_crud.go
type Query[T any] struct {
	BaseQuery
}

// fullTableName returns the table name optionally prefixed by a schema.
func (q *BaseQuery) fullTableName() string {
	if q.schema != "" {
		return q.dialect.Quote(q.schema) + "." + q.dialect.Quote(q.table)
	}
	return q.dialect.Quote(q.table)
}

// clone creates a shallow copy of the Query with deep-copied slices.
// This ensures builder methods are safe for concurrent use from a shared base.
func (q *Query[T]) clone() *Query[T] {
	c := *q // shallow copy (copies all scalar fields)
	c.where = append([]condition(nil), q.where...)
	c.orderBy = append([]order(nil), q.orderBy...)
	c.selectCols = append([]string(nil), q.selectCols...)
	c.joins = append([]join(nil), q.joins...)
	c.preloads = append([]string(nil), q.preloads...)
	c.groupBy = append([]string(nil), q.groupBy...)
	c.having = append([]condition(nil), q.having...)
	c.unscoped = q.unscoped
	c.onlyTrashed = q.onlyTrashed
	c.distinct = q.distinct
	c.tenantID = q.tenantID
	c.tenantCol = q.tenantCol
	c.cache = q.cache
	return &c
}

// Preload specifies relations to load automatically.
func (q *Query[T]) Preload(relations ...string) *Query[T] {
	c := q.clone()
	c.preloads = append(c.preloads, relations...)
	return c
}

// Unscoped ignores soft-delete filters for the query, returning both
// trashed and non-trashed rows. Equivalent to WithTrashed; kept for
// backward compatibility.
func (q *Query[T]) Unscoped() *Query[T] {
	c := q.clone()
	c.unscoped = true
	c.onlyTrashed = false
	return c
}

// WithTrashed returns a query that includes both soft-deleted (trashed) and
// live rows — the same effect as Unscoped, named for parity with the
// scope-driven idiom that other ORMs use. Only meaningful when the model
// carries a deleted_at column.
func (q *Query[T]) WithTrashed() *Query[T] {
	c := q.clone()
	c.unscoped = true
	c.onlyTrashed = false
	return c
}

// OnlyTrashed returns a query that filters down to soft-deleted rows
// (deleted_at IS NOT NULL) so callers can list, restore, or hard-delete
// the trash. A no-op when the model has no deleted_at column.
func (q *Query[T]) OnlyTrashed() *Query[T] {
	c := q.clone()
	c.unscoped = false
	c.onlyTrashed = true
	return c
}

// Select specifies columns to select. If empty, all columns are selected.
func (q *Query[T]) Select(columns ...string) *Query[T] {
	c := q.clone()
	c.selectCols = columns
	return c
}

// Where adds a WHERE condition with AND logic.
func (q *Query[T]) Where(column string, operator string, value any) *Query[T] {
	c := q.clone()
	c.where = append(c.where, condition{
		column:   column,
		operator: operator,
		value:    value,
		logic:    "AND",
	})
	return c
}

// WhereIn adds a WHERE ... IN condition.
func (q *Query[T]) WhereIn(column string, values []any) *Query[T] {
	c := q.clone()
	c.where = append(c.where, condition{
		column:   column,
		operator: "IN",
		value:    values,
		logic:    "AND",
	})
	return c
}

// WhereBetween adds a WHERE ... BETWEEN condition.
func (q *Query[T]) WhereBetween(column string, start, end any) *Query[T] {
	c := q.clone()
	c.where = append(c.where, condition{
		column:   column,
		operator: "BETWEEN",
		value:    []any{start, end},
		logic:    "AND",
	})
	return c
}

// cloneForGroup returns a BaseQuery that carries the parent's isolation and context
// state (tenantID/tenantCol/schema/cache/client/dialect/limits/etc.) but with the
// query-shape slices (where/orderBy/joins/preloads/groupBy/having/selectCols)
// left empty so a callback can build a fresh sub-clause.
//
// When the parent has an active RowLevelSecurity tenantID, the tenant predicate
// is pre-injected into the returned where slice so any group built on top of it
// inherits the isolation filter. This pre-injection is intentionally redundant
// with the one in client.go's For[T] constructor: the constructor protects the
// outer query, and this protects the OR/group sub-clause. Removing either side
// re-opens the precedence leak (`A AND B OR C` parses as `(A AND B) OR C`), so
// keep both.
//
// Internal helper. Not part of the public API.
func (b *BaseQuery) cloneForGroup() BaseQuery {
	c := BaseQuery{
		client:      b.client,
		ctx:         b.ctx,
		table:       b.table,
		schema:      b.schema,
		dialect:     b.dialect,
		guard:       b.guard,
		pk:          b.pk,
		exec:        b.exec,
		meta:        b.meta,
		tenantID:    b.tenantID,
		tenantCol:   b.tenantCol,
		cache:       b.cache,
		limit:       b.limit,
		offset:      b.offset,
		hasLimit:    b.hasLimit,
		unscoped:    b.unscoped,
		onlyTrashed: b.onlyTrashed,
		err:         b.err,
	}
	if c.tenantID != "" && c.tenantCol != "" {
		c.where = []condition{{
			column:   c.tenantCol,
			operator: "=",
			value:    c.tenantID,
			logic:    "AND",
		}}
	}
	return c
}

// Or adds an OR condition group. The callback receives a fresh Query to build conditions.
// All conditions within the callback are grouped with AND and joined to the outer query with OR.
//
// Example:
//
//	quark.For[User](ctx, client).
//	    Where("active", "=", true).
//	    Or(func(q *Query[User]) *Query[User] {
//	        return q.Where("role", "=", "admin").Where("role", "=", "superadmin")
//	    }).List()
//
// Generates: WHERE "active" = $1 OR ("role" = $2 AND "role" = $3)
//
// Under the RowLevelSecurity tenant strategy the OR group inherits the parent's
// tenant_id predicate so it cannot escape isolation via SQL operator precedence.
func (q *Query[T]) Or(fn func(*Query[T]) *Query[T]) *Query[T] {
	blank := &Query[T]{BaseQuery: q.cloneForGroup()}
	result := fn(blank)

	c := q.clone()
	c.where = append(c.where, condition{
		logic: "OR",
		group: result.where,
	})
	return c
}

// OrderBy adds an ORDER BY clause.
func (q *Query[T]) OrderBy(column string, direction string) *Query[T] {
	c := q.clone()
	c.orderBy = append(c.orderBy, order{
		column: column,
		desc:   direction == "DESC" || direction == "desc",
	})
	return c
}

// Limit sets the maximum number of rows to return.
func (q *Query[T]) Limit(n int) *Query[T] {
	c := q.clone()
	c.limit = n
	c.hasLimit = true
	return c
}

// Offset sets the number of rows to skip.
func (q *Query[T]) Offset(n int) *Query[T] {
	c := q.clone()
	c.offset = n
	return c
}

// Join adds an INNER JOIN clause.
//
// The on argument must match the minimal identifier-only grammar that
// guard.ValidateJoinOn enforces (e.g. "users.id = orders.user_id" or
// "users.id = orders.user_id AND users.tenant_id = orders.tenant_id").
// Literals, function calls, subqueries, and parentheses are rejected;
// drop down to RawQuery for shapes outside the grammar. Invalid input
// surfaces ErrInvalidJoin at execution time (List, First, Iter, ...).
//
// Deprecated: the string-raw form will be removed in v0.4 in favor of a
// structured builder Join(table).On(col, op, otherCol). Track the
// migration in docs/MIGRATION_v0.2.0.md.
//
// Example:
//
//	quark.For[Order](ctx, client).
//	    Join("users", "users.id = orders.user_id").
//	    List()
func (q *Query[T]) Join(table, on string) *Query[T] {
	c := q.clone()
	c.joins = append(c.joins, join{joinType: "INNER JOIN", table: table, onClause: on})
	return c
}

// LeftJoin adds a LEFT JOIN clause. The on grammar matches Join — see its
// docstring for accepted shapes and the v0.4 deprecation notice.
//
// Deprecated: see Join.
func (q *Query[T]) LeftJoin(table, on string) *Query[T] {
	c := q.clone()
	c.joins = append(c.joins, join{joinType: "LEFT JOIN", table: table, onClause: on})
	return c
}

// RightJoin adds a RIGHT JOIN clause. The on grammar matches Join — see its
// docstring for accepted shapes and the v0.4 deprecation notice.
//
// Deprecated: see Join.
func (q *Query[T]) RightJoin(table, on string) *Query[T] {
	c := q.clone()
	c.joins = append(c.joins, join{joinType: "RIGHT JOIN", table: table, onClause: on})
	return c
}

// Cache enables caching for this query results with the given TTL.
func (q *Query[T]) Cache(ttl time.Duration, tags ...string) *Query[T] {
	c := q.clone()
	c.cache = CacheConfig{
		TTL:     ttl,
		Tags:    tags,
		Enabled: true,
	}
	// Automatically add the table name as a tag if not provided
	if len(c.cache.Tags) == 0 && q.table != "" {
		c.cache.Tags = []string{q.table}
	}
	return c
}

// WhereNot adds a WHERE NOT condition with AND logic.
//
// Example:
//
//	quark.For[User](ctx, client).WhereNot("active", "=", false).List()
//
// Generates: WHERE NOT ("active" = $1)
func (q *Query[T]) WhereNot(column string, operator string, value any) *Query[T] {
	c := q.clone()
	c.where = append(c.where, condition{
		column:   column,
		operator: operator,
		value:    value,
		logic:    "AND NOT",
	})
	return c
}

// Distinct adds SELECT DISTINCT to the query.
func (q *Query[T]) Distinct() *Query[T] {
	c := q.clone()
	c.distinct = true
	return c
}

// GroupBy adds a GROUP BY clause.
func (q *Query[T]) GroupBy(columns ...string) *Query[T] {
	c := q.clone()
	c.groupBy = append(c.groupBy, columns...)
	return c
}

// Having adds a HAVING condition (used together with GroupBy).
func (q *Query[T]) Having(column string, operator string, value any) *Query[T] {
	c := q.clone()
	c.having = append(c.having, condition{
		column:   column,
		operator: operator,
		value:    value,
		logic:    "AND",
	})
	return c
}

// Apply applies one or more Scope functions to the query.
// Scopes are composable, reusable query fragments.
//
// Example:
//
//	activeUsers := func(q *quark.Query[User]) *quark.Query[User] {
//	    return q.Where("active", "=", true)
//	}
//	users, _ := quark.For[User](ctx, client).Apply(activeUsers).List()
func (q *Query[T]) Apply(scopes ...Scope[T]) *Query[T] {
	current := q
	for _, s := range scopes {
		current = s(current)
	}
	return current
}

// WhereJSON adds a WHERE condition for a JSON field.
// column is the JSON column name, path is a dotted key path within the JSON
// object (e.g. "user.name"). The path is validated and bound as a parameter
// — never interpolated into the SQL surface — so it cannot carry SQL
// injection. See guard.ValidateJSONPath for the accepted grammar.
//
// On invalid path the error is stashed on the query and surfaces at execution
// time (List, First, etc.), wrapping ErrInvalidJSONPath.
func (q *Query[T]) WhereJSON(column, path, operator string, value any) *Query[T] {
	c := q.clone()
	frag, pathArgs, err := q.dialect.JSONExtract(column, path)
	if err != nil {
		// guard.ValidateJSONPath returns a descriptive message; wrap with the
		// public sentinel so callers can errors.Is(err, ErrInvalidJSONPath)
		// and reach the underlying detail with errors.Unwrap.
		c.err = fmt.Errorf("%w: %v", ErrInvalidJSONPath, err)
		return c
	}
	c.where = append(c.where, condition{
		column:    frag,
		operator:  operator,
		value:     value,
		logic:     "AND",
		isRaw:     true,
		extraArgs: pathArgs,
	})
	return c
}

// notifyObservers notifies all registered observers of a query event.
func (q *BaseQuery) notifyObservers(event QueryEvent) {
	if q.client == nil {
		return
	}
	for _, obs := range q.client.observers {
		obs.ObserveQuery(event)
	}
}
