// Copyright 2026 jcsvwinston
// SPDX-License-Identifier: Apache-2.0

package enginesuite

import (
	"context"
	"testing"

	"github.com/jcsvwinston/quark"
)

// QK-48: Upsert and UpsertBatch with an empty updateCols, pinned per engine.
//
// The engines do not agree, and the reference says so. On PostgreSQL, SQLite,
// MySQL and MariaDB a conflicting row is left as it was — insert-or-ignore.
// On SQL Server and Oracle the MERGE updates every non-conflict column. The
// godoc used to promise the second everywhere. Making the engines agree
// changes what callers observe on one side or the other, which is a decision
// for the major (QADR-0010); until then this test holds each engine to what it
// does, so a change on either side turns that engine's lane red.
//
// What did change: on PostgreSQL and SQLite a conflict without updateCols
// returned sql.ErrNoRows — RETURNING after DO NOTHING has no row — while the
// other four engines returned nil for the same outcome. It returns nil now.

type ueRow struct {
	ID   int64  `db:"id" pk:"true"`
	Code string `db:"code,size=20" quark:"unique"`
	Name string `db:"name,size=40"`
	N    int64  `db:"n"`
}

func (ueRow) TableName() string { return "upsert_empty_rows" }

func testUpsertEmptyUpdateCols(ctx context.Context, t *testing.T, client *quark.Client) {
	engine := client.Dialect().Name()
	var overwrites bool
	switch engine {
	case "postgres", "sqlite", "mysql", "mariadb":
		overwrites = false
	case "mssql", "oracle":
		overwrites = true
	default:
		t.Skipf("no behaviour pinned for an empty updateCols on dialect %q", engine)
	}
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
	// wantAfter is the conflicting row after an upsert that carried name and
	// n, with no updateCols.
	wantAfter := func(seed ueRow, name string, n int64) ueRow {
		if overwrites {
			return ueRow{ID: seed.ID, Code: seed.Code, Name: name, N: n}
		}
		return seed
	}

	t.Run("Upsert", func(t *testing.T) {
		seed := reseed(t)
		if err := q().Upsert(&ueRow{Code: "a", Name: "second", N: 2}, []string{"code"}, nil); err != nil {
			t.Fatalf("Upsert on a conflicting key with no updateCols on %s: %v, want nil", engine, err)
		}
		if got, want := read(t, "a"), wantAfter(seed, "second", 2); got != want {
			t.Errorf("Upsert with no updateCols on %s left the conflicting row %+v, want %+v (overwrites every non-conflict column: %v)", engine, got, want, overwrites)
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
		if got, want := read(t, "a"), wantAfter(seed, "third", 3); got != want {
			t.Errorf("UpsertBatch with no updateCols on %s left the conflicting row %+v, want %+v (overwrites every non-conflict column: %v)", engine, got, want, overwrites)
		}
		if got := read(t, "b"); got.Name != "new" || got.N != 9 {
			t.Errorf("UpsertBatch with no updateCols on %s inserted %+v, want name=new n=9", engine, got)
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
		// Oracle's MERGE has no RETURNING: the key stays zero there, as with
		// updateCols (documented).
		if engine != "oracle" && fresh.ID != got.ID {
			t.Errorf("Upsert of a new key with no updateCols on %s wrote back key %d, the row's is %d", engine, fresh.ID, got.ID)
		}
	})
}
