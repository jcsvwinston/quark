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

// QK-61 and QK-62: two edges of Upsert that QK-48 measured and documented,
// pinned per engine.
//
// QK-61 (MySQL, MariaDB). Without updateCols the no-op assignment was
// `<first conflict col> = VALUES(<it>)`. ON DUPLICATE KEY UPDATE fires on a
// duplicate of ANY unique key, so when the duplicate was on another key —
// the primary key, say — the existing row's first conflict column took the
// incoming value, and Upsert returned nil. The no-op is `<col> = <col>` now:
// the row is left as it was, as on the engines that do nothing. With
// updateCols the update branch still fires on a duplicate of any unique key:
// that is what ON DUPLICATE KEY UPDATE is, there is no conflict target to
// name, and the reference says so.
//
// QK-62 (SQL Server, Oracle). Without updateCols the MERGE updated every
// column the insert writes except the conflict columns: a non-zero integer
// key was in the update set, which the engine refuses even when the row would
// be inserted (Msg 8102, ORA-32796), and a created_at the timestamp
// convention stamped overwrote the row's. The inferred update set leaves out
// the primary key and created_at now, as Update does; updated_at stays in it.

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
	var merge, duplicateKey bool
	switch engine {
	case "postgres", "sqlite":
	case "mysql", "mariadb":
		duplicateKey = true
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

	// QK-62: the entity carries the row's key, the conflict is on code.
	t.Run("NonZeroKeyWithoutUpdateCols", func(t *testing.T) {
		s := seed(t, "a")["a"]
		if err := q().Upsert(&uxRow{ID: s.ID, Code: "a", Name: "second"}, []string{"code"}, nil); err != nil {
			t.Fatalf("Upsert of the row's own key with no updateCols on %s: %v, want nil", engine, err)
		}
		got := rows(t)["a"]
		if merge {
			if got.ID != s.ID || got.Name != "second" {
				t.Errorf("on %s the MERGE updates every non-conflict column but the key: got %+v, want id %d name second", engine, got, s.ID)
			}
			if !keptCreated(got) {
				t.Errorf("on %s the update branch overwrote created_at: %v, want the seeded %v", engine, got.CreatedAt, uxPast)
			}
			if !refreshedUpdated(got) {
				t.Errorf("on %s the update branch left updated_at at %v; it is refreshed, as by Update", engine, got.UpdatedAt)
			}
		} else if !untouched(got, s) {
			t.Errorf("on %s an upsert with no updateCols changed the conflicting row: %+v, seeded %+v", engine, got, s)
		}
	})

	// QK-62: "even when the row would be inserted" — Msg 8102 and
	// ORA-32796 are raised when the statement is compiled.
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
		// The MERGE's insert branch does not write an identity key; the
		// INSERT-based engines insert the entity's.
		if merge && got.ID == key {
			t.Errorf("on %s the inserted row has the entity's key %d; the identity assigns it", engine, got.ID)
		}
		if !merge && got.ID != key {
			t.Errorf("on %s the inserted row has key %d, want the entity's %d", engine, got.ID, key)
		}
	})

	// QK-62: with a zero key, created_at is the one the convention stamped
	// on the entity, and it used to overwrite the row's.
	t.Run("ZeroKeyWithoutUpdateColsKeepsCreatedAt", func(t *testing.T) {
		s := seed(t, "a")["a"]
		if err := q().Upsert(&uxRow{Code: "a", Name: "second"}, []string{"code"}, nil); err != nil {
			t.Fatalf("Upsert with no updateCols on %s: %v", engine, err)
		}
		got := rows(t)["a"]
		if !keptCreated(got) {
			t.Errorf("on %s an upsert with no updateCols overwrote created_at: %v, want the seeded %v", engine, got.CreatedAt, uxPast)
		}
		if merge && (got.Name != "second" || !refreshedUpdated(got)) {
			t.Errorf("on %s the MERGE updates name and updated_at: %+v", engine, got)
		}
		if !merge && !untouched(got, s) {
			t.Errorf("on %s an upsert with no updateCols changed the conflicting row: %+v, seeded %+v", engine, got, s)
		}
	})

	// QK-62, UpsertBatch: one bulk MERGE on SQL Server, one MERGE per row on
	// Oracle. The first entity's non-zero key puts the key column in the
	// statement.
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
		for code, want := range map[string]string{"a": "a2", "b": "b2"} {
			r := got[code]
			if !keptCreated(r) {
				t.Errorf("UpsertBatch on %s overwrote created_at of %q: %v", engine, code, r.CreatedAt)
			}
			if merge && (r.ID != s[code].ID || r.Name != want || !refreshedUpdated(r)) {
				t.Errorf("UpsertBatch on %s: row %q is %+v, want id %d name %s and updated_at refreshed", engine, code, r, s[code].ID, want)
			}
			if !merge && !untouched(r, s[code]) {
				t.Errorf("UpsertBatch with no updateCols on %s changed row %q: %+v", engine, code, r)
			}
		}
	})

	// The update branch never overwrites created_at on any engine when
	// updateCols does not name it — answered here for the four engines whose
	// update branch writes only updateCols.
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

	// QK-61: the duplicate is on the primary key, not on the conflict
	// column. The entity carries the existing row's key and a code no row
	// holds.
	t.Run("DuplicateOnAnotherKey", func(t *testing.T) {
		// checkOther holds every engine to what it does when the duplicate
		// is on a key other than conflictCols.
		checkOther := func(t *testing.T, err error, s uxRow, wantName string) {
			t.Helper()
			got := rows(t)
			a, ok := got["a"]
			switch {
			case !ok:
				t.Errorf("on %s no row has code a any more: the row holding key %d had its conflict column rewritten (rows %+v)", engine, s.ID, got)
			case a.ID != s.ID:
				t.Errorf("on %s the row with code a has key %d, want %d", engine, a.ID, s.ID)
			case !keptCreated(a):
				t.Errorf("on %s the row holding the key lost its created_at: %v", engine, a.CreatedAt)
			case a.Name != wantName:
				t.Errorf("on %s the row holding the key has name %q, want %q", engine, a.Name, wantName)
			}
			z, inserted := got["z"]
			if inserted && z.ID == s.ID {
				// Reported above: it is the existing row, rewritten.
				inserted = false
			}
			switch {
			case duplicateKey:
				if err != nil || inserted {
					t.Errorf("on %s: err %v, inserted %+v (%v); want nil and no row: ON DUPLICATE KEY UPDATE takes a duplicate of any unique key", engine, err, z, inserted)
				}
			case merge:
				// The MERGE matches on the conflict columns only; the
				// integer key is an identity, which its insert branch
				// does not write.
				if err != nil || !inserted || z.ID == s.ID {
					t.Errorf("on %s: err %v, inserted %+v (%v); want nil and a new row with a key of its own", engine, err, z, inserted)
				}
			default:
				// ON CONFLICT (code) names its arbiter: a duplicate of
				// another key is the error it always was.
				if !errors.Is(err, quark.ErrConstraintViolation) || inserted {
					t.Errorf("on %s: err %v, inserted %v; want ErrConstraintViolation and no row", engine, err, inserted)
				}
			}
		}

		t.Run("WithoutUpdateCols", func(t *testing.T) {
			s := seed(t, "a")["a"]
			err := q().Upsert(&uxRow{ID: s.ID, Code: "z", Name: "other"}, []string{"code"}, nil)
			checkOther(t, err, s, "first")
		})
		t.Run("UpsertBatchWithoutUpdateCols", func(t *testing.T) {
			s := seed(t, "a")["a"]
			err := q().UpsertBatch([]*uxRow{{ID: s.ID, Code: "z", Name: "other"}}, []string{"code"}, nil)
			checkOther(t, err, s, "first")
		})
		// With updateCols MySQL and MariaDB update the row that holds the
		// duplicate key, whichever key it is: the reference says so.
		t.Run("WithUpdateCols", func(t *testing.T) {
			s := seed(t, "a")["a"]
			err := q().Upsert(&uxRow{ID: s.ID, Code: "z", Name: "other"}, []string{"code"}, []string{"name"})
			want := "first"
			if duplicateKey {
				want = "other"
			}
			checkOther(t, err, s, want)
		})
	})

	// QK-61 under RowLevelSecurityClient: the guarded no-op assignment on
	// MySQL and MariaDB was IF(<tenant> = ?, VALUES(<col>), <col>), which
	// wrote the incoming value into the tenant's own row.
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
		err := quark.For[uxTenantRow](ta, router).Upsert(&uxTenantRow{ID: s.ID, Code: "z", Name: "a-z"}, []string{"code"}, nil)
		got, lerr := quark.For[uxTenantRow](ctx, client).List()
		if lerr != nil {
			t.Fatalf("list on %s: %v", engine, lerr)
		}
		byCode := make(map[string]uxTenantRow, len(got))
		for _, r := range got {
			byCode[r.Code] = r
		}
		if y, ok := byCode["y"]; !ok || y != s {
			t.Errorf("on %s the tenant's row holding key %d is not as seeded: rows %+v, want %+v among them", engine, s.ID, got, s)
		}
		z, inserted := byCode["z"]
		if inserted && z.ID == s.ID {
			// Reported above: it is the tenant's row, rewritten.
			inserted = false
		}
		switch {
		case duplicateKey:
			if err != nil || inserted {
				t.Errorf("on %s: err %v, inserted %+v (%v); want nil and no row", engine, err, z, inserted)
			}
		case merge:
			if err != nil || !inserted || z.TenantID != "ta" || z.ID == s.ID {
				t.Errorf("on %s: err %v, inserted %+v (%v); want nil and a new row of ta with a key of its own", engine, err, z, inserted)
			}
		default:
			if !errors.Is(err, quark.ErrConstraintViolation) || inserted {
				t.Errorf("on %s: err %v, inserted %v; want ErrConstraintViolation and no row", engine, err, inserted)
			}
		}
	})
}
