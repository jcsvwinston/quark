// Copyright 2026 jcsvwinston
// SPDX-License-Identifier: Apache-2.0

package quark_test

import (
	"context"
	"database/sql"
	"errors"
	"testing"

	"github.com/jcsvwinston/quark"
	_ "modernc.org/sqlite"
)

type nwdUser struct {
	ID   int64  `db:"id" pk:"true"`
	Name string `db:"name"`
}

// TestNewWithDB covers PR-COH-02: mounting quark on a host-owned *sql.DB
// (the Nucleus seam) must reuse the host's pool instead of opening a second
// one, and must NOT close the borrowed handle on Client.Close — the pool's
// lifecycle stays with its owner.
func TestNewWithDB(t *testing.T) {
	ctx := context.Background()

	db, err := sql.Open("sqlite", "file:newwithdb?mode=memory&cache=shared")
	if err != nil {
		t.Fatalf("host open: %v", err)
	}
	defer db.Close()

	client, err := quark.NewWithDB("sqlite", db)
	if err != nil {
		t.Fatalf("NewWithDB: %v", err)
	}

	t.Run("ReusesTheHostPool", func(t *testing.T) {
		if client.Raw() != db {
			t.Fatalf("client must speak through the host's *sql.DB, not a new pool")
		}
	})

	t.Run("FullCRUDWorks", func(t *testing.T) {
		if err := client.Migrate(ctx, &nwdUser{}); err != nil {
			t.Fatalf("migrate: %v", err)
		}
		u := nwdUser{Name: "host"}
		if err := quark.For[nwdUser](ctx, client).Create(&u); err != nil {
			t.Fatalf("create: %v", err)
		}
		got, err := quark.For[nwdUser](ctx, client).Find(u.ID)
		if err != nil || got.Name != "host" {
			t.Fatalf("find: %+v err=%v", got, err)
		}

		// The write is visible through the host's own handle — one pool.
		var n int
		if err := db.QueryRowContext(ctx, "SELECT COUNT(*) FROM nwd_users").Scan(&n); err != nil || n != 1 {
			t.Fatalf("host handle sees %d rows err=%v, want 1", n, err)
		}
	})

	t.Run("WithOptionsDerivesOverTheSameHandle", func(t *testing.T) {
		derived, err := client.WithOptions(quark.WithStrictColumns())
		if err != nil {
			t.Fatalf("WithOptions: %v", err)
		}
		if derived.Raw() != db {
			t.Errorf("derived client must reuse the same borrowed *sql.DB")
		}
		if _, err := quark.For[nwdUser](ctx, derived).Where("nmae", "=", "x").Limit(1).List(); !errors.Is(err, quark.ErrInvalidQuery) {
			t.Errorf("derived client must carry the new option; got %v", err)
		}
		_ = derived.Close()
	})

	t.Run("CloseLeavesTheBorrowedPoolOpen", func(t *testing.T) {
		if err := client.Close(); err != nil {
			t.Fatalf("close: %v", err)
		}
		// The host's pool must still be alive after quark lets go.
		if err := db.PingContext(ctx); err != nil {
			t.Fatalf("Client.Close closed the borrowed *sql.DB: %v", err)
		}
	})

	t.Run("NilDBRejected", func(t *testing.T) {
		if _, err := quark.NewWithDB("sqlite", nil); err == nil {
			t.Errorf("nil *sql.DB must be rejected")
		}
	})

	t.Run("InvalidOptionRejected", func(t *testing.T) {
		if _, err := quark.NewWithDB("sqlite", db, 42); err == nil {
			t.Errorf("non-option values must be rejected, as in New")
		}
	})
}
