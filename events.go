// Copyright 2026 jcsvwinston
// SPDX-License-Identifier: Apache-2.0

package quark

import (
	"context"
	"fmt"
	"github.com/jcsvwinston/quark/quarkdriver"
	"log/slog"
)

// Event is a single CRUD lifecycle event published to an [EventBus]
// after the originating write is durable. Kind is one of "created",
// "updated", or "deleted"; Table is the affected table name; Payload
// is the model value involved in the operation (the inserted /
// updated / deleted struct pointer).
//
// Implementations are free to type-switch on Payload to recover the
// concrete model. Quark emits a value that satisfies this interface;
// see [Client.UseEventBus].
type Event interface {
	Kind() string
	Table() string
	Payload() any
}

// EventBus receives CRUD lifecycle events. Implement it to route
// events to a logger, OpenTelemetry, or an external broker
// (NATS / Kafka / Redis Streams). Wire an implementation with
// [Client.UseEventBus].
//
// Delivery semantics are **synchronous, at-least-once, no outbox**:
// the event is published after the write commits (post-commit via
// [Tx.OnCommit] under an explicit transaction, or inline after the
// statement for non-transactional CRUD). If Publish returns an error
// the data is already persisted — the failure does not roll anything
// back. See [ErrEventEmitFailed] and ADR-0013 for the rationale and
// the explicit decision NOT to ship a transactional outbox in v0.9.
type EventBus interface {
	Publish(ctx context.Context, event Event) error
}

// modelEvent is Quark's concrete [Event]. Unexported — callers
// consume the interface, not the struct.
type modelEvent struct {
	kind    string
	table   string
	payload any
}

func (e modelEvent) Kind() string  { return e.kind }
func (e modelEvent) Table() string { return e.table }
func (e modelEvent) Payload() any  { return e.payload }

// Event kind constants emitted by the CRUD pipeline.
const (
	eventCreated = "created"
	eventUpdated = "updated"
	eventDeleted = "deleted"
)

// LoggerEventBus is an in-tree [EventBus] that writes each event to a
// [slog.Logger] at Info level. Useful for development, smoke tests,
// and as a reference implementation for custom buses.
type LoggerEventBus struct {
	logger *slog.Logger
}

// NewLoggerEventBus returns a LoggerEventBus writing to logger. A nil
// logger falls back to [slog.Default].
func NewLoggerEventBus(logger *slog.Logger) *LoggerEventBus {
	if logger == nil {
		logger = slog.Default()
	}
	return &LoggerEventBus{logger: logger}
}

// Publish logs the event. Never returns an error — logging is
// best-effort and a logger sink cannot meaningfully fail.
func (b *LoggerEventBus) Publish(ctx context.Context, event Event) error {
	b.logger.InfoContext(ctx, "quark event",
		"event", "quark.event",
		"kind", event.Kind(),
		"table", event.Table(),
	)
	return nil
}

// OTelEventBus is an in-tree [EventBus] that records each event as a
// structured log line tagged for OpenTelemetry log/trace correlation.
// It deliberately does NOT pull in the OTel SDK — Quark's otel package
// owns the tracer/meter wiring, and forcing a span here would couple
// the core package to the SDK. Instead it writes a correlation-ready
// slog record; deployments running the otel bridge pick it up as a
// log record on the active span. Swap in a real span-emitting bus by
// implementing [EventBus] yourself if you need first-class spans.
type OTelEventBus struct {
	logger *slog.Logger
}

// NewOTelEventBus returns an OTelEventBus. A nil logger falls back to
// [slog.Default].
func NewOTelEventBus(logger *slog.Logger) *OTelEventBus {
	if logger == nil {
		logger = slog.Default()
	}
	return &OTelEventBus{logger: logger}
}

// Publish records the event as a correlation-tagged log line. Never
// returns an error.
func (b *OTelEventBus) Publish(ctx context.Context, event Event) error {
	b.logger.InfoContext(ctx, "quark event",
		"event", "quark.event.emit",
		"otel.kind", "event",
		"kind", event.Kind(),
		"table", event.Table(),
	)
	return nil
}

// --- Legacy LISTEN/NOTIFY placeholder (renamed in v0.9.0) ---

// EventPayload represents a message received from a database event channel.
//
// It is an alias: the type is defined in quarkdriver, because a listener
// module has to construct it without compiling the ORM (ADR-0026 shape).
// Callers see no difference.
type EventPayload = quarkdriver.EventPayload

// EventListener defines an interface for listening to database events.
//
// It is an alias for quarkdriver.Listener, defined there so that the
// PostgreSQL implementation can live in the driver module that has the pgx
// connection it needs. Callers see no difference.
type EventListener = quarkdriver.Listener

// ListenerFactory is a dialect-agnostic factory for creating
// [EventListener]s over a database PubSub channel (PostgreSQL
// LISTEN/NOTIFY). It is unrelated to the [EventBus] CRUD-event
// interface above — this is the *inbound* channel-listener side.
//
// Renamed from the v0.8.0 `EventBus` struct in v0.9.0 to free the
// `EventBus` name for the CRUD-event interface. The PostgreSQL listener
// is implemented over a dedicated pool connection (ADR-0019); other
// dialects return [ErrDialectNotSupported].
type ListenerFactory struct {
	client *Client
}

// NewListenerFactory creates a ListenerFactory for the given client.
//
// Renamed from `NewEventBus` in v0.9.0; see [ListenerFactory].
func NewListenerFactory(client *Client) *ListenerFactory {
	return &ListenerFactory{client: client}
}

// CreateListener returns an EventListener for the client's dialect.
//
// PostgreSQL is supported: the listener pins a dedicated connection from
// the pool (acquired lazily on the first Listen) and consumes
// notifications via pgx's WaitForNotification (ADR-0019). It is
// single-goroutine — see [EventListener] and pgListener. Every other
// dialect returns [ErrDialectNotSupported]: LISTEN/NOTIFY has no
// portable equivalent and Quark does not emulate it with polling.
func (f *ListenerFactory) CreateListener() (EventListener, error) {
	if f.client.dialect.Name() != "postgres" {
		return nil, fmt.Errorf("%w: LISTEN/NOTIFY listener is PostgreSQL-only, dialect %q has no equivalent (ADR-0019)",
			ErrDialectNotSupported, f.client.dialect.Name())
	}
	newListener, ok := quarkdriver.LookupListenerFactory("postgres")
	if !ok {
		// The listener needs the pgx connection underneath database/sql, so
		// it ships with the driver module rather than with the ORM. Saying
		// which import is missing is the whole point: the alternative is a
		// nil listener and a panic three lines into the caller's loop.
		return nil, fmt.Errorf("%w: the PostgreSQL listener ships with the driver module and is not imported yet.\n\n"+
			"\tAdd it to your build:\n\n"+
			"\t\tgo get github.com/jcsvwinston/quark/drivers/postgres\n\n"+
			"\tand import it for its side effect:\n\n"+
			"\t\timport _ \"github.com/jcsvwinston/quark/drivers/postgres\"",
			ErrDialectNotSupported)
	}
	return newListener(f.client.db, f.client.guard)
}

// Notify triggers a database PubSub notification (PostgreSQL
// `pg_notify`). It is unrelated to [EventBus.Publish] — Notify is the
// raw LISTEN/NOTIFY emit, not a CRUD lifecycle event. Only PostgreSQL
// is supported; other dialects return an error.
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
		// pg_notify (the function form) supports bound parameters,
		// unlike the NOTIFY command.
		sqlStr = "SELECT pg_notify($1, $2)"
		_, err = client.db.ExecContext(ctx, sqlStr, channel, payload)
	case "mysql":
		return fmt.Errorf("notify not supported in MySQL")
	case "sqlite":
		return fmt.Errorf("notify not supported in SQLite")
	default:
		return fmt.Errorf("notify not supported by dialect")
	}

	return err
}
