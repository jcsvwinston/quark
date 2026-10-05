// Copyright 2026 jcsvwinston
// SPDX-License-Identifier: Apache-2.0

package suite

import (
	"context"
	"testing"

	"github.com/jcsvwinston/quark"
)

// testBatchOps is wired into Run to run all batch operations
// cross-engine (SQLite, Postgres, MySQL, MariaDB, MSSQL, Oracle).
func testBatchOps(ctx context.Context, t *testing.T, client *quark.Client) {
	type BSOp struct {
		ID    int64  `db:"id"    pk:"true"`
		Name  string `db:"name"`
		Email string `db:"email" quark:"unique"`
		Score int    `db:"score"`
	}

	dropTable(client, "bs_ops")
	if err := client.Migrate(ctx, &BSOp{}); err != nil {
		t.Fatalf("migrate bs_ops: %v", err)
	}

	seed := []*BSOp{
		{Name: "Alpha", Email: "a@bs.com", Score: 1},
		{Name: "Beta", Email: "b@bs.com", Score: 2},
		{Name: "Gamma", Email: "c@bs.com", Score: 3},
		{Name: "Delta", Email: "d@bs.com", Score: 4},
		{Name: "Epsilon", Email: "e@bs.com", Score: 5},
	}

	// Seed via CreateBatch (already tested separately).
	if err := quark.For[BSOp](ctx, client).CreateBatch(seed); err != nil {
		t.Fatalf("CreateBatch seed: %v", err)
	}

	// Finding C + G regression: CreateBatch must back-fill the generated PK into
	// EVERY entity on ALL SIX engines — not just the last one. RETURNING dialects
	// (PG/SQLite/MariaDB) scan them back; Oracle uses a per-row RETURNING INTO
	// (Finding C); MySQL and SQL Server — which can't read keys back from a
	// multi-row INSERT — now insert per row and back-fill via LastInsertId /
	// SCOPE_IDENTITY (Finding G). Before that, MySQL/MSSQL left every PK at 0, a
	// silent divergence from single Create that the old SupportsReturning()-gated
	// check skipped. The single auto-generated PK here exercises every dialect.
	pkSeen := make(map[int64]bool, len(seed))
	for i, e := range seed {
		switch {
		case e.ID == 0:
			t.Errorf("CreateBatch: seed[%d] (%s) PK not back-filled (ID==0)", i, e.Email)
		case pkSeen[e.ID]:
			t.Errorf("CreateBatch: seed[%d] PK %d duplicated — PKs not distinct", i, e.ID)
		}
		pkSeen[e.ID] = true
	}

	// Re-fetch to get real PKs.
	allRows, err := quark.For[BSOp](ctx, client).OrderBy("score", "ASC").List()
	if err != nil || len(allRows) != 5 {
		t.Fatalf("list after seed: err=%v len=%d", err, len(allRows))
	}
	// Convert []BSOp → []*BSOp for pointer-based batch methods.
	all := make([]*BSOp, len(allRows))
	for i := range allRows {
		v := allRows[i]
		all[i] = &v
	}

	t.Run("UpsertBatch_Insert", func(t *testing.T) {
		newRows := []*BSOp{
			{Name: "Zeta", Email: "z@bs.com", Score: 99},
		}
		if err := quark.For[BSOp](ctx, client).UpsertBatch(newRows, []string{"email"}, []string{"name", "score"}); err != nil {
			t.Fatalf("UpsertBatch insert: %v", err)
		}
		count, _ := quark.For[BSOp](ctx, client).Count()
		if count != 6 {
			t.Errorf("expected 6 rows, got %d", count)
		}
	})

	t.Run("UpsertBatch_Update", func(t *testing.T) {
		updated := []*BSOp{
			{Name: "Alpha-X", Email: "a@bs.com", Score: 100},
			{Name: "Beta-X", Email: "b@bs.com", Score: 200},
		}
		if err := quark.For[BSOp](ctx, client).UpsertBatch(updated, []string{"email"}, []string{"name", "score"}); err != nil {
			t.Fatalf("UpsertBatch update: %v", err)
		}
		alpha, _ := quark.For[BSOp](ctx, client).Where("email", "=", "a@bs.com").First()
		if alpha.Score != 100 {
			t.Errorf("expected Score=100, got %d", alpha.Score)
		}
	})

	t.Run("UpdateBatch", func(t *testing.T) {
		for _, u := range all[:3] {
			u.Score = u.Score + 500
		}
		if err := quark.For[BSOp](ctx, client).UpdateBatch(all[:3]); err != nil {
			t.Fatalf("UpdateBatch: %v", err)
		}
		for _, u := range all[:3] {
			got, err := quark.For[BSOp](ctx, client).Find(u.ID)
			if err != nil {
				t.Fatalf("find %d: %v", u.ID, err)
			}
			if got.Score != u.Score {
				t.Errorf("id=%d: expected Score=%d, got %d", u.ID, u.Score, got.Score)
			}
		}
	})

	t.Run("DeleteBatch", func(t *testing.T) {
		before, _ := quark.For[BSOp](ctx, client).Count()
		ids := []any{all[0].ID, all[1].ID}
		affected, err := quark.For[BSOp](ctx, client).DeleteBatch(ids)
		if err != nil {
			t.Fatalf("DeleteBatch: %v", err)
		}
		if affected != 2 {
			t.Errorf("expected 2 affected, got %d", affected)
		}
		count, _ := quark.For[BSOp](ctx, client).Count()
		if count != before-2 {
			t.Errorf("expected %d rows remaining, got %d", before-2, count)
		}
	})
}

// batchHookProbe verifies that batch and upsert ops fire Before* hooks per
// entity (Findings H + I). Stamp is written ONLY by the hooks, so it stays "" —
// both in memory and in the row — if a hook didn't run. Name is unique so it can
// serve as an Upsert conflict column.
type batchHookProbe struct {
	ID    int64  `db:"id" pk:"true"`
	Name  string `db:"name" quark:"unique"`
	Stamp string `db:"stamp"`
}

func (batchHookProbe) TableName() string { return "batch_hook_probes" }

func (b *batchHookProbe) BeforeCreate(ctx context.Context) error {
	b.Stamp = "created"
	return nil
}

func (b *batchHookProbe) BeforeUpdate(ctx context.Context) error {
	b.Stamp = "updated"
	return nil
}

// testBatchHooks is the Findings H + I regression: CreateBatch must fire
// BeforeCreate, UpdateBatch must fire BeforeUpdate, and Upsert/UpsertBatch must
// fire BeforeCreate (insert-prep) — once per entity, with the mutation reaching
// the row. Before the fixes, these ops skipped hooks entirely, so Stamp stayed
// empty in the database — and a hook setting a NOT NULL timestamp produced a
// zero datetime that MySQL strict mode rejected.
func testBatchHooks(ctx context.Context, t *testing.T, client *quark.Client) {
	dropTable(client, "batch_hook_probes")
	if err := client.Migrate(ctx, &batchHookProbe{}); err != nil {
		t.Fatalf("migrate batch_hook_probes: %v", err)
	}
	defer dropTable(client, "batch_hook_probes")

	rows := []*batchHookProbe{{Name: "a"}, {Name: "b"}, {Name: "c"}}
	if err := quark.For[batchHookProbe](ctx, client).CreateBatch(rows); err != nil {
		t.Fatalf("CreateBatch: %v", err)
	}
	for i, r := range rows {
		if r.Stamp != "created" {
			t.Errorf("CreateBatch: BeforeCreate did not run on rows[%d] (stamp=%q, want \"created\")", i, r.Stamp)
		}
	}
	persisted, err := quark.For[batchHookProbe](ctx, client).OrderBy("id", "ASC").List()
	if err != nil || len(persisted) != 3 {
		t.Fatalf("list after CreateBatch: err=%v len=%d", err, len(persisted))
	}
	for i, p := range persisted {
		if p.Stamp != "created" {
			t.Errorf("CreateBatch: row %d persisted stamp=%q, want \"created\" — the BeforeCreate mutation must reach the INSERT", i, p.Stamp)
		}
	}

	upd := make([]*batchHookProbe, len(persisted))
	for i := range persisted {
		v := persisted[i]
		upd[i] = &v
	}
	if err := quark.For[batchHookProbe](ctx, client).UpdateBatch(upd); err != nil {
		t.Fatalf("UpdateBatch: %v", err)
	}
	for i, u := range upd {
		if u.Stamp != "updated" {
			t.Errorf("UpdateBatch: BeforeUpdate did not run on upd[%d] (stamp=%q, want \"updated\")", i, u.Stamp)
		}
	}
	after, err := quark.For[batchHookProbe](ctx, client).OrderBy("id", "ASC").List()
	if err != nil {
		t.Fatalf("list after UpdateBatch: %v", err)
	}
	for i, a := range after {
		if a.Stamp != "updated" {
			t.Errorf("UpdateBatch: row %d persisted stamp=%q, want \"updated\"", i, a.Stamp)
		}
	}

	// Finding I: Upsert (single) and UpsertBatch fire BeforeCreate on the insert
	// path. conflictCols=["name"] is the unique column; the rows below are new,
	// so they insert and BeforeCreate must stamp them (in memory and in the row).
	ins := &batchHookProbe{Name: "upsert-1"}
	if err := quark.For[batchHookProbe](ctx, client).Upsert(ins, []string{"name"}, []string{"stamp"}); err != nil {
		t.Fatalf("Upsert: %v", err)
	}
	if ins.Stamp != "created" {
		t.Errorf("Upsert: BeforeCreate did not run (stamp=%q, want \"created\")", ins.Stamp)
	}
	gotUp, err := quark.For[batchHookProbe](ctx, client).Where("name", "=", "upsert-1").First()
	if err != nil {
		t.Fatalf("Upsert re-fetch: %v", err)
	}
	if gotUp.Stamp != "created" {
		t.Errorf("Upsert: persisted stamp=%q, want \"created\"", gotUp.Stamp)
	}

	ub := []*batchHookProbe{{Name: "upsert-2"}, {Name: "upsert-3"}}
	if err := quark.For[batchHookProbe](ctx, client).UpsertBatch(ub, []string{"name"}, []string{"stamp"}); err != nil {
		t.Fatalf("UpsertBatch: %v", err)
	}
	for i, b := range ub {
		if b.Stamp != "created" {
			t.Errorf("UpsertBatch: BeforeCreate did not run on ub[%d] (stamp=%q, want \"created\")", i, b.Stamp)
		}
	}
	for _, name := range []string{"upsert-2", "upsert-3"} {
		got, ferr := quark.For[batchHookProbe](ctx, client).Where("name", "=", name).First()
		if ferr != nil {
			t.Fatalf("UpsertBatch re-fetch %s: %v", name, ferr)
		}
		if got.Stamp != "created" {
			t.Errorf("UpsertBatch: %q persisted stamp=%q, want \"created\"", name, got.Stamp)
		}
	}
}
