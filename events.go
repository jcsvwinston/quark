// Copyright 2026 jcsvwinston
// SPDX-License-Identifier: Apache-2.0

package quark

import (
	"context"
	"fmt"
)

// EventPayload represents a message received from a database event channel.
type EventPayload struct {
	Channel string
	Payload string
}

// EventListener defines an interface for listening to database events.
// This is typically implemented via PubSub mechanisms like PostgreSQL's LISTEN/NOTIFY.
type EventListener interface {
	// Listen subscribes to a specific channel.
	Listen(ctx context.Context, channel string) error

	// Unlisten unsubscribes from a channel.
	Unlisten(ctx context.Context, channel string) error

	// Receive blocks until an event is received, returning the payload or an error.
	Receive(ctx context.Context) (EventPayload, error)

	// Close terminates the listener connection.
	Close() error
}

// EventBus provides a dialect-agnostic factory for creating EventListeners.
// Since not all databases support PubSub natively (e.g., SQLite), this may return
// ErrNotSupported for certain dialects.
type EventBus struct {
	client *Client
}

// NewEventBus creates a new EventBus for the given client.
func NewEventBus(client *Client) *EventBus {
	return &EventBus{client: client}
}

// CreateListener creates an EventListener based on the dialect.
//
// NOTE: EventBus is experimental in V1. Native LISTEN/NOTIFY (PostgreSQL)
// requires a dedicated connection with a driver-specific implementation
// (e.g., github.com/lib/pq). This will be completed in a future release.
func (eb *EventBus) CreateListener() (EventListener, error) {
	return nil, fmt.Errorf("%w: EventBus.CreateListener is not yet implemented for dialect %q — this feature is experimental in V1",
		ErrDialectNotSupported, eb.client.dialect.Name())
}

// Notify is a helper to trigger a database event (e.g., NOTIFY in Postgres).
func Notify(ctx context.Context, provider ClientProvider, channel, payload string) error {
	client, err := provider.GetClient(ctx)
	if err != nil {
		return err
	}

	if err := client.guard.ValidateIdentifier(channel); err != nil {
		return fmt.Errorf("invalid channel name: %w", err)
	}

	var sqlStr string
	switch client.dialect.Name() {
	case "postgres":
		// In Postgres, payload must be a string literal, we use bound parameters if supported
		// However, NOTIFY command doesn't typically support prepared parameters in pq.
		// For safety, we use pg_notify function which DOES support parameters.
		sqlStr = "SELECT pg_notify($1, $2)"
		_, err = client.db.ExecContext(ctx, sqlStr, channel, payload)
	case "mysql":
		// MySQL doesn't have PubSub. This could fall back to a dummy or error.
		return fmt.Errorf("notify not supported in MySQL")
	case "sqlite":
		return fmt.Errorf("notify not supported in SQLite")
	default:
		return fmt.Errorf("notify not supported by dialect")
	}

	return err
}
