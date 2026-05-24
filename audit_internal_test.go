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
// when the Client has no audit sink, recordAudit must return before
// computing rowToMap / pkStringFromMeta, so the write path allocates no
// diff map. Regression guard against re-introducing eager
// rowToMap(entity, q.meta) at the call sites.
func TestRecordAuditNoAllocWhenDisabled(t *testing.T) {
	q := &BaseQuery{
		client: &Client{}, // audit == nil
		table:  "alloc_widgets",
		meta:   GetModelMetaByType(reflect.TypeOf(allocWidget{})),
	}
	entity := &allocWidget{ID: 7, Name: "foo", Qty: 3}
	ctx := context.Background()

	allocs := testing.AllocsPerRun(1000, func() {
		if err := q.recordAudit(ctx, eventCreated, entity); err != nil {
			t.Fatalf("recordAudit: %v", err)
		}
	})
	if allocs != 0 {
		t.Errorf("recordAudit allocated %.0f times with audit disabled, want 0 "+
			"(row diff must be built only when a sink is configured)", allocs)
	}
}
