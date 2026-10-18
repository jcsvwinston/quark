// Copyright 2026 jcsvwinston
// SPDX-License-Identifier: Apache-2.0

package enginesuite

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/jcsvwinston/quark"
)

// QK-58 (DEP-2026-004) on the engine this lane runs: the writes of a
// versioned model outside Update, UpdateFields, Tracked.Save and UpdateBatch
// are inside optimistic locking too. The update branch of Upsert and
// UpsertBatch holds the row to the entity's version and bumps it — the
// statement shapes differ per engine (ON CONFLICT … DO UPDATE … WHERE, a
// guarded ON DUPLICATE KEY UPDATE, MERGE … WHEN MATCHED AND), the contract
// does not — and a zero version is treated as new; the soft delete and
// Restore hold and bump; UpdateMap bumps. WhereGuards/Versioned covers
// Delete and HardDelete under conditions.

type uvRow struct {
	ID      int64  `db:"id" pk:"true"`
	Code    string `db:"code,size=20" quark:"unique"`
	Balance int64  `db:"balance"`
	Version int64  `db:"version" quark:"version"`
}

func (uvRow) TableName() string { return "upsert_versioned_rows" }

type uvTrashed struct {
	ID        int64      `db:"id" pk:"true"`
	Code      string     `db:"code,size=20" quark:"unique"`
	Balance   int64      `db:"balance"`
	Version   int64      `db:"version" quark:"version"`
	DeletedAt *time.Time `db:"deleted_at"`
}

func (uvTrashed) TableName() string { return "upsert_versioned_trashed" }

type uvTenantRow struct {
	ID       int64  `db:"id" pk:"true"`
	TenantID string `db:"tenant_id,size=20"`
	Code     string `db:"code,size=20" quark:"unique"`
	Balance  int64  `db:"balance"`
	Version  int64  `db:"version" quark:"version"`
}

func (uvTenantRow) TableName() string { return "upsert_versioned_tenant_rows" }

type uvTenantKey struct{}

func testUpsertVersioned(ctx context.Context, t *testing.T, client *quark.Client) {
	engine := client.Dialect().Name()
	dropTable(client, "upsert_versioned_rows")
	if err := client.Migrate(ctx, &uvRow{}); err != nil {
		t.Fatalf("migrate on %s: %v", engine, err)
	}
	t.Cleanup(func() { dropTable(client, "upsert_versioned_rows") })

	q := func() *quark.Query[uvRow] { return quark.For[uvRow](ctx, client) }
	// reseed empties the table and writes a and b at version 1, balance 10
	// and 20.
	reseed := func(t *testing.T) map[string]uvRow {
		t.Helper()
		if _, err := client.Raw().Exec("DELETE FROM " + client.Dialect().Quote("upsert_versioned_rows")); err != nil {
			t.Fatalf("clear on %s: %v", engine, err)
		}
		out := map[string]uvRow{}
		for i, code := range []string{"a", "b"} {
			r := uvRow{Code: code, Balance: int64(i+1) * 10, Version: 1}
			if err := q().Create(&r); err != nil {
				t.Fatalf("seed %q on %s: %v", code, engine, err)
			}
			out[code] = r
		}
		return out
	}
	// table reads code → "balance@version".
	table := func(t *testing.T) map[string]string {
		t.Helper()
		rows, err := q().OrderBy("id", "ASC").Limit(100).List()
		if err != nil {
			t.Fatalf("read back on %s: %v", engine, err)
		}
		out := map[string]string{}
		for _, r := range rows {
			out[r.Code] = fmt.Sprintf("%d@%d", r.Balance, r.Version)
		}
		return out
	}
	same := func(a, b map[string]string) bool {
		if len(a) != len(b) {
			return false
		}
		for k, v := range a {
			if b[k] != v {
				return false
			}
		}
		return true
	}
	load := func(t *testing.T, id int64) *uvRow {
		t.Helper()
		r, err := q().Find(id)
		if err != nil {
			t.Fatalf("load %d on %s: %v", id, engine, err)
		}
		return &r
	}
	moveUnderneath := func(t *testing.T, id int64) {
		t.Helper()
		other := load(t, id)
		other.Balance += 1000
		if _, err := q().Update(other); err != nil {
			t.Fatalf("concurrent writer on %s: %v", engine, err)
		}
	}

	t.Run("StaleUpsertWritesNothing", func(t *testing.T) {
		s := reseed(t)
		stale := load(t, s["a"].ID)  // 10@1
		moveUnderneath(t, s["a"].ID) // → 1010@2
		before := table(t)
		stale.Balance = 77
		err := q().Upsert(stale, []string{"code"}, []string{"balance"})
		if !errors.Is(err, quark.ErrStaleEntity) || !strings.Contains(err.Error(), fmt.Sprintf("pk=%d", s["a"].ID)) {
			t.Fatalf("Upsert with a stale entity on %s = %v, want ErrStaleEntity naming the key", engine, err)
		}
		if got := table(t); !same(got, before) {
			t.Errorf("the stale Upsert on %s wrote: %v, want %v", engine, got, before)
		}
		if stale.Version != 1 {
			t.Errorf("the stale Upsert on %s moved the in-memory version to %d", engine, stale.Version)
		}
	})

	t.Run("FreshUpsertBumps", func(t *testing.T) {
		s := reseed(t)
		fresh := load(t, s["a"].ID)
		fresh.Balance = 11
		if err := q().Upsert(fresh, []string{"code"}, []string{"balance"}); err != nil {
			t.Fatalf("Upsert with a fresh entity on %s: %v", engine, err)
		}
		if got := table(t)["a"]; got != "11@2" {
			t.Errorf("after the fresh Upsert on %s row a is %s, want 11@2", engine, got)
		}
		if fresh.Version != 2 || fresh.ID != s["a"].ID {
			t.Errorf("after the fresh Upsert on %s the entity is at version %d with key %d, want 2 and %d", engine, fresh.Version, fresh.ID, s["a"].ID)
		}
		// The same struct goes on being written.
		fresh.Balance = 12
		if _, err := q().Update(fresh); err != nil {
			t.Errorf("Update after Upsert on %s = %v, want nil", engine, err)
		}
	})

	t.Run("ZeroVersionIsNew", func(t *testing.T) {
		s := reseed(t)
		moveUnderneath(t, s["a"].ID) // → 1010@2
		e := &uvRow{Code: "a", Balance: 5}
		if err := q().Upsert(e, []string{"code"}, []string{"balance"}); err != nil {
			t.Fatalf("Upsert with a zero version on %s = %v, want nil", engine, err)
		}
		if got := table(t)["a"]; got != "5@3" {
			t.Errorf("after the zero-version Upsert on %s row a is %s, want 5@3", engine, got)
		}
		if e.Version != 3 || e.ID != s["a"].ID {
			t.Errorf("after the zero-version Upsert on %s the entity is at version %d with key %d, want 3 and %d", engine, e.Version, e.ID, s["a"].ID)
		}
		// Inserted: the row carries the entity's version.
		n := &uvRow{Code: "n", Balance: 1, Version: 4}
		if err := q().Upsert(n, []string{"code"}, []string{"balance"}); err != nil {
			t.Fatalf("Upsert of a new code on %s = %v, want nil", engine, err)
		}
		if got := table(t)["n"]; got != "1@4" || n.Version != 4 {
			t.Errorf("after the insert on %s row n is %s and the entity at %d, want 1@4 and 4", engine, got, n.Version)
		}
	})

	t.Run("WithoutUpdateColsChecksNothing", func(t *testing.T) {
		s := reseed(t)
		stale := load(t, s["a"].ID)
		moveUnderneath(t, s["a"].ID)
		before := table(t)
		stale.Balance = 77
		if err := q().Upsert(stale, []string{"code"}, nil); err != nil {
			t.Fatalf("Upsert with a stale entity and no updateCols on %s = %v, want nil", engine, err)
		}
		if got := table(t); !same(got, before) {
			t.Errorf("the upsert without updateCols on %s wrote: %v, want %v", engine, got, before)
		}
	})

	t.Run("UpsertBatch", func(t *testing.T) {
		s := reseed(t)
		rows := []*uvRow{load(t, s["a"].ID), load(t, s["b"].ID)}
		moveUnderneath(t, s["b"].ID) // → 1020@2
		before := table(t)
		rows[0].Balance, rows[1].Balance = 11, 22
		err := q().UpsertBatch(rows, []string{"code"}, []string{"balance"})
		if !errors.Is(err, quark.ErrStaleEntity) || !strings.Contains(err.Error(), fmt.Sprintf("pk=%d", s["b"].ID)) {
			t.Fatalf("UpsertBatch with a stale entity on %s = %v, want ErrStaleEntity naming the key", engine, err)
		}
		if got := table(t); !same(got, before) {
			t.Errorf("the failed batch on %s left %v, want %v: nothing written", engine, got, before)
		}
		fresh := []*uvRow{load(t, s["a"].ID), load(t, s["b"].ID), {Code: "n", Balance: 3, Version: 1}}
		fresh[0].Balance, fresh[1].Balance = 11, 22
		if err := q().UpsertBatch(fresh, []string{"code"}, []string{"balance"}); err != nil {
			t.Fatalf("UpsertBatch with fresh entities on %s: %v", engine, err)
		}
		want := map[string]string{"a": "11@2", "b": "22@3", "n": "3@1"}
		if got := table(t); !same(got, want) {
			t.Errorf("after the batch on %s: %v, want %v", engine, got, want)
		}
		if fresh[0].Version != 2 || fresh[1].Version != 3 || fresh[2].Version != 1 {
			t.Errorf("in-memory versions after the batch on %s: %d, %d and %d, want 2, 3 and 1", engine, fresh[0].Version, fresh[1].Version, fresh[2].Version)
		}
	})

	t.Run("UpdateMapBumps", func(t *testing.T) {
		s := reseed(t)
		holder := load(t, s["a"].ID)
		n, err := q().Where("code", "=", "a").UpdateMap(map[string]any{"balance": int64(50)})
		if err != nil || n != 1 {
			t.Fatalf("UpdateMap on %s = (%d, %v), want (1, nil)", engine, n, err)
		}
		if got := table(t)["a"]; got != "50@2" {
			t.Errorf("after UpdateMap on %s row a is %s, want 50@2", engine, got)
		}
		holder.Balance = 11
		if _, err := q().Update(holder); !errors.Is(err, quark.ErrStaleEntity) {
			t.Errorf("Update after UpdateMap on %s = %v, want ErrStaleEntity", engine, err)
		}
		if _, err := q().Where("code", "=", "a").UpdateMap(map[string]any{"version": int64(9)}); !errors.Is(err, quark.ErrInvalidQuery) {
			t.Errorf("UpdateMap naming the version column on %s = %v, want ErrInvalidQuery", engine, err)
		}
	})

	t.Run("SoftDeleteAndRestore", func(t *testing.T) {
		dropTable(client, "upsert_versioned_trashed")
		if err := client.Migrate(ctx, &uvTrashed{}); err != nil {
			t.Fatalf("migrate on %s: %v", engine, err)
		}
		t.Cleanup(func() { dropTable(client, "upsert_versioned_trashed") })
		tq := func() *quark.Query[uvTrashed] { return quark.For[uvTrashed](ctx, client) }
		seed := uvTrashed{Code: "a", Balance: 10, Version: 1}
		if err := tq().Create(&seed); err != nil {
			t.Fatalf("seed on %s: %v", engine, err)
		}
		stale, err := tq().Find(seed.ID)
		if err != nil {
			t.Fatalf("load on %s: %v", engine, err)
		}
		winner, err := tq().Find(seed.ID)
		if err != nil {
			t.Fatalf("load on %s: %v", engine, err)
		}
		winner.Balance = 1010
		if _, err := tq().Update(&winner); err != nil { // → 1010@2
			t.Fatalf("concurrent writer on %s: %v", engine, err)
		}

		if n, err := tq().Delete(&stale); !errors.Is(err, quark.ErrStaleEntity) || n != 0 {
			t.Fatalf("soft Delete of a stale entity on %s = (%d, %v), want (0, ErrStaleEntity)", engine, n, err)
		}
		if n, err := tq().Delete(&winner); err != nil || n != 1 || winner.Version != 3 {
			t.Fatalf("soft Delete of a fresh entity on %s = (%d, %v), version %d; want (1, nil), 3", engine, n, err, winner.Version)
		}
		trashed, err := tq().WithTrashed().Find(seed.ID)
		if err != nil || trashed.DeletedAt == nil || trashed.Version != 3 {
			t.Fatalf("after the soft delete on %s the row is %+v (%v), want trashed at version 3", engine, trashed, err)
		}
		if n, err := tq().Delete(&winner); err != nil || n != 0 {
			t.Errorf("soft Delete of a trashed row on %s = (%d, %v), want (0, nil)", engine, n, err)
		}

		if n, err := tq().Restore(&stale); !errors.Is(err, quark.ErrStaleEntity) || n != 0 {
			t.Fatalf("Restore with a stale entity on %s = (%d, %v), want (0, ErrStaleEntity)", engine, n, err)
		}
		if n, err := tq().Restore(&winner); err != nil || n != 1 || winner.Version != 4 || winner.DeletedAt != nil {
			t.Fatalf("Restore with a fresh entity on %s = (%d, %v), version %d; want (1, nil), 4", engine, n, err, winner.Version)
		}
		restored, err := tq().Find(seed.ID)
		if err != nil || restored.Version != 4 {
			t.Fatalf("after Restore on %s the row is %+v (%v), want live at version 4", engine, restored, err)
		}
		if n, err := tq().Restore(&winner); err != nil || n != 0 {
			t.Errorf("Restore of a live row on %s = (%d, %v), want (0, nil)", engine, n, err)
		}
	})

	// Under RowLevelSecurityClient both guards are on the update branch:
	// another tenant's row is the constraint violation, the tenant's own
	// stale row the version conflict, and its fresh row is written.
	t.Run("TenantGuarded", func(t *testing.T) {
		dropTable(client, "upsert_versioned_tenant_rows")
		if err := client.Migrate(ctx, &uvTenantRow{}); err != nil {
			t.Fatalf("migrate on %s: %v", engine, err)
		}
		t.Cleanup(func() { dropTable(client, "upsert_versioned_tenant_rows") })
		cfg := quark.DefaultTenantConfig()
		cfg.Strategy = quark.RowLevelSecurityClient
		cfg.BaseClient = client
		router := quark.NewTenantRouter(cfg, func(c context.Context) string {
			tenant, _ := c.Value(uvTenantKey{}).(string)
			return tenant
		}, nil)
		ta := context.WithValue(ctx, uvTenantKey{}, "ta")
		guarded := func() *quark.Query[uvTenantRow] { return quark.For[uvTenantRow](ta, router) }

		seed := map[string]uvTenantRow{}
		for _, r := range []uvTenantRow{{TenantID: "tb", Code: "x", Balance: 1, Version: 1}, {TenantID: "ta", Code: "y", Balance: 2, Version: 1}} {
			r := r
			if err := quark.For[uvTenantRow](ctx, client).Create(&r); err != nil {
				t.Fatalf("seed %q on %s: %v", r.Code, engine, err)
			}
			seed[r.Code] = r
		}
		rows := func(t *testing.T) map[string]uvTenantRow {
			t.Helper()
			got, err := quark.For[uvTenantRow](ctx, client).List()
			if err != nil {
				t.Fatalf("list on %s: %v", engine, err)
			}
			out := map[string]uvTenantRow{}
			for _, r := range got {
				out[r.Code] = r
			}
			return out
		}

		foreign := uvTenantRow{Code: "x", Balance: 9, Version: 1}
		if err := guarded().Upsert(&foreign, []string{"code"}, []string{"balance"}); !errors.Is(err, quark.ErrConstraintViolation) {
			t.Errorf("Upsert of tb's code under ta on %s = %v, want ErrConstraintViolation", engine, err)
		}
		if got := rows(t)["x"]; got != seed["x"] {
			t.Errorf("tb's row on %s was written: %+v", engine, got)
		}

		stale := uvTenantRow{Code: "y", Balance: 9, Version: 7}
		if err := guarded().Upsert(&stale, []string{"code"}, []string{"balance"}); !errors.Is(err, quark.ErrStaleEntity) {
			t.Errorf("Upsert of ta's own code with a stale version on %s = %v, want ErrStaleEntity", engine, err)
		}
		if got := rows(t)["y"]; got != seed["y"] {
			t.Errorf("ta's row on %s was written by a stale upsert: %+v", engine, got)
		}

		fresh := uvTenantRow{Code: "y", Balance: 3, Version: 1}
		if err := guarded().Upsert(&fresh, []string{"code"}, []string{"balance"}); err != nil {
			t.Fatalf("Upsert of ta's own code at its version on %s: %v", engine, err)
		}
		if got := rows(t)["y"]; got.Balance != 3 || got.Version != 2 || got.TenantID != "ta" {
			t.Errorf("after the fresh guarded upsert on %s ta's row is %+v, want balance 3 at version 2", engine, got)
		}
		if fresh.Version != 2 || fresh.ID != seed["y"].ID {
			t.Errorf("after the fresh guarded upsert on %s the entity is at version %d with key %d, want 2 and %d", engine, fresh.Version, fresh.ID, seed["y"].ID)
		}
	})
}
