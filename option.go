// Copyright 2026 jcsvwinston
// SPDX-License-Identifier: Apache-2.0

package quark

import (
	"context"
	"database/sql"
	"log/slog"
	"time"
)

// Option configures a Client.
type Option func(*Client)

// WithDialect sets the SQL dialect for the client.
// If not set, the dialect will be auto-detected from the database driver.
func WithDialect(d Dialect) Option {
	return func(c *Client) {
		c.dialect = d
	}
}

// WithLogger sets the logger for the client. Passing nil is a no-op:
// the client keeps its default ([slog.Default]) rather than dropping
// to a nil logger. This guarantees the internal log sites (slow-query
// log, deadlock-retry, post-commit hook errors, event-emit failures)
// always have a non-nil logger to write to.
func WithLogger(l *slog.Logger) Option {
	return func(c *Client) {
		if l != nil {
			c.logger = l
		}
	}
}

// WithQueryObserver adds a query observer to the client.
// Multiple observers can be added and will be called in order.
func WithQueryObserver(o QueryObserver) Option {
	return func(c *Client) {
		c.observers = append(c.observers, o)
	}
}

// WithMiddleware adds middleware to the query execution chain.
// Middleware is applied in the order they are added.
func WithMiddleware(m Middleware) Option {
	return func(c *Client) {
		c.middleware = append(c.middleware, m)
	}
}

// Limits defines security and performance limits for queries.
//
// For the numeric fields, zero means "not set": [WithLimits] fills any
// zero-valued numeric field from [DefaultLimits], so a partial literal such
// as Limits{MaxResults: 500} raises one limit without silently zeroing the
// rest. A negative value is passed through untouched — that is the explicit
// form for "no cap" where a consumer honours it (e.g. MaxQueryLength < 0
// disables the generated-SQL length check).
//
// The boolean fields (AllowRawQueries, SafeMigrations) are NOT normalized:
// an explicit false is indistinguishable from the zero value, so a partial
// literal carries AllowRawQueries=false and SafeMigrations=false regardless
// of the defaults. To change one field while keeping every default —
// including SafeMigrations=true — start from [DefaultLimits] and modify it.
// When a partial literal leaves SafeMigrations=false, [New] logs one
// structured WARN pointing here — see [WithLimits].
type Limits struct {
	MaxQueryLength     int
	MaxResults         int
	MaxJoins           int
	MaxWhereConditions int
	QueryTimeout       time.Duration
	AllowRawQueries    bool
	SafeMigrations     bool
}

// DefaultLimits returns sensible default limits.
func DefaultLimits() Limits {
	return Limits{
		MaxQueryLength:     10 * 1024, // 10KB
		MaxResults:         10000,     // 10k rows max
		MaxJoins:           5,
		MaxWhereConditions: 20,
		QueryTimeout:       30 * time.Second,
		AllowRawQueries:    false, // Must explicitly enable
		SafeMigrations:     true,  // Prevent accidental data loss
	}
}

// WithLimits sets the security and performance limits.
//
// Zero-valued NUMERIC fields are filled from [DefaultLimits] before the
// struct is installed: zero means "not touched", never "no limit" or "no
// timeout". Without this normalization a partial literal — the natural way
// to raise one limit — left QueryTimeout at 0, and since every builder
// execution path derives context.WithTimeout from it, every query context
// was born already expired (#262). Negative values pass through untouched.
//
// Asymmetry, on purpose: the boolean fields are NOT normalized (an explicit
// false is indistinguishable from the zero value). A partial literal
// therefore keeps AllowRawQueries=false AND SafeMigrations=false — the
// latter differs from the DefaultLimits value (true), which means Sync may
// drop columns. When you want the defaults for the booleans, start from
// [DefaultLimits] and modify only what you need.
//
// Because that asymmetry is easy to miss, [New] logs one structured WARN
// (event quark.limits.partial_literal_safe_migrations_off) when the literal
// shows the partial-literal signal — the numeric normalization actually
// filled at least one zero field — AND SafeMigrations ended up false. A
// full literal (every numeric field set, e.g. DefaultLimits() with
// SafeMigrations flipped off on purpose) carries no signal and stays
// silent. The warning changes no behaviour.
func WithLimits(l Limits) Option {
	return func(c *Client) {
		n, filled := l.normalized()
		c.limits = n
		// Partial-literal signal: the numeric normalization DID something,
		// so at least one field carried the "not set" zero — the same
		// literal shape that silently drops SafeMigrations to false. An
		// untouched literal (filled == false) is a deliberate full value
		// and must stay silent. Last WithLimits wins, like the limits
		// themselves; New() emits the WARN once the logger is final.
		c.warnPartialLimitsSafeMigrationsOff = filled && !n.SafeMigrations
	}
}

// normalized returns a copy of l with every zero-valued numeric field
// replaced by its [DefaultLimits] counterpart (#262), plus whether it
// filled at least one field — the partial-literal signal WithLimits uses
// for the SafeMigrations WARN. Negative values and the boolean fields pass
// through unchanged — see the godoc on [Limits] and [WithLimits] for the
// rationale.
func (l Limits) normalized() (Limits, bool) {
	d := DefaultLimits()
	filled := false
	if l.MaxQueryLength == 0 {
		l.MaxQueryLength = d.MaxQueryLength
		filled = true
	}
	if l.MaxResults == 0 {
		l.MaxResults = d.MaxResults
		filled = true
	}
	if l.MaxJoins == 0 {
		l.MaxJoins = d.MaxJoins
		filled = true
	}
	if l.MaxWhereConditions == 0 {
		l.MaxWhereConditions = d.MaxWhereConditions
		filled = true
	}
	if l.QueryTimeout == 0 {
		l.QueryTimeout = d.QueryTimeout
		filled = true
	}
	return l, filled
}

// WithCacheStore sets the caching backend for the client.
func WithCacheStore(s CacheStore) Option {
	return func(c *Client) {
		c.cacheStore = s
	}
}

// WithDeadlockRetry enables transparent retry of Client.Tx when the
// transaction is killed by a deadlock (F4-7).
//
// maxAttempts is the total number of attempts (1 = no retry, the
// historical behaviour; 0 or negative also disables). When the
// transaction closure returns an error that isDeadlock recognises
// from the active driver (PG 40P01, MySQL 1213, MSSQL 1205, Oracle
// ORA-00060), the runner sleeps with exponential backoff + jitter
// and re-executes the closure against a fresh transaction. Non-
// deadlock errors propagate immediately.
//
// The retry wraps the ENTIRE closure, not individual queries — a
// deadlock aborts the whole tx, so re-running just the failed query
// would race against a half-committed state. Disabled by default to
// keep the historical at-most-once-per-call semantics; opt in when
// the workload genuinely deadlocks under contention.
//
//	client, _ := quark.New("pgx", dsn, quark.WithDeadlockRetry(3))
func WithDeadlockRetry(maxAttempts int) Option {
	return func(c *Client) {
		c.deadlockRetries = maxAttempts
	}
}

// WithCacheJitter tunes the ±jitter factor applied to every TTL when a
// CacheStore is installed (F4-5, ADR-0011). Default 0.1 (±10%). Range
// [0, 1]; values outside are clamped. Setting to 0 disables jitter but
// keeps singleflight and XFetch on — the "todo o nada" of ADR-0011
// applies to the wrapper's installation, not to each individual
// protection. No effect when WithCacheStore is not used.
func WithCacheJitter(pct float64) Option {
	return func(c *Client) {
		c.stampedeJitterPct = pct
	}
}

// WithCacheXFetchBeta tunes the XFetch probabilistic-early-refresh
// parameter (F4-5, ADR-0011). Default 1.0; range >= 0. Higher β makes
// early refresh more aggressive; β = 0 disables XFetch entirely (still
// keeps singleflight and jitter active). No effect when WithCacheStore
// is not used.
//
// Both the on/off flag AND the stored beta are written on every call,
// so a sequence like WithCacheXFetchBeta(2.0) → WithCacheXFetchBeta(0)
// leaves no residual β behind: the final state is XFetch off with
// stampedeXFetchBeta = 0.
func WithCacheXFetchBeta(beta float64) Option {
	return func(c *Client) {
		c.stampedeXFetchOn = beta > 0
		c.stampedeXFetchBeta = beta
	}
}

// WithCacheCrossInstance enables cross-instance cache-stampede coordination
// (ADR-0020). Off by default. When enabled AND the installed CacheStore
// implements CacheLocker (the redis store does; the in-memory store provides a
// single-process equivalent), a cache miss on a hot key is serialised across
// processes: one instance acquires a per-key lock and recomputes while the
// others wait briefly and re-read its result, instead of every instance hitting
// the database. Costs one lock round-trip per hot-key miss — opt in when you run
// multiple instances against a shared cache and a hot key's recompute is
// expensive. No effect without WithCacheStore, or when the store does not
// implement CacheLocker (the wrapper then falls back to in-process singleflight
// + XFetch unchanged).
func WithCacheCrossInstance() Option {
	return func(c *Client) {
		c.stampedeCrossInstance = true
	}
}

// WithSlowQueryThreshold enables structured slow-query logging.
//
// Every query, exec, query-row, raw-query and raw-exec whose duration
// exceeds the threshold is emitted at WARN through Client.logger with
// structured attributes (duration_ms, threshold_ms, operation, table,
// rows, sql). Bind arguments are NOT included — the SQL is the
// parameterised form, mirroring the F4-2 span redaction principle:
// logs MUST NOT see user values they have no authority to retain.
//
// A threshold of 0 or negative (the default) disables the feature
// entirely — the check is a single cheap comparison on the hot
// observer path. Recommended starting point: 100ms.
//
//	client, _ := quark.New("pgx", dsn, quark.WithSlowQueryThreshold(100*time.Millisecond))
func WithSlowQueryThreshold(d time.Duration) Option {
	return func(c *Client) {
		c.slowQueryThreshold = d
	}
}

// WithDefaultTZ sets the fallback timezone for time.Time columns that do
// not carry their own quark:"tz=..." tag.
//
// The contract (see docs/adr/0010): time.Time values always go to the
// database as UTC — the column stores the same instant regardless of
// engine — and are converted to loc in memory when scanned back. loc
// therefore affects only how the struct field reads in Go, not what is
// persisted.
//
// A column-level quark:"tz=America/New_York" tag always overrides this
// default. When neither a default nor a tag is set, time.Time values
// pass through to the driver untouched (the historical v0.6 behaviour),
// so this feature is fully opt-in.
//
//	client, _ := quark.New("pgx", dsn, quark.WithDefaultTZ(time.UTC))
func WithDefaultTZ(loc *time.Location) Option {
	return func(c *Client) {
		c.defaultTZ = loc
	}
}

// WithReplicas registers read-replica DSNs (F6-5, ADR-0015). [New] opens one
// read-only connection pool per DSN (same pool options and dialect as the
// primary) and routes reads to them — both multi-row reads (List / Iter /
// eager-loading) and single-row reads (First / Find / Count / aggregates) —
// while writes always go to the primary. Pass the same engine's replica
// endpoints; a read-your-writes path uses [Sticky]. The selection strategy
// defaults to round-robin; change it with [WithReplicaStrategy].
//
// Opt-in: without it, every operation uses the single primary connection,
// unchanged. Reads inside [Client.Tx] and under RowLevelSecurityNative always
// use the primary regardless (see ADR-0015).
//
// Failover (F6-6): if a replica fails a read with a transient connection error,
// the read fails over to the primary and the replica is taken out of rotation
// for a cooldown (default 5s), then retried — a downed replica degrades
// performance, not correctness.
//
//	client, err := quark.New("pgx", primaryDSN,
//	    quark.WithReplicas(replica1DSN, replica2DSN),
//	    quark.WithMaxOpenConns(16),
//	)
func WithReplicas(dsns ...string) Option {
	return func(c *Client) {
		c.replicaDSNs = append(c.replicaDSNs, dsns...)
	}
}

// WithReplicaStrategy sets how a routed read picks among healthy replicas
// (F6-5, ADR-0015): [ReplicaRoundRobin] (default), [ReplicaRandom], or
// [ReplicaLeastConn]. Only meaningful alongside [WithReplicas] with more than
// one replica; with a single replica all strategies pick it. Every strategy
// honours the F6-6 health cooldown.
//
//	client, err := quark.New("pgx", primaryDSN,
//	    quark.WithReplicas(replica1DSN, replica2DSN),
//	    quark.WithReplicaStrategy(quark.ReplicaLeastConn),
//	)
func WithReplicaStrategy(s ReplicaStrategy) Option {
	return func(c *Client) {
		c.replicaStrategy = s
	}
}

// WithReplicaDownCooldown sets how long a read replica stays out of rotation
// after a transient connection failure before it is retried (F6-6, ADR-0015).
// The default is 5s. Tune it to your topology: shorter for same-AZ replicas,
// longer for cross-region ones where a flap is costlier to re-probe. Only
// meaningful alongside [WithReplicas]; a non-positive value keeps the default.
func WithReplicaDownCooldown(d time.Duration) Option {
	return func(c *Client) {
		if d > 0 {
			c.replicaDownCooldown = d
		}
	}
}

// PoolOption is a configuration option for the database connection pool.
// These are applied to the *sql.DB before creating the Client.
type PoolOption interface {
	isPoolOption()
	apply(*sql.DB)
}

type poolOption struct {
	fn func(*sql.DB)
}

func (o poolOption) isPoolOption() {}
func (o poolOption) apply(db *sql.DB) {
	o.fn(db)
}

// WithMaxOpenConns sets the maximum number of open connections to the database.
func WithMaxOpenConns(n int) PoolOption {
	return poolOption{
		fn: func(db *sql.DB) {
			db.SetMaxOpenConns(n)
		},
	}
}

// WithMaxIdleConns sets the maximum number of idle connections in the pool.
func WithMaxIdleConns(n int) PoolOption {
	return poolOption{
		fn: func(db *sql.DB) {
			db.SetMaxIdleConns(n)
		},
	}
}

// WithConnMaxLifetime sets the maximum amount of time a connection may be reused.
func WithConnMaxLifetime(d time.Duration) PoolOption {
	return poolOption{
		fn: func(db *sql.DB) {
			db.SetConnMaxLifetime(d)
		},
	}
}

// WithConnMaxIdleTime sets the maximum amount of time a connection may be idle.
func WithConnMaxIdleTime(d time.Duration) PoolOption {
	return poolOption{
		fn: func(db *sql.DB) {
			db.SetConnMaxIdleTime(d)
		},
	}
}

// QueryObserver is called after each query execution.
// Use this for logging, metrics, auditing, etc.
type QueryObserver interface {
	ObserveQuery(event QueryEvent)
}

// QueryEvent represents a executed query.
type QueryEvent struct {
	SQL       string
	Args      []any
	Duration  time.Duration
	Rows      int64
	Error     error
	Table     string
	Operation string // "SELECT", "INSERT", "UPDATE", "DELETE"
}

// ExecFunc is the signature for SQL execution functions used by middleware.
type ExecFunc func(ctx context.Context, exec Executor, sqlStr string, args []any) (sql.Result, error)

// QueryFunc is the signature for SQL query functions used by middleware.
type QueryFunc func(ctx context.Context, exec Executor, sqlStr string, args []any) (*sql.Rows, error)

// QueryRowFunc is the signature for SQL single-row query functions used by middleware.
type QueryRowFunc func(ctx context.Context, exec Executor, sqlStr string, args []any) *sql.Row

// Middleware wraps query execution for cross-cutting concerns like
// logging, retry logic, caching, rate limiting, etc.
// It intercepts all types of database interactions (Exec, Query, QueryRow).
type Middleware interface {
	WrapExec(next ExecFunc) ExecFunc
	WrapQuery(next QueryFunc) QueryFunc
	WrapQueryRow(next QueryRowFunc) QueryRowFunc
}

// BaseMiddleware provides default implementations that pass through to the next handler.
// Embed this in your middleware so you only need to override the methods you care about.
type BaseMiddleware struct{}

func (BaseMiddleware) WrapExec(next ExecFunc) ExecFunc             { return next }
func (BaseMiddleware) WrapQuery(next QueryFunc) QueryFunc          { return next }
func (BaseMiddleware) WrapQueryRow(next QueryRowFunc) QueryRowFunc { return next }
