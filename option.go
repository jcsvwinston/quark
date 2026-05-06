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

// WithLogger sets the logger for the client.
// If not set, a no-op logger will be used.
func WithLogger(l *slog.Logger) Option {
	return func(c *Client) {
		c.logger = l
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
func WithLimits(l Limits) Option {
	return func(c *Client) {
		c.limits = l
	}
}

// WithCacheStore sets the caching backend for the client.
func WithCacheStore(s CacheStore) Option {
	return func(c *Client) {
		c.cacheStore = s
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
