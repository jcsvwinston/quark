// Copyright 2026 jcsvwinston
// SPDX-License-Identifier: Apache-2.0

package quark

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"reflect"
	"strings"
	"sync"
	"testing"

	_ "modernc.org/sqlite"
)

// QK-43: under RowLevelSecurityClient the update branch of Upsert and
// UpsertBatch only touches a row of the resolved tenant; a conflict with
// another tenant's row writes nothing and returns ErrConstraintViolation.
// QK-44: UpdateMap keeps the tenant column at the resolved tenant.
// internal/enginesuite (UpsertTenant) runs the same on the six engines.

type utRow struct {
	ID       int64  `db:"id" pk:"true"`
	TenantID string `db:"tenant_id"`
	Code     string `db:"code" quark:"unique"`
	Name     string `db:"name"`
}

func (utRow) TableName() string { return "ut_rows" }

// utClient seeds tb's "x" and ta's "y".
func utClient(t *testing.T, name string, opts ...any) (*Client, *txStatementRecorder) {
	t.Helper()
	ctx := context.Background()
	rec := &txStatementRecorder{}
	c, err := New("sqlite", "file:"+name+"?mode=memory&cache=shared",
		append([]any{WithLogger(slog.New(slog.NewTextHandler(io.Discard, nil))), WithQueryObserver(rec)}, opts...)...)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { _ = c.Close() })
	if err := c.Migrate(ctx, &utRow{}); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	for _, r := range []utRow{{TenantID: "tb", Code: "x", Name: "b-x"}, {TenantID: "ta", Code: "y", Name: "a-y"}} {
		r := r
		if err := For[utRow](ctx, c).Create(&r); err != nil {
			t.Fatalf("seed: %v", err)
		}
	}
	rec.reset()
	return c, rec
}

func utRows(t *testing.T, c *Client) map[string]utRow {
	t.Helper()
	rows, err := For[utRow](context.Background(), c).Limit(100).List()
	if err != nil {
		t.Fatalf("read back: %v", err)
	}
	out := map[string]utRow{}
	for _, r := range rows {
		out[r.Code] = r
	}
	return out
}

func TestUpsertUnderTenantLeavesAnotherTenantsRow(t *testing.T) {
	c, rec := utClient(t, "qk43_single")
	router, ta := wgRouter(c)
	before := utRows(t, c)

	err := For[utRow](ta, router).Upsert(&utRow{Code: "x", Name: "a-x"}, []string{"code"}, []string{"name"})
	if !errors.Is(err, ErrConstraintViolation) {
		t.Fatalf("Upsert of tb's key under ta = %v, want ErrConstraintViolation\n%v", err, rec.all)
	}
	if got := utRows(t, c); !reflect.DeepEqual(got, before) {
		t.Fatalf("Upsert of tb's key under ta changed the table:\nbefore %+v\nafter  %+v", before, got)
	}
	if !rec.any(`WHERE "ut_rows"."tenant_id" = ?`) {
		t.Errorf("the update branch carries no tenant predicate: %v", rec.all)
	}

	// Without updateCols there is no update branch (DO NOTHING): tb's row
	// stays as it was, and nothing is reported — it used to be sql.ErrNoRows
	// from the RETURNING that had no row (QK-48).
	if err := For[utRow](ta, router).Upsert(&utRow{Code: "x", Name: "a-x"}, []string{"code"}, nil); err != nil {
		t.Errorf("Upsert without updateCols under ta on tb's key = %v, want nil", err)
	}
	if got := utRows(t, c); !reflect.DeepEqual(got, before) {
		t.Fatalf("Upsert without updateCols under ta changed the table: %+v", got)
	}

	// The tenant's own key is updated, and the entity gets the row's id.
	own := utRow{Code: "y", Name: "a-y2"}
	if err := For[utRow](ta, router).Upsert(&own, []string{"code"}, []string{"name"}); err != nil {
		t.Fatalf("Upsert of ta's own key: %v", err)
	}
	if got := utRows(t, c)["y"]; got.Name != "a-y2" || got.TenantID != "ta" || own.ID != got.ID {
		t.Errorf("Upsert of ta's own key: row %+v, entity id %d", got, own.ID)
	}

	// A new key is inserted under the resolved tenant, whatever the entity said.
	fresh := utRow{Code: "z", TenantID: "tb", Name: "a-z"}
	if err := For[utRow](ta, router).Upsert(&fresh, []string{"code"}, []string{"name"}); err != nil {
		t.Fatalf("Upsert of a new key: %v", err)
	}
	if got := utRows(t, c)["z"]; got.TenantID != "ta" || fresh.ID == 0 {
		t.Errorf("Upsert of a new key carrying tb stored %+v (entity id %d), want tenant ta", got, fresh.ID)
	}
}

func TestUpsertBatchUnderTenantIsAllOrNothing(t *testing.T) {
	c, rec := utClient(t, "qk43_batch")
	router, ta := wgRouter(c)
	before := utRows(t, c)

	batch := func() []*utRow {
		return []*utRow{{Code: "y", Name: "a-y2"}, {Code: "w", Name: "a-w"}, {Code: "x", Name: "a-x"}}
	}
	err := For[utRow](ta, router).UpsertBatch(batch(), []string{"code"}, []string{"name"})
	if !errors.Is(err, ErrConstraintViolation) {
		t.Fatalf("UpsertBatch with tb's key under ta = %v, want ErrConstraintViolation\n%v", err, rec.all)
	}
	if got := utRows(t, c); !reflect.DeepEqual(got, before) {
		t.Fatalf("a failed UpsertBatch left writes behind:\nbefore %+v\nafter  %+v", before, got)
	}

	// The same inside the caller's transaction: the error rolls it back.
	txErr := router.Tx(ta, func(tx *Tx) error {
		return ForTx[utRow](ta, tx).UpsertBatch(batch(), []string{"code"}, []string{"name"})
	})
	if !errors.Is(txErr, ErrConstraintViolation) {
		t.Fatalf("UpsertBatch inside router.Tx = %v, want ErrConstraintViolation", txErr)
	}
	if got := utRows(t, c); !reflect.DeepEqual(got, before) {
		t.Fatalf("a failed UpsertBatch inside a transaction left writes behind: %+v", got)
	}

	// And when the caller goes on and commits: a savepoint undid the batch,
	// so its writes are not in the commit (QK-57).
	txErr = router.Tx(ta, func(tx *Tx) error {
		if err := ForTx[utRow](ta, tx).UpsertBatch(batch(), []string{"code"}, []string{"name"}); !errors.Is(err, ErrConstraintViolation) {
			t.Errorf("UpsertBatch inside router.Tx = %v, want ErrConstraintViolation", err)
		}
		return nil
	})
	if txErr != nil {
		t.Fatalf("router.Tx: %v", txErr)
	}
	if got := utRows(t, c); !reflect.DeepEqual(got, before) {
		t.Fatalf("a failed UpsertBatch inside a committed transaction left writes behind: %+v", got)
	}

	// Without the foreign key the batch goes through.
	ok := []*utRow{{Code: "y", Name: "a-y2"}, {Code: "w", Name: "a-w", TenantID: "tb"}}
	if err := For[utRow](ta, router).UpsertBatch(ok, []string{"code"}, []string{"name"}); err != nil {
		t.Fatalf("UpsertBatch of ta's keys: %v", err)
	}
	got := utRows(t, c)
	if got["y"].Name != "a-y2" || got["w"].TenantID != "ta" || got["x"] != before["x"] {
		t.Errorf("UpsertBatch of ta's keys: %+v", got)
	}
}

// The guarded update branch, per dialect, as the statement renders it.
func TestTenantGuardedUpsertShapes(t *testing.T) {
	type tc struct {
		dialect Dialect
		want    string
		merge   bool
	}
	for _, tc := range []tc{
		{PostgreSQL(), ` ON CONFLICT ("code") DO UPDATE SET "name" = excluded."name" WHERE "ut_rows"."tenant_id" = $4`, false},
		{SQLite(), ` ON CONFLICT ("code") DO UPDATE SET "name" = excluded."name" WHERE "ut_rows"."tenant_id" = ?`, false},
		{MySQL(), " ON DUPLICATE KEY UPDATE `name` = IF(`tenant_id` = ?, VALUES(`name`), `name`)", false},
		{MariaDB(), " ON DUPLICATE KEY UPDATE `name` = IF(`tenant_id` = ?, VALUES(`name`), `name`)", false},
		{MSSQL(), "WHEN MATCHED AND target.[tenant_id] = @p5 THEN UPDATE SET target.[name] = src.[name]", true},
		{Oracle(), `WHEN MATCHED THEN UPDATE SET target."NAME" = src."NAME" WHERE target."TENANT_ID" = :5`, true},
	} {
		t.Run(tc.dialect.Name(), func(t *testing.T) {
			c, err := New("sqlite", "file:qk43_shape_"+tc.dialect.Name()+"?mode=memory&cache=shared",
				WithDialect(tc.dialect), WithLogger(slog.New(slog.NewTextHandler(io.Discard, nil))))
			if err != nil {
				t.Fatalf("open: %v", err)
			}
			defer c.Close()
			router, ta := wgRouter(c)
			q := For[utRow](ta, router)
			if err := q.checkTenantUpsert(); err != nil {
				t.Fatalf("checkTenantUpsert: %v", err)
			}
			var got string
			if tc.merge {
				e := utRow{ID: 1, Code: "x", Name: "n"}
				sqlStr, args, hasUpdate, err := q.buildMerge(reflect.ValueOf(&e).Elem(), []string{"code"}, []string{"name"})
				if err != nil || !hasUpdate {
					t.Fatalf("buildMerge: %v (update branch %v)", err, hasUpdate)
				}
				if last := args[len(args)-1]; last != "ta" {
					t.Errorf("the last bind is %v, want the tenant", last)
				}
				got = sqlStr
			} else {
				clause, args, hasUpdate, err := q.guardedConflictClause([]string{"code"}, []string{"name"}, 4)
				if err != nil || !hasUpdate || !reflect.DeepEqual(args, []any{"ta"}) {
					t.Fatalf("guardedConflictClause: %v, update branch %v, args %v", err, hasUpdate, args)
				}
				got = clause
			}
			if !strings.Contains(got, tc.want) {
				t.Errorf("statement:\n got %s\nwant … %s …", got, tc.want)
			}
		})
	}
}

// acmeDialect is a dialect quark has no guarded upsert form for.
type acmeDialect struct{ Dialect }

func (acmeDialect) Name() string { return "acme" }

// A dialect quark cannot guard refuses the upsert under the tenant rather
// than run it unguarded; so does a model without the tenant column.
func TestUpsertUnderTenantRefusesWhatItCannotConfine(t *testing.T) {
	c, err := New("sqlite", "file:qk43_acme?mode=memory&cache=shared", WithDialect(acmeDialect{SQLite()}),
		WithLogger(slog.New(slog.NewTextHandler(io.Discard, nil))))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer c.Close()
	router, ta := wgRouter(c)
	if err := For[utRow](ta, router).Upsert(&utRow{Code: "x"}, []string{"code"}, []string{"name"}); !errors.Is(err, ErrUnsupportedFeature) {
		t.Errorf("Upsert under the tenant on dialect acme = %v, want ErrUnsupportedFeature", err)
	}
	if err := For[utRow](ta, router).UpsertBatch([]*utRow{{Code: "x"}}, []string{"code"}, []string{"name"}); !errors.Is(err, ErrUnsupportedFeature) {
		t.Errorf("UpsertBatch under the tenant on dialect acme = %v, want ErrUnsupportedFeature", err)
	}

	plain, _ := wwClient(t, "qk43_no_tenant_col")
	prouter, pta := wgRouter(plain)
	if err := For[wwRow](pta, prouter).Upsert(&wwRow{ID: 1, Name: "n"}, []string{"id"}, []string{"name"}); !errors.Is(err, ErrInvalidModel) {
		t.Errorf("Upsert under the tenant of a model without the tenant column = %v, want ErrInvalidModel", err)
	}
}

// QK-44: a map that names the tenant column writes the resolved tenant.
func TestUpdateMapUnderTenantKeepsTheTenantColumn(t *testing.T) {
	var logs strings.Builder
	var mu sync.Mutex
	c, rec := utClient(t, "qk44_updatemap", WithLogger(slog.New(slog.NewTextHandler(&lockedWriter{w: &logs, mu: &mu}, nil))))
	router, ta := wgRouter(c)

	data := map[string]any{"tenant_id": "tb", "name": "moved"}
	n, err := For[utRow](ta, router).Where("code", "=", "y").UpdateMap(data)
	if err != nil || n != 1 {
		t.Fatalf("UpdateMap = (%d, %v), want (1, nil)", n, err)
	}
	if got := utRows(t, c)["y"]; got.TenantID != "ta" || got.Name != "moved" {
		t.Errorf("UpdateMap naming the tenant column left %+v; want tenant ta, name moved\n%v", got, rec.all)
	}
	if data["tenant_id"] != "tb" {
		t.Errorf("UpdateMap modified the caller's map: %v", data)
	}
	mu.Lock()
	out := logs.String()
	mu.Unlock()
	if !strings.Contains(out, "quark.tenant.foreign_value_replaced") {
		t.Errorf("a foreign tenant value in the map was replaced without a log line")
	}

	// Any spelling of the column, and an expression, are the column too.
	if _, err := For[utRow](ta, router).Where("code", "=", "y").UpdateMap(map[string]any{"TENANT_ID": Col("name")}); err != nil {
		t.Fatalf("UpdateMap with an expression: %v", err)
	}
	if got := utRows(t, c)["y"]; got.TenantID != "ta" {
		t.Errorf("UpdateMap with the tenant column as an expression stored %q", got.TenantID)
	}
}
