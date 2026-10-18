// Copyright 2026 jcsvwinston
// SPDX-License-Identifier: Apache-2.0

package enginesuite

import (
	"context"
	"errors"
	"testing"

	"github.com/jcsvwinston/quark"
)

// QK-63 and QK-64: the key Upsert and UpsertBatch write into the entity,
// pinned per engine and per branch. The rule: when Upsert returns nil, the
// entity's key is the key of the row the statement inserted or updated, or
// it is left as it was where nothing was written or the engine does not say
// which row that is — never the key of another row.
//
// QK-63 (MySQL, and MariaDB through the MySQL path). The key was read with
// SELECT LAST_INSERT_ID() as a second statement, from whichever connection
// the pool handed out. LAST_INSERT_ID() belongs to a connection, and on the
// update branch it is that connection's last generated key, not the
// conflicting row's: with rows 1 and 2 seeded, an upsert that met row 1 set
// the entity's key to 2.
//
// QK-64 (SQL Server, Oracle). The key is an identity column there, which
// the MERGE's insert branch does not write: an entity that carried a
// non-zero key got a row with the key the engine assigned and kept its own,
// 424242 against 2.
//
// DEP-2026-006 (QK-59, QK-65): a conflict without updateCols writes nothing
// on every engine and leaves the key as carried, and a duplicate of another
// unique key is ErrConstraintViolation everywhere with the key as carried.
// MySQL and MariaDB upsert a batch row by row, so each entity gets its key
// as with Upsert.

type ukRow struct {
	ID   int64  `db:"id" pk:"true"`
	Code string `db:"code,size=20" quark:"unique"`
	Name string `db:"name,size=40"`
}

func (ukRow) TableName() string { return "upsert_key_rows" }

// ukBare has no column outside the key and the conflict column, so a MERGE
// upsert of it has no update branch.
type ukBare struct {
	ID   int64  `db:"id" pk:"true"`
	Code string `db:"code,size=20" quark:"unique"`
}

func (ukBare) TableName() string { return "upsert_key_bare" }

type ukTenantRow struct {
	ID       int64  `db:"id" pk:"true"`
	TenantID string `db:"tenant_id,size=20"`
	Code     string `db:"code,size=20" quark:"unique"`
	Name     string `db:"name,size=40"`
}

func (ukTenantRow) TableName() string { return "upsert_key_tenant_rows" }

type ukTenantKey struct{}

const ukCarried = 424242

func testUpsertKey(ctx context.Context, t *testing.T, client *quark.Client) {
	runUpsertKey(ctx, t, client)
	// MariaDB answers RETURNING, which quark's MariaDB dialect uses; a
	// MariaDB server driven through the MySQL dialect takes MySQL's path,
	// where QK-63 lived.
	if client.Dialect().Name() == "mariadb" {
		t.Run("MySQLDialectOnMariaDB", func(t *testing.T) {
			viaMySQL, err := quark.NewWithDB("mysql", client.Raw(), quark.WithDialect(quark.MySQL()))
			if err != nil {
				t.Fatalf("a MySQL-dialect client over the MariaDB pool: %v", err)
			}
			defer viaMySQL.Close()
			runUpsertKey(ctx, t, viaMySQL)
		})
	}
}

func runUpsertKey(ctx context.Context, t *testing.T, client *quark.Client) {
	engine := client.Dialect().Name()
	// returning: PostgreSQL and SQLite, whose multi-row INSERT reports no
	// key per row; duplicateKey and merge: the engines that upsert a batch
	// row by row, or read every key back from the MERGE.
	var returning, duplicateKey, merge bool
	switch engine {
	case "postgres", "sqlite":
		returning = true
	case "mysql", "mariadb":
		duplicateKey = true
	case "mssql", "oracle":
		merge = true
	default:
		t.Skipf("no Upsert key contract pinned for dialect %q", engine)
	}
	quote := client.Dialect().Quote

	dropTable(client, "upsert_key_rows")
	dropTable(client, "upsert_key_bare")
	if err := client.Migrate(ctx, &ukRow{}, &ukBare{}); err != nil {
		t.Fatalf("migrate on %s: %v", engine, err)
	}
	t.Cleanup(func() {
		dropTable(client, "upsert_key_rows")
		dropTable(client, "upsert_key_bare")
	})

	// seed empties the table and inserts a and then b, so the last key the
	// seeding connection generated is b's: the key QK-63 handed an upsert
	// that met a.
	seed := func(t *testing.T) {
		t.Helper()
		if _, err := client.Raw().Exec("DELETE FROM " + quote("upsert_key_rows")); err != nil {
			t.Fatalf("clear on %s: %v", engine, err)
		}
		for _, code := range []string{"a", "b"} {
			if err := quark.For[ukRow](ctx, client).Create(&ukRow{Code: code, Name: "first"}); err != nil {
				t.Fatalf("seed %q on %s: %v", code, engine, err)
			}
		}
	}
	keys := func(t *testing.T) map[string]int64 {
		t.Helper()
		got, err := quark.For[ukRow](ctx, client).List()
		if err != nil {
			t.Fatalf("list on %s: %v", engine, err)
		}
		out := make(map[string]int64, len(got))
		for _, r := range got {
			out[r.Code] = r.ID
		}
		return out
	}

	type upsertCase struct {
		name       string
		entity     ukRow
		updateCols []string
		// want is the key the entity must hold afterwards, given the keys
		// of the rows by code.
		want func(rows map[string]int64) int64
	}
	rowKey := func(code string) func(map[string]int64) int64 {
		return func(rows map[string]int64) int64 { return rows[code] }
	}
	// kept answers the key the entity carried: nothing was written, so
	// nothing is written back, on every engine.
	kept := func(carried int64) func(map[string]int64) int64 {
		return func(map[string]int64) int64 { return carried }
	}
	cases := []upsertCase{
		// QK-63: the update branch of a zero key. MySQL wrote b's key.
		{"ZeroKeyUpdateBranch", ukRow{Code: "a", Name: "x"}, []string{"name"}, rowKey("a")},
		{"ZeroKeyInsertBranch", ukRow{Code: "n", Name: "x"}, []string{"name"}, rowKey("n")},
		// Without updateCols nothing is written and no key is written back
		// (QK-59), on the engines that used to answer the conflicting row
		// too.
		{"ZeroKeyWithoutUpdateCols", ukRow{Code: "a", Name: "x"}, nil, kept(0)},
		// QK-64: the insert branch with a key the entity carried. The
		// INSERT-based engines write it; the MERGE's identity assigns
		// another, which the entity now gets.
		{"NonZeroKeyInsertBranch", ukRow{ID: ukCarried, Code: "n", Name: "x"}, []string{"name"}, rowKey("n")},
		{"NonZeroKeyInsertBranchWithoutUpdateCols", ukRow{ID: ukCarried, Code: "n", Name: "x"}, nil, rowKey("n")},
		// The row that took the update has a key of its own, which the
		// entity gets, as the RETURNING engines always gave it.
		{"NonZeroKeyUpdateBranch", ukRow{ID: ukCarried, Code: "a", Name: "x"}, []string{"name"}, rowKey("a")},
		{"NonZeroKeyWithoutUpdateCols", ukRow{ID: ukCarried, Code: "a", Name: "x"}, nil, kept(ukCarried)},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			seed(t)
			e := c.entity
			if err := quark.For[ukRow](ctx, client).Upsert(&e, []string{"code"}, c.updateCols); err != nil {
				t.Fatalf("Upsert %+v with updateCols %v on %s: %v", c.entity, c.updateCols, engine, err)
			}
			rows := keys(t)
			if want := c.want(rows); e.ID != want {
				t.Errorf("on %s the entity holds key %d, want %d (rows by code %v)", engine, e.ID, want, rows)
			}
		})
	}

	// The duplicate is on the primary key, not on the conflict column:
	// ErrConstraintViolation on every engine, nothing written, the key as
	// carried (QK-65). MySQL and MariaDB used to update that row, the MERGE
	// to insert a new one with a key the identity assigned.
	t.Run("DuplicateOnTheKey", func(t *testing.T) {
		seed(t)
		a := keys(t)["a"]
		e := ukRow{ID: a, Code: "z", Name: "x"}
		err := quark.For[ukRow](ctx, client).Upsert(&e, []string{"code"}, []string{"name"})
		rows := keys(t)
		if !errors.Is(err, quark.ErrConstraintViolation) || e.ID != a {
			t.Errorf("on %s: err %v, key %d; want ErrConstraintViolation and the key carried, %d", engine, err, e.ID, a)
		}
		if _, inserted := rows["z"]; inserted || len(rows) != 2 {
			t.Errorf("on %s the refused upsert wrote: rows %v", engine, rows)
		}
	})

	// A model with no column outside the key and the conflict column: a
	// conflict without updateCols writes nothing, returns nil, and leaves
	// the key as carried on every engine.
	t.Run("NoUpdateBranch", func(t *testing.T) {
		bare := func(t *testing.T) map[string]int64 {
			t.Helper()
			if _, err := client.Raw().Exec("DELETE FROM " + quote("upsert_key_bare")); err != nil {
				t.Fatalf("clear on %s: %v", engine, err)
			}
			for _, code := range []string{"a", "b"} {
				if err := quark.For[ukBare](ctx, client).Create(&ukBare{Code: code}); err != nil {
					t.Fatalf("seed %q on %s: %v", code, engine, err)
				}
			}
			got, err := quark.For[ukBare](ctx, client).List()
			if err != nil {
				t.Fatalf("list on %s: %v", engine, err)
			}
			out := make(map[string]int64, len(got))
			for _, r := range got {
				out[r.Code] = r.ID
			}
			return out
		}
		for _, carried := range []int64{0, ukCarried} {
			rows := bare(t)
			e := ukBare{ID: carried, Code: "a"}
			if err := quark.For[ukBare](ctx, client).Upsert(&e, []string{"code"}, nil); err != nil {
				t.Fatalf("Upsert of an existing code, key %d, no update branch on %s: %v, want nil", carried, engine, err)
			}
			if e.ID != carried {
				t.Errorf("on %s a conflict with no update branch left key %d, want the carried %d (rows %v)", engine, e.ID, carried, rows)
			}

			e = ukBare{ID: carried, Code: "n"}
			if err := quark.For[ukBare](ctx, client).Upsert(&e, []string{"code"}, nil); err != nil {
				t.Fatalf("Upsert of a new code, key %d, no update branch on %s: %v", carried, engine, err)
			}
			got, err := quark.For[ukBare](ctx, client).Where("code", "=", "n").First()
			if err != nil {
				t.Fatalf("read back on %s: %v", engine, err)
			}
			if e.ID != got.ID {
				t.Errorf("on %s the insert branch left key %d (carried %d), the row has %d", engine, e.ID, carried, got.ID)
			}
		}
	})

	// SQL Server refuses an OUTPUT without INTO on a table with an enabled
	// trigger (Msg 334), so the key goes INTO a table variable first.
	t.Run("TableWithATrigger", func(t *testing.T) {
		if engine != "mssql" {
			t.Skipf("a trigger shape for SQL Server only, not %s", engine)
		}
		if _, err := client.Raw().Exec("CREATE TRIGGER upsert_key_rows_touch ON upsert_key_rows AFTER INSERT, UPDATE AS BEGIN SET NOCOUNT ON; END"); err != nil {
			t.Fatalf("create trigger: %v", err)
		}
		defer func() { _, _ = client.Raw().Exec("DROP TRIGGER upsert_key_rows_touch") }()
		for _, carried := range []int64{0, ukCarried} {
			seed(t)
			e := ukRow{ID: carried, Code: "a", Name: "x"}
			if err := quark.For[ukRow](ctx, client).Upsert(&e, []string{"code"}, []string{"name"}); err != nil {
				t.Fatalf("Upsert, key %d, on a table with a trigger: %v", carried, err)
			}
			batch := []*ukRow{{ID: carried, Code: "n", Name: "x"}}
			if err := quark.For[ukRow](ctx, client).UpsertBatch(batch, []string{"code"}, []string{"name"}); err != nil {
				t.Fatalf("UpsertBatch, key %d, on a table with a trigger: %v", carried, err)
			}
			rows := keys(t)
			if e.ID != rows["a"] || batch[0].ID != rows["n"] {
				t.Errorf("on a table with a trigger, key %d: Upsert left %d (row %d), UpsertBatch %d (row %d)", carried, e.ID, rows["a"], batch[0].ID, rows["n"])
			}
		}
	})

	// UpsertBatch: the MERGE engines write each entity's key, read back
	// with the row's position in the statement (SQL Server) or per row
	// (Oracle); MySQL and MariaDB upsert row by row and write each key as
	// Upsert does. The multi-row INSERT of PostgreSQL and SQLite reports no
	// key per row, and the entities keep what they carried.
	t.Run("UpsertBatch", func(t *testing.T) {
		for _, carried := range []int64{0, ukCarried} {
			seed(t)
			batch := []*ukRow{
				{ID: carried, Code: "n", Name: "x"},
				{ID: carried + 1, Code: "a", Name: "x"},
			}
			if carried == 0 {
				batch[1].ID = 0
			}
			before := []int64{batch[0].ID, batch[1].ID}
			if err := quark.For[ukRow](ctx, client).UpsertBatch(batch, []string{"code"}, []string{"name"}); err != nil {
				t.Fatalf("UpsertBatch with keys %v on %s: %v", before, engine, err)
			}
			rows := keys(t)
			for i, code := range []string{"n", "a"} {
				want := before[i]
				if merge || duplicateKey {
					want = rows[code]
				}
				if batch[i].ID != want {
					t.Errorf("UpsertBatch on %s: entity %d (code %s, carried %d) holds key %d, want %d (rows %v)", engine, i, code, before[i], batch[i].ID, want, rows)
				}
			}
		}
	})

	// Under RowLevelSecurityClient MySQL and MariaDB upsert through their
	// own path, one row at a time in UpsertBatch, with the key assignment
	// confined to the tenant: a row of another tenant gives no key.
	t.Run("TenantGuarded", func(t *testing.T) {
		dropTable(client, "upsert_key_tenant_rows")
		if err := client.Migrate(ctx, &ukTenantRow{}); err != nil {
			t.Fatalf("migrate on %s: %v", engine, err)
		}
		t.Cleanup(func() { dropTable(client, "upsert_key_tenant_rows") })
		cfg := quark.DefaultTenantConfig()
		cfg.Strategy = quark.RowLevelSecurityClient
		cfg.BaseClient = client
		router := quark.NewTenantRouter(cfg, func(c context.Context) string {
			tenant, _ := c.Value(ukTenantKey{}).(string)
			return tenant
		}, nil)
		ta := context.WithValue(ctx, ukTenantKey{}, "ta")
		guarded := func() *quark.Query[ukTenantRow] { return quark.For[ukTenantRow](ta, router) }

		// a and c are ta's, b is tb's; c is seeded last, so its key is the
		// seeding connection's last.
		seedT := func(t *testing.T) {
			t.Helper()
			if _, err := client.Raw().Exec("DELETE FROM " + quote("upsert_key_tenant_rows")); err != nil {
				t.Fatalf("clear on %s: %v", engine, err)
			}
			for _, r := range []ukTenantRow{{TenantID: "ta", Code: "a"}, {TenantID: "tb", Code: "b"}, {TenantID: "ta", Code: "c"}} {
				r.Name = "first"
				if err := quark.For[ukTenantRow](ctx, client).Create(&r); err != nil {
					t.Fatalf("seed %q on %s: %v", r.Code, engine, err)
				}
			}
		}
		tkeys := func(t *testing.T) map[string]int64 {
			t.Helper()
			got, err := quark.For[ukTenantRow](ctx, client).List()
			if err != nil {
				t.Fatalf("list on %s: %v", engine, err)
			}
			out := make(map[string]int64, len(got))
			for _, r := range got {
				out[r.Code] = r.ID
			}
			return out
		}

		type guardedCase struct {
			name       string
			entity     ukTenantRow
			updateCols []string
			wantErr    bool
			want       func(rows map[string]int64) int64
		}
		zero := func(map[string]int64) int64 { return 0 }
		for _, c := range []guardedCase{
			{"OwnRowUpdated", ukTenantRow{Code: "a", Name: "x"}, []string{"name"}, false, rowKey("a")},
			{"OwnRowWithoutUpdateCols", ukTenantRow{Code: "a", Name: "x"}, nil, false, zero},
			{"Inserted", ukTenantRow{Code: "n", Name: "x"}, []string{"name"}, false, rowKey("n")},
			{"InsertedCarryingAKey", ukTenantRow{ID: ukCarried, Code: "n", Name: "x"}, []string{"name"}, false, rowKey("n")},
			{"AnotherTenantsRow", ukTenantRow{Code: "b", Name: "x"}, []string{"name"}, true, zero},
			// Without updateCols there is no update branch on any engine:
			// another tenant's row is left alone and not reported (QK-59).
			{"AnotherTenantsRowWithoutUpdateCols", ukTenantRow{Code: "b", Name: "x"}, nil, false, zero},
		} {
			t.Run(c.name, func(t *testing.T) {
				seedT(t)
				e := c.entity
				err := guarded().Upsert(&e, []string{"code"}, c.updateCols)
				if c.wantErr != (err != nil) || (err != nil && !errors.Is(err, quark.ErrConstraintViolation)) {
					t.Fatalf("guarded Upsert %+v on %s: %v, want an error: %v", c.entity, engine, err, c.wantErr)
				}
				rows := tkeys(t)
				if want := c.want(rows); e.ID != want {
					t.Errorf("guarded Upsert on %s left key %d, want %d (rows by code %v)", engine, e.ID, want, rows)
				}
			})
		}

		t.Run("UpsertBatch", func(t *testing.T) {
			seedT(t)
			batch := []*ukTenantRow{{Code: "a", Name: "x"}, {Code: "n", Name: "x"}}
			if err := guarded().UpsertBatch(batch, []string{"code"}, []string{"name"}); err != nil {
				t.Fatalf("guarded UpsertBatch on %s: %v", engine, err)
			}
			rows := tkeys(t)
			for i, code := range []string{"a", "n"} {
				want := rows[code]
				if returning {
					// One multi-row INSERT, as without the guard.
					want = 0
				}
				if batch[i].ID != want {
					t.Errorf("guarded UpsertBatch on %s: entity %s holds key %d, want %d (rows %v)", engine, code, batch[i].ID, want, rows)
				}
			}
		})
	})
}
