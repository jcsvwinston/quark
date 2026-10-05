// Copyright 2026 jcsvwinston
// SPDX-License-Identifier: Apache-2.0

package suite

import (
	"context"
	"fmt"
	"testing"

	"github.com/jcsvwinston/quark"
)

func testStress(ctx context.Context, t *testing.T, client *quark.Client) {
	dropTable(client, "stress_records")
	type StressRecord struct {
		ID    int64  `db:"id" pk:"true"`
		Data  string `db:"data"`
		Value int    `db:"value"`
	}

	if err := client.Migrate(ctx, &StressRecord{}); err != nil {
		t.Fatalf("stress migrate failed: %v", err)
	}

	const count = 1000 // A smaller number for logs, but still significant

	t.Logf("🚀 Starting stress test with %d records...", count)

	// 1. Bulk Create
	for i := 0; i < count; i++ {
		rec := &StressRecord{
			Data:  fmt.Sprintf("stress-data-%d", i),
			Value: i,
		}
		if err := quark.For[StressRecord](ctx, client).Create(rec); err != nil {
			t.Fatalf("failed at record %d: %v", i, err)
		}
	}

	// 2. Count
	total, err := quark.For[StressRecord](ctx, client).Count()
	if err != nil || total != int64(count) {
		t.Errorf("expected %d records, got %d (err: %v)", count, total, err)
	}

	// 3. Paginated List
	res, err := quark.For[StressRecord](ctx, client).OrderBy("value", "ASC").Paginate(100, 1)
	if err != nil {
		t.Fatalf("pagination failed: %v", err)
	}
	if len(res.Items) != 100 {
		t.Errorf("expected 100 items on page 1, got %d", len(res.Items))
	}

	// 4. Cleanup
	for i := 0; i < count; i++ {
		if _, err := quark.For[StressRecord](ctx, client).Where("value", "=", i).DeleteBy(); err != nil {
			t.Fatalf("failed to delete record %d: %v", i, err)
		}
	}
}
