// Copyright 2026 jcsvwinston
// SPDX-License-Identifier: Apache-2.0

package quark

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"reflect"
	"strings"
	"time"

	"github.com/jcsvwinston/quark/internal/guard"
)

// timeFormats is the ordered list of layouts tried when parsing datetime strings from drivers
// (e.g. MySQL without parseTime=true returns []uint8 / string instead of time.Time).
var timeFormats = []string{
	time.RFC3339Nano,
	"2006-01-02 15:04:05.999999999",
	"2006-01-02 15:04:05",
	"2006-01-02",
}

// timeScanner wraps a *time.Time destination so that []uint8 / string values
// returned by MySQL/MariaDB (when parseTime is not set) are parsed correctly.
//
// When loc is non-nil the scanned value is converted with .In(loc) before it
// lands in the destination — this is the read half of the per-column timezone
// contract (ADR-0010). loc nil keeps the historical behaviour: the value is
// stored exactly as the driver returned it.
type timeScanner struct {
	dest *time.Time
	loc  *time.Location
}

func (ts timeScanner) Scan(src any) error {
	if src == nil {
		*ts.dest = time.Time{}
		return nil
	}
	switch v := src.(type) {
	case time.Time:
		*ts.dest = v
	case []byte:
		if err := ts.parse(string(v)); err != nil {
			return err
		}
	case string:
		if err := ts.parse(v); err != nil {
			return err
		}
	default:
		return fmt.Errorf("timeScanner: unsupported type %T", src)
	}
	// Applied to every shape, including a driver-supplied time.Time that
	// already carries a zone (PG/pgx TIMESTAMPTZ): .In only changes the
	// representation, not the instant, so re-localising is safe and gives
	// the per-column contract a single, consistent application point.
	if ts.loc != nil {
		*ts.dest = ts.dest.In(ts.loc)
	}
	return nil
}

func (ts timeScanner) parse(s string) error {
	for _, layout := range timeFormats {
		if t, err := time.Parse(layout, s); err == nil {
			*ts.dest = t
			return nil
		}
	}
	return fmt.Errorf("timeScanner: cannot parse %q as time", s)
}

// nullTimeScanner wraps a **time.Time (nullable) destination with the same
// []uint8 handling and the same loc conversion as timeScanner.
type nullTimeScanner struct {
	dest **time.Time
	loc  *time.Location
}

func (ns nullTimeScanner) Scan(src any) error {
	if src == nil {
		*ns.dest = nil
		return nil
	}
	t := new(time.Time)
	if err := (timeScanner{dest: t, loc: ns.loc}).Scan(src); err != nil {
		return err
	}
	*ns.dest = t
	return nil
}

// nullableTimeScanner wraps a *Nullable[time.Time] (== *sql.Null[time.Time])
// destination. It reuses timeScanner for the robust []uint8 / string parsing
// and the loc conversion, then rebuilds the Null wrapper. Only installed when
// loc is non-nil — without a configured timezone the field keeps using
// sql.Null[time.Time]'s own Scanner, so the v0.6 behaviour is untouched.
type nullableTimeScanner struct {
	dest *sql.Null[time.Time]
	loc  *time.Location
}

func (ns nullableTimeScanner) Scan(src any) error {
	if src == nil {
		*ns.dest = sql.Null[time.Time]{}
		return nil
	}
	var t time.Time
	if err := (timeScanner{dest: &t, loc: ns.loc}).Scan(src); err != nil {
		return err
	}
	*ns.dest = sql.Null[time.Time]{V: t, Valid: true}
	return nil
}

// emptyStringScanner wraps a *string destination so a NULL column scans as the
// empty string instead of erroring with "converting NULL to string is
// unsupported". This reconciles Oracle — which stores an empty string as NULL
// and therefore returns NULL for a round-tripped empty string — with the other
// engines that preserve it verbatim. A field declared as a plain string opts
// into this coercion; use *string or sql.Null[string] to keep the NULL vs
// empty-string distinction.
type emptyStringScanner struct{ dest *string }

func (s emptyStringScanner) Scan(src any) error {
	switch v := src.(type) {
	case nil:
		*s.dest = ""
	case string:
		*s.dest = v
	case []byte:
		*s.dest = string(v)
	default:
		// Match database/sql's lenient string conversion for any other driver
		// shape so non-NULL values keep behaving as before.
		*s.dest = fmt.Sprintf("%v", v)
	}
	return nil
}

// makeScanDest returns the scan destination for a single column, wrapping
// time-shaped fields so MySQL/MariaDB []uint8 values parse correctly and so
// the per-column timezone (loc) is applied on the way in. loc nil means no
// timezone conversion — for *sql.Null[time.Time] that also means the field
// keeps its native Scanner rather than being wrapped at all.
func makeScanDest(field reflect.Value, loc *time.Location) any {
	return scanDestForPtr(field.Addr().Interface(), loc)
}

// scanDestForPtr is the reflection-free core of makeScanDest: given a typed
// field pointer it returns the rows.Scan target, wrapping time.Time variants
// so the same string/[]byte parsing and optional timezone conversion apply.
func scanDestForPtr(iface any, loc *time.Location) any {
	switch dst := iface.(type) {
	case *time.Time:
		return timeScanner{dest: dst, loc: loc}
	case **time.Time:
		return nullTimeScanner{dest: dst, loc: loc}
	case *sql.Null[time.Time]:
		if loc == nil {
			return iface
		}
		return nullableTimeScanner{dest: dst, loc: loc}
	case *string:
		// Non-pointer string field: coerce a NULL column to "" (see
		// emptyStringScanner). A *string field arrives here as **string and is
		// left untouched so its NULL / "" distinction is preserved.
		return emptyStringScanner{dest: dst}
	}
	return iface
}

// ScanTarget returns the rows.Scan target for a model field pointer, matching
// the no-timezone scan behavior of the reflection path (including the
// string/[]byte time parsing that drivers like SQLite require). It is called
// by generated scanners — which run only when the per-column timezone feature
// is inactive, so a nil location is always correct. Not intended for hand use.
func ScanTarget(ptr any) any {
	return scanDestForPtr(ptr, nil)
}

// executeQuery runs a multi-row SELECT through the middleware chain, routing to
// a read replica when one is configured (F6-5, ADR-0015). readExec returns the
// primary/tx exec unchanged when routing does not apply.
//
// IMPORTANT: this is for genuine reads only. A write path that reads rows back
// with a multi-row shape — INSERT ... RETURNING in CreateBatch — must NOT route
// to a replica; it calls executeQueryPrimary instead.
func (q *BaseQuery) executeQuery(ctx context.Context, sqlStr string, args []any) (*sql.Rows, error) {
	exec := q.readExec(ctx)
	rows, err := q.executeQueryOn(ctx, exec, sqlStr, args)
	// Replica failover (F6-6): if the read was routed to a replica (exec is a
	// *sql.DB other than the primary) and it failed with a transient connection
	// error, take that replica out of rotation and retry once on the primary.
	// exec == q.exec for the non-routed cases (no replicas, Sticky, a *sql.Tx,
	// or the nativeRLSExecutor — readExec already returned q.exec for those), so
	// this branch only fires for an actually-routed replica read.
	if err != nil && q.client != nil && exec != Executor(q.exec) {
		if rdb, ok := exec.(*sql.DB); ok && isTransientConnErr(err) {
			q.client.markReplicaDown(rdb)
			return q.executeQueryOn(ctx, q.exec, sqlStr, args)
		}
	}
	return rows, err
}

// executeQueryPrimary runs a multi-row query on the primary connection (q.exec)
// without replica routing. Used by write paths that read rows back (RETURNING).
func (q *BaseQuery) executeQueryPrimary(ctx context.Context, sqlStr string, args []any) (*sql.Rows, error) {
	return q.executeQueryOn(ctx, q.exec, sqlStr, args)
}

// executeQueryOn runs a QueryContext on the given exec through the middleware
// chain. The exec selection (replica vs primary) is the caller's decision.
func (q *BaseQuery) executeQueryOn(ctx context.Context, exec Executor, sqlStr string, args []any) (*sql.Rows, error) {
	if q.err != nil {
		return nil, q.err
	}
	// Base handler: direct execution
	handler := QueryFunc(func(ctx context.Context, exec Executor, s string, a []any) (*sql.Rows, error) {
		return exec.QueryContext(ctx, s, a...)
	})

	// Wrap with middleware in reverse order
	for i := len(q.client.middleware) - 1; i >= 0; i-- {
		handler = q.client.middleware[i].WrapQuery(handler)
	}

	return handler(ctx, exec, sqlStr, args)
}

// executeQueryRow runs a single-row QueryRowContext on the primary (q.exec)
// through the middleware chain. It is the WRITE-path single-row primitive:
// INSERT ... RETURNING and MSSQL SCOPE_IDENTITY() read one row back but must
// never touch a read replica. Genuine single-row reads (Count / aggregates)
// use [BaseQuery.executeReadRow] instead, which is replica-routed (ADR-0015).
func (q *BaseQuery) executeQueryRow(ctx context.Context, sqlStr string, args []any) *sql.Row {
	return q.queryRowOn(ctx, q.exec, sqlStr, args)
}

// executeReadRow runs a single-row read, routing to a read replica when one is
// available (F6-5, ADR-0015) with the same transient-error failover as
// executeQuery (F6-6). It performs the Scan internally because *sql.Row defers
// its error until Scan, so observing a transient failure to fail over requires
// materializing the error here. Used by Count and the aggregates — never by a
// write path (those use [BaseQuery.executeQueryRow], primary-only).
func (q *BaseQuery) executeReadRow(ctx context.Context, sqlStr string, args []any, dest ...any) error {
	exec := q.readExec(ctx)
	err := q.queryRowOn(ctx, exec, sqlStr, args).Scan(dest...)
	// Failover (F6-6): mirror executeQuery. exec != q.exec only when readExec
	// actually routed to a replica (not Sticky / tx / native-RLS / no replicas),
	// so this fires only for an actually-routed replica read that failed
	// transiently. The retry re-runs on the primary; for an idempotent read,
	// re-Scanning into dest is safe.
	if err != nil && q.client != nil && exec != Executor(q.exec) {
		if rdb, ok := exec.(*sql.DB); ok && isTransientConnErr(err) {
			q.client.markReplicaDown(rdb)
			return q.queryRowOn(ctx, q.exec, sqlStr, args).Scan(dest...)
		}
	}
	return err
}

// queryRowOn runs a QueryRowContext on the given exec through the middleware
// chain. The exec selection (replica vs primary) is the caller's decision.
func (q *BaseQuery) queryRowOn(ctx context.Context, exec Executor, sqlStr string, args []any) *sql.Row {
	// A build error (q.err) surfaces naturally through Scan: *sql.Row defers its
	// error until Scan, so there is nothing to return early here.
	// Base handler: direct execution
	handler := QueryRowFunc(func(ctx context.Context, exec Executor, s string, a []any) *sql.Row {
		start := time.Now()
		row := exec.QueryRowContext(ctx, s, a...)
		duration := time.Since(start)

		// Notify observers (we don't know the rows yet, but it's always 1 for Row)
		q.notifyObservers(QueryEvent{
			SQL:       s,
			Args:      a,
			Duration:  duration,
			Table:     q.table,
			Operation: "QUERY_ROW",
			Rows:      1,
		})

		return row
	})

	// Wrap with middleware in reverse order
	for i := len(q.client.middleware) - 1; i >= 0; i-- {
		handler = q.client.middleware[i].WrapQueryRow(handler)
	}

	return handler(ctx, exec, sqlStr, args)
}

// List executes the query and returns all matching rows.
// If Limit() is not called, uses a safe default (100) to prevent OOM.
// Use Iter() for unbounded streaming or Paginate() for large datasets.
func (q *Query[T]) List() ([]T, error) {
	if q.client == nil {
		return nil, fmt.Errorf("%w: client not initialized", ErrInvalidQuery)
	}

	// F5-4: BeforeFind fires before any SQL is built or cached.
	// Returning an error from the hook short-circuits the call.
	if err := q.callBeforeFind(q.ctx); err != nil {
		return nil, err
	}

	// Safety: if no explicit limit, apply safe default
	if !q.hasLimit {
		q.limit = 100 // Safe default
		// Skip the generic "using cap of 100" notice when the cap is about to
		// be dropped anyway: on Oracle a lock suppresses the row-limiting
		// clause (buildSelect logs the dropped-cap WARN instead), so emitting
		// both would contradict each other in the logs. (BB-4)
		if !(q.dialect.Name() == "oracle" && !q.lock.IsZero()) {
			q.client.logger.Warn("List() called without explicit Limit(), using safe default of 100. Use Iter() for unbounded queries or call Limit() explicitly.")
		}
	}

	// Build query
	sqlStr, args, err := q.buildSelect()
	if err != nil {
		return nil, err
	}

	if q.limit > q.client.limits.MaxResults {
		q.limit = q.client.limits.MaxResults
	}

	// 1. Resolve the timeout context once. It's used by both the
	// stampede getOrCompute path and the legacy cache-aside fallback.
	ctx, cancel := context.WithTimeout(q.ctx, q.client.limits.QueryTimeout)
	defer cancel()

	// computeFromDB executes the SQL, scans the rows, marshals the
	// result slice and notifies observers. Used both as the singleflight
	// compute callback (when the cacheStore is a *stampedeStore) and
	// directly by the legacy cache-aside path. Wrapping it once keeps
	// the observer / log semantics identical: exactly one observer
	// event per actual SQL trip, regardless of how many concurrent
	// callers were collapsed by singleflight.
	computeFromDB := func(ctx context.Context) ([]T, []byte, error) {
		start := time.Now()
		rows, err := q.executeQuery(ctx, sqlStr, args)
		duration := time.Since(start)
		if err != nil {
			return nil, nil, fmt.Errorf("query failed: %w", wrapDBError(err))
		}
		defer rows.Close()

		// Pre-size the result slice to the (capped) row limit so the append loop
		// avoids repeated slice-growth reallocations — the dominant read-path
		// allocator per benchmarks/PROFILING.md. Capped so a large MaxResults
		// with few actual rows doesn't over-allocate.
		capHint := q.limit
		if capHint < 0 {
			capHint = 0
		} else if capHint > 1024 {
			capHint = 1024
		}
		results := make([]T, 0, capHint)
		for rows.Next() {
			var entity T
			if scanErr := q.scanRow(rows, &entity); scanErr != nil {
				return nil, nil, scanErr
			}
			results = append(results, entity)
		}
		if rerr := rows.Err(); rerr != nil {
			return nil, nil, wrapDBError(rerr)
		}

		data, _ := json.Marshal(results)

		q.notifyObservers(QueryEvent{
			SQL:       sqlStr,
			Args:      args,
			Duration:  duration,
			Table:     q.table,
			Operation: "SELECT",
			Rows:      int64(len(results)),
		})
		return results, data, nil
	}

	// 2. Try the cache path. When a *stampedeStore is installed (the
	// default once WithCacheStore is in play), the singleflight + XFetch
	// machinery routes through getOrCompute. Other CacheStore
	// implementations stay on the historical cache-aside dance.
	var (
		cacheKey string
		results  []T
	)
	if q.cache.Enabled && q.client.cacheStore != nil {
		cacheKey = q.generateCacheKey(sqlStr, args)
		// stampedeStore is the wrapper auto-installed by WithCacheStore
		// (see ADR-0011). Third-party CacheStore implementations fail
		// this assertion and stay on the historical cache-aside path
		// below — that's documented and intentional.
		if ss, ok := q.client.cacheStore.(*stampedeStore); ok {
			data, err := ss.getOrCompute(ctx, cacheKey, q.cache.TTL, q.cache.Tags, func(ctx context.Context) ([]byte, error) {
				r, bytes, err := computeFromDB(ctx)
				if err != nil {
					return nil, err
				}
				results = r // capture for the post-compute path below
				return bytes, nil
			})
			if err != nil {
				return nil, err
			}
			if results == nil {
				// Came from cache (no compute) — unmarshal here.
				if uerr := json.Unmarshal(data, &results); uerr != nil {
					// Cached bytes don't match the current model shape
					// (typically a schema change deployed with a warm
					// cache). Delete the stale entry and re-enter
					// getOrCompute so concurrent callers funnel through
					// ONE singleflight compute instead of each running
					// their own — preserves stampede protection across
					// schema-incompatible cache rotations.
					_ = ss.Delete(ctx, cacheKey)
					results = nil // re-arm capture for the recompute path
					data2, rerr := ss.getOrCompute(ctx, cacheKey, q.cache.TTL, q.cache.Tags, func(ctx context.Context) ([]byte, error) {
						r, bytes, cerr := computeFromDB(ctx)
						if cerr != nil {
							return nil, cerr
						}
						results = r
						return bytes, nil
					})
					if rerr != nil {
						return nil, rerr
					}
					if results == nil {
						// Singleflight result came from another waiter's
						// fresh compute. Try one final unmarshal; if it
						// still fails the model and cache really are
						// incompatible — surface a real error rather
						// than retry forever.
						if uerr := json.Unmarshal(data2, &results); uerr != nil {
							return nil, fmt.Errorf("cache payload incompatible with model: %w", uerr)
						}
					}
				} else {
					q.client.logger.Debug("cache hit", "key", cacheKey, "table", q.table)
				}
			}
		} else {
			// Legacy cache-aside for non-stampede CacheStore impls.
			if data, err := q.client.cacheStore.Get(ctx, cacheKey); err == nil {
				if uerr := json.Unmarshal(data, &results); uerr == nil {
					q.client.logger.Debug("cache hit", "key", cacheKey, "table", q.table)
				} else {
					results = nil
				}
			}
			if results == nil {
				r, bytes, err := computeFromDB(ctx)
				if err != nil {
					return nil, err
				}
				results = r
				_ = q.client.cacheStore.Set(ctx, cacheKey, bytes, q.cache.TTL, q.cache.Tags...)
			}
		}
	} else {
		// No cache configured — straight compute.
		r, _, err := computeFromDB(ctx)
		if err != nil {
			return nil, err
		}
		results = r
	}

	if len(q.preloads) > 0 && len(results) > 0 {
		if err := q.loadRelations(results); err != nil {
			return nil, err
		}
	}

	// F5-4: AfterFind fires once per call after results are
	// hydrated (including relations from Preload), regardless of
	// whether the rows came from cache or DB.
	if err := q.callAfterFind(q.ctx); err != nil {
		return nil, err
	}

	return results, nil
}

// First returns the first matching row or ErrNotFound.
func (q *Query[T]) First() (T, error) {
	var zero T

	q.limit = 1
	q.hasLimit = true
	results, err := q.List()
	if err != nil {
		return zero, err
	}

	if len(results) == 0 {
		return zero, ErrNotFound
	}

	return results[0], nil
}

// Find retrieves a single row by primary key.
func (q *Query[T]) Find(id any) (T, error) {
	var zero T

	if q.client == nil {
		return zero, fmt.Errorf("%w: client not initialized", ErrInvalidQuery)
	}

	q.where = []condition{{
		column:   q.pk.Column,
		operator: "=",
		value:    id,
		logic:    "AND",
	}}
	q.limit = 1

	return q.First()
}

// Cursor returns a Cursor for manual iteration over large result sets.
// The Cursor must be closed after use (defer cursor.Close()).
//
// Example:
//
//	cursor, err := quark.For[User](ctx, client).Where("active", "=", true).Cursor()
//	if err != nil { log.Fatal(err) }
//	defer cursor.Close()
//
//	for cursor.Next() {
//	    var user User
//	    if err := cursor.Scan(&user); err != nil { break }
//	    process(user)
//	}
func (q *Query[T]) Cursor() (*Cursor[T], error) {
	if q.client == nil {
		return nil, fmt.Errorf("%w: client not initialized", ErrInvalidQuery)
	}

	// F5-4: BeforeFind fires before the cursor opens. AfterFind
	// fires from the cursor's Close path (see cursor.go) so
	// streaming consumers get the same exactly-once contract as
	// List() — once per call, after rows are exhausted.
	if err := q.callBeforeFind(q.ctx); err != nil {
		return nil, err
	}

	sqlStr, args, err := q.buildSelect()
	if err != nil {
		return nil, err
	}

	ctx, cancel := context.WithTimeout(q.ctx, q.client.limits.QueryTimeout)
	rows, err := q.executeQuery(ctx, sqlStr, args)
	if err != nil {
		cancel()
		return nil, fmt.Errorf("query failed: %w", wrapDBError(err))
	}

	return &Cursor[T]{
		rows:   rows,
		ctx:    ctx,
		cancel: cancel,
		query:  q,
		sql:    sqlStr,
		args:   args,
		start:  time.Now(),
	}, nil
}

// Iter executes the query and iterates over results one by one.
// Uses streaming to handle large datasets without loading all into memory.
//
// Example:
//
//	err := quark.For[User](ctx, client).Where("active", "=", true).Iter(func(user User) error {
//	    process(user)
//	    return nil
//	})
func (q *Query[T]) Iter(fn func(T) error) error {
	if q.client == nil {
		return fmt.Errorf("%w: client not initialized", ErrInvalidQuery)
	}

	// F5-4: BeforeFind fires before any rows are pulled. AfterFind
	// fires once the streaming loop completes WITHOUT error — a
	// fn-returned error or rows.Err short-circuits AfterFind, since
	// the read effectively did not "succeed".
	if err := q.callBeforeFind(q.ctx); err != nil {
		return err
	}

	sqlStr, args, err := q.buildSelect()
	if err != nil {
		return err
	}

	ctx, cancel := context.WithTimeout(q.ctx, q.client.limits.QueryTimeout)
	defer cancel()

	start := time.Now()
	rows, err := q.executeQuery(ctx, sqlStr, args)
	duration := time.Since(start)

	q.notifyObservers(QueryEvent{
		SQL:       sqlStr,
		Args:      args,
		Duration:  duration,
		Error:     err,
		Table:     q.table,
		Operation: "SELECT (stream)",
	})

	if err != nil {
		return fmt.Errorf("query failed: %w", wrapDBError(err))
	}
	defer rows.Close()

	for rows.Next() {
		var entity T
		if err := q.scanRow(rows, &entity); err != nil {
			return err
		}
		if err := fn(entity); err != nil {
			return err
		}
	}

	if err := wrapDBError(rows.Err()); err != nil {
		return err
	}
	return q.callAfterFind(q.ctx)
}

// Count returns the total number of matching rows.
func (q *Query[T]) Count() (int64, error) {
	if q.err != nil {
		return 0, q.err
	}
	if q.client == nil {
		return 0, fmt.Errorf("%w: client not initialized", ErrInvalidQuery)
	}

	// A compound select (UNION / INTERSECT / EXCEPT) cannot be counted by
	// the flat `SELECT COUNT(*) FROM table [JOIN…] [WHERE…]` built below —
	// that counts only the base operand and silently ignores the set-op
	// (Paginate then reports the same wrong total). Count the combined
	// result instead.
	if len(q.setOps) > 0 {
		return q.countCompound()
	}

	var sqlBuf strings.Builder
	var args []any

	if cteSQL, cteArgs, err := q.buildCTEPrefix(1); err != nil {
		return 0, err
	} else if cteSQL != "" {
		sqlBuf.WriteString(cteSQL)
		args = append(args, cteArgs...)
	}

	sqlBuf.WriteString("SELECT COUNT(*) FROM ")
	sqlBuf.WriteString(q.fullTableName())

	// JOIN clauses
	for _, j := range q.joins {
		if err := q.guard.ValidateIdentifier(j.table); err != nil {
			return 0, err
		}
		if err := guard.ValidateJoinOn(j.onClause); err != nil {
			return 0, fmt.Errorf("%w: %v", ErrInvalidJoin, err)
		}
		sqlBuf.WriteString(" ")
		sqlBuf.WriteString(j.joinType)
		sqlBuf.WriteString(" ")
		sqlBuf.WriteString(q.dialect.Quote(j.table))
		sqlBuf.WriteString(" ON ")
		sqlBuf.WriteString(j.onClause)
	}

	// WHERE clause
	whereConds := q.where
	if pred := q.softDeletePredicate(); pred != nil {
		whereConds = append([]condition{*pred}, whereConds...)
	}

	if len(whereConds) > 0 {
		// Start the WHERE arg index after any CTE args already enqueued so
		// dialect placeholders ($N / @pN / :N) line up with args slice.
		argIndex := len(args) + 1
		whereSQL, whereArgs, err := q.buildWhereClause(whereConds, argIndex)
		if err != nil {
			return 0, err
		}
		sqlBuf.WriteString(" WHERE ")
		sqlBuf.WriteString(whereSQL)
		args = append(args, whereArgs...)
	}

	ctx, cancel := context.WithTimeout(q.ctx, q.client.limits.QueryTimeout)
	defer cancel()

	var count int64
	// Count is a genuine read → replica-routed (ADR-0015 follow-up).
	err := q.executeReadRow(ctx, sqlBuf.String(), args, &count)
	if err != nil {
		return 0, fmt.Errorf("count failed: %w", wrapDBError(err))
	}

	return count, nil
}

// countCompound counts a set-op (UNION/INTERSECT/EXCEPT) query by wrapping
// the full compound select in a derived table: SELECT COUNT(*) FROM (…) qk_count.
// ORDER BY / LIMIT / OFFSET are stripped from the wrapped select — they don't
// change the total, and MSSQL/Oracle reject ORDER BY inside a derived table.
// Any CTE prefix is hoisted outside the derived table (`WITH … SELECT COUNT(*)
// FROM (…)`) because MSSQL does not accept WITH inside a subquery.
func (q *Query[T]) countCompound() (int64, error) {
	cq := q.clone()
	cq.orderBy = nil
	cq.limit = 0
	cq.hasLimit = false
	cq.offset = 0

	fullSQL, args, err := cq.buildSelect()
	if err != nil {
		return 0, err
	}

	var sqlBuf strings.Builder
	inner := fullSQL
	// buildSelect writes the buildCTEPrefix output verbatim at the start of
	// the statement, so re-rendering the prefix identifies the exact prefix
	// to hoist. Alias `qk_count` is unquoted lowercase — valid (and required
	// by MySQL/MSSQL for derived tables) on all six dialects; Oracle accepts
	// an alias without AS.
	if cteSQL, _, cteErr := cq.buildCTEPrefix(1); cteErr != nil {
		return 0, cteErr
	} else if cteSQL != "" {
		inner = strings.TrimPrefix(fullSQL, cteSQL)
		sqlBuf.WriteString(cteSQL)
	}
	sqlBuf.WriteString("SELECT COUNT(*) FROM (")
	sqlBuf.WriteString(inner)
	sqlBuf.WriteString(") qk_count")

	ctx, cancel := context.WithTimeout(q.ctx, q.client.limits.QueryTimeout)
	defer cancel()

	var count int64
	// Same replica routing as the flat Count path — a genuine read.
	if err := q.executeReadRow(ctx, sqlBuf.String(), args, &count); err != nil {
		return 0, fmt.Errorf("count failed: %w", wrapDBError(err))
	}
	return count, nil
}

// buildCTEPrefix renders the `WITH ... AS (...)` (or `WITH RECURSIVE ...`)
// prefix and returns the SQL plus the inner args, in the same order they
// must appear in the final args slice. Returns ("", nil, nil) when the
// query has no CTE definitions. Shared by buildSelect, Count, and
// aggregate so any caller emitting SELECT against the table inherits the
// CTE prefix and the correct argIndex offset.
func (q *BaseQuery) buildCTEPrefix(startArgIndex int) (string, []any, error) {
	if len(q.ctes) == 0 {
		return "", nil, nil
	}
	anyRecursive := false
	for _, e := range q.ctes {
		if e.recursive {
			anyRecursive = true
			break
		}
	}
	var sqlBuf strings.Builder
	sqlBuf.WriteString("WITH ")
	// Oracle and SQL Server recognise recursion from the query structure and
	// REJECT the RECURSIVE keyword (Oracle: ORA-02000 "missing AS keyword";
	// T-SQL: syntax error). PostgreSQL, MySQL 8+, MariaDB and SQLite require it.
	if anyRecursive && recursiveCTEKeyword(q.dialect.Name()) {
		sqlBuf.WriteString("RECURSIVE ")
	}
	args := make([]any, 0)
	for i, e := range q.ctes {
		if i > 0 {
			sqlBuf.WriteString(", ")
		}
		rendered, _, err := substitutePathMarkers(e.sql, len(e.args), q.dialect, startArgIndex+len(args))
		if err != nil {
			return "", nil, err
		}
		sqlBuf.WriteString(q.dialect.Quote(e.name))
		sqlBuf.WriteString(" AS (")
		sqlBuf.WriteString(rendered)
		sqlBuf.WriteString(")")
		args = append(args, e.args...)
	}
	sqlBuf.WriteString(" ")
	return sqlBuf.String(), args, nil
}

// recursiveCTEKeyword reports whether the dialect spells a recursive CTE with
// the explicit RECURSIVE keyword. Oracle and SQL Server infer recursion from
// the query structure and reject the keyword; PostgreSQL, MySQL, MariaDB and
// SQLite require it. Name-based to stay non-breaking for custom dialects (they
// default to emitting RECURSIVE, the standard-SQL spelling).
func recursiveCTEKeyword(dialectName string) bool {
	switch dialectName {
	case "oracle", "mssql":
		return false
	default:
		return true
	}
}

// buildSelect constructs the SELECT SQL query.
func (q *Query[T]) buildSelect() (string, []any, error) {
	var sqlBuf strings.Builder
	var args []any

	if cteSQL, cteArgs, err := q.buildCTEPrefix(1); err != nil {
		return "", nil, err
	} else if cteSQL != "" {
		sqlBuf.WriteString(cteSQL)
		args = append(args, cteArgs...)
	}

	// SELECT clause
	sqlBuf.WriteString("SELECT ")
	if q.distinct {
		sqlBuf.WriteString("DISTINCT ")
	}
	// SELECT list: regular cols (Select), then AST projections
	// (SelectExpr). Both render in the order they were added; '?' markers
	// in the AST projections get reindexed to the dialect placeholder at
	// the current argIndex.
	hasCols := len(q.selectCols) > 0
	hasExprs := len(q.selectExprs) > 0
	if !hasCols && !hasExprs {
		// A bare `*` under a JOIN pulls every joined table's columns into the
		// result set: duplicate names (id, deleted_at, …) collide and the
		// scanner mis-binds another table's column into T (e.g. a NULL
		// order_lines.id from an outer join scanned into Order.ID). Project
		// only the base table's columns so the result set matches T. (BB-2)
		if len(q.joins) > 0 {
			sqlBuf.WriteString(q.dialect.Quote(q.table))
			sqlBuf.WriteString(".*")
		} else {
			sqlBuf.WriteString("*")
		}
	} else {
		if hasCols {
			quoted := make([]string, len(q.selectCols))
			for i, col := range q.selectCols {
				if err := q.guard.ValidateIdentifier(col); err != nil {
					return "", nil, err
				}
				quoted[i] = q.dialect.Quote(col)
			}
			sqlBuf.WriteString(strings.Join(quoted, ", "))
		}
		if hasExprs {
			// Track the AST-projection arg index explicitly (matching the
			// buildCTEPrefix pattern) instead of relying on len(args)+1 in
			// each iteration. Sanity-check the substitution count against
			// len(e.args) so a marker/arg mismatch surfaces here, not as
			// a malformed SQL the driver rejects.
			exprArgIndex := len(args) + 1
			for i, e := range q.selectExprs {
				if i > 0 || hasCols {
					sqlBuf.WriteString(", ")
				}
				rendered, n, err := substitutePathMarkers(e.sql, len(e.args), q.dialect, exprArgIndex)
				if err != nil {
					return "", nil, err
				}
				if n != len(e.args) {
					return "", nil, fmt.Errorf("%w: SelectExpr %q expected %d markers, substituted %d",
						ErrInvalidQuery, e.alias, len(e.args), n)
				}
				sqlBuf.WriteString(rendered)
				sqlBuf.WriteString(" AS ")
				sqlBuf.WriteString(q.dialect.Quote(e.alias))
				args = append(args, e.args...)
				exprArgIndex += len(e.args)
			}
		}
	}

	// FROM clause
	sqlBuf.WriteString(" FROM ")
	if err := q.guard.ValidateIdentifier(q.table); err != nil {
		return "", nil, err
	}
	sqlBuf.WriteString(q.fullTableName())

	// Pessimistic locking: MSSQL emits its hint right after the table name
	// (`FROM users WITH (UPDLOCK, ROWLOCK)`); the row-level dialects emit a
	// suffix at the very end of the SELECT — handled below.
	lockHint, lockSuffix, lockErr := q.dialect.LockSuffix(q.lock)
	if lockErr != nil {
		return "", nil, lockErr
	}
	if lockHint != "" {
		sqlBuf.WriteString(lockHint)
	}

	// Oracle cannot combine a row-limiting clause (OFFSET/FETCH) with
	// FOR UPDATE/SKIP LOCKED/NOWAIT: ORA-02014, because Oracle implements the
	// row-limiting clause as an analytic-function view and FOR UPDATE may not
	// read from it. List() adds an implicit safety Limit(100); when the caller
	// did not request limiting explicitly, drop the row-limiting clause so the
	// lock still applies — to every matching row (warned below). When the
	// caller DID ask for an explicit Limit/Offset alongside the lock there is
	// no valid single-statement form, so fail clearly rather than silently
	// widening the lock or mis-truncating the result. (BB-4)
	suppressRowLimit := false
	if !q.lock.IsZero() && q.dialect.Name() == "oracle" {
		if q.hasLimit || q.offset > 0 {
			return "", nil, fmt.Errorf("%w: oracle cannot combine row locking (FOR UPDATE/SKIP LOCKED/NOWAIT) with an explicit Limit/Offset (ORA-02014); drop the limit to lock all matching rows, or fetch the keys first and lock them by id inside a transaction", ErrUnsupportedFeature)
		}
		suppressRowLimit = true
		// Only warn when there is actually a cap to drop. Cursor()/Iter() set
		// no limit (q.limit == 0 → nothing suppressed), so the dropped-cap
		// notice would be a false positive for them. (BB-4, S-4)
		if q.limit > 0 && q.client != nil && q.client.logger != nil {
			q.client.logger.Warn("locking query on Oracle: the implicit List() row cap is not applied because Oracle forbids FOR UPDATE with OFFSET/FETCH (ORA-02014); the lock spans every matching row — narrow the WHERE or lock by key inside a transaction")
		}
	}

	// JOIN clauses
	if len(q.joins) > 0 {
		if len(q.joins) > q.client.limits.MaxJoins {
			return "", nil, fmt.Errorf("%w: query exceeds maximum of %d joins", ErrInvalidQuery, q.client.limits.MaxJoins)
		}
		for _, j := range q.joins {
			if err := q.guard.ValidateIdentifier(j.table); err != nil {
				return "", nil, err
			}
			if err := guard.ValidateJoinOn(j.onClause); err != nil {
				return "", nil, fmt.Errorf("%w: %v", ErrInvalidJoin, err)
			}
			sqlBuf.WriteString(" ")
			sqlBuf.WriteString(j.joinType)
			sqlBuf.WriteString(" ")
			sqlBuf.WriteString(q.dialect.Quote(j.table))
			sqlBuf.WriteString(" ON ")
			sqlBuf.WriteString(j.onClause)
		}
	}

	// WHERE clause — enforce MaxWhereConditions limit
	if q.client != nil && len(q.where) > q.client.limits.MaxWhereConditions {
		return "", nil, fmt.Errorf("%w: query has %d WHERE conditions, exceeds maximum of %d",
			ErrInvalidQuery, len(q.where), q.client.limits.MaxWhereConditions)
	}

	whereConds := q.where
	if pred := q.softDeletePredicate(); pred != nil {
		whereConds = append([]condition{*pred}, whereConds...)
	}

	if len(whereConds) > 0 {
		// Start the WHERE arg index after any CTE args already enqueued so
		// dialect placeholders ($N / @pN / :N) line up with the args slice.
		argIndex := len(args) + 1
		whereSQL, whereArgs, err := q.buildWhereClause(whereConds, argIndex)
		if err != nil {
			return "", nil, err
		}
		sqlBuf.WriteString(" WHERE ")
		sqlBuf.WriteString(whereSQL)
		args = append(args, whereArgs...)
	}

	// GROUP BY clause
	if len(q.groupBy) > 0 {
		quotedGrp := make([]string, len(q.groupBy))
		for i, col := range q.groupBy {
			if err := q.guard.ValidateIdentifier(col); err != nil {
				return "", nil, err
			}
			quotedGrp[i] = q.dialect.Quote(col)
		}
		sqlBuf.WriteString(" GROUP BY ")
		sqlBuf.WriteString(strings.Join(quotedGrp, ", "))
	}

	// HAVING clause
	if len(q.having) > 0 {
		argIndex := len(args) + 1
		havingSQL, havingArgs, err := q.buildWhereClause(q.having, argIndex)
		if err != nil {
			return "", nil, err
		}
		sqlBuf.WriteString(" HAVING ")
		sqlBuf.WriteString(havingSQL)
		args = append(args, havingArgs...)
	}

	// Set operations (UNION / INTERSECT / EXCEPT). The standard SQL
	// compound-select form is `SELECT ... <op> SELECT ... ORDER BY ...
	// LIMIT ...` — flat, no parens around operands (SQLite rejects
	// parens here). Operands are guaranteed to be core-only (no ORDER /
	// LIMIT / lock / nested set-ops / CTEs) by the attachSetOp guards,
	// so splicing them in flat is portable across all six dialects.
	// The outer query's ORDER BY / LIMIT then bind to the combined
	// result.
	if len(q.setOps) > 0 {
		for _, op := range q.setOps {
			kw, err := setOpKeyword(q.dialect, op.kind, op.all)
			if err != nil {
				return "", nil, err
			}
			rendered, n, err := substitutePathMarkers(op.sql, len(op.args), q.dialect, len(args)+1)
			if err != nil {
				return "", nil, err
			}
			if n != len(op.args) {
				return "", nil, fmt.Errorf("%w: %s operand expected %d markers, substituted %d",
					ErrInvalidQuery, op.kind, len(op.args), n)
			}
			sqlBuf.WriteString(" ")
			sqlBuf.WriteString(kw)
			sqlBuf.WriteString(" ")
			sqlBuf.WriteString(rendered)
			args = append(args, op.args...)
		}
	}

	// ORDER BY clause
	if len(q.orderBy) > 0 {
		sqlBuf.WriteString(" ORDER BY ")
		for i, o := range q.orderBy {
			if i > 0 {
				sqlBuf.WriteString(", ")
			}
			if err := q.guard.ValidateIdentifier(o.column); err != nil {
				return "", nil, err
			}
			sqlBuf.WriteString(q.dialect.Quote(o.column))
			if o.desc {
				sqlBuf.WriteString(" DESC")
			} else {
				sqlBuf.WriteString(" ASC")
			}
		}
	} else if !suppressRowLimit && (q.limit > 0 || q.offset > 0) && (q.dialect.Name() == "mssql" || q.dialect.Name() == "oracle") {
		// MSSQL/Oracle REQUIRE ORDER BY for OFFSET/FETCH. Use PK as default.
		// DISTINCT, GROUP BY and set-ops (UNION/INTERSECT/EXCEPT) all restrict
		// which items may appear in ORDER BY — only select-list columns or an
		// ordinal position — so fall back to positional "1" there. Otherwise the
		// PK isn't in the projected SELECT list and the engine rejects it
		// (ORA-01791/ORA-00979 on Oracle; on a compound-select MSSQL/Oracle raise
		// "ORDER BY items must appear in the select list if the statement contains
		// a UNION/INTERSECT/EXCEPT operator" — Finding J).
		sqlBuf.WriteString(" ORDER BY ")
		if q.distinct || len(q.groupBy) > 0 || len(q.setOps) > 0 {
			sqlBuf.WriteString("1")
		} else if q.pk.Column != "" {
			// Under a JOIN the bare PK name is ambiguous when the joined
			// table also has it (Oracle: ORA-00918; SQL Server resolves it
			// against the projected SELECT list, but qualifying is correct on
			// both). Qualify the framework-injected ordering column with the
			// base table, matching the base-table projection above. (BB-2)
			if len(q.joins) > 0 {
				sqlBuf.WriteString(q.dialect.Quote(q.table))
				sqlBuf.WriteString(".")
			}
			sqlBuf.WriteString(q.dialect.Quote(q.pk.Column))
		} else {
			sqlBuf.WriteString("(SELECT NULL)") // MSSQL fallback when no PK
		}
		sqlBuf.WriteString(" ASC")
	}

	// LIMIT/OFFSET — suppressed under an Oracle lock (see ORA-02014 note above).
	if !suppressRowLimit {
		limitOffset := q.dialect.LimitOffset(q.limit, q.offset)
		if limitOffset != "" {
			sqlBuf.WriteString(" ")
			sqlBuf.WriteString(limitOffset)
		}
	}

	// Pessimistic-lock suffix (PG / MySQL / MariaDB / Oracle).
	if lockSuffix != "" {
		sqlBuf.WriteString(lockSuffix)
	}

	sqlStr := sqlBuf.String()

	// Enforce MaxQueryLength
	if q.client != nil && q.client.limits.MaxQueryLength > 0 && len(sqlStr) > q.client.limits.MaxQueryLength {
		return "", nil, fmt.Errorf("%w: generated SQL length %d exceeds maximum of %d bytes",
			ErrInvalidQuery, len(sqlStr), q.client.limits.MaxQueryLength)
	}

	return sqlStr, args, nil
}

// inChunkSize is the maximum number of parent-keys we put inside a single
// IN(...) clause when eager-loading relations. Sized to stay under Oracle's
// 1000-element IN cap; MSSQL's ~2100 bind-parameter ceiling tolerates the
// same chunk plus the handful of tenant / poly-type parameters added on
// top, leaving comfortable headroom.
const inChunkSize = 1000

// chunkParentKeys yields slices of allKeys of at most inChunkSize elements
// to fn. Used by the eager-loading paths so a giant Preload doesn't blow
// dialect IN / bind-parameter caps. fn is invoked once per chunk and any
// error short-circuits the iteration.
func chunkParentKeys(allKeys []any, fn func(chunk []any) error) error {
	for start := 0; start < len(allKeys); start += inChunkSize {
		end := start + inChunkSize
		if end > len(allKeys) {
			end = len(allKeys)
		}
		if err := fn(allKeys[start:end]); err != nil {
			return err
		}
	}
	return nil
}

// substitutePathMarkers walks fragment, replacing each literal '?' with the
// dialect-specific placeholder for the next argIndex, and returns the rendered
// fragment plus the number of substitutions made. expectedMarkers is the
// number of '?' the caller intends to substitute (i.e. len(cond.extraArgs));
// a mismatch is treated as a builder bug and surfaced as an error so it cannot
// silently produce malformed SQL.
//
// Index arithmetic: the first '?' becomes Placeholder(argIndex+0), the second
// Placeholder(argIndex+1), etc. After the function returns, the caller
// advances its argIndex by the returned count and appends extraArgs to args
// in the same order, keeping the parameters aligned with the rendered SQL.
//
// dialect.Quote does not introduce '?' into identifiers in any of the
// supported dialects (`"`, backtick, `[ ]`, uppercased), so '?' inside the
// fragment is unambiguously a bind marker placed by JSONExtract.
func substitutePathMarkers(fragment string, expectedMarkers int, dialect Dialect, argIndex int) (string, int, error) {
	if expectedMarkers == 0 {
		return fragment, 0, nil
	}
	var b strings.Builder
	b.Grow(len(fragment))
	count := 0
	for i := 0; i < len(fragment); i++ {
		if fragment[i] == '?' {
			b.WriteString(dialect.Placeholder(argIndex + count))
			count++
			continue
		}
		b.WriteByte(fragment[i])
	}
	if count != expectedMarkers {
		return "", 0, fmt.Errorf("%w: raw fragment has %d '?' markers, expected %d (extraArgs mismatch)", ErrInvalidQuery, count, expectedMarkers)
	}
	return b.String(), count, nil
}

// buildWhereClause recursively builds WHERE SQL from conditions,
// handling AND/OR logic and grouped sub-conditions.
func (q *Query[T]) buildWhereClause(conds []condition, argIndex int) (string, []any, error) {
	var parts []string
	var args []any

	for i, cond := range conds {
		// Determine connector
		connector := ""
		not := ""
		if i > 0 {
			switch cond.logic {
			case "OR":
				connector = " OR "
			case "AND NOT":
				connector = " AND "
				not = "NOT "
			default:
				connector = " AND "
			}
		} else if cond.logic == "AND NOT" {
			not = "NOT "
		}

		// Handle grouped sub-conditions (from Or())
		if len(cond.group) > 0 {
			groupSQL, groupArgs, err := q.buildWhereClause(cond.group, argIndex)
			if err != nil {
				return "", nil, err
			}
			parts = append(parts, connector+"("+groupSQL+")")
			args = append(args, groupArgs...)
			argIndex += len(groupArgs)
			continue
		}

		// Normal condition
		if !cond.isRaw {
			if err := q.guard.ValidateIdentifier(cond.column); err != nil {
				return "", nil, err
			}
		}
		// Raw expressions with empty operator are written as-is plus any
		// '?' markers in the fragment substituted for dialect placeholders.
		// This is the path the AST (WhereExpr/HavingExpr) takes: the
		// Expr.ToSQL output already contains the fully-formed predicate;
		// buildWhereClause's only job is to swap '?' for $N / @p1 / : / etc.
		// at the right argIndex and thread extraArgs into args in order.
		if cond.isRaw && cond.operator == "" {
			rendered, n, err := substitutePathMarkers(cond.column, len(cond.extraArgs), q.dialect, argIndex)
			if err != nil {
				return "", nil, err
			}
			parts = append(parts, connector+not+rendered)
			args = append(args, cond.extraArgs...)
			argIndex += n
			continue
		}

		if err := q.guard.ValidateOperator(cond.operator); err != nil {
			return "", nil, err
		}

		var condSQL strings.Builder
		condSQL.WriteString(connector)
		if not != "" {
			condSQL.WriteString(not)
		}
		if cond.isRaw {
			// Raw fragments may contain '?' bind markers (e.g. JSONExtract path
			// args). Substitute each marker for the dialect placeholder and
			// thread the corresponding extraArgs into args at the matching
			// argIndex, so the path components land before the value bind.
			rendered, n, err := substitutePathMarkers(cond.column, len(cond.extraArgs), q.dialect, argIndex)
			if err != nil {
				return "", nil, err
			}
			condSQL.WriteString(rendered)
			args = append(args, cond.extraArgs...)
			argIndex += n
		} else {
			condSQL.WriteString(q.dialect.Quote(cond.column))
		}
		condSQL.WriteString(" ")
		condSQL.WriteString(cond.operator)
		condSQL.WriteString(" ")

		switch cond.operator {
		case "IN", "NOT IN":
			values := cond.value.([]any)
			placeholders := make([]string, len(values))
			for j := range values {
				placeholders[j] = q.dialect.Placeholder(argIndex)
				args = append(args, values[j])
				argIndex++
			}
			condSQL.WriteString("(")
			condSQL.WriteString(strings.Join(placeholders, ", "))
			condSQL.WriteString(")")
		case "BETWEEN", "NOT BETWEEN":
			values := cond.value.([]any)
			condSQL.WriteString(q.dialect.Placeholder(argIndex))
			condSQL.WriteString(" AND ")
			condSQL.WriteString(q.dialect.Placeholder(argIndex + 1))
			args = append(args, values[0], values[1])
			argIndex += 2
		case "IS NULL", "IS NOT NULL":
			// No placeholder or value needed
		default:
			condSQL.WriteString(q.dialect.Placeholder(argIndex))
			args = append(args, cond.value)
			argIndex++
		}

		parts = append(parts, condSQL.String())
	}

	return strings.Join(parts, ""), args, nil
}

// scanRow scans a single row into the entity.
// Uses cached ModelMeta for O(1) field lookups when available.
func (q *Query[T]) scanRow(rows *sql.Rows, dest *T) error {
	// Fast path: a generated typed scanner registered for this model (F6-2),
	// resolved once per query and memoized so the reflect.Type lookup and
	// registry read are not repeated per row. It is skipped when the
	// per-column timezone feature is active for this query (the generated
	// scanner carries no runtime timezone state, so those queries stay on the
	// reflection path) and when no compatible generated code is registered.
	if !q.typedScanResolved {
		if !q.tzActive() {
			q.typedScan, _ = lookupTypedScanner(reflect.TypeOf(dest))
		}
		q.typedScanResolved = true
	}
	if q.typedScan != nil {
		return q.typedScan(rows, dest)
	}

	v := reflect.ValueOf(dest)
	if v.Kind() != reflect.Ptr || v.IsNil() {
		return fmt.Errorf("dest must be a non-nil pointer")
	}

	elem := v.Elem()
	if elem.Kind() != reflect.Struct {
		return fmt.Errorf("dest must point to a struct")
	}

	// Resolve the column→field plan once per query. The columns, their field
	// indices and the []any target buffer are invariant across rows — only the
	// per-row struct (elem) changes — so this hoists rows.Columns(), the
	// per-column lookup and the slice allocation out of the per-row path.
	if !q.scanPlanResolved {
		if err := q.resolveScanPlan(rows, elem); err != nil {
			return err
		}
		q.scanPlanResolved = true
	}

	// Per row: re-point the reused scan-target buffer at this row's fields.
	for i := range q.scanPlan {
		sc := &q.scanPlan[i]
		if sc.fieldIndex >= 0 {
			q.scanDest[i] = makeScanDest(elem.Field(sc.fieldIndex), sc.loc)
		} else {
			q.scanDest[i] = &q.scanDiscard
		}
	}
	return rows.Scan(q.scanDest...)
}

// resolveScanPlan computes, once per query, the mapping from each result column
// to a struct field index (-1 to discard) plus its per-column timezone, and
// allocates the reusable scan-target buffer. The matching mirrors the historical
// per-row path: cached FieldMeta first (FieldByCol), then the reflection
// fallback (findFieldIndex); a column matching no field is discarded.
func (q *Query[T]) resolveScanPlan(rows *sql.Rows, elem reflect.Value) error {
	columns, err := rows.Columns()
	if err != nil {
		return err
	}
	// Resolve the per-column timezone state once. When the feature is inactive
	// for this query, tzOn is false and every column scans with loc nil —
	// makeScanDest then behaves exactly as it did on the per-row path.
	tzOn := q.tzActive()
	var clientDefaultTZ *time.Location
	if tzOn && q.client != nil {
		clientDefaultTZ = q.client.defaultTZ
	}
	plan := make([]scanCol, len(columns))
	for i, col := range columns {
		idx := -1
		var loc *time.Location
		if q.meta != nil {
			if fm, ok := q.meta.FieldByCol[strings.ToLower(col)]; ok {
				idx = fm.Index
				if tzOn {
					loc = resolveFieldTZ(fm, clientDefaultTZ)
				}
			}
		}
		if idx < 0 {
			// Slow path: reflection lookup. No FieldMeta here, so only the
			// client default can apply — a column tag override needs the cached
			// metadata.
			if fi := q.findFieldIndex(elem, col); fi >= 0 {
				idx = fi
				if tzOn {
					loc = clientDefaultTZ
				}
			}
		}
		plan[i] = scanCol{fieldIndex: idx, loc: loc}
	}
	q.scanPlan = plan
	q.scanDest = make([]any, len(columns))
	return nil
}

// findFieldIndex returns the struct field index matching the column name (the
// reflection fallback for uncached lookups), or -1 if no field matches. elem
// comes from a pointer's Elem(), so a matched field is always addressable.
func (q *Query[T]) findFieldIndex(elem reflect.Value, column string) int {
	t := elem.Type()
	for i := 0; i < t.NumField(); i++ {
		field := t.Field(i)
		dbTag := columnFromDBTag(field.Tag.Get("db"))
		if strings.EqualFold(dbTag, column) {
			return i
		}
		if strings.EqualFold(toSnakeCase(field.Name), column) || strings.EqualFold(field.Name, column) {
			return i
		}
	}
	return -1
}

// scanCol is one column's resolved scan target in a Query's reflection scan
// plan (see scanPlan): the struct field index to scan into, or -1 to discard
// the column, plus the per-column timezone (nil when the tz feature is off).
type scanCol struct {
	fieldIndex int
	loc        *time.Location
}

// loadRelations eager loads requested relations for the given results,
// recursing into dotted paths (Phase 2: Preload("Orders.Items.Product")).
//
// reflect.ValueOf(&results).Elem() is used (rather than reflect.ValueOf(results))
// so the slice is addressable: when we recurse into nested levels we take
// pointers into the parent's relation slices (Posts[j].Addr()) and those
// pointers must alias back into the original results so mutations
// propagate up.
func (q *Query[T]) loadRelations(results []T) error {
	if len(results) == 0 || len(q.preloads) == 0 {
		return nil
	}
	tree := parsePreloads(q.preloads)
	return q.loadPreloadTree(reflect.ValueOf(&results).Elem(), q.meta, tree)
}

// loadPreloadTree walks the preload tree against an arbitrary parent slice
// (reflect.Value of []T for some T). Owner meta describes the parent shape;
// each iteration loads the named relation, then if children are present,
// gathers the loaded child slice across all parents and recurses with the
// related model's meta.
func (q *BaseQuery) loadPreloadTree(parents reflect.Value, ownerMeta *ModelMeta, nodes []*preloadNode) error {
	if parents.Len() == 0 || len(nodes) == 0 {
		return nil
	}
	for _, node := range nodes {
		relMeta, ok := ownerMeta.Relations[node.name]
		if !ok {
			return fmt.Errorf("relation %s not found on model %s", node.name, ownerMeta.Table)
		}
		relModel := GetModelMetaByType(relMeta.RefType)

		switch relMeta.Type {
		case "m2m", "many_to_many":
			if err := q.loadM2M(parents, ownerMeta, node.name, relMeta, relModel); err != nil {
				return err
			}
		case "polymorphic":
			if err := q.loadPolymorphic(parents, ownerMeta, node.name, relMeta, relModel); err != nil {
				return err
			}
		default:
			if err := q.loadStandard(parents, ownerMeta, node.name, relMeta, relModel); err != nil {
				return err
			}
		}

		// Recurse into nested levels: gather every loaded child across the
		// parent slice into a flat slice, then preload its own relations.
		if len(node.children) > 0 {
			children := gatherLoadedChildren(parents, node.name, relMeta)
			if children.Len() > 0 {
				if err := q.loadPreloadTree(children, relModel, node.children); err != nil {
					return err
				}
			}
		}
	}

	return nil
}

// aggregate executes SELECT agg_func(column) FROM table WHERE …
//
// NOTE: aggregates intentionally do not apply joins or preloads — the SQL
// is built from the base table only. If you need an aggregate over a join,
// use a database view, RawQuery, or wait for the Phase 2 AST.
func (q *Query[T]) aggregate(fn, column string) (float64, error) {
	if q.client == nil {
		return 0, fmt.Errorf("%w: client not initialized", ErrInvalidQuery)
	}
	if err := q.guard.ValidateIdentifier(column); err != nil {
		return 0, err
	}

	var sqlBuf strings.Builder
	var args []any

	if cteSQL, cteArgs, err := q.buildCTEPrefix(1); err != nil {
		return 0, err
	} else if cteSQL != "" {
		sqlBuf.WriteString(cteSQL)
		args = append(args, cteArgs...)
	}

	sqlBuf.WriteString("SELECT ")
	sqlBuf.WriteString(fn)
	sqlBuf.WriteString("(")
	sqlBuf.WriteString(q.dialect.Quote(column))
	sqlBuf.WriteString(") FROM ")
	sqlBuf.WriteString(q.fullTableName())

	whereConds := q.where
	if pred := q.softDeletePredicate(); pred != nil {
		whereConds = append([]condition{*pred}, whereConds...)
	}
	if len(whereConds) > 0 {
		// Start arg index after any CTE args already enqueued.
		whereSQL, whereArgs, err := q.buildWhereClause(whereConds, len(args)+1)
		if err != nil {
			return 0, err
		}
		sqlBuf.WriteString(" WHERE ")
		sqlBuf.WriteString(whereSQL)
		args = append(args, whereArgs...)
	}

	ctx, cancel := context.WithTimeout(q.ctx, q.client.limits.QueryTimeout)
	defer cancel()

	// Aggregates are genuine reads → replica-routed (ADR-0015 follow-up).
	var result sql.NullFloat64
	if err := q.executeReadRow(ctx, sqlBuf.String(), args, &result); err != nil {
		return 0, wrapDBError(err)
	}
	if !result.Valid {
		return 0, nil
	}
	return result.Float64, nil
}

// Sum returns the sum of the given column across all matching rows.
func (q *Query[T]) Sum(column string) (float64, error) {
	return q.aggregate("SUM", column)
}

// Avg returns the average of the given column across all matching rows.
func (q *Query[T]) Avg(column string) (float64, error) {
	return q.aggregate("AVG", column)
}

// Min returns the minimum value of the given column across all matching rows.
func (q *Query[T]) Min(column string) (float64, error) {
	return q.aggregate("MIN", column)
}

// Max returns the maximum value of the given column across all matching rows.
func (q *Query[T]) Max(column string) (float64, error) {
	return q.aggregate("MAX", column)
}

// WhereSubquery adds a WHERE column operator (subquery) condition.
// The subquery is a raw SQL string. Use this only when AllowRawQueries is enabled.
//
// Example:
//
//	sub := "SELECT MAX(id) FROM orders WHERE status = 'open'"
//	quark.For[User](ctx, client).WhereSubquery("id", "IN", sub).List()
func (q *Query[T]) WhereSubquery(column, operator, subquery string) *Query[T] {
	c := q.clone()
	if !c.client.limits.AllowRawQueries {
		c.err = fmt.Errorf("%w: WhereSubquery requires AllowRawQueries to be enabled", ErrInvalidQuery)
		return c
	}
	c.where = ownedAppend(c.where, condition{
		column:   q.dialect.Quote(column) + " " + operator + " (" + subquery + ")",
		operator: "IS NOT NULL", // sentinel — overridden by isRaw rendering below
		logic:    "AND",
		isRaw:    true,
		value:    nil,
	})
	// Override: store as a raw expression without the sentinel operator
	last := &c.where[len(c.where)-1]
	last.column = q.dialect.Quote(column) + " " + operator + " (" + subquery + ")"
	last.operator = ""
	return c
}

// scanAndMapPolymorphicRelations scans rows and maps them to parent structs (for polymorphic relations)
func (q *Query[T]) scanAndMapPolymorphicRelations(rows *sql.Rows, results []T, relName string, relMeta *RelationMeta, relModel *ModelMeta, parentKeyMap map[any][]int) error {
	cols, _ := rows.Columns()

	// Find the poly ID column in related model
	polyIDFieldMeta, ok := relModel.FieldByCol[strings.ToLower(relMeta.PolyIDColumn)]
	if !ok {
		return fmt.Errorf("could not find polymorphic ID column %s in related model", relMeta.PolyIDColumn)
	}

	for rows.Next() {
		relPtr := reflect.New(relMeta.RefType)
		relVal := relPtr.Elem()

		scanDest := make([]any, len(cols))
		for i, col := range cols {
			if fm, ok := relModel.FieldByCol[strings.ToLower(col)]; ok {
				scanDest[i] = makeScanDest(relVal.Field(fm.Index), q.preloadColumnTZ(relModel, fm))
			} else {
				var discard any
				scanDest[i] = &discard
			}
		}

		if err := rows.Scan(scanDest...); err != nil {
			return err
		}

		// Get the parent ID from the polymorphic foreign key
		parentID := relVal.Field(polyIDFieldMeta.Index).Interface()

		if parentIndexes, ok := parentKeyMap[parentID]; ok {
			for _, pIdx := range parentIndexes {
				parentVal := reflect.ValueOf(&results[pIdx]).Elem()
				relField := parentVal.FieldByName(relName)

				if relMeta.IsSlice {
					relField.Set(reflect.Append(relField, relVal))
				} else {
					if relField.Kind() == reflect.Ptr {
						relField.Set(relPtr)
					} else {
						relField.Set(relVal)
					}
				}
			}
		}
	}

	return rows.Err()
}
