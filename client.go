// Copyright 2026 jcsvwinston
// SPDX-License-Identifier: Apache-2.0

package quark

import (
	"context"
	"database/sql"
	"fmt"
	"log/slog"
	"reflect"
	"strings"
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

// New creates a new quark Client with the given database connection and options.
//
// Example:
//
//	db, err := sql.Open("postgres", "postgres://user:pass@localhost/db")
//	if err != nil {
//	    log.Fatal(err)
//	}
//
//	client, err := quark.New(db,
//	    quark.WithDialect(quark.PostgreSQL()),
//	    quark.WithLogger(slog.Default()),
//	)
func New(db *sql.DB, opts ...Option) (*Client, error) {
	if db == nil {
		return nil, fmt.Errorf("%w: db cannot be nil", ErrConnection)
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
	}

	// Auto-detect dialect if not specified
	if c.dialect == nil {
		driver := reflect.TypeOf(db.Driver()).String()
		// Try to extract driver name from type string like "*pq.Driver" or "*stdlib.Driver"
		dialect, err := detectDialectFromDriver(driver, db)
		if err != nil {
			c.logger.Warn("could not auto-detect dialect, defaulting to generic",
				"driver", driver,
				"error", err)
			// Default to PostgreSQL as most common
			c.dialect = PostgreSQL()
		} else {
			c.dialect = dialect
		}
	}

	// Apply options
	for _, opt := range opts {
		opt(c)
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

		if router.config.Strategy == SchemaPerTenant {
			q.schema = tenantID
		} else if router.config.Strategy == RowLevelSecurity {
			q.tenantID = tenantID
			q.tenantCol = router.config.TenantColumn
			// Pre-inject the RLS WHERE condition
			q.where = append(q.where, condition{
				column:   router.config.TenantColumn,
				operator: "=",
				value:    tenantID,
				logic:    "AND",
			})
		}
	}

	return q
}

// tableNameFromType derives table name from generic type T.
func tableNameFromType[T any]() string {
	var zero T
	name := toSnakeCase(pluralize(reflect.TypeOf(zero).Elem().Name()))
	return name
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

// detectDialectFromDriver attempts to detect the dialect from the driver type.
func detectDialectFromDriver(driverType string, db *sql.DB) (Dialect, error) {
	// Check registered dialects first
	if d, err := DetectDialectByName(driverType); err == nil {
		return d, nil
	}

	// Try to get the driver name from the db
	// This is heuristic-based
	switch {
	case containsAny(driverType, "pgx", "pq.", "postgres"):
		return PostgreSQL(), nil
	case containsAny(driverType, "mysql", "mariadb"):
		return MySQL(), nil
	case containsAny(driverType, "sqlite", "modernc"):
		return SQLite(), nil
	case containsAny(driverType, "sqlserver", "mssql", "azure"):
		return MSSQL(), nil
	case containsAny(driverType, "oracle", "godror", "oci8"):
		return Oracle(), nil
	default:
		return nil, fmt.Errorf("could not detect dialect from driver: %s", driverType)
	}
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
