// Copyright 2026 jcsvwinston
// SPDX-License-Identifier: Apache-2.0

package enginesuite

import (
	"context"
	"testing"

	"github.com/jcsvwinston/quark"
)

// QK-48, QK-59 (DEP-2026-006): Upsert and UpsertBatch with an empty
// updateCols do the same on the six engines — insert-or-ignore. A
// conflicting row is left as it was, the call returns nil, and the entity's
// key is written back only where a row was inserted.
//
// In v1 the engines did not agree: on SQL Server and Oracle the MERGE
// updated every non-conflict column but the key and created_at (QK-62),
// and this test pinned each engine to what it did. The MERGE has no WHEN
// MATCHED branch now without updateCols, so the two take the branch the
// other four took.
//
// What QK-48 changed within v1: on PostgreSQL and SQLite a conflict without
// updateCols returned sql.ErrNoRows — RETURNING after DO NOTHING has no row —
// while the other four engines returned nil for the same outcome.

type ueRow struct {
	ID   int64  `db:"id" pk:"true"`
	Code string `db:"code,size=20" quark:"unique"`
	Name string `db:"name,size=40"`
	N    int64  `db:"n"`
}

func (ueRow) TableName() string { return "upsert_empty_rows" }

func testUpsertEmptyUpdateCols(ctx context.Context, t *testing.T, client *quark.Client) {
	engine := client.Dialect().Name()
	dropTable(client, "upsert_empty_rows")
	if err := client.Migrate(ctx, &ueRow{}); err != nil {
		t.Fatalf("migrate on %s: %v", engine, err)
	}
	t.Cleanup(func() { dropTable(client, "upsert_empty_rows") })

	q := func() *quark.Query[ueRow] { return quark.For[ueRow](ctx, client) }
	reseed := func(t *testing.T) ueRow {
		t.Helper()
		if _, err := client.Raw().Exec("DELETE FROM " + client.Dialect().Quote("upsert_empty_rows")); err != nil {
			t.Fatalf("clear on %s: %v", engine, err)
		}
		row := ueRow{Code: "a", Name: "first", N: 1}
		if err := q().Create(&row); err != nil {
			t.Fatalf("seed on %s: %v", engine, err)
		}
		return row
	}
	read := func(t *testing.T, code string) ueRow {
		t.Helper()
		got, err := q().Where("code", "=", code).List()
		if err != nil || len(got) != 1 {
			t.Fatalf("read %q on %s: %d rows, %v", code, engine, len(got), err)
		}
		return got[0]
	}

	t.Run("Upsert", func(t *testing.T) {
		seed := reseed(t)
		again := ueRow{Code: "a", Name: "second", N: 2}
		if err := q().Upsert(&again, []string{"code"}, nil); err != nil {
			t.Fatalf("Upsert on a conflicting key with no updateCols on %s: %v, want nil", engine, err)
		}
		if got := read(t, "a"); got != seed {
			t.Errorf("Upsert with no updateCols on %s changed the conflicting row: %+v, want it left as %+v", engine, got, seed)
		}
		if again.ID != 0 {
			t.Errorf("Upsert with no updateCols on %s wrote key %d into the entity; nothing was written, so nothing is written back", engine, again.ID)
		}
		if n, err := q().Count(); err != nil || n != 1 {
			t.Errorf("Upsert on a conflicting key on %s: the table has %d rows (%v), want 1", engine, n, err)
		}
	})

	t.Run("UpsertBatch", func(t *testing.T) {
		seed := reseed(t)
		batch := []*ueRow{{Code: "a", Name: "third", N: 3}, {Code: "b", Name: "new", N: 9}}
		if err := q().UpsertBatch(batch, []string{"code"}, []string{}); err != nil {
			t.Fatalf("UpsertBatch with no updateCols on %s: %v", engine, err)
		}
		if got := read(t, "a"); got != seed {
			t.Errorf("UpsertBatch with no updateCols on %s changed the conflicting row: %+v, want it left as %+v", engine, got, seed)
		}
		if got := read(t, "b"); got.Name != "new" || got.N != 9 {
			t.Errorf("UpsertBatch with no updateCols on %s inserted %+v, want name=new n=9", engine, got)
		}
		if batch[0].ID != 0 {
			t.Errorf("UpsertBatch with no updateCols on %s wrote key %d into the entity it left alone", engine, batch[0].ID)
		}
	})

	t.Run("NoConflictInserts", func(t *testing.T) {
		reseed(t)
		fresh := ueRow{Code: "c", Name: "fresh", N: 5}
		if err := q().Upsert(&fresh, []string{"code"}, nil); err != nil {
			t.Fatalf("Upsert of a new key with no updateCols on %s: %v", engine, err)
		}
		got := read(t, "c")
		if got.Name != "fresh" || got.N != 5 {
			t.Errorf("Upsert of a new key with no updateCols on %s inserted %+v", engine, got)
		}
		if fresh.ID != got.ID {
			t.Errorf("Upsert of a new key with no updateCols on %s wrote back key %d, the row's is %d", engine, fresh.ID, got.ID)
		}
	})
}
