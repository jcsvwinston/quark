// Copyright 2026 jcsvwinston
// SPDX-License-Identifier: Apache-2.0

package enginesuite

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/jcsvwinston/quark"
)

// QK-61, QK-62, QK-65 and QK-59: two edges of Upsert, the same on the six
// engines since DEP-2026-006.
//
// A duplicate of a unique key other than the conflict columns — the entity
// carries an existing row's primary key and a conflict value no row has —
// is ErrConstraintViolation everywhere, nothing written (QK-65). ON
// CONFLICT names its arbiter on PostgreSQL and SQLite; on MySQL and MariaDB,
// whose ON DUPLICATE KEY UPDATE fires on a duplicate of any unique key,
// every assignment is guarded by the conflict columns and a statement that
// changed nothing reads the row back; on SQL Server and Oracle the entity's
// key is checked before the MERGE, whose insert branch does not write the
// identity key. In v1 MySQL and MariaDB updated the row holding the
// duplicate whichever key it was (QK-61 made the no-op clause a no-op), and
// SQL Server and Oracle inserted a second row.
//
// An empty updateCols is insert-or-ignore everywhere (QK-59): the MERGE has
// no WHEN MATCHED branch. In v1 it inferred an update set — every column the
// insert writes but the conflict columns, the key and created_at (QK-62).

type uxRow struct {
	ID        int64     `db:"id" pk:"true"`
	Code      string    `db:"code,size=20" quark:"unique"`
	Name      string    `db:"name,size=40"`
	CreatedAt time.Time `db:"created_at"`
	UpdatedAt time.Time `db:"updated_at"`
}

func (uxRow) TableName() string { return "upsert_edge_rows" }

type uxTenantRow struct {
	ID       int64  `db:"id" pk:"true"`
	TenantID string `db:"tenant_id,size=20"`
	Code     string `db:"code,size=20" quark:"unique"`
	Name     string `db:"name,size=40"`
}

func (uxTenantRow) TableName() string { return "upsert_edge_tenant_rows" }

type uxTenantKey struct{}

// uxPast is the seeded rows' created_at and updated_at: a year no stamp of
// this run can produce, so a timestamp the upsert wrote is told from the
// seeded one by its year, whatever precision and zone the engine keeps.
var uxPast = time.Date(2020, 1, 2, 3, 4, 5, 0, time.UTC)

func testUpsertEngineEdges(ctx context.Context, t *testing.T, client *quark.Client) {
	engine := client.Dialect().Name()
	// merge: the key is an identity the MERGE's insert branch does not
	// write, so an inserted row has the key the engine assigned (QK-64).
	var merge bool
	switch engine {
	case "postgres", "sqlite", "mysql", "mariadb":
	case "mssql", "oracle":
		merge = true
	default:
		t.Skipf("no Upsert edges pinned for dialect %q", engine)
	}
	dropTable(client, "upsert_edge_rows")
	if err := client.Migrate(ctx, &uxRow{}); err != nil {
		t.Fatalf("migrate on %s: %v", engine, err)
	}
	t.Cleanup(func() { dropTable(client, "upsert_edge_rows") })

	q := func() *quark.Query[uxRow] { return quark.For[uxRow](ctx, client) }
	seed := func(t *testing.T, codes ...string) map[string]uxRow {
		t.Helper()
		if _, err := client.Raw().Exec("DELETE FROM " + client.Dialect().Quote("upsert_edge_rows")); err != nil {
			t.Fatalf("clear on %s: %v", engine, err)
		}
		out := make(map[string]uxRow, len(codes))
		for _, code := range codes {
			row := uxRow{Code: code, Name: "first", CreatedAt: uxPast, UpdatedAt: uxPast}
			if err := q().Create(&row); err != nil {
				t.Fatalf("seed %q on %s: %v", code, engine, err)
			}
			out[code] = row
		}
		return out
	}
	rows := func(t *testing.T) map[string]uxRow {
		t.Helper()
		got, err := q().List()
		if err != nil {
			t.Fatalf("list on %s: %v", engine, err)
		}
		out := make(map[string]uxRow, len(got))
		for _, r := range got {
			out[r.Code] = r
		}
		return out
	}
	// keptCreated reports whether the row still holds the seeded created_at.
	keptCreated := func(r uxRow) bool { return r.CreatedAt.UTC().Year() == uxPast.Year() }
	refreshedUpdated := func(r uxRow) bool { return r.UpdatedAt.UTC().Year() != uxPast.Year() }
	// untouched reports whether the row is the seeded one, as it was.
	untouched := func(r uxRow, s uxRow) bool {
		return r.ID == s.ID && r.Code == s.Code && r.Name == s.Name && keptCreated(r) && !refreshedUpdated(r)
	}

	// QK-59: the entity carries the row's key, the conflict is on code, no
	// updateCols: the row is left as it was.
	t.Run("NonZeroKeyWithoutUpdateCols", func(t *testing.T) {
		s := seed(t, "a")["a"]
		if err := q().Upsert(&uxRow{ID: s.ID, Code: "a", Name: "second"}, []string{"code"}, nil); err != nil {
			t.Fatalf("Upsert of the row's own key with no updateCols on %s: %v, want nil", engine, err)
		}
		if got := rows(t)["a"]; !untouched(got, s) {
			t.Errorf("on %s an upsert with no updateCols changed the conflicting row: %+v, seeded %+v", engine, got, s)
		}
	})

	// QK-64: a new code carrying a key no row holds is inserted. The
	// INSERT-based engines write the entity's key; the MERGE's identity
	// assigns another.
	t.Run("NonZeroKeyInserts", func(t *testing.T) {
		seed(t, "a")
		const key = 424242
		if err := q().Upsert(&uxRow{ID: key, Code: "n", Name: "new"}, []string{"code"}, nil); err != nil {
			t.Fatalf("Upsert of a new code carrying a non-zero key on %s: %v, want nil", engine, err)
		}
		got, ok := rows(t)["n"]
		if !ok || got.Name != "new" {
			t.Errorf("Upsert of a new code carrying a non-zero key on %s inserted %+v (found %v)", engine, got, ok)
		}
		if merge && got.ID == key {
			t.Errorf("on %s the inserted row has the entity's key %d; the identity assigns it", engine, got.ID)
		}
		if !merge && got.ID != key {
			t.Errorf("on %s the inserted row has key %d, want the entity's %d", engine, got.ID, key)
		}
	})

	// QK-59, QK-62: with a zero key and no updateCols the row is left as it
	// was, created_at included — the stamp the convention put on the entity
	// used to overwrite the row's on the MERGE engines.
	t.Run("ZeroKeyWithoutUpdateColsKeepsCreatedAt", func(t *testing.T) {
		s := seed(t, "a")["a"]
		if err := q().Upsert(&uxRow{Code: "a", Name: "second"}, []string{"code"}, nil); err != nil {
			t.Fatalf("Upsert with no updateCols on %s: %v", engine, err)
		}
		if got := rows(t)["a"]; !untouched(got, s) {
			t.Errorf("on %s an upsert with no updateCols changed the conflicting row: %+v, seeded %+v", engine, got, s)
		}
	})

	// QK-59, UpsertBatch: the rows' own keys, no updateCols — every row
	// left as it was, on the bulk MERGE of SQL Server too.
	t.Run("UpsertBatchNonZeroKeysWithoutUpdateCols", func(t *testing.T) {
		s := seed(t, "a", "b")
		batch := []*uxRow{
			{ID: s["a"].ID, Code: "a", Name: "a2"},
			{ID: s["b"].ID, Code: "b", Name: "b2"},
		}
		if err := q().UpsertBatch(batch, []string{"code"}, nil); err != nil {
			t.Fatalf("UpsertBatch of the rows' own keys with no updateCols on %s: %v, want nil", engine, err)
		}
		got := rows(t)
		for _, code := range []string{"a", "b"} {
			if !untouched(got[code], s[code]) {
				t.Errorf("UpsertBatch with no updateCols on %s changed row %q: %+v, seeded %+v", engine, code, got[code], s[code])
			}
		}
	})

	// The update branch never overwrites created_at when updateCols does
	// not name it, and refreshes updated_at when it does.
	t.Run("UpdateColsLeaveCreatedAt", func(t *testing.T) {
		seed(t, "a")
		if err := q().Upsert(&uxRow{Code: "a", Name: "second"}, []string{"code"}, []string{"name", "updated_at"}); err != nil {
			t.Fatalf("Upsert with updateCols on %s: %v", engine, err)
		}
		got := rows(t)["a"]
		if got.Name != "second" || !keptCreated(got) || !refreshedUpdated(got) {
			t.Errorf("Upsert with updateCols [name updated_at] on %s left %+v, want name second, created_at kept, updated_at refreshed", engine, got)
		}
		if err := q().UpsertBatch([]*uxRow{{Code: "a", Name: "third"}}, []string{"code"}, []string{"name", "updated_at"}); err != nil {
			t.Fatalf("UpsertBatch with updateCols on %s: %v", engine, err)
		}
		if got := rows(t)["a"]; got.Name != "third" || !keptCreated(got) {
			t.Errorf("UpsertBatch with updateCols [name updated_at] on %s left %+v, want name third and created_at kept", engine, got)
		}
	})

	// QK-65: the duplicate is on the primary key, not on the conflict
	// column. The entity carries the existing row's key and a code no row
	// holds: ErrConstraintViolation, nothing written, on every engine.
	t.Run("DuplicateOnAnotherKey", func(t *testing.T) {
		checkOther := func(t *testing.T, err error, s uxRow, carried int64) {
			t.Helper()
			if !errors.Is(err, quark.ErrConstraintViolation) {
				t.Errorf("on %s: err %v, want ErrConstraintViolation: the conflict columns name the key Upsert arbitrates", engine, err)
			}
			got := rows(t)
			a, ok := got["a"]
			switch {
			case !ok:
				t.Errorf("on %s no row has code a any more: the row holding key %d had its conflict column rewritten (rows %+v)", engine, s.ID, got)
			case !untouched(a, s):
				t.Errorf("on %s the row holding the key was written: %+v, seeded %+v", engine, a, s)
			}
			if z, inserted := got["z"]; inserted {
				t.Errorf("on %s a row with the new code was inserted: %+v", engine, z)
			}
			if n := len(got); n != 1 {
				t.Errorf("on %s the table has %d rows after the refused upsert, want 1", engine, n)
			}
			if carried != s.ID {
				t.Errorf("on %s the refused upsert changed the entity's key to %d, want %d kept", engine, carried, s.ID)
			}
		}

		t.Run("WithoutUpdateCols", func(t *testing.T) {
			s := seed(t, "a")["a"]
			e := uxRow{ID: s.ID, Code: "z", Name: "other"}
			err := q().Upsert(&e, []string{"code"}, nil)
			checkOther(t, err, s, e.ID)
		})
		t.Run("UpsertBatchWithoutUpdateCols", func(t *testing.T) {
			s := seed(t, "a")["a"]
			e := uxRow{ID: s.ID, Code: "z", Name: "other"}
			err := q().UpsertBatch([]*uxRow{&e}, []string{"code"}, nil)
			checkOther(t, err, s, e.ID)
		})
		t.Run("WithUpdateCols", func(t *testing.T) {
			s := seed(t, "a")["a"]
			e := uxRow{ID: s.ID, Code: "z", Name: "other"}
			err := q().Upsert(&e, []string{"code"}, []string{"name"})
			checkOther(t, err, s, e.ID)
		})
		// A batch that meets the duplicate after a row it would insert
		// leaves nothing written: one statement for both rows on
		// PostgreSQL, SQLite and SQL Server, a transaction around the rows
		// on MySQL, MariaDB and Oracle. Both entities carry a key: the
		// multi-row INSERT writes the key column only when the first
		// entity carries one.
		t.Run("UpsertBatchWritesNothing", func(t *testing.T) {
			s := seed(t, "a")["a"]
			batch := []*uxRow{{ID: 424242, Code: "n", Name: "new"}, {ID: s.ID, Code: "z", Name: "other"}}
			err := q().UpsertBatch(batch, []string{"code"}, []string{"name"})
			if !errors.Is(err, quark.ErrConstraintViolation) {
				t.Fatalf("UpsertBatch meeting a duplicate on another key on %s = %v, want ErrConstraintViolation", engine, err)
			}
			got := rows(t)
			if _, inserted := got["n"]; inserted {
				t.Errorf("on %s the failed batch left the row written before the duplicate: %+v", engine, got)
			}
			if !untouched(got["a"], s) {
				t.Errorf("on %s the failed batch wrote the row holding the key: %+v", engine, got["a"])
			}
		})
	})

	// QK-65 under RowLevelSecurityClient: the tenant's own row holds the
	// key, the code is new — refused, nothing written.
	t.Run("TenantGuardedDuplicateOnAnotherKey", func(t *testing.T) {
		dropTable(client, "upsert_edge_tenant_rows")
		if err := client.Migrate(ctx, &uxTenantRow{}); err != nil {
			t.Fatalf("migrate on %s: %v", engine, err)
		}
		t.Cleanup(func() { dropTable(client, "upsert_edge_tenant_rows") })
		cfg := quark.DefaultTenantConfig()
		cfg.Strategy = quark.RowLevelSecurityClient
		cfg.BaseClient = client
		router := quark.NewTenantRouter(cfg, func(c context.Context) string {
			tenant, _ := c.Value(uxTenantKey{}).(string)
			return tenant
		}, nil)
		ta := context.WithValue(ctx, uxTenantKey{}, "ta")

		s := uxTenantRow{TenantID: "ta", Code: "y", Name: "a-y"}
		if err := quark.For[uxTenantRow](ctx, client).Create(&s); err != nil {
			t.Fatalf("seed on %s: %v", engine, err)
		}
		for _, update := range [][]string{nil, {"name"}} {
			err := quark.For[uxTenantRow](ta, router).Upsert(&uxTenantRow{ID: s.ID, Code: "z", Name: "a-z"}, []string{"code"}, update)
			if !errors.Is(err, quark.ErrConstraintViolation) {
				t.Errorf("on %s with updateCols %v: err %v, want ErrConstraintViolation", engine, update, err)
			}
			got, lerr := quark.For[uxTenantRow](ctx, client).List()
			if lerr != nil {
				t.Fatalf("list on %s: %v", engine, lerr)
			}
			if len(got) != 1 || got[0] != s {
				t.Errorf("on %s with updateCols %v the table is %+v, want the seeded %+v alone", engine, update, got, s)
			}
		}
	})
}
