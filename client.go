// Copyright 2026 jcsvwinston
// SPDX-License-Identifier: Apache-2.0

package quark

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// Client is the main entry point for quark ORM operations.
// It wraps a database connection and provides type-safe query building.
type Client struct {
	db         *sql.DB
	dialect    Dialect
	logger     *slog.Logger
	guard      *SQLGuard
	observers  []QueryObserver
	middleware []Middleware
	limits     Limits
	cacheStore CacheStore
	driverName string
	dataSource string

	// strictReads is the opt-in enforcement level for unbounded reads and
	// N+1 detection (#247, set by WithStrictReads). The zero value
	// StrictReadsOff keeps the historical behaviour; the read-path gates
	// check it with a single integer comparison.
	strictReads StrictReadsMode

	// Read replicas (F6-5, ADR-0015). replicaDSNs is set by WithReplicas;
	// New() opens one read-only *sql.DB per DSN into replicas. Reads route
	// to a replica (selected by replicaStrategy) when one is configured and the
	// query is not bound to a tx / native-RLS executor / Sticky context;
	// writes always use the primary db. Empty replicas = single-DB behaviour,
	// zero cost. replicas is read-only after New(); never mutated concurrently
	// with queries. A read to a replica that fails transiently fails over to
	// the primary and the replica is taken out of rotation for a cooldown
	// (F6-6, see replicaUnhealthyUntil / markReplicaDown).
	replicaDSNs []string
	replicas    []*sql.DB
	// replicaStrategy selects which healthy replica serves a read (set by
	// WithReplicaStrategy; round-robin is the zero-value default). See
	// pickReplica / ReplicaStrategy.
	replicaStrategy ReplicaStrategy
	// replicaRR is the round-robin cursor (only used by ReplicaRoundRobin).
	replicaRR atomic.Uint64
	// replicaUnhealthyUntil[i] is a unix-nano deadline (F6-6): reads skip
	// replica i until now passes it. Set when a read to that replica fails
	// with a transient connection error (see markReplicaDown); 0 = healthy.
	// Parallel to replicas, fixed-size after New() — never appended, so the
	// non-copyable atomics are safe to index in place.
	replicaUnhealthyUntil []atomic.Int64
	// replicaDownCooldown is how long a replica stays out of rotation after a
	// transient failure. Defaulted in New().
	replicaDownCooldown time.Duration

	// registeredModels holds the list of models the user has
	// pre-registered with this Client via [Client.RegisterModel].
	// Per-Client (not global) so multi-tenant deployments with
	// different Clients can manage different model sets. Mutex-
	// protected because RegisterModel is documented as safe to
	// call concurrently with itself (rare but easy to surface for
	// users who init their schema lazily).
	//
	// NOT to be confused with the global [GetModelMetaByType]
	// cache in `internal/schema` — that one is keyed by
	// `reflect.Type` and is correct as global state (the cached
	// meta is deterministic per type). F3-7's per-Client registry
	// is about which models this Client manages, not about the
	// meta computation cache.
	registeredModelsMu sync.Mutex
	registeredModels   []any

	// slowQueryThreshold is the minimum duration that flags a query as
	// "slow" for the F4-3 structured log line. Zero (the default)
	// disables the feature entirely. Set via WithSlowQueryThreshold.
	slowQueryThreshold time.Duration

	// Cache stampede tuning (F4-5, ADR-0011). Defaults set in New() so
	// callers that don't tune get sane behaviour. The wrapper installs
	// once at construction time using these values.
	stampedeJitterPct  float64
	stampedeXFetchOn   bool
	stampedeXFetchBeta float64
	// stampedeCrossInstance opts into cross-instance lock coordination
	// (ADR-0020); off by default. Effective only when the CacheStore
	// implements CacheLocker.
	stampedeCrossInstance bool

	// Deadlock retry budget for Client.Tx (F4-7). 0 (the default) means
	// no retry — a deadlock from inside the closure propagates on the
	// first attempt. Values >= 2 enable retry: the closure runs up to
	// this many times against fresh transactions, with exponential
	// backoff + jitter between attempts. Set via WithDeadlockRetry.
	deadlockRetries int

	// defaultTZ is the fallback timezone for time.Time columns that do
	// not carry their own quark:"tz=..." tag. nil (the zero value) means
	// pass-through — the time.Time goes to the driver untouched, which is
	// the historical v0.6 behaviour. Set via WithDefaultTZ. A column tag
	// always overrides this. See docs/adr/0010 for the wire-format
	// contract (UTC on the wire, loc applied in memory on scan).
	defaultTZ *time.Location

	// eventBus, when non-nil, receives a CRUD lifecycle Event after
	// each Create/Update/Delete commits (F5-6). Set via UseEventBus.
	// nil (the default) disables emission entirely — zero cost for
	// callers that don't opt in. Read on the hot CRUD path, so it is
	// only ever assigned at setup time via UseEventBus, not mutated
	// concurrently with queries.
	eventBus EventBus

	// audit, when non-nil, drives the optional audit log (F5-7): each
	// Create/Update/Delete writes a quark_audit row inline on the
	// CRUD connection/transaction. nil (the default) is zero cost.
	// Set once at setup via EnableAuditLog.
	audit *auditState

	// deferredCommitFailures counts implicit-tx deferred commits (the
	// RowLevelSecurityNative QueryContext/QueryRowContext paths) that
	// failed after the request ctx ended. Incremented from the
	// context.AfterFunc in rls_native.go; read via
	// [Client.DeferredCommitFailures]. Each increment means a write that
	// appeared to succeed was NOT committed (QK6-3).
	deferredCommitFailures atomic.Uint64

	// blockedPanicCleanups counts detached panic-path cleanups (the QK7-1
	// goroutine that rolls back the implicit tx and returns its pooled
	// connection after a driver panic) that did not finish within the
	// watchdog deadline — each unit is a tx/conn pair still held because
	// database/sql never released the locks the panic left taken.
	// Incremented from the watchdog in rls_native.go; read via
	// [Client.BlockedPanicCleanups] (QK8-1).
	blockedPanicCleanups atomic.Uint64

	// nativeTenantResolver is set by [NewTenantRouter] when this Client
	// is the BaseClient of a RowLevelSecurityNative router. When
	// non-nil, RawQuery/Exec emit a quark.tenant.raw_under_native_rls
	// warning if it resolves a non-empty tenant from the call's context
	// — a cue that the raw call sidesteps the tenant-scoped query
	// builder. The PostgreSQL policy still enforces isolation
	// server-side, so this is a developer-experience signal, not a
	// security boundary (ADR-0012). nil (the default) makes the check a
	// no-op. Assigned once at router setup, before queries run.
	nativeTenantResolver func(context.Context) string
}

// warnRawUnderNativeRLS emits a developer-experience warning when a raw
// query/exec runs under a RowLevelSecurityNative router with a tenant
// resolvable from ctx. Raw SQL bypasses the tenant-scoped query builder;
// under Native RLS the PostgreSQL policy still filters rows server-side
// (this is not a security bypass), but the call forgoes the builder's
// conveniences, so the warning flags the pattern. No-op unless the
// Client was stamped by a Native TenantRouter.
func (c *Client) warnRawUnderNativeRLS(ctx context.Context, op string) {
	if c.nativeTenantResolver == nil || c.logger == nil {
		return
	}
	if tenant := c.nativeTenantResolver(ctx); tenant != "" {
		c.logger.Warn("raw SQL under RowLevelSecurityNative sidesteps the tenant-scoped query builder (the PostgreSQL policy still enforces isolation)",
			"event", "quark.tenant.raw_under_native_rls",
			"op", op,
			"tenant", tenant)
	}
}

// UseEventBus wires an [EventBus] to the Client's CRUD pipeline. After
// this call, every Create / Update / Delete publishes a lifecycle
// [Event] once the write is durable: post-commit via [Tx.OnCommit]
// when the operation runs inside an explicit transaction, or inline
// after the statement for non-transactional CRUD.
//
// Delivery is synchronous and at-least-once with no outbox (ADR-0013).
// A Publish failure never rolls back the committed write. In the
// non-transactional path the failure surfaces to the CRUD caller
// wrapped in [ErrEventEmitFailed]; in the transactional path it is
// logged with the event `quark.event.emit_failure` (the commit has
// already returned success, so there is nothing to propagate to).
//
// Passing nil disables emission. UseEventBus is intended to be called
// once at setup, before queries run.
func (c *Client) UseEventBus(bus EventBus) {
	c.eventBus = bus
}

// ClientProvider is an interface that provides a database client.
// Both *Client and *TenantRouter implement this.
type ClientProvider interface {
	GetClient(ctx context.Context) (*Client, error)
}

// GetClient implements ClientProvider for the basic Client.
func (c *Client) GetClient(ctx context.Context) (*Client, error) {
	return c, nil
}

// New creates a new quark Client with the given driver name and data source.
//
// Example:
//
//	client, err := quark.New("sqlite", "example.db",
//	    quark.WithMaxOpenConns(25),
//	    quark.WithMaxIdleConns(5),
//	)
//
// For PostgreSQL:
//
//	client, err := quark.New("pgx", "postgres://user:pass@localhost/db",
//	    quark.WithMaxOpenConns(25),
//	)
//
// The dialect is auto-detected from the driver name. You can override it with WithDialect().
func New(driverName, dataSource string, opts ...any) (*Client, error) {
	// Open database connection
	db, err := sql.Open(driverName, dataSource)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrConnection, err)
	}

	// Apply pool options before creating the client
	for _, opt := range opts {
		if poolOpt, ok := opt.(PoolOption); ok {
			poolOpt.apply(db)
		}
	}

	// Test connection
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := db.PingContext(ctx); err != nil {
		return nil, fmt.Errorf("%w: %v", ErrConnection, err)
	}

	c := &Client{
		db:         db,
		logger:     slog.Default(),
		guard:      NewSQLGuard(),
		observers:  make([]QueryObserver, 0),
		middleware: make([]Middleware, 0),
		limits:     DefaultLimits(),
		driverName: driverName,
		dataSource: dataSource,
		// Cache stampede defaults (F4-5, ADR-0011). The wrapper is
		// installed below if WithCacheStore is configured. Options can
		// override these before the wrapper is built.
		stampedeJitterPct:  0.1, // ±10%
		stampedeXFetchOn:   true,
		stampedeXFetchBeta: 1.0,
	}

	// Auto-detect dialect from driverName if not specified
	if c.dialect == nil {
		dialect, err := DetectDialect(driverName)
		if err != nil {
			c.logger.Warn("could not auto-detect dialect, defaulting to generic",
				"driver", driverName,
				"error", err)
			// Default to PostgreSQL as most common
			c.dialect = PostgreSQL()
		} else {
			c.dialect = dialect
		}
		// MariaDB ships no dedicated database/sql driver — it speaks the MySQL
		// wire protocol through go-sql-driver/mysql ("mysql"), so DetectDialect
		// cannot tell them apart by name. Probe the server version and upgrade
		// to the MariaDB dialect when actually connected to MariaDB, so the
		// dialect divergences (e.g. LOCK IN SHARE MODE vs the MySQL-8-only
		// FOR SHARE — BB-3) are emitted correctly. An explicit WithDialect
		// applied below still wins. (BB-3)
		if c.dialect.Name() == "mysql" && isMariaDBServer(ctx, db) {
			c.dialect = MariaDB()
			// Debug, not Info: WithOptions re-runs New (and thus this probe) on
			// every clone, so an Info line here would spam logs in apps that
			// derive clients per-request or in test suites.
			c.logger.Debug("detected a MariaDB server via SELECT VERSION(); using the MariaDB dialect instead of MySQL")
		}
	}

	// Apply client options (skip pool options)
	for _, opt := range opts {
		if clientOpt, ok := opt.(Option); ok {
			clientOpt(c)
		}
	}

	// Open read replicas (F6-5, ADR-0015) after options, since WithReplicas
	// records the DSNs as a client option. Each replica gets the same pool
	// options as the primary and is pinged; any failure closes everything
	// opened so far (primary + earlier replicas) and aborts construction.
	for _, rdsn := range c.replicaDSNs {
		rdb, err := sql.Open(driverName, rdsn)
		if err != nil {
			_ = c.Close()
			return nil, fmt.Errorf("%w: open replica: %v", ErrConnection, err)
		}
		for _, opt := range opts {
			if poolOpt, ok := opt.(PoolOption); ok {
				poolOpt.apply(rdb)
			}
		}
		if err := rdb.PingContext(ctx); err != nil {
			// rdb is not yet in c.replicas (the append is below, on purpose),
			// so close it explicitly; c.Close then closes the primary + any
			// earlier replicas. Keep the append AFTER the ping to avoid a
			// double-close if this ordering is ever refactored.
			_ = rdb.Close()
			_ = c.Close()
			return nil, fmt.Errorf("%w: ping replica: %v", ErrConnection, err)
		}
		c.replicas = append(c.replicas, rdb)
	}
	// Health state parallel to replicas (F6-6). Fixed-size, indexed in place.
	if len(c.replicas) > 0 {
		c.replicaUnhealthyUntil = make([]atomic.Int64, len(c.replicas))
		if c.replicaDownCooldown == 0 {
			c.replicaDownCooldown = 5 * time.Second
		}
	}

	// Install the stampede protection wrapper around any caller-supplied
	// CacheStore (F4-5, ADR-0011). This is "todo o nada" per the cache
	// playbook: singleflight + jitter + (optionally) XFetch are layered
	// on every Quark-managed cache, never opt-in. memory.Store and
	// redis.Store are NOT touched — they keep working unchanged inside
	// the wrapper. Done after the options loop so WithCacheJitter and
	// WithCacheXFetchBeta have already taken effect.
	if c.cacheStore != nil {
		c.cacheStore = newStampedeStore(c.cacheStore, c.stampedeJitterPct, c.stampedeXFetchOn, c.stampedeXFetchBeta, c.stampedeCrossInstance, c.logger)
	}

	c.logger.Info("quark client initialized",
		"dialect", c.dialect.Name(),
		"max_results", c.limits.MaxResults,
	)

	return c, nil
}

// isMariaDBServer reports whether the server reached through db identifies as
// MariaDB. MariaDB embeds the literal "MariaDB" in its version string (e.g.
// "11.4.2-MariaDB-ubu2404"), while MySQL does not. Any probe error is treated
// as "not MariaDB" so dialect detection never blocks New() — the worst case is
// the MySQL dialect on a MariaDB server, i.e. the pre-BB-3 behaviour.
func isMariaDBServer(ctx context.Context, db *sql.DB) bool {
	var version string
	if err := db.QueryRowContext(ctx, "SELECT VERSION()").Scan(&version); err != nil {
		return false
	}
	return strings.Contains(strings.ToLower(version), "mariadb")
}

// For creates a Query builder for the given model type.
// This is the primary entry point for type-safe database operations.
//
// Example:
//
//	type User struct {
//	    ID   int64  `db:"id"`
//	    Name string `db:"name"`
//	}
//
//	user, err := quark.For[User](ctx, client).Find(1)
//	users, err := quark.For[User](ctx, client).Where("active", "=", true).List()
func For[T any](ctx context.Context, provider ClientProvider) *Query[T] {
	meta := GetModelMeta[T]()

	client, err := provider.GetClient(ctx)
	if err != nil {
		// Return a Query with an internal error state that will be returned on execution
		return &Query[T]{
			BaseQuery: BaseQuery{
				ctx:   ctx,
				table: meta.Table,
				pk:    meta.PK,
				meta:  meta,
				err:   fmt.Errorf("failed to get database client from provider: %w", err),
			},
		}
	}

	q := &Query[T]{
		BaseQuery: BaseQuery{
			ctx:     ctx,
			client:  client,
			dialect: client.dialect,
			guard:   client.guard,
			table:   meta.Table,
			pk:      meta.PK,
			exec:    client.db,
			meta:    meta,
		},
	}

	// Apply multi-tenant configurations if the provider is a TenantRouter
	if router, ok := provider.(*TenantRouter); ok {
		tenantID, err := router.ResolveTenant(ctx)
		if err != nil {
			q.err = err
			return q
		}

		switch router.config.Strategy {
		case SchemaPerTenant:
			q.schema = tenantID
		case RowLevelSecurityClient:
			q.tenantID = tenantID
			q.tenantCol = router.config.TenantColumn
			// Pre-inject the RLS WHERE condition
			q.where = ownedAppend(q.where, condition{
				column:   router.config.TenantColumn,
				operator: "=",
				value:    tenantID,
				logic:    "AND",
			})
		case RowLevelSecurityNative:
			// PostgreSQL-only: the engine itself enforces isolation
			// via row-level security policies referencing the session
			// variable set by nativeRLSExecutor. We don't inject a
			// WHERE predicate — the policy does it server-side. See
			// ADR-0012.
			if client.dialect.Name() != "postgres" {
				q.err = fmt.Errorf("%w: RowLevelSecurityNative requires PostgreSQL, got dialect %q",
					ErrUnsupportedFeature, client.dialect.Name())
				return q
			}
			q.exec = newNativeRLSExecutor(client, tenantID, router.config.defaultNativeRLSVar())
		}
	}

	return q
}

// RawQuery executes a raw SQL query with the given arguments.
// By default, this requires placeholders to prevent SQL injection.
// Enable with WithLimits(Limits{AllowRawQueries: true}).
func (c *Client) RawQuery(ctx context.Context, query string, args ...any) (*sql.Rows, error) {
	if !c.limits.AllowRawQueries {
		return nil, fmt.Errorf("%w: raw queries are disabled by default, enable with WithLimits", ErrInvalidQuery)
	}

	if err := c.guard.ValidateRawQuery(query, true); err != nil {
		return nil, err
	}

	c.warnRawUnderNativeRLS(ctx, "RawQuery")

	start := time.Now()
	rows, err := c.db.QueryContext(ctx, query, args...)
	duration := time.Since(start)

	// Notify observers
	qEvent := QueryEvent{
		SQL:       query,
		Args:      args,
		Duration:  duration,
		Error:     err,
		Operation: "RAW_QUERY",
	}
	c.logSlowQueryIfNeeded(qEvent)
	for _, obs := range c.observers {
		obs.ObserveQuery(qEvent)
	}

	return rows, err
}

// Exec executes a raw SQL statement (INSERT, UPDATE, DELETE, DDL).
// This is primarily used for migrations and schema changes.
func (c *Client) Exec(ctx context.Context, query string, args ...any) error {
	if !c.limits.AllowRawQueries {
		return fmt.Errorf("%w: raw queries are disabled by default, enable with WithLimits", ErrInvalidQuery)
	}

	if err := c.guard.ValidateRawQuery(query, false); err != nil {
		return err
	}

	c.warnRawUnderNativeRLS(ctx, "Exec")

	start := time.Now()
	res, err := c.db.ExecContext(ctx, query, args...)
	duration := time.Since(start)

	// Notify observers
	rowsAffected := int64(0)
	if err == nil {
		rowsAffected, _ = res.RowsAffected()
	}
	qEvent := QueryEvent{
		SQL:       query,
		Args:      args,
		Duration:  duration,
		Error:     err,
		Operation: "RAW_EXEC",
		Rows:      rowsAffected,
	}
	c.logSlowQueryIfNeeded(qEvent)
	for _, obs := range c.observers {
		obs.ObserveQuery(qEvent)
	}

	return err
}

// Raw returns the underlying *sql.DB for advanced operations.
// Use with caution - this bypasses quark's safety features.
func (c *Client) Raw() *sql.DB {
	return c.db
}

// Close closes the underlying database connection and any read-replica
// pools (F6-5). The primary's error is returned; replica close errors are
// joined so none is silently dropped.
func (c *Client) Close() error {
	err := c.db.Close()
	for _, r := range c.replicas {
		if cerr := r.Close(); cerr != nil {
			err = errors.Join(err, cerr)
		}
	}
	return err
}

// Dialect returns the dialect being used.
func (c *Client) Dialect() Dialect {
	return c.dialect
}

// WithOptions creates a new client with the same database connection but different options.
// This is useful for tests that need to create clients with different configurations.
func (c *Client) WithOptions(opts ...any) (*Client, error) {
	return New(c.driverName, c.dataSource, opts...)
}

func containsAny(s string, substrs ...string) bool {
	lower := strings.ToLower(s)
	for _, sub := range substrs {
		if strings.Contains(lower, sub) {
			return true
		}
	}
	return false
}
