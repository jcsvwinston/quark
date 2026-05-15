// Copyright 2026 jcsvwinston
// SPDX-License-Identifier: Apache-2.0

package quark

import (
	"context"
	"database/sql"
	"fmt"
	"log/slog"
	"strings"
	"sync"
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
	}

	// Apply client options (skip pool options)
	for _, opt := range opts {
		if clientOpt, ok := opt.(Option); ok {
			clientOpt(c)
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
		c.cacheStore = newStampedeStore(c.cacheStore, c.stampedeJitterPct, c.stampedeXFetchOn, c.stampedeXFetchBeta, c.logger)
	}

	c.logger.Info("quark client initialized",
		"dialect", c.dialect.Name(),
		"max_results", c.limits.MaxResults,
	)

	return c, nil
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
			q.where = append(q.where, condition{
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

// Close closes the underlying database connection.
func (c *Client) Close() error {
	return c.db.Close()
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
