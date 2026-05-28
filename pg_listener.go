// Copyright 2026 jcsvwinston
// SPDX-License-Identifier: Apache-2.0

package quark

import (
	"context"
	"database/sql"
	"fmt"
	"sync"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/stdlib"
)

// pgListener is the PostgreSQL implementation of [EventListener]. It
// holds a single *sql.Conn borrowed from the Client pool for its whole
// lifetime: LISTEN registers the subscription on the physical
// connection, so the connection must be pinned (the pool rotates
// connections freely). See ADR-0019.
//
// Concurrency: single-goroutine. All methods are serialized by mu;
// Receive blocks while holding mu, so Listen/Unlisten/Close cannot run
// from another goroutine while a Receive is in flight. The supported
// pattern is: register channels with Listen, then loop Receive in one
// goroutine; to stop, cancel the Receive context and then Close.
type pgListener struct {
	db    *sql.DB
	guard *SQLGuard

	mu     sync.Mutex
	conn   *sql.Conn // dedicated, acquired lazily on first Listen
	closed bool
}

// raw runs fn with the underlying *pgx.Conn of the pinned connection.
// Caller must hold l.mu.
func (l *pgListener) raw(fn func(*pgx.Conn) error) error {
	return l.conn.Raw(func(driverConn any) error {
		c, ok := driverConn.(*stdlib.Conn)
		if !ok {
			return fmt.Errorf("%w: expected pgx stdlib conn, got %T", ErrDialectNotSupported, driverConn)
		}
		return fn(c.Conn())
	})
}

// ensureConn pins a dedicated connection on first use. Caller must hold l.mu.
func (l *pgListener) ensureConn(ctx context.Context) error {
	if l.closed {
		return ErrListenerClosed
	}
	if l.conn != nil {
		return nil
	}
	conn, err := l.db.Conn(ctx)
	if err != nil {
		return fmt.Errorf("acquire dedicated listen connection: %w", err)
	}
	l.conn = conn
	return nil
}

// Listen subscribes the pinned connection to channel. The channel name
// is validated (it cannot be a bound parameter — LISTEN is a command,
// not a function) and quoted before being concatenated into the SQL.
func (l *pgListener) Listen(ctx context.Context, channel string) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	if err := l.guard.ValidateIdentifier(channel); err != nil {
		return fmt.Errorf("invalid channel name: %w", err)
	}
	if err := l.ensureConn(ctx); err != nil {
		return err
	}
	return l.raw(func(c *pgx.Conn) error {
		_, err := c.Exec(ctx, "LISTEN "+pgx.Identifier{channel}.Sanitize())
		return err
	})
}

// Unlisten removes the subscription to channel.
func (l *pgListener) Unlisten(ctx context.Context, channel string) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	if err := l.guard.ValidateIdentifier(channel); err != nil {
		return fmt.Errorf("invalid channel name: %w", err)
	}
	if l.closed {
		return ErrListenerClosed
	}
	if l.conn == nil {
		return nil // never subscribed to anything
	}
	return l.raw(func(c *pgx.Conn) error {
		_, err := c.Exec(ctx, "UNLISTEN "+pgx.Identifier{channel}.Sanitize())
		return err
	})
}

// Receive blocks until a notification arrives on any subscribed channel
// or ctx is cancelled. Cancelling ctx is the way to interrupt a blocked
// Receive (the dedicated connection cannot accept a concurrent call).
func (l *pgListener) Receive(ctx context.Context) (EventPayload, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.closed {
		return EventPayload{}, ErrListenerClosed
	}
	if l.conn == nil {
		return EventPayload{}, ErrNoSubscription
	}
	var out EventPayload
	err := l.raw(func(c *pgx.Conn) error {
		n, err := c.WaitForNotification(ctx)
		if err != nil {
			// Returned raw, deliberately not through wrapDBError: a
			// WaitForNotification failure (ctx cancellation or a dropped
			// connection) is the caller's signal to reconnect with a fresh
			// listener, not a constraint/query error to be classified.
			return err
		}
		out = EventPayload{Channel: n.Channel, Payload: n.Payload}
		return nil
	})
	if err != nil {
		return EventPayload{}, err
	}
	return out, nil
}

// Close drops every subscription (best-effort UNLISTEN *, so the
// connection does not return to the pool carrying listener state —
// pgx/stdlib ResetSession does not clear it) and returns the dedicated
// connection to the pool. Idempotent.
func (l *pgListener) Close() error {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.closed {
		return nil
	}
	l.closed = true
	if l.conn == nil {
		return nil
	}
	conn := l.conn
	l.conn = nil
	// Best-effort: the connection may already be poisoned (e.g. a
	// Receive whose ctx was cancelled). Ignore the UNLISTEN error;
	// database/sql discards a bad connection on Close anyway.
	_ = conn.Raw(func(driverConn any) error {
		if c, ok := driverConn.(*stdlib.Conn); ok {
			_, _ = c.Conn().Exec(context.Background(), "UNLISTEN *")
		}
		return nil
	})
	return conn.Close()
}
