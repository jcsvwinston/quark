// Copyright 2026 jcsvwinston
// SPDX-License-Identifier: Apache-2.0

package quark_test

import (
	"context"
	"errors"
	"sync"
	"testing"

	"github.com/jcsvwinston/quark"
)

// hookRecorder is the per-test sink that the spyOrder fixture
// writes to. The hook methods on *spyOrder reach this recorder via
// the package-level activeHookRecorder pointer (see setHookRecorder).
// Tests run SEQUENTIALLY because activeHookRecorder is global —
// concurrent tests would race on the pointer and steal each other's
// events. Sequential execution is acceptable here: each F5-4 test is
// sub-millisecond and there are only five of them.
type hookRecorder struct {
	mu     sync.Mutex
	events []string
}

func (r *hookRecorder) record(name string) {
	r.mu.Lock()
	r.events = append(r.events, name)
	r.mu.Unlock()
}

func (r *hookRecorder) snapshot() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]string, len(r.events))
	copy(out, r.events)
	return out
}

var (
	hookRecorderMu     sync.Mutex
	activeHookRecorder *hookRecorder
)

type spyOrder struct {
	ID   int64  `db:"id" pk:"true"`
	Name string `db:"name"`
}

func (s *spyOrder) BeforeCreate(_ context.Context) error { return spyRecord("BeforeCreate") }
func (s *spyOrder) AfterCreate(_ context.Context) error  { return spyRecord("AfterCreate") }
func (s *spyOrder) BeforeUpdate(_ context.Context) error { return spyRecord("BeforeUpdate") }
func (s *spyOrder) AfterUpdate(_ context.Context) error  { return spyRecord("AfterUpdate") }
func (s *spyOrder) BeforeDelete(_ context.Context) error { return spyRecord("BeforeDelete") }
func (s *spyOrder) AfterDelete(_ context.Context) error  { return spyRecord("AfterDelete") }
func (s *spyOrder) BeforeFind(_ context.Context) error   { return spyRecord("BeforeFind") }
func (s *spyOrder) AfterFind(_ context.Context) error    { return spyRecord("AfterFind") }

func spyRecord(name string) error {
	hookRecorderMu.Lock()
	r := activeHookRecorder
	hookRecorderMu.Unlock()
	if r != nil {
		r.record(name)
	}
	return nil
}

func setHookRecorder(r *hookRecorder) func() {
	hookRecorderMu.Lock()
	prev := activeHookRecorder
	activeHookRecorder = r
	hookRecorderMu.Unlock()
	return func() {
		hookRecorderMu.Lock()
		activeHookRecorder = prev
		hookRecorderMu.Unlock()
	}
}

func newSpyClient(t *testing.T) *quark.Client {
	t.Helper()
	c, err := quark.New("sqlite", ":memory:")
	if err != nil {
		t.Fatalf("new sqlite: %v", err)
	}
	t.Cleanup(func() { _ = c.Close() })
	if err := c.Migrate(context.Background(), &spyOrder{}); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	return c
}

// TestF5_4_AfterCreate_FiresAfterCommit verifies the core contract
// of F5-4: when a CRUD operation runs inside an explicit Client.Tx,
// the AfterCreate hook fires AFTER Tx.Commit returns, not inline
// after the INSERT. The event order from the recorder is the
// canonical assertion — BeforeCreate must appear before
// AfterCreate, and AfterCreate must NOT appear until Commit has
// completed.
func TestF5_4_AfterCreate_FiresAfterCommit(t *testing.T) {
	c := newSpyClient(t)

	rec := &hookRecorder{}
	defer setHookRecorder(rec)()

	ctx := context.Background()
	err := c.Tx(ctx, func(tx *quark.Tx) error {
		row := &spyOrder{Name: "a"}
		if err := quark.ForTx[spyOrder](ctx, tx).Create(row); err != nil {
			return err
		}
		// Capture the events visible IN-tx — AfterCreate must NOT
		// be present yet because the tx hasn't committed.
		mid := rec.snapshot()
		if got := joinEvents(mid); got != "BeforeCreate" {
			t.Errorf("in-tx events = %q, want %q (AfterCreate leaked before commit)", got, "BeforeCreate")
		}
		return nil
	})
	if err != nil {
		t.Fatalf("Tx: %v", err)
	}

	got := joinEvents(rec.snapshot())
	want := "BeforeCreate,AfterCreate"
	if got != want {
		t.Errorf("final events = %q, want %q", got, want)
	}
}

// TestF5_4_AfterCreate_SkippedOnRollback asserts the other side of
// the contract: if Tx.Rollback is invoked (or fn returns an
// error), the queued After hooks are discarded. The DB never
// committed; AfterCreate must not fire because there is nothing
// downstream to "react" to.
func TestF5_4_AfterCreate_SkippedOnRollback(t *testing.T) {
	c := newSpyClient(t)

	rec := &hookRecorder{}
	defer setHookRecorder(rec)()

	ctx := context.Background()
	sentinel := errors.New("force-rollback")
	err := c.Tx(ctx, func(tx *quark.Tx) error {
		row := &spyOrder{Name: "b"}
		if err := quark.ForTx[spyOrder](ctx, tx).Create(row); err != nil {
			return err
		}
		return sentinel
	})
	if !errors.Is(err, sentinel) {
		t.Fatalf("Tx returned %v, want sentinel", err)
	}

	got := joinEvents(rec.snapshot())
	want := "BeforeCreate"
	if got != want {
		t.Errorf("events on rollback = %q, want %q (AfterCreate must not fire)", got, want)
	}
}

// TestF5_4_AfterCreate_NonTxStillInline asserts the
// no-behaviour-change contract for callers that do NOT use an
// explicit transaction. Hooks invoked through For[T] (instead of
// ForTx[T]) keep the pre-F5-4 semantics: AfterCreate fires inline
// right after the SQL, before Create returns. This avoids paying
// the implicit-tx cost on every single-statement CRUD call.
func TestF5_4_AfterCreate_NonTxStillInline(t *testing.T) {
	c := newSpyClient(t)

	rec := &hookRecorder{}
	defer setHookRecorder(rec)()

	row := &spyOrder{Name: "c"}
	if err := quark.For[spyOrder](context.Background(), c).Create(row); err != nil {
		t.Fatalf("Create: %v", err)
	}

	got := joinEvents(rec.snapshot())
	want := "BeforeCreate,AfterCreate"
	if got != want {
		t.Errorf("non-tx events = %q, want %q", got, want)
	}
}

// TestF5_4_FindHooksFireAroundList confirms that the new
// BeforeFindHook and AfterFindHook interfaces are dispatched at
// the documented points: BeforeFind before any SQL is emitted,
// AfterFind once after the slice is hydrated.
func TestF5_4_FindHooksFireAroundList(t *testing.T) {
	c := newSpyClient(t)

	// Seed two rows so the result is non-empty.
	if err := quark.For[spyOrder](context.Background(), c).Create(&spyOrder{Name: "x"}); err != nil {
		t.Fatalf("seed x: %v", err)
	}
	if err := quark.For[spyOrder](context.Background(), c).Create(&spyOrder{Name: "y"}); err != nil {
		t.Fatalf("seed y: %v", err)
	}

	rec := &hookRecorder{}
	defer setHookRecorder(rec)()

	got, err := quark.For[spyOrder](context.Background(), c).List()
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("List len = %d, want 2", len(got))
	}

	events := joinEvents(rec.snapshot())
	want := "BeforeFind,AfterFind"
	if events != want {
		t.Errorf("List events = %q, want %q", events, want)
	}
}

// TestF5_4_AfterCreate_FIFOOrder confirms that multiple CRUD
// operations queued on the same tx fire their After hooks in the
// order the CRUD calls were made, not the order entities were
// scanned or any other surprise ordering.
func TestF5_4_AfterCreate_FIFOOrder(t *testing.T) {
	c := newSpyClient(t)

	rec := &hookRecorder{}
	defer setHookRecorder(rec)()

	ctx := context.Background()
	err := c.Tx(ctx, func(tx *quark.Tx) error {
		for _, name := range []string{"r1", "r2", "r3"} {
			if err := quark.ForTx[spyOrder](ctx, tx).Create(&spyOrder{Name: name}); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		t.Fatalf("Tx: %v", err)
	}

	events := joinEvents(rec.snapshot())
	want := "BeforeCreate,BeforeCreate,BeforeCreate,AfterCreate,AfterCreate,AfterCreate"
	if events != want {
		t.Errorf("FIFO events = %q, want %q", events, want)
	}
}

// TestF5_4_TrackedSave_AfterUpdate_FiresAfterCommit closes the gap
// the R1 reviewer flagged: `Tracked.Save` was firing AfterUpdate
// inline regardless of tx context. The F5-4 fix propagates the
// `*Tx` reference through TrackedQuery.wrap and queues AfterUpdate
// on the tx queue when present.
func TestF5_4_TrackedSave_AfterUpdate_FiresAfterCommit(t *testing.T) {
	c := newSpyClient(t)

	// Seed a row outside the assertion window so its Create hooks
	// don't pollute the recorder.
	row := &spyOrder{Name: "seed"}
	if err := quark.For[spyOrder](context.Background(), c).Create(row); err != nil {
		t.Fatalf("seed: %v", err)
	}

	rec := &hookRecorder{}
	defer setHookRecorder(rec)()

	ctx := context.Background()
	err := c.Tx(ctx, func(tx *quark.Tx) error {
		// Load via Track() bound to the tx, mutate, Save.
		tracked, err := quark.ForTx[spyOrder](ctx, tx).Track().Find(row.ID)
		if err != nil {
			return err
		}
		tracked.Entity.Name = "after-save"
		if _, err := tracked.Save(ctx); err != nil {
			return err
		}
		// Mid-tx snapshot: AfterUpdate must NOT have fired yet.
		mid := rec.snapshot()
		for _, e := range mid {
			if e == "AfterUpdate" {
				t.Errorf("AfterUpdate fired inline during tx — leaked before commit. events=%v", mid)
			}
		}
		return nil
	})
	if err != nil {
		t.Fatalf("Tx: %v", err)
	}

	events := joinEvents(rec.snapshot())
	// Track().Find() walks the loading query so BeforeFind /
	// AfterFind also fire; the order is: BeforeFind → AfterFind
	// (during load) → BeforeUpdate (Save inside tx) → AfterUpdate
	// (queued, fires post-commit).
	want := "BeforeFind,AfterFind,BeforeUpdate,AfterUpdate"
	if events != want {
		t.Errorf("Tracked.Save events = %q, want %q", events, want)
	}
}

func joinEvents(events []string) string {
	switch len(events) {
	case 0:
		return ""
	case 1:
		return events[0]
	}
	out := events[0]
	for _, e := range events[1:] {
		out += "," + e
	}
	return out
}
