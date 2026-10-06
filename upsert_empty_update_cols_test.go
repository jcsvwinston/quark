// Copyright 2026 jcsvwinston
// SPDX-License-Identifier: Apache-2.0

package quark

import (
	"context"
	"io"
	"log/slog"
	"testing"

	_ "modernc.org/sqlite"
)

// QK-48: an Upsert with no updateCols on a conflicting key leaves the row as
// it was on SQLite (DO NOTHING) and returns nil — it returned sql.ErrNoRows,
// from the RETURNING that had no row, while MySQL, MariaDB, SQL Server and
// Oracle answered nil. internal/enginesuite (UpsertEmptyUpdateCols) pins what
// each of the six engines does with an empty updateCols.
func TestUpsertWithoutUpdateColsOnAConflictIsNotAnError(t *testing.T) {
	type row struct {
		ID   int64  `db:"id" pk:"true"`
		Code string `db:"code" quark:"unique"`
		Name string `db:"name"`
	}
	ctx := context.Background()
	c, err := New("sqlite", "file:qk48_upsert?mode=memory&cache=shared", WithLogger(slog.New(slog.NewTextHandler(io.Discard, nil))))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = c.Close() })
	if err := c.Migrate(ctx, &row{}); err != nil {
		t.Fatal(err)
	}
	seed := row{Code: "a", Name: "first"}
	if err := For[row](ctx, c).Create(&seed); err != nil {
		t.Fatal(err)
	}

	again := row{Code: "a", Name: "second"}
	if err := For[row](ctx, c).Upsert(&again, []string{"code"}, nil); err != nil {
		t.Fatalf("Upsert of a conflicting key with no updateCols = %v, want nil", err)
	}
	got, err := For[row](ctx, c).Where("code", "=", "a").First()
	if err != nil {
		t.Fatal(err)
	}
	if got.Name != "first" {
		t.Errorf("the conflicting row became %+v, want it left as it was", got)
	}
	if again.ID != 0 {
		t.Errorf("the entity got key %d: nothing was written, so nothing is written back", again.ID)
	}

	// A new key is inserted, and its key written back, as before.
	fresh := row{Code: "b", Name: "new"}
	if err := For[row](ctx, c).Upsert(&fresh, []string{"code"}, nil); err != nil || fresh.ID == 0 {
		t.Errorf("Upsert of a new key with no updateCols: id %d, %v", fresh.ID, err)
	}

	// With updateCols nothing changed: the row is updated and its key
	// written back.
	upd := row{Code: "a", Name: "third"}
	if err := For[row](ctx, c).Upsert(&upd, []string{"code"}, []string{"name"}); err != nil || upd.ID != seed.ID {
		t.Errorf("Upsert with updateCols: id %d (want %d), %v", upd.ID, seed.ID, err)
	}
}
