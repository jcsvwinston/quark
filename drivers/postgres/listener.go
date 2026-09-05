// Copyright 2026 jcsvwinston
// SPDX-License-Identifier: Apache-2.0

package postgres

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"sync"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/stdlib"

	"github.com/jcsvwinston/quark/quarkdriver"
)

// errNotPGXConn is returned when the pinned connection is not a pgx one,
// which can only happen if a caller registered a different PostgreSQL driver
// under this dialect. LISTEN/NOTIFY needs the pgx connection specifically —
// it is not reachable through database/sql.
var errNotPGXConn = errors.New("quark/drivers/postgres: LISTEN/NOTIFY requires the pgx driver")

func init() {
	quarkdriver.MustRegisterListenerFactory("postgres", func(db *sql.DB, v quarkdriver.IdentifierValidator) (quarkdriver.Listener, error) {
		return &listener{db: db, guard: v}, nil
	})
}

// listener is the PostgreSQL implementation of quarkdriver.Listener. It
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
type listener struct {
	db    *sql.DB
	guard quarkdriver.IdentifierValidator // the ORM's SQL guard, through the public contract

	mu     sync.Mutex
	conn   *sql.Conn // dedicated, acquired lazily on first Listen
	closed bool
}

// raw runs fn with the underlying *pgx.Conn of the pinned connection.
// Caller must hold l.mu.
func (l *listener) raw(fn func(*pgx.Conn) error) error {
	return l.conn.Raw(func(driverConn any) error {
		c, ok := driverConn.(*stdlib.Conn)
		if !ok {
			return fmt.Errorf("%w: expected pgx stdlib conn, got %T", errNotPGXConn, driverConn)
		}
		return fn(c.Conn())
	})
}

// ensureConn pins a dedicated connection on first use. Caller must hold l.mu.
func (l *listener) ensureConn(ctx context.Context) error {
	if l.closed {
		return quarkdriver.ErrListenerClosed
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
func (l *listener) Listen(ctx context.Context, channel string) error {
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
func (l *listener) Unlisten(ctx context.Context, channel string) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	if err := l.guard.ValidateIdentifier(channel); err != nil {
		return fmt.Errorf("invalid channel name: %w", err)
	}
	if l.closed {
		return quarkdriver.ErrListenerClosed
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
func (l *listener) Receive(ctx context.Context) (quarkdriver.EventPayload, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.closed {
		return quarkdriver.EventPayload{}, quarkdriver.ErrListenerClosed
	}
	if l.conn == nil {
		return quarkdriver.EventPayload{}, quarkdriver.ErrNoSubscription
	}
	var out quarkdriver.EventPayload
	err := l.raw(func(c *pgx.Conn) error {
		n, err := c.WaitForNotification(ctx)
		if err != nil {
			// Returned raw, deliberately not through wrapDBError: a
			// WaitForNotification failure (ctx cancellation or a dropped
			// connection) is the caller's signal to reconnect with a fresh
			// listener, not a constraint/query error to be classified.
			return err
		}
		out = quarkdriver.EventPayload{Channel: n.Channel, Payload: n.Payload}
		return nil
	})
	if err != nil {
		return quarkdriver.EventPayload{}, err
	}
	return out, nil
}

// Close drops every subscription (best-effort UNLISTEN *, so the
// connection does not return to the pool carrying listener state —
// pgx/stdlib ResetSession does not clear it) and returns the dedicated
// connection to the pool. Idempotent.
func (l *listener) Close() error {
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
