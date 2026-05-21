// Copyright 2026 jcsvwinston
// SPDX-License-Identifier: Apache-2.0

// The EventBus emit machinery is implemented entirely in Quark's Go
// CRUD/transaction path (it hangs off Tx.OnCommit and the inline
// non-tx fallback). It does not generate dialect-specific SQL, so
// SQLite in-memory is sufficient coverage; the six-engine matrix
// (CLAUDE.md Rule 1) is not required for this suite.

package quark_test

import (
	"context"
	"errors"
	"sync"
	"testing"

	"github.com/jcsvwinston/quark"
)

// captureBus records every published event in order. failOn, when
// non-empty, makes Publish return an error for events of that kind —
// used to exercise the ErrEventEmitFailed path.
type captureBus struct {
	mu     sync.Mutex
	kinds  []string
	tables []string
	failOn string
}

func (b *captureBus) Publish(_ context.Context, e quark.Event) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.failOn != "" && e.Kind() == b.failOn {
		return errors.New("sink unavailable")
	}
	b.kinds = append(b.kinds, e.Kind())
	b.tables = append(b.tables, e.Table())
	return nil
}

func (b *captureBus) snapshotKinds() []string {
	b.mu.Lock()
	defer b.mu.Unlock()
	out := make([]string, len(b.kinds))
	copy(out, b.kinds)
	return out
}

type busRow struct {
	ID   int64  `db:"id" pk:"true"`
	Name string `db:"name"`
}

func newBusClient(t *testing.T) *quark.Client {
	t.Helper()
	c, err := quark.New("sqlite", ":memory:")
	if err != nil {
		t.Fatalf("new sqlite: %v", err)
	}
	t.Cleanup(func() { _ = c.Close() })
	if err := c.Migrate(context.Background(), &busRow{}); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	return c
}

func joinStr(ss []string) string {
	out := ""
	for i, s := range ss {
		if i > 0 {
			out += ","
		}
		out += s
	}
	return out
}

// TestF5_6_LoggerEventBus_PublishNeverErrors confirms the in-tree
// logger bus is a safe default sink.
func TestF5_6_LoggerEventBus_PublishNeverErrors(t *testing.T) {
	t.Parallel()
	bus := quark.NewLoggerEventBus(nil) // nil → slog.Default
	type ev struct{}
	if err := bus.Publish(context.Background(), fakeEvent{kind: "created", table: "t"}); err != nil {
		t.Errorf("LoggerEventBus.Publish returned error: %v", err)
	}
	_ = ev{}
}

// TestF5_6_OTelEventBus_PublishNeverErrors mirrors the logger bus
// check for the OTel correlation bus.
func TestF5_6_OTelEventBus_PublishNeverErrors(t *testing.T) {
	t.Parallel()
	bus := quark.NewOTelEventBus(nil)
	if err := bus.Publish(context.Background(), fakeEvent{kind: "updated", table: "t"}); err != nil {
		t.Errorf("OTelEventBus.Publish returned error: %v", err)
	}
}

// fakeEvent is a minimal Event for unit-testing the in-tree buses
// without going through the CRUD pipeline.
type fakeEvent struct {
	kind, table string
	payload     any
}

func (e fakeEvent) Kind() string  { return e.kind }
func (e fakeEvent) Table() string { return e.table }
func (e fakeEvent) Payload() any  { return e.payload }

// TestF5_6_Create_EmitsCreatedAfterCommit verifies the e2e path: a
// Create inside Client.Tx publishes a "created" event after commit,
// and nothing publishes when the tx rolls back.
func TestF5_6_Create_EmitsCreatedAfterCommit(t *testing.T) {
	t.Parallel()
	c := newBusClient(t)
	bus := &captureBus{}
	c.UseEventBus(bus)

	ctx := context.Background()

	// Commit path → event emitted.
	if err := c.Tx(ctx, func(tx *quark.Tx) error {
		return quark.ForTx[busRow](ctx, tx).Create(&busRow{Name: "a"})
	}); err != nil {
		t.Fatalf("Tx commit: %v", err)
	}
	if got := joinStr(bus.snapshotKinds()); got != "created" {
		t.Fatalf("after commit kinds = %q, want %q", got, "created")
	}

	// Rollback path → no new event.
	sentinel := errors.New("rollback")
	_ = c.Tx(ctx, func(tx *quark.Tx) error {
		if err := quark.ForTx[busRow](ctx, tx).Create(&busRow{Name: "b"}); err != nil {
			return err
		}
		return sentinel
	})
	if got := joinStr(bus.snapshotKinds()); got != "created" {
		t.Fatalf("after rollback kinds = %q, want unchanged %q", got, "created")
	}
}

// TestF5_6_Update_Delete_Emit verifies updated/deleted events emit
// with the correct kind and table, in CRUD order, across a tx.
func TestF5_6_Update_Delete_Emit(t *testing.T) {
	t.Parallel()
	c := newBusClient(t)
	bus := &captureBus{}
	c.UseEventBus(bus)
	ctx := context.Background()

	row := &busRow{Name: "x"}
	if err := quark.For[busRow](ctx, c).Create(row); err != nil {
		t.Fatalf("seed create: %v", err)
	}
	// Non-tx create emitted "created" inline.
	if got := joinStr(bus.snapshotKinds()); got != "created" {
		t.Fatalf("after create kinds = %q, want %q", got, "created")
	}

	row.Name = "y"
	if _, err := quark.For[busRow](ctx, c).Update(row); err != nil {
		t.Fatalf("update: %v", err)
	}
	if _, err := quark.For[busRow](ctx, c).Delete(row); err != nil {
		t.Fatalf("delete: %v", err)
	}
	if got := joinStr(bus.snapshotKinds()); got != "created,updated,deleted" {
		t.Errorf("kinds = %q, want %q", got, "created,updated,deleted")
	}
	if bus.tables[0] != "bus_rows" {
		t.Errorf("table = %q, want %q", bus.tables[0], "bus_rows")
	}
}

// TestF5_6_EmitFailure_NonTx_ReturnsErrEventEmitFailed verifies that
// a Publish failure on the non-transactional path surfaces to the
// caller wrapped in ErrEventEmitFailed, while the row stays
// persisted (the write already committed).
func TestF5_6_EmitFailure_NonTx_ReturnsErrEventEmitFailed(t *testing.T) {
	t.Parallel()
	c := newBusClient(t)
	bus := &captureBus{failOn: "created"}
	c.UseEventBus(bus)
	ctx := context.Background()

	row := &busRow{Name: "z"}
	err := quark.For[busRow](ctx, c).Create(row)
	if err == nil {
		t.Fatal("expected ErrEventEmitFailed, got nil")
	}
	if !errors.Is(err, quark.ErrEventEmitFailed) {
		t.Fatalf("expected ErrEventEmitFailed, got %v", err)
	}

	// The row must still be persisted — emit failure does not undo
	// the write.
	got, ferr := quark.For[busRow](ctx, c).Find(row.ID)
	if ferr != nil {
		t.Fatalf("row should be persisted despite emit failure: %v", ferr)
	}
	if got.Name != "z" {
		t.Errorf("persisted name = %q, want %q", got.Name, "z")
	}
}

// TestF5_6_EmitFailure_Tx_DoesNotPropagate verifies that a Publish
// failure on the transactional path does NOT fail Client.Tx (the
// commit already succeeded) — the failure is logged, not propagated.
func TestF5_6_EmitFailure_Tx_DoesNotPropagate(t *testing.T) {
	t.Parallel()
	c := newBusClient(t)
	bus := &captureBus{failOn: "created"}
	c.UseEventBus(bus)
	ctx := context.Background()

	err := c.Tx(ctx, func(tx *quark.Tx) error {
		return quark.ForTx[busRow](ctx, tx).Create(&busRow{Name: "q"})
	})
	if err != nil {
		t.Fatalf("Tx must return nil — emit failure is post-commit, not a tx error; got %v", err)
	}
}

// TestF5_6_Update_NoMatch_StillEmits documents the deliberate
// decision that Update emits "updated" even when the WHERE matched
// zero rows. Emission means "operation attempted and committed", not
// "a row was mutated" — gating on rows-affected would be
// engine-dependent (see events.mdx). A subscriber must not assume a
// row changed.
func TestF5_6_Update_NoMatch_StillEmits(t *testing.T) {
	t.Parallel()
	c := newBusClient(t)
	bus := &captureBus{}
	c.UseEventBus(bus)
	ctx := context.Background()

	// Update an entity whose PK does not exist → 0 rows affected,
	// no error. The event still fires.
	ghost := &busRow{ID: 9999, Name: "ghost"}
	n, err := quark.For[busRow](ctx, c).Update(ghost)
	if err != nil {
		t.Fatalf("update no-match: %v", err)
	}
	if n != 0 {
		t.Fatalf("expected 0 rows affected, got %d", n)
	}
	if got := joinStr(bus.snapshotKinds()); got != "updated" {
		t.Errorf("kinds = %q, want %q (emit fires regardless of rows affected)", got, "updated")
	}
}

// TestF5_6_NoBus_ZeroCost confirms CRUD works unchanged when no bus
// is configured (the common case).
func TestF5_6_NoBus_ZeroCost(t *testing.T) {
	t.Parallel()
	c := newBusClient(t)
	ctx := context.Background()
	if err := quark.For[busRow](ctx, c).Create(&busRow{Name: "n"}); err != nil {
		t.Fatalf("create without bus: %v", err)
	}
}
