// Copyright 2026 jcsvwinston
// SPDX-License-Identifier: Apache-2.0

package quarkdriver

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"sync"

	"github.com/jcsvwinston/quark/internal/guard"
)

// EventPayload is a message received from a database event channel.
type EventPayload struct {
	Channel string
	Payload string
}

// Listener subscribes to database event channels. PostgreSQL implements it
// over LISTEN/NOTIFY on a dedicated pinned connection; no other engine has a
// portable equivalent, and Quark does not emulate one with polling.
//
// It lives here rather than in package quark because implementing it requires
// the driver — the PostgreSQL listener reaches through database/sql to the
// underlying pgx connection — and a contract that only the driver can
// implement belongs where the driver can reach it without compiling the ORM.
// Package quark aliases these types, so callers see no difference.
type Listener interface {
	// Listen subscribes to a specific channel.
	Listen(ctx context.Context, channel string) error

	// Unlisten unsubscribes from a channel.
	Unlisten(ctx context.Context, channel string) error

	// Receive blocks until an event arrives, returning the payload or an error.
	Receive(ctx context.Context) (EventPayload, error)

	// Close terminates the listener connection.
	Close() error
}

// IdentifierValidator is the one thing a listener needs from the ORM's SQL
// guard: channel names reach the server as identifiers, and validating them
// is the same job the ORM does everywhere else — a listener module must not
// grow its own escaping. It is an interface so a listener module implements
// ListenerFactory without importing an internal package: the previous
// contract named *internal/guard.SQLGuard, which compiled only by path
// prefix and which no third party could have implemented (QK-8). The ORM's
// *quark.SQLGuard satisfies it.
type IdentifierValidator interface {
	ValidateIdentifier(name string) error
}

// ListenerFactory builds a Listener over an open pool.
type ListenerFactory func(db *sql.DB, v IdentifierValidator) (Listener, error)

// NewListenerFunc is the previous shape of ListenerFactory, kept so a driver
// module built against it keeps registering; it names an internal type,
// which is why ListenerFactory replaces it.
//
// Deprecated: implement ListenerFactory and register it with
// RegisterListenerFactory.
type NewListenerFunc func(db *sql.DB, g *guard.SQLGuard) (Listener, error)

var (
	listenerMu sync.RWMutex
	listeners  = map[string]ListenerFactory{}
)

// RegisterListenerFactory records the listener constructor for engine.
func RegisterListenerFactory(engine string, f ListenerFactory) error {
	if engine == "" {
		return fmt.Errorf("quarkdriver: engine name is required")
	}
	if f == nil {
		return fmt.Errorf("quarkdriver: %s: listener constructor is required", engine)
	}
	listenerMu.Lock()
	defer listenerMu.Unlock()
	if _, dup := listeners[engine]; dup {
		return fmt.Errorf("quarkdriver: a listener for %s is already registered", engine)
	}
	listeners[engine] = f
	return nil
}

// MustRegisterListenerFactory is RegisterListenerFactory for use in an init().
func MustRegisterListenerFactory(engine string, f ListenerFactory) {
	if err := RegisterListenerFactory(engine, f); err != nil {
		panic(err)
	}
}

// LookupListenerFactory returns the constructor registered for engine.
func LookupListenerFactory(engine string) (ListenerFactory, bool) {
	listenerMu.RLock()
	defer listenerMu.RUnlock()
	f, ok := listeners[engine]
	return f, ok
}

// RegisterListener records a constructor of the previous shape. The ORM
// always hands the factory its own guard, so the adapter's type assertion
// holds; a caller passing another validator gets an error, not a panic.
//
// Deprecated: use RegisterListenerFactory.
func RegisterListener(engine string, f NewListenerFunc) error {
	if f == nil {
		return fmt.Errorf("quarkdriver: %s: listener constructor is required", engine)
	}
	return RegisterListenerFactory(engine, func(db *sql.DB, v IdentifierValidator) (Listener, error) {
		g, ok := v.(*guard.SQLGuard)
		if !ok {
			return nil, fmt.Errorf("quarkdriver: %s: this listener was registered with the previous contract and needs the ORM's guard, got %T", engine, v)
		}
		return f(db, g)
	})
}

// MustRegisterListener is RegisterListener for use in an init().
//
// Deprecated: use MustRegisterListenerFactory.
func MustRegisterListener(engine string, f NewListenerFunc) {
	if err := RegisterListener(engine, f); err != nil {
		panic(err)
	}
}

// LookupListener returns the constructor registered for engine in the
// previous shape.
//
// Deprecated: use LookupListenerFactory.
func LookupListener(engine string) (NewListenerFunc, bool) {
	f, ok := LookupListenerFactory(engine)
	if !ok {
		return nil, false
	}
	return func(db *sql.DB, g *guard.SQLGuard) (Listener, error) { return f(db, g) }, true
}

// The sentinels a Listener implementation returns. They live here, not in
// package quark, for the same reason the interface does: a listener module
// has to return them, and package quark aliases them so callers keep writing
// errors.Is(err, quark.ErrListenerClosed) exactly as before. Aliasing a
// variable preserves identity, so errors.Is matches across both names.
var (
	// ErrListenerClosed reports an operation attempted after Close. The
	// dedicated connection has gone back to the pool; create a fresh
	// listener. See ADR-0019.
	ErrListenerClosed = errors.New("event listener closed")

	// ErrNoSubscription reports Receive called with no channel subscribed —
	// Listen must be called at least once first. See ADR-0019.
	ErrNoSubscription = errors.New("event listener has no channel subscribed")
)
