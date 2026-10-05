// Copyright 2026 jcsvwinston
// SPDX-License-Identifier: Apache-2.0

package suite

import (
	"context"
	"testing"

	"github.com/jcsvwinston/quark"
)

// testCompositePK is called from Run to run composite PK scenarios
// against any supported engine.
func testCompositePK(ctx context.Context, t *testing.T, client *quark.Client) {
	type CPKItem struct {
		TenantID int64  `db:"tenant_id" pk:"true"`
		ItemID   int64  `db:"item_id"   pk:"true"`
		Label    string `db:"label"`
	}

	dropTable(client, "cpk_items")
	if err := client.Migrate(ctx, &CPKItem{}); err != nil {
		t.Fatalf("migrate cpk_items: %v", err)
	}

	// Create
	a := CPKItem{TenantID: 1, ItemID: 100, Label: "alpha"}
	if err := quark.For[CPKItem](ctx, client).Create(&a); err != nil {
		t.Fatalf("create a: %v", err)
	}
	b := CPKItem{TenantID: 1, ItemID: 200, Label: "beta"}
	if err := quark.For[CPKItem](ctx, client).Create(&b); err != nil {
		t.Fatalf("create b: %v", err)
	}

	// Update — change label via composite PK WHERE
	a.Label = "alpha-updated"
	rows, err := quark.For[CPKItem](ctx, client).Update(&a)
	if err != nil {
		t.Fatalf("update a: %v", err)
	}
	if rows != 1 {
		t.Errorf("update: expected 1 row affected, got %d", rows)
	}

	// List
	all, err := quark.For[CPKItem](ctx, client).Where("tenant_id", "=", 1).List()
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(all) != 2 {
		t.Errorf("expected 2 items, got %d", len(all))
	}

	// HardDelete
	if _, err := quark.For[CPKItem](ctx, client).HardDelete(&b); err != nil {
		t.Fatalf("hard delete b: %v", err)
	}
	remaining, _ := quark.For[CPKItem](ctx, client).Where("tenant_id", "=", 1).List()
	if len(remaining) != 1 {
		t.Errorf("expected 1 item after delete, got %d", len(remaining))
	}
}
