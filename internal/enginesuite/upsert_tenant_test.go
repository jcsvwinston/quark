// Copyright 2026 jcsvwinston
// SPDX-License-Identifier: Apache-2.0

package enginesuite

import (
	"context"
	"errors"
	"testing"

	"github.com/jcsvwinston/quark"
)

// uteRow has a unique code that is NOT scoped by tenant, so a row of tenant
// tb and an upsert of tenant ta can meet on it.
type uteRow struct {
	ID       int64  `db:"id" pk:"true"`
	TenantID string `db:"tenant_id"`
	Code     string `db:"code" quark:"unique"`
	Name     string `db:"name"`
}

func (uteRow) TableName() string { return "ut_tenant_rows" }

type uteTenantKey struct{}

// testUpsertTenant proves QK-43 and QK-44 on the engine this lane runs.
//
// QK-43: under RowLevelSecurityClient the update branch of Upsert and
// UpsertBatch — ON CONFLICT … DO UPDATE, ON DUPLICATE KEY UPDATE, MERGE …
// WHEN MATCHED — only touches a row of the resolved tenant. Tenant ta
// upserting the code tb already holds leaves tb's row as it was and gets
// ErrConstraintViolation, with nothing written; a batch that meets tb's row
// is rolled back whole. ta's own code is updated and a new one inserted under
// ta, whatever tenant the entity carried.
//
// QK-44: UpdateMap naming the tenant column writes the resolved tenant.
func testUpsertTenant(ctx context.Context, t *testing.T, client *quark.Client) {
	engine := client.Dialect().Name()
	dropTable(client, "ut_tenant_rows")
	if err := client.Migrate(ctx, &uteRow{}); err != nil {
		t.Fatalf("migrate on %s: %v", engine, err)
	}
	t.Cleanup(func() { dropTable(client, "ut_tenant_rows") })

	cfg := quark.DefaultTenantConfig()
	cfg.Strategy = quark.RowLevelSecurityClient
	cfg.BaseClient = client
	router := quark.NewTenantRouter(cfg, func(c context.Context) string {
		tenant, _ := c.Value(uteTenantKey{}).(string)
		return tenant
	}, nil)
	ta := context.WithValue(ctx, uteTenantKey{}, "ta")

	reseed := func(t *testing.T) map[string]uteRow {
		t.Helper()
		if _, err := client.Raw().Exec("DELETE FROM " + client.Dialect().Quote("ut_tenant_rows")); err != nil {
			t.Fatalf("clear on %s: %v", engine, err)
		}
		for _, r := range []uteRow{{TenantID: "tb", Code: "x", Name: "b-x"}, {TenantID: "ta", Code: "y", Name: "a-y"}} {
			r := r
			if err := quark.For[uteRow](ctx, client).Create(&r); err != nil {
				t.Fatalf("seed %s on %s: %v", r.Code, engine, err)
			}
		}
		return uteRows(t, ctx, client)
	}

	t.Run("ForeignKeyIsNotUpdated", func(t *testing.T) {
		before := reseed(t)
		err := quark.For[uteRow](ta, router).Upsert(&uteRow{Code: "x", Name: "a-x"}, []string{"code"}, []string{"name"})
		if !errors.Is(err, quark.ErrConstraintViolation) {
			t.Fatalf("Upsert of tb's code under ta on %s = %v, want ErrConstraintViolation", engine, err)
		}
		if got := uteRows(t, ctx, client); !sameUteRows(got, before) {
			t.Errorf("Upsert of tb's code under ta on %s changed the table: before %+v, after %+v", engine, before, got)
		}
	})

	// Without updateCols PostgreSQL, SQLite, MySQL and MariaDB do nothing on
	// a conflict, and say nothing (QK-48: PostgreSQL and SQLite used to
	// return sql.ErrNoRows); SQL Server and Oracle update every non-conflict
	// column — the tenant column included, which used to move tb's row into
	// ta — and refuse. Either way tb's row stays as it was.
	t.Run("ForeignKeyWithoutUpdateCols", func(t *testing.T) {
		before := reseed(t)
		err := quark.For[uteRow](ta, router).Upsert(&uteRow{Code: "x", Name: "a-x"}, []string{"code"}, nil)
		if engine == "mssql" || engine == "oracle" {
			if !errors.Is(err, quark.ErrConstraintViolation) {
				t.Errorf("Upsert of tb's code with every column to update on %s = %v, want ErrConstraintViolation", engine, err)
			}
		} else if err != nil {
			t.Errorf("Upsert of tb's code with no update branch on %s = %v, want nil: nothing is written and nothing is reported", engine, err)
		}
		if got := uteRows(t, ctx, client); !sameUteRows(got, before) {
			t.Errorf("Upsert of tb's code without updateCols on %s changed the table: before %+v, after %+v", engine, before, got)
		}
	})

	t.Run("OwnKeyIsUpdatedAndNewKeyInsertedUnderTheTenant", func(t *testing.T) {
		before := reseed(t)
		own := uteRow{Code: "y", Name: "a-y2"}
		if err := quark.For[uteRow](ta, router).Upsert(&own, []string{"code"}, []string{"name"}); err != nil {
			t.Fatalf("Upsert of ta's own code on %s: %v", engine, err)
		}
		fresh := uteRow{Code: "z", TenantID: "tb", Name: "a-z"}
		if err := quark.For[uteRow](ta, router).Upsert(&fresh, []string{"code"}, []string{"name"}); err != nil {
			t.Fatalf("Upsert of a new code on %s: %v", engine, err)
		}
		got := uteRows(t, ctx, client)
		if got["y"].Name != "a-y2" || got["y"].TenantID != "ta" {
			t.Errorf("ta's own code on %s: %+v", engine, got["y"])
		}
		if got["z"].TenantID != "ta" {
			t.Errorf("a new code carrying tb was stored under %q on %s, want ta", got["z"].TenantID, engine)
		}
		if got["x"] != before["x"] {
			t.Errorf("tb's row moved on %s: %+v", engine, got["x"])
		}
		// Oracle's MERGE has no RETURNING; the key stays zero there (documented).
		if engine != "oracle" && (own.ID != got["y"].ID || fresh.ID != got["z"].ID) {
			t.Errorf("ids on %s: own %d (row %d), fresh %d (row %d)", engine, own.ID, got["y"].ID, fresh.ID, got["z"].ID)
		}
	})

	// MySQL and MariaDB report zero rows affected when the tenant's own row
	// already holds the values — the same count the guard produces for
	// another tenant's row. It is not a conflict, on any engine.
	t.Run("OwnKeyWithTheValuesItHas", func(t *testing.T) {
		before := reseed(t)
		same := uteRow{Code: "y", Name: "a-y"}
		if err := quark.For[uteRow](ta, router).Upsert(&same, []string{"code"}, []string{"name"}); err != nil {
			t.Fatalf("Upsert of ta's own code with its own values on %s: %v", engine, err)
		}
		if engine != "oracle" && same.ID != before["y"].ID {
			t.Errorf("Upsert of ta's own unchanged row on %s set id %d, the row's is %d", engine, same.ID, before["y"].ID)
		}
		batch := []*uteRow{{Code: "y", Name: "a-y"}}
		if err := quark.For[uteRow](ta, router).UpsertBatch(batch, []string{"code"}, []string{"name"}); err != nil {
			t.Fatalf("UpsertBatch of ta's own code with its own values on %s: %v", engine, err)
		}
	})

	t.Run("BatchMeetingAForeignKeyWritesNothing", func(t *testing.T) {
		before := reseed(t)
		batch := []*uteRow{{Code: "y", Name: "a-y2"}, {Code: "w", Name: "a-w"}, {Code: "x", Name: "a-x"}}
		err := quark.For[uteRow](ta, router).UpsertBatch(batch, []string{"code"}, []string{"name"})
		if !errors.Is(err, quark.ErrConstraintViolation) {
			t.Fatalf("UpsertBatch with tb's code under ta on %s = %v, want ErrConstraintViolation", engine, err)
		}
		if got := uteRows(t, ctx, client); !sameUteRows(got, before) {
			t.Errorf("a failed UpsertBatch on %s left writes behind: before %+v, after %+v", engine, before, got)
		}
		ok := []*uteRow{{Code: "y", Name: "a-y2"}, {Code: "w", Name: "a-w", TenantID: "tb"}}
		if err := quark.For[uteRow](ta, router).UpsertBatch(ok, []string{"code"}, []string{"name"}); err != nil {
			t.Fatalf("UpsertBatch of ta's codes on %s: %v", engine, err)
		}
		got := uteRows(t, ctx, client)
		if got["y"].Name != "a-y2" || got["w"].TenantID != "ta" || got["x"] != before["x"] {
			t.Errorf("UpsertBatch of ta's codes on %s: %+v", engine, got)
		}
	})

	// Inside the caller's transaction the batch is all or nothing too: the
	// rows it wrote before it met tb's row are undone by a savepoint, so they
	// are not in the commit of a caller that goes on (QK-57).
	t.Run("BatchMeetingAForeignKeyInTheCallersTransaction", func(t *testing.T) {
		before := reseed(t)
		err := router.Tx(ta, func(tx *quark.Tx) error {
			batch := []*uteRow{{Code: "y", Name: "a-y2"}, {Code: "w", Name: "a-w"}, {Code: "x", Name: "a-x"}}
			if err := quark.ForTx[uteRow](ta, tx).UpsertBatch(batch, []string{"code"}, []string{"name"}); !errors.Is(err, quark.ErrConstraintViolation) {
				t.Errorf("UpsertBatch with tb's code under ta inside router.Tx on %s = %v, want ErrConstraintViolation", engine, err)
			}
			return nil
		})
		if err != nil {
			t.Fatalf("router.Tx on %s: %v", engine, err)
		}
		if got := uteRows(t, ctx, client); !sameUteRows(got, before) {
			t.Errorf("a failed UpsertBatch inside a committed transaction on %s left writes behind: before %+v, after %+v", engine, before, got)
		}
	})

	t.Run("UpdateMapKeepsTheTenantColumn", func(t *testing.T) {
		reseed(t)
		n, err := quark.For[uteRow](ta, router).Where("code", "=", "y").
			UpdateMap(map[string]any{"tenant_id": "tb", "name": "moved"})
		if err != nil || n != 1 {
			t.Fatalf("UpdateMap on %s = (%d, %v), want (1, nil)", engine, n, err)
		}
		if got := uteRows(t, ctx, client)["y"]; got.TenantID != "ta" || got.Name != "moved" {
			t.Errorf("UpdateMap naming the tenant column on %s left %+v; want tenant ta", engine, got)
		}
	})
}

func uteRows(t *testing.T, ctx context.Context, client *quark.Client) map[string]uteRow {
	t.Helper()
	rows, err := quark.For[uteRow](ctx, client).Limit(100).List()
	if err != nil {
		t.Fatalf("read back: %v", err)
	}
	out := map[string]uteRow{}
	for _, r := range rows {
		out[r.Code] = r
	}
	return out
}

func sameUteRows(a, b map[string]uteRow) bool {
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
