// Copyright 2026 jcsvwinston
// SPDX-License-Identifier: Apache-2.0

package quark

import (
	"context"
	"reflect"
	"testing"
)

// allocWidget is a small model with enough columns that building its
// full-row diff map would allocate. Used to prove recordAudit does NOT
// build that map when no audit sink is configured.
type allocWidget struct {
	ID   int64  `db:"id" pk:"true"`
	Name string `db:"name"`
	Qty  int    `db:"qty"`
}

// TestRecordAuditNoAllocWhenDisabled locks in the lazy-diff optimisation:
// recordAudit must return before computing rowToMap / pkStringFromMeta
// whenever the write will not be audited, so the write path allocates no
// diff map. Regression guard against re-introducing eager
// rowToMap(entity, q.meta) at the call sites — or against moving the diff
// computation ahead of the shouldAudit gate.
//
// Both gated paths must be allocation-free:
//   - no sink configured at all (client.audit == nil)
//   - a sink configured but this table filtered out (shouldAudit == false)
//
// Not parallel: AllocsPerRun is sensitive to GC pressure from concurrent
// goroutines, so these subtests run serially.
func TestRecordAuditNoAllocWhenDisabled(t *testing.T) {
	meta := GetModelMetaByType(reflect.TypeOf(allocWidget{}))
	entity := &allocWidget{ID: 7, Name: "foo", Qty: 3}
	ctx := context.Background()

	assertNoAlloc := func(t *testing.T, q *BaseQuery) {
		t.Helper()
		allocs := testing.AllocsPerRun(1000, func() {
			if err := q.recordAudit(ctx, eventCreated, entity); err != nil {
				t.Fatalf("recordAudit: %v", err)
			}
		})
		if allocs != 0 {
			t.Errorf("recordAudit allocated %.0f times, want 0 "+
				"(row diff must be built only when the write is audited)", allocs)
		}
	}

	t.Run("no sink configured", func(t *testing.T) {
		assertNoAlloc(t, &BaseQuery{
			client: &Client{}, // audit == nil
			table:  "alloc_widgets",
			meta:   meta,
		})
	})

	t.Run("sink configured but table excluded", func(t *testing.T) {
		// st != nil, but shouldAudit("alloc_widgets") == false, so the
		// gate still short-circuits ahead of rowToMap / pkStringFromMeta.
		st := &auditState{exclude: map[string]struct{}{"alloc_widgets": {}}}
		assertNoAlloc(t, &BaseQuery{
			client: &Client{audit: st},
			table:  "alloc_widgets",
			meta:   meta,
		})
	})
}
