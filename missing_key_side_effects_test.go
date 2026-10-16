// Copyright 2026 jcsvwinston
// SPDX-License-Identifier: Apache-2.0

package quark

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"sync/atomic"
	"testing"
)

// QK-46. A write by entity that carries no conditions, on a key no row has,
// writes nothing and returns (0, nil) — and still runs the After hook, writes
// the audit row and publishes the event, as if it had written. Under
// conditions the same write reports nothing (QK-40,
// TestAWriteTheConditionsExcludeReportsNothing). Quark 2.0 makes the
// unconditioned write report nothing too (DEP-2026-005); this test pins the
// v1 behaviour so the change at the major is a diff to it.
//
// The fixtures are those of where_guard_test.go.
func TestAWriteWithoutConditionsOnAMissingKeyStillReportsInV1(t *testing.T) {
	ctx := context.Background()
	writes := []struct {
		name  string
		hooks *atomic.Int64
		write func(q *Query[wgHooked], e *wgHooked) (int64, error)
	}{
		{"Update", &wgAfterUpdates, func(q *Query[wgHooked], e *wgHooked) (int64, error) { return q.Update(e) }},
		{"UpdateFields", &wgAfterUpdates, func(q *Query[wgHooked], e *wgHooked) (int64, error) { return q.UpdateFields(e, "name") }},
		{"Delete", &wgAfterDeletes, func(q *Query[wgHooked], e *wgHooked) (int64, error) { return q.Delete(e) }},
		{"HardDelete", &wgAfterDeletes, func(q *Query[wgHooked], e *wgHooked) (int64, error) { return q.HardDelete(e) }},
	}
	for i, w := range writes {
		t.Run(w.name, func(t *testing.T) {
			c, bus := wgHookedClient(t, fmt.Sprintf("qk46_hooks_%d", i))
			audits := func() int64 {
				n, err := For[quarkAuditRow](ctx, c).Count()
				if err != nil {
					t.Fatalf("count audit rows: %v", err)
				}
				return n
			}

			// Rows 1 and 2 exist; 99 does not. No Where on the query.
			hooksBefore := w.hooks.Load()
			n, err := w.write(For[wgHooked](ctx, c), &wgHooked{ID: 99, Owner: "nobody", Name: "x"})
			if err != nil || n != 0 {
				t.Fatalf("%s on a missing key = (%d, %v), want (0, nil)", w.name, n, err)
			}
			if got := w.hooks.Load() - hooksBefore; got != 1 {
				t.Errorf("%s on a missing key ran the After hook %d times, want 1 (v1 behaviour)", w.name, got)
			}
			if got := audits(); got != 1 {
				t.Errorf("%s on a missing key wrote %d audit rows, want 1 (v1 behaviour)", w.name, got)
			}
			if got := bus.count(); got != 1 {
				t.Errorf("%s on a missing key emitted %d events, want 1 (v1 behaviour)", w.name, got)
			}
			if n, err := For[wgHooked](ctx, c).Count(); err != nil || n != 2 {
				t.Errorf("the table changed: %d rows, %v", n, err)
			}
		})
	}

	// Out of the notice: on a versioned model Update and UpdateFields read a
	// missing key as a version conflict already, so no hook, audit row or
	// event runs there in v1 either.
	t.Run("VersionedUpdateIsAlreadyAnError", func(t *testing.T) {
		c, err := New("sqlite", "file:qk46_versioned?mode=memory&cache=shared",
			WithLogger(slog.New(slog.NewTextHandler(io.Discard, nil))))
		if err != nil {
			t.Fatalf("open: %v", err)
		}
		t.Cleanup(func() { _ = c.Close() })
		if err := c.Migrate(ctx, &wgVersioned{}); err != nil {
			t.Fatalf("migrate: %v", err)
		}
		e := wgVersioned{ID: 99, Owner: "nobody", Name: "x", Version: 1}
		if _, err := For[wgVersioned](ctx, c).Update(&e); !errors.Is(err, ErrStaleEntity) {
			t.Errorf("Update of a missing key on a versioned model = %v, want ErrStaleEntity", err)
		}
		if _, err := For[wgVersioned](ctx, c).UpdateFields(&e, "name"); !errors.Is(err, ErrStaleEntity) {
			t.Errorf("UpdateFields of a missing key on a versioned model = %v, want ErrStaleEntity", err)
		}
	})
}
