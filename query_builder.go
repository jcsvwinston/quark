// Copyright 2026 jcsvwinston
// SPDX-License-Identifier: Apache-2.0

package quark

import (
	"context"
	"fmt"
	"strings"
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
	// tx is the *quark.Tx the query was bound to via [ForTx], or
	// nil when constructed via [For] against a plain Client. CRUD
	// operations queue their After* hooks on tx.afterHooks so the
	// hooks fire after Commit succeeds (F5-4) rather than inline.
	// When tx is nil, hooks run inline (preserving the pre-F5-4
	// behaviour for callers that never opened an explicit
	// transaction).
	tx   *Tx
	meta *ModelMeta

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
	tenantID    string // for RowLevelSecurityClient isolation
	tenantCol   string // column name for tenant isolation
	cache       CacheConfig
	groupBy     []string          // GROUP BY columns
	having      []condition       // HAVING conditions
	distinct    bool              // SELECT DISTINCT
	lock        LockOptions       // pessimistic locking (ForUpdate / ForShare / SkipLocked / NoWait)
	ctes        []cteEntry        // common table expressions (WITH ...) prepended to the SELECT
	selectExprs []selectExprEntry // AST projections rendered in the SELECT list (window funcs, scalar subqueries, aliased computations)
	setOps      []setOpEntry      // UNION / INTERSECT / EXCEPT operands appended after the base SELECT
	err         error             // stores initialization error from ClientProvider

	// typedScanResolved memoizes the F6-2 generated-scanner lookup so the
	// reflect.Type lookup + registry read happen once per query rather than
	// once per row. typedScan is the resolved generated scanner, or nil when
	// codegen is absent for the model or the per-column timezone feature is
	// active for this query (both fall through to the reflection scan path).
	typedScanResolved bool
	typedScan         TypedScanner

	// scanPlanResolved memoizes the reflection scan path's column→field
	// resolution so rows.Columns(), the per-column FieldByCol/findField lookup,
	// and the []any target buffer are computed once per query, not once per row
	// — the columns and field indices are invariant across rows; only the
	// per-row struct changes. scanPlan maps each result column (by position) to
	// a struct field index (-1 = discard); scanDest is the reused Scan-target
	// buffer and scanDiscard the shared sink for unmapped columns.
	scanPlanResolved bool
	scanPlan         []scanCol
	scanDest         []any
	scanDiscard      any
}

// selectExprEntry holds one AST-rendered projection in the SELECT list.
// The sql carries '?' bind markers; buildSelect substitutes them for
// the dialect's placeholder syntax at the correct argIndex.
type selectExprEntry struct {
	alias string
	sql   string
	args  []any
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

// clone returns a copy of the Query for a builder method to mutate, leaving
// the receiver untouched. The struct copy carries every scalar and isolation
// field (tenantID, tenantCol, schema, cache, lock, limit, …) — propagating
// tenant state here is mandatory (ADR-0007 / P0-1).
//
// The slice fields (where, orderBy, joins, …) are shared with the receiver,
// NOT deep-copied: builder methods append to them through [ownedAppend],
// whose capacity-bounded append allocates a fresh backing array on the first
// growth, so a shared base query and the queries derived from it never see
// each other's appends (copy-on-write). This keeps clone allocation-free.
// The previous implementation deep-copied all ten slices on every builder
// call — even slices the method never touched — which measured ~7% of the
// read-path allocations (benchmarks/PROFILING.md). The immutability contract
// is unchanged; only the point at which the copy happens moved (to the first
// append, and only for the slice actually mutated).
func (q *Query[T]) clone() *Query[T] {
	c := *q
	return &c
}

// ownedAppend appends v to s and guarantees the result does not share s's
// backing array. The three-index slice `s[:len(s):len(s)]` caps capacity at
// the current length, forcing append to allocate a new array when it grows.
//
// This is the copy-on-write primitive that lets [Query.clone] share slices
// cheaply: a builder appending to a slice it shares with a sibling query can
// never overwrite the sibling's elements, because the append reallocates
// instead of writing into spare capacity of the shared array. It is safe
// (never corrupts) whether or not s is actually shared; when s is uniquely
// owned the only cost is one copy on grow — the same copy clone() used to do
// unconditionally, now deferred to the mutation and scoped to one slice.
func ownedAppend[E any](s []E, v ...E) []E {
	return append(s[:len(s):len(s)], v...)
}

// Preload specifies relations to load automatically.
func (q *Query[T]) Preload(relations ...string) *Query[T] {
	c := q.clone()
	c.preloads = ownedAppend(c.preloads, relations...)
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
	c.where = ownedAppend(c.where, condition{
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
	c.where = ownedAppend(c.where, condition{
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
	c.where = ownedAppend(c.where, condition{
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
// When the parent has an active RowLevelSecurityClient tenantID, the tenant predicate
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
		client:  b.client,
		ctx:     b.ctx,
		table:   b.table,
		schema:  b.schema,
		dialect: b.dialect,
		guard:   b.guard,
		pk:      b.pk,
		exec:    b.exec,
		// F5-4: propagate the tx back-reference so that any CRUD
		// executed via a group sub-clause keeps queuing After
		// hooks on the same tx. Today Or() builds only WHERE
		// conditions and never executes CRUD, but copying the
		// reference defensively closes a latent footgun if the
		// group surface ever grows to accept CRUD inside the
		// callback.
		tx:          b.tx,
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
// Under the RowLevelSecurityClient tenant strategy the OR group inherits the parent's
// tenant_id predicate so it cannot escape isolation via SQL operator precedence.
func (q *Query[T]) Or(fn func(*Query[T]) *Query[T]) *Query[T] {
	blank := &Query[T]{BaseQuery: q.cloneForGroup()}
	result := fn(blank)

	c := q.clone()
	c.where = ownedAppend(c.where, condition{
		logic: "OR",
		group: result.where,
	})
	return c
}

// OrderBy adds an ORDER BY clause.
func (q *Query[T]) OrderBy(column string, direction string) *Query[T] {
	c := q.clone()
	c.orderBy = ownedAppend(c.orderBy, order{
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

// JoinBuilder is the structured form returned by Join, LeftJoin, and
// RightJoin. Complete the JOIN by chaining `.On(left, op, right)` (the
// typed identifier form) or `.OnRaw(onClause)` (the legacy free-form
// string for ON clauses outside the simple binary grammar). Both
// chain-terminate by returning *Query[T] so subsequent builder calls
// pick up where the JOIN left off.
//
// JoinBuilder values are immutable; the underlying *Query[T] is cloned
// before the JOIN is appended, matching the rest of the builder's
// thread-safety contract.
type JoinBuilder[T any] struct {
	q        *Query[T]
	joinType string
	table    string
}

// On appends an INNER/LEFT/RIGHT JOIN with a single binary identifier
// comparison as the ON clause: `<left> <op> <right>`. The three
// arguments are concatenated as `left + " " + op + " " + right` and
// the resulting clause is validated as a whole against
// `guard.ValidateJoinOn` at exec time, surfacing `ErrInvalidJoin` for
// any shape outside the identifier-only grammar (literal RHS, function
// calls, parens, comments, mismatched operators). The grammar accepts
// the binary comparison operators `=`, `!=`, `<>`, `<`, `<=`, `>`, `>=`
// and AND-chained compound clauses.
//
// Most JOINs need only this form — the typical
// `users.id = orders.user_id` shape. For multi-condition ON clauses or
// any expression the ValidateJoinOn grammar accepts (AND-chained
// identifier comparisons), use `OnRaw`.
//
// Example:
//
//	quark.For[Order](ctx, client).
//	    Join("users").On("users.id", "=", "orders.user_id").
//	    List()
func (b *JoinBuilder[T]) On(left, op, right string) *Query[T] {
	c := b.q.clone()
	onClause := left + " " + op + " " + right
	c.joins = ownedAppend(c.joins, join{
		joinType: b.joinType,
		table:    b.table,
		onClause: onClause,
	})
	return c
}

// OnRaw appends the JOIN with a free-form ON clause string. The clause
// must match the minimal identifier-only grammar that
// `guard.ValidateJoinOn` enforces (AND-chained binary comparisons of
// qualified identifiers, e.g.
// `users.id = orders.user_id AND users.tenant_id = orders.tenant_id`).
// Literals, function calls, subqueries, and parentheses are rejected.
// Drop down to `RawQuery` for shapes outside this grammar.
//
// OnRaw is the migration path for callers of the v0.3.x string-raw
// `Join(table, onClause)`: rewrite as
// `Join(table).OnRaw(onClause)`.
func (b *JoinBuilder[T]) OnRaw(onClause string) *Query[T] {
	c := b.q.clone()
	c.joins = ownedAppend(c.joins, join{
		joinType: b.joinType,
		table:    b.table,
		onClause: onClause,
	})
	return c
}

// Join opens an INNER JOIN against `table`. Complete the JOIN with
// `.On(left, op, right)` (typed binary identifier comparison) or
// `.OnRaw(onClause)` (free-form, validated through the same identifier
// grammar as `On`).
//
// The structured form replaces the v0.3.x string-raw `Join(table, on)`
// signature; see `MIGRATION_v0.4.0.md` for the mechanical rewrite.
func (q *Query[T]) Join(table string) *JoinBuilder[T] {
	return &JoinBuilder[T]{q: q, joinType: "INNER JOIN", table: table}
}

// LeftJoin opens a LEFT JOIN. See Join for ON-clause grammar.
func (q *Query[T]) LeftJoin(table string) *JoinBuilder[T] {
	return &JoinBuilder[T]{q: q, joinType: "LEFT JOIN", table: table}
}

// RightJoin opens a RIGHT JOIN. See Join for ON-clause grammar.
func (q *Query[T]) RightJoin(table string) *JoinBuilder[T] {
	return &JoinBuilder[T]{q: q, joinType: "RIGHT JOIN", table: table}
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
	c.where = ownedAppend(c.where, condition{
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
	c.groupBy = ownedAppend(c.groupBy, columns...)
	return c
}

// Having adds a HAVING condition (used together with GroupBy).
//
// The column argument is validated as a plain identifier — no parentheses,
// function calls, or expressions. To filter on aggregates such as
// COUNT(*) or SUM(col), use HavingAggregate instead.
func (q *Query[T]) Having(column string, operator string, value any) *Query[T] {
	c := q.clone()
	c.having = ownedAppend(c.having, condition{
		column:   column,
		operator: operator,
		value:    value,
		logic:    "AND",
	})
	return c
}

// allowedAggregateFns is the whitelist of function names that
// HavingAggregate accepts as its first argument. The list mirrors the
// SQL-92 standard aggregates the dialects share without translation.
var allowedAggregateFns = map[string]struct{}{
	"COUNT": {},
	"SUM":   {},
	"AVG":   {},
	"MIN":   {},
	"MAX":   {},
}

// HavingAggregate adds a HAVING condition over an aggregate function.
//
// fn must be one of COUNT, SUM, AVG, MIN, MAX (case-insensitive). column is
// either a regular column name (validated through SQLGuard) or "*" — only
// accepted with COUNT, since "SUM(*)" / "AVG(*)" / etc. are not valid SQL.
// operator goes through the same whitelist Where uses (=, !=, <>, <, <=,
// >, >=, IN, NOT IN, BETWEEN, NOT BETWEEN, IS [NOT] NULL, LIKE, NOT LIKE).
//
// Example:
//
//	groups, err := quark.For[Order](ctx, client).
//	    GroupBy("status").
//	    HavingAggregate("COUNT", "*", ">", 5).
//	    List()
//	// emitted: ... GROUP BY "status" HAVING COUNT(*) > $1
//
// This closes the historic Having(column, op, value) limitation where the
// column went through ValidateIdentifier and aggregates therefore could
// not be expressed without RawQuery. The structured-AST form
// Having(Func("count", Col("*")), ">", 5) arrives with the full Phase 2
// AST; HavingAggregate is the focused, type-safe shortcut for the
// overwhelmingly common case.
func (q *Query[T]) HavingAggregate(fn, column, operator string, value any) *Query[T] {
	c := q.clone()

	upperFn := strings.ToUpper(strings.TrimSpace(fn))
	if _, ok := allowedAggregateFns[upperFn]; !ok {
		c.err = fmt.Errorf("%w: HavingAggregate fn %q must be one of COUNT/SUM/AVG/MIN/MAX", ErrInvalidQuery, fn)
		return c
	}
	var expr string
	if column == "*" {
		if upperFn != "COUNT" {
			c.err = fmt.Errorf("%w: HavingAggregate column \"*\" only valid with COUNT, got %s", ErrInvalidQuery, upperFn)
			return c
		}
		expr = upperFn + "(*)"
	} else {
		if err := q.guard.ValidateIdentifier(column); err != nil {
			c.err = err
			return c
		}
		expr = upperFn + "(" + q.dialect.Quote(column) + ")"
	}

	// Reuse the condition raw-fragment slot. buildWhereClause renders
	// isRaw conditions verbatim and validates the operator separately.
	c.having = ownedAppend(c.having, condition{
		column:   expr,
		operator: operator,
		value:    value,
		logic:    "AND",
		isRaw:    true,
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

// SelectExpr adds an AST projection to the SELECT list, aliased as
// `alias`. Use it for window functions, scalar subqueries, or any
// expression the plain `Select(cols...)` API can't model:
//
//	q := quark.For[Order](ctx, client).
//	    SelectExpr("rank", quark.Over(quark.Rank(),
//	        quark.NewWindow().
//	            PartitionBy(quark.Col("status")).
//	            OrderBy(quark.Col("amount"), true))).
//	    SelectExpr("running_total", quark.Over(
//	        quark.Func("SUM", quark.Col("amount")),
//	        quark.NewWindow().OrderBy(quark.Col("id"), false)))
//
// The expression is rendered against a `qmark`-emitting dialect at
// SelectExpr time, so the inner '?' markers are reindexed to the outer
// dialect's placeholder syntax when buildSelect runs. The args land in
// the args slice between any CTE args and the WHERE args — matching
// the SQL-surface order of the SELECT projection.
//
// Composing SelectExpr with the plain Select(cols...) is allowed: the
// regular columns render first, the AST projections after, comma-
// separated. If neither is set, the SELECT defaults to '*'.
func (q *Query[T]) SelectExpr(alias string, e Expr) *Query[T] {
	c := q.clone()
	if e == nil {
		c.err = fmt.Errorf("%w: SelectExpr(%q, nil)", ErrInvalidQuery, alias)
		return c
	}
	if err := c.guard.ValidateIdentifier(alias); err != nil {
		c.err = err
		return c
	}
	// Render with a qmarkDialect so the projection comes out with '?' as
	// the bind marker; buildSelect reindexes them at outer render time.
	qmark := qmarkDialect{Dialect: c.dialect}
	sql, args, err := e.ToSQL(qmark, c.guard)
	if err != nil {
		c.err = err
		return c
	}
	c.selectExprs = ownedAppend(c.selectExprs, selectExprEntry{alias: alias, sql: sql, args: args})
	return c
}

// WhereExpr adds a WHERE condition built from a composable Expr AST.
//
// The AST is rendered against the active dialect at call time, producing a
// fragment with '?' bind markers plus the args. Storage and execution reuse
// the existing raw-fragment slot in condition: buildWhereClause substitutes
// each '?' for the dialect placeholder at the correct argIndex, so the AST
// stays dialect-agnostic at construction time and integrates cleanly with
// WhereJSON, Or, and the rest of the builder.
//
// Errors raised during ToSQL — unknown function names, invalid identifiers,
// invalid operators, empty IN lists — are stashed on the query and surface
// at execution time wrapping ErrInvalidQuery.
//
// Example:
//
//	q := quark.For[User](ctx, client).WhereExpr(
//	    quark.Or(
//	        quark.Eq(quark.Col("role"), quark.Lit("admin")),
//	        quark.And(
//	            quark.Gt(quark.Col("logins"), quark.Lit(10)),
//	            quark.Eq(quark.Col("verified"), quark.Lit(true)),
//	        ),
//	    ),
//	)
func (q *Query[T]) WhereExpr(e Expr) *Query[T] {
	c := q.clone()
	if e == nil {
		return c
	}
	frag, args, err := e.ToSQL(q.dialect, q.guard)
	if err != nil {
		c.err = err
		return c
	}
	if frag == "" {
		return c
	}
	c.where = ownedAppend(c.where, condition{
		column:    frag,
		operator:  "",
		logic:     "AND",
		isRaw:     true,
		extraArgs: args,
	})
	return c
}

// HavingExpr adds a HAVING condition built from the Expr AST. Same rendering
// pipeline as WhereExpr; useful for aggregate predicates that need the full
// composition surface (Func("COUNT", Col("*")) > Lit(5), and so on).
func (q *Query[T]) HavingExpr(e Expr) *Query[T] {
	c := q.clone()
	if e == nil {
		return c
	}
	frag, args, err := e.ToSQL(q.dialect, q.guard)
	if err != nil {
		c.err = err
		return c
	}
	if frag == "" {
		return c
	}
	c.having = ownedAppend(c.having, condition{
		column:    frag,
		operator:  "",
		logic:     "AND",
		isRaw:     true,
		extraArgs: args,
	})
	return c
}

// WhereJSON adds a WHERE condition for a JSON field.
// column is the JSON column name, path is a dotted key path within the JSON
// object (e.g. "user.name"). The path is always validated by
// guard.ValidateJSONPath and, on every dialect except Oracle, bound as a
// parameter rather than interpolated. Oracle's JSON_VALUE rejects a bound
// path (ORA-40454), so there the validated path is inlined as a literal; the
// [A-Za-z0-9_.] grammar keeps it injection-safe. See guard.ValidateJSONPath
// for the accepted grammar.
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
	c.where = ownedAppend(c.where, condition{
		column:    frag,
		operator:  operator,
		value:     value,
		logic:     "AND",
		isRaw:     true,
		extraArgs: pathArgs,
	})
	return c
}

// notifyObservers notifies all registered observers of a query event and
// emits the F4-3 slow-query log line when configured. Doing the slow-log
// check here keeps every emit site honest: all five callers (cursor,
// query_exec ×3, query_crud) already build a QueryEvent with the
// authoritative duration, so the threshold check has the right number to
// compare against without redundant timing.
func (q *BaseQuery) notifyObservers(event QueryEvent) {
	if q.client == nil {
		return
	}
	q.client.logSlowQueryIfNeeded(event)
	for _, obs := range q.client.observers {
		obs.ObserveQuery(event)
	}
}
