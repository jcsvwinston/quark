// Copyright 2026 jcsvwinston
// SPDX-License-Identifier: Apache-2.0

package quark_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/jcsvwinston/quark"

	_ "github.com/jackc/pgx/v5/stdlib"
)

// TestCreateListener_NonPGReturnsErrDialectNotSupported verifies the
// inbound LISTEN/NOTIFY listener is PostgreSQL-only: every other dialect
// returns ErrDialectNotSupported (ADR-0019). Runs on SQLite, no
// integration needed.
func TestCreateListener_NonPGReturnsErrDialectNotSupported(t *testing.T) {
	t.Parallel()
	c, err := quark.New("sqlite", ":memory:")
	if err != nil {
		t.Fatalf("new sqlite: %v", err)
	}
	t.Cleanup(func() { _ = c.Close() })

	listener, err := quark.NewListenerFactory(c).CreateListener()
	if !errors.Is(err, quark.ErrDialectNotSupported) {
		t.Fatalf("CreateListener on sqlite: got err=%v, want ErrDialectNotSupported", err)
	}
	if listener != nil {
		t.Fatalf("CreateListener on sqlite returned a non-nil listener: %#v", listener)
	}
}

// TestPgListener_RoundTrip exercises the inbound listener against a real
// PostgreSQL engine: subscribe to a channel, emit via Notify on a
// separate pooled connection, and assert Receive returns the payload.
// Also covers an invalid channel name, Unlisten, and idempotent Close.
//
// Runs only when QUARK_TEST_POSTGRES_DSN is set (or under
// -tags=integration via testcontainers). LISTEN/NOTIFY is PG-only, so
// this cannot run on SQLite.
func TestPgListener_RoundTrip(t *testing.T) {
	dsn := resolvePostgresDSN(t)
	if dsn == "" {
		t.Skip("QUARK_TEST_POSTGRES_DSN not set (rebuild with -tags=integration to spin up a container)")
	}

	ctx := context.Background()
	client, err := quark.New("pgx", dsn)
	if err != nil {
		t.Fatalf("new pgx client: %v", err)
	}
	t.Cleanup(func() { _ = client.Close() })

	listener, err := quark.NewListenerFactory(client).CreateListener()
	if err != nil {
		t.Fatalf("CreateListener on postgres: %v", err)
	}
	t.Cleanup(func() { _ = listener.Close() })

	const channel = "quark_listen_roundtrip"

	// Receive before any Listen is a programming error → ErrNoSubscription.
	if _, err := listener.Receive(ctx); !errors.Is(err, quark.ErrNoSubscription) {
		t.Fatalf("Receive before Listen: got err=%v, want ErrNoSubscription", err)
	}

	// Invalid channel names are rejected before touching the connection.
	if err := listener.Listen(ctx, "bad name; DROP TABLE"); err == nil {
		t.Fatal("Listen accepted an invalid channel name")
	}

	if err := listener.Listen(ctx, channel); err != nil {
		t.Fatalf("Listen %q: %v", channel, err)
	}

	// Emit from a separate pooled connection. The LISTEN is already
	// registered on the pinned connection, so the notification is
	// delivered to it.
	if err := quark.Notify(ctx, client, channel, "hello-quark"); err != nil {
		t.Fatalf("Notify: %v", err)
	}

	recvCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	payload, err := listener.Receive(recvCtx)
	if err != nil {
		t.Fatalf("Receive: %v", err)
	}
	if payload.Channel != channel {
		t.Errorf("payload.Channel = %q, want %q", payload.Channel, channel)
	}
	if payload.Payload != "hello-quark" {
		t.Errorf("payload.Payload = %q, want %q", payload.Payload, "hello-quark")
	}

	// After Unlisten, a notification on the channel is no longer
	// delivered: a short Receive must time out rather than return it.
	if err := listener.Unlisten(ctx, channel); err != nil {
		t.Fatalf("Unlisten: %v", err)
	}
	if err := quark.Notify(ctx, client, channel, "after-unlisten"); err != nil {
		t.Fatalf("Notify after unlisten: %v", err)
	}
	shortCtx, shortCancel := context.WithTimeout(ctx, 500*time.Millisecond)
	defer shortCancel()
	if _, err := listener.Receive(shortCtx); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("Receive after Unlisten: got err=%v, want context.DeadlineExceeded (no delivery)", err)
	}

	// Close is idempotent and makes further ops report ErrListenerClosed.
	if err := listener.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if err := listener.Close(); err != nil {
		t.Fatalf("second Close should be a no-op: %v", err)
	}
	if err := listener.Listen(ctx, channel); !errors.Is(err, quark.ErrListenerClosed) {
		t.Fatalf("Listen after Close: got err=%v, want ErrListenerClosed", err)
	}
}
