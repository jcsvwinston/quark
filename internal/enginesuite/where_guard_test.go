// Copyright 2026 jcsvwinston
// SPDX-License-Identifier: Apache-2.0

package enginesuite

import (
	"context"
	"errors"
	"reflect"
	"sort"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jcsvwinston/quark"
)

// testWhereGuards proves QK-40 and QK-41 on the engine this lane runs.
//
// QK-40: the writes that address a row by key — Update, Delete, HardDelete,
// DeleteBatch, UpdateBatch, Restore — AND the query's conditions with the
// key, so a row is written exactly when a SELECT with the same conditions
// returns it; a row of another tenant is left alone under
// RowLevelSecurityClient; and a versioned write tells a version conflict
// (ErrStaleEntity) from a row the conditions exclude (zero rows, no error).
// The engines differ in one place this has to survive: MySQL and MariaDB
// count CHANGED rows, not matched ones, so an UPDATE that rewrites the values
// a row already has reports zero there and one elsewhere. A guarded write
// that matched must still be reported as one — its After hook runs — on all
// six.
//
// QK-41: the soft-delete scope ANDs with the caller's whole expression, so an
// Or group does not bring a trashed row back, on every read path.
//
// QK-42: under RowLevelSecurityClient a read by key of another tenant's id is
// not found, and the tenant column of an insert or update holds the resolved
// tenant whatever the entity carried.
func testWhereGuards(ctx context.Context, t *testing.T, client *quark.Client) {
	t.Run("ByKeyWrites", func(t *testing.T) { testWhereGuardByKeyWrites(ctx, t, client) })
	t.Run("TenantScope", func(t *testing.T) { testWhereGuardTenantScope(ctx, t, client) })
	t.Run("Versioned", func(t *testing.T) { testWhereGuardVersioned(ctx, t, client) })
	t.Run("UnchangedRowUnderAGuard", func(t *testing.T) { testWhereGuardUnchangedRow(ctx, t, client) })
	t.Run("SoftDeleteOr", func(t *testing.T) { testWhereGuardSoftDeleteOr(ctx, t, client) })
}

// ---------------------------------------------------------------------------
// The property, per write path, over the WriteWhereParity catalogue
// ---------------------------------------------------------------------------

func testWhereGuardByKeyWrites(ctx context.Context, t *testing.T, client *quark.Client) {
	engine := client.Dialect().Name()
	dropTable(client, "ww_parity_rows")
	if err := client.Migrate(ctx, &wwpRow{}); err != nil {
		t.Fatalf("migrate on %s: %v", engine, err)
	}
	t.Cleanup(func() { dropTable(client, "ww_parity_rows") })

	reseed := func(t *testing.T) []wwpRow {
		t.Helper()
		if _, err := client.Raw().Exec("DELETE FROM " + client.Dialect().Quote("ww_parity_rows")); err != nil {
			t.Fatalf("clear on %s: %v", engine, err)
		}
		for _, r := range wwpFixture() {
			r := r
			if err := quark.For[wwpRow](ctx, client).Create(&r); err != nil {
				t.Fatalf("seed %s on %s: %v", r.Name, engine, err)
			}
		}
		return wwpReadAll(t, ctx, client)
	}
	names := func(rows []wwpRow, keep func(wwpRow) bool) []string {
		out := []string{}
		for _, r := range rows {
			if keep(r) {
				out = append(out, r.Name)
			}
		}
		sort.Strings(out)
		return out
	}
	hitsWritten := func(t *testing.T) []string {
		return names(wwpReadAll(t, ctx, client), func(r wwpRow) bool { return r.Hits == 1 })
	}
	removed := func(t *testing.T) []string {
		left := map[string]bool{}
		for _, r := range wwpReadAll(t, ctx, client) {
			left[r.Name] = true
		}
		return names(wwpFixture(), func(r wwpRow) bool { return !left[r.Name] })
	}

	type path struct {
		name    string
		run     func(t *testing.T, q func() *quark.Query[wwpRow], rows []wwpRow)
		touched func(t *testing.T) []string
	}
	perRow := func(write func(q *quark.Query[wwpRow], e *wwpRow) (int64, error)) func(*testing.T, func() *quark.Query[wwpRow], []wwpRow) {
		return func(t *testing.T, q func() *quark.Query[wwpRow], rows []wwpRow) {
			t.Helper()
			for _, r := range rows {
				e := r
				e.Hits = 1
				if _, err := write(q(), &e); err != nil {
					t.Fatalf("%s on %s: %v", r.Name, engine, err)
				}
			}
		}
	}
	paths := []path{
		{"Update", perRow(func(q *quark.Query[wwpRow], e *wwpRow) (int64, error) { return q.Update(e) }), hitsWritten},
		{"Delete", perRow(func(q *quark.Query[wwpRow], e *wwpRow) (int64, error) { return q.Delete(e) }), removed},
		{"HardDelete", perRow(func(q *quark.Query[wwpRow], e *wwpRow) (int64, error) { return q.HardDelete(e) }), removed},
		{"DeleteBatch", func(t *testing.T, q func() *quark.Query[wwpRow], rows []wwpRow) {
			t.Helper()
			ids := []any{}
			for _, r := range rows {
				ids = append(ids, r.ID)
			}
			if _, err := q().DeleteBatch(ids); err != nil {
				t.Fatalf("DeleteBatch on %s: %v", engine, err)
			}
		}, removed},
		{"UpdateBatch", func(t *testing.T, q func() *quark.Query[wwpRow], rows []wwpRow) {
			t.Helper()
			var all []*wwpRow
			for _, r := range rows {
				e := r
				e.Hits = 1
				all = append(all, &e)
			}
			if err := q().UpdateBatch(all); err != nil {
				t.Fatalf("UpdateBatch on %s: %v", engine, err)
			}
		}, hitsWritten},
	}

	for _, p := range paths {
		for _, tc := range wwpCases() {
			t.Run(p.name+"/"+tc.name, func(t *testing.T) {
				rows := reseed(t)
				listed, err := tc.apply(quark.For[wwpRow](ctx, client)).Limit(100).List()
				if err != nil {
					t.Fatalf("select on %s: %v", engine, err)
				}
				want := names(listed, func(wwpRow) bool { return true })
				if pred := names(wwpFixture(), tc.want); !reflect.DeepEqual(want, pred) {
					t.Fatalf("SELECT on %s returned %v, the predicate names %v", engine, want, pred)
				}
				p.run(t, func() *quark.Query[wwpRow] { return tc.apply(quark.For[wwpRow](ctx, client)) }, rows)
				if got := p.touched(t); !reflect.DeepEqual(got, want) {
					t.Errorf("%s on %s touched %v, the SELECT returned %v", p.name, engine, got, want)
				}
			})
		}
	}
}

// ---------------------------------------------------------------------------
// The tenant scope, per write path
// ---------------------------------------------------------------------------

type wgeTenantRow struct {
	ID        int64      `db:"id" pk:"true"`
	TenantID  string     `db:"tenant_id"`
	Label     string     `db:"label"`
	Name      string     `db:"name"`
	Hits      int64      `db:"hits"`
	Weight    int64      `db:"weight"`
	DeletedAt *time.Time `db:"deleted_at"`
}

func (wgeTenantRow) TableName() string { return "wg_tenant_rows" }

type wgeTenantKey struct{}

// wgeSeed creates ta-a, ta-b, tb-a, tb-b — weights 1, 2, 4, 8, so a sum names
// the rows it added — trashes the two b rows, and returns the id of each
// label.
func wgeSeed(ctx context.Context, t *testing.T, client *quark.Client) map[string]int64 {
	t.Helper()
	engine := client.Dialect().Name()
	if _, err := client.Raw().Exec("DELETE FROM " + client.Dialect().Quote("wg_tenant_rows")); err != nil {
		t.Fatalf("clear on %s: %v", engine, err)
	}
	ids := map[string]int64{}
	for _, r := range []wgeTenantRow{
		{TenantID: "ta", Label: "ta-a", Name: "a", Weight: 1},
		{TenantID: "ta", Label: "ta-b", Name: "b", Weight: 2},
		{TenantID: "tb", Label: "tb-a", Name: "a", Weight: 4},
		{TenantID: "tb", Label: "tb-b", Name: "b", Weight: 8},
	} {
		r := r
		if err := quark.For[wgeTenantRow](ctx, client).Create(&r); err != nil {
			t.Fatalf("seed %s on %s: %v", r.Label, engine, err)
		}
		ids[r.Label] = r.ID
		if r.Name == "b" {
			if _, err := quark.For[wgeTenantRow](ctx, client).Delete(&r); err != nil {
				t.Fatalf("trash %s on %s: %v", r.Label, engine, err)
			}
		}
	}
	return ids
}

func wgeRows(ctx context.Context, t *testing.T, client *quark.Client) map[string]wgeTenantRow {
	t.Helper()
	rows, err := quark.For[wgeTenantRow](ctx, client).WithTrashed().Limit(100).List()
	if err != nil {
		t.Fatalf("read back: %v", err)
	}
	out := map[string]wgeTenantRow{}
	for _, r := range rows {
		out[r.Label] = r
	}
	return out
}

func testWhereGuardTenantScope(ctx context.Context, t *testing.T, client *quark.Client) {
	engine := client.Dialect().Name()
	dropTable(client, "wg_tenant_rows")
	if err := client.Migrate(ctx, &wgeTenantRow{}); err != nil {
		t.Fatalf("migrate on %s: %v", engine, err)
	}
	t.Cleanup(func() { dropTable(client, "wg_tenant_rows") })

	cfg := quark.DefaultTenantConfig()
	cfg.Strategy = quark.RowLevelSecurityClient
	cfg.BaseClient = client
	router := quark.NewTenantRouter(cfg, func(c context.Context) string {
		tenant, _ := c.Value(wgeTenantKey{}).(string)
		return tenant
	}, nil)
	ta := context.WithValue(ctx, wgeTenantKey{}, "ta")

	writes := []struct {
		name  string
		write func(q *quark.Query[wgeTenantRow], id int64) error
		done  func(r wgeTenantRow, ok bool) bool
		live  bool // the write addresses a live row (a) rather than a trashed one (b)
	}{
		{"Update", func(q *quark.Query[wgeTenantRow], id int64) error {
			_, err := q.Update(&wgeTenantRow{ID: id, Hits: 1})
			return err
		}, func(r wgeTenantRow, ok bool) bool { return ok && r.Hits == 1 }, true},
		{"UpdateFields", func(q *quark.Query[wgeTenantRow], id int64) error {
			_, err := q.UpdateFields(&wgeTenantRow{ID: id, Hits: 1}, "hits")
			return err
		}, func(r wgeTenantRow, ok bool) bool { return ok && r.Hits == 1 }, true},
		{"UpdateBatch", func(q *quark.Query[wgeTenantRow], id int64) error {
			return q.UpdateBatch([]*wgeTenantRow{{ID: id, Hits: 1}})
		}, func(r wgeTenantRow, ok bool) bool { return ok && r.Hits == 1 }, true},
		{"Delete", func(q *quark.Query[wgeTenantRow], id int64) error {
			_, err := q.Delete(&wgeTenantRow{ID: id})
			return err
		}, func(r wgeTenantRow, ok bool) bool { return ok && r.DeletedAt != nil }, true},
		{"HardDelete", func(q *quark.Query[wgeTenantRow], id int64) error {
			_, err := q.HardDelete(&wgeTenantRow{ID: id})
			return err
		}, func(_ wgeTenantRow, ok bool) bool { return !ok }, true},
		{"DeleteBatch", func(q *quark.Query[wgeTenantRow], id int64) error {
			_, err := q.DeleteBatch([]any{id})
			return err
		}, func(_ wgeTenantRow, ok bool) bool { return !ok }, true},
		{"Restore", func(q *quark.Query[wgeTenantRow], id int64) error {
			_, err := q.Restore(&wgeTenantRow{ID: id})
			return err
		}, func(r wgeTenantRow, ok bool) bool { return ok && r.DeletedAt == nil }, false},
	}
	for _, w := range writes {
		t.Run(w.name, func(t *testing.T) {
			ids := wgeSeed(ctx, t, client)
			own, foreign := "ta-a", "tb-a"
			if !w.live {
				own, foreign = "ta-b", "tb-b"
			}
			before := wgeRows(ctx, t, client)[foreign]
			if err := w.write(quark.For[wgeTenantRow](ta, router), ids[foreign]); err != nil {
				t.Fatalf("%s of tb's row under ta on %s: %v", w.name, engine, err)
			}
			after, ok := wgeRows(ctx, t, client)[foreign]
			if !ok || after.Hits != before.Hits || (after.DeletedAt == nil) != (before.DeletedAt == nil) {
				t.Errorf("tenant ta's %s changed tb's row on %s: before %+v, after %+v (present=%v)", w.name, engine, before, after, ok)
			}
			if err := w.write(quark.For[wgeTenantRow](ta, router), ids[own]); err != nil {
				t.Fatalf("%s of ta's own row on %s: %v", w.name, engine, err)
			}
			if mine, ok := wgeRows(ctx, t, client)[own]; !w.done(mine, ok) {
				t.Errorf("tenant ta's %s did not write its own row on %s: %+v (present=%v)", w.name, engine, mine, ok)
			}
		})
	}

	// QK-42: a read by key of another tenant's id is not found; of its own,
	// found. Track().Find goes through the same door.
	t.Run("Find", func(t *testing.T) {
		ids := wgeSeed(ctx, t, client)
		if got, err := quark.For[wgeTenantRow](ta, router).Find(ids["tb-a"]); !errors.Is(err, quark.ErrNotFound) {
			t.Fatalf("Find of tb's row under ta on %s = (%+v, %v), want ErrNotFound", engine, got, err)
		}
		if _, err := quark.For[wgeTenantRow](ta, router).Track().Find(ids["tb-a"]); !errors.Is(err, quark.ErrNotFound) {
			t.Errorf("Track().Find of tb's row under ta on %s = %v, want ErrNotFound", engine, err)
		}
		if _, err := quark.For[wgeTenantRow](ta, router).WithTrashed().Find(ids["tb-b"]); !errors.Is(err, quark.ErrNotFound) {
			t.Errorf("WithTrashed().Find of tb's trashed row under ta on %s = %v, want ErrNotFound", engine, err)
		}
		mine, err := quark.For[wgeTenantRow](ta, router).Find(ids["ta-a"])
		if err != nil || mine.Label != "ta-a" {
			t.Fatalf("Find of ta's own row on %s = (%+v, %v)", engine, mine, err)
		}
	})

	// QK-42: the tenant column holds the resolved tenant on every insert and
	// update by entity, whatever the entity carried.
	t.Run("TenantColumnIsTheResolvedTenant", func(t *testing.T) {
		ids := wgeSeed(ctx, t, client)
		planted := wgeTenantRow{TenantID: "tb", Label: "planted", Name: "p"}
		if err := quark.For[wgeTenantRow](ta, router).Create(&planted); err != nil {
			t.Fatalf("Create on %s: %v", engine, err)
		}
		if got := wgeRows(ctx, t, client)["planted"].TenantID; got != "ta" {
			t.Errorf("Create under ta of an entity carrying tb stored %q on %s, want ta", got, engine)
		}
		moved := wgeTenantRow{ID: ids["ta-a"], TenantID: "tb", Label: "ta-a", Name: "a", Hits: 2}
		if n, err := quark.For[wgeTenantRow](ta, router).Update(&moved); err != nil || n != 1 {
			t.Fatalf("Update of ta's row carrying tb on %s = (%d, %v), want (1, nil)", engine, n, err)
		}
		if got := wgeRows(ctx, t, client)["ta-a"].TenantID; got != "ta" {
			t.Errorf("Update under ta moved ta's row to %q on %s", got, engine)
		}
	})
}

// ---------------------------------------------------------------------------
// Optimistic locking
// ---------------------------------------------------------------------------

type wgeVersioned struct {
	ID      int64  `db:"id" pk:"true"`
	Owner   string `db:"owner"`
	Name    string `db:"name"`
	Version int64  `db:"version" quark:"version"`
}

func (wgeVersioned) TableName() string { return "wg_versioned" }

func testWhereGuardVersioned(ctx context.Context, t *testing.T, client *quark.Client) {
	engine := client.Dialect().Name()
	dropTable(client, "wg_versioned")
	if err := client.Migrate(ctx, &wgeVersioned{}); err != nil {
		t.Fatalf("migrate on %s: %v", engine, err)
	}
	t.Cleanup(func() { dropTable(client, "wg_versioned") })

	writes := []struct {
		name  string
		write func(q *quark.Query[wgeVersioned], e *wgeVersioned) (int64, error)
	}{
		{"Update", func(q *quark.Query[wgeVersioned], e *wgeVersioned) (int64, error) { return q.Update(e) }},
		{"UpdateFields", func(q *quark.Query[wgeVersioned], e *wgeVersioned) (int64, error) { return q.UpdateFields(e, "name") }},
	}
	for _, w := range writes {
		t.Run(w.name, func(t *testing.T) {
			if _, err := client.Raw().Exec("DELETE FROM " + client.Dialect().Quote("wg_versioned")); err != nil {
				t.Fatalf("clear on %s: %v", engine, err)
			}
			seed := wgeVersioned{Owner: "alice", Name: "a", Version: 1}
			if err := quark.For[wgeVersioned](ctx, client).Create(&seed); err != nil {
				t.Fatalf("seed on %s: %v", engine, err)
			}
			id := seed.ID

			e := wgeVersioned{ID: id, Name: "x", Version: 1}
			if n, err := w.write(quark.For[wgeVersioned](ctx, client).Where("owner", "=", "bob"), &e); err != nil || n != 0 {
				t.Fatalf("excluded %s on %s = (%d, %v), want (0, nil) — not ErrStaleEntity", w.name, engine, n, err)
			}
			e = wgeVersioned{ID: id, Name: "x", Version: 7}
			if _, err := w.write(quark.For[wgeVersioned](ctx, client).Where("owner", "=", "alice"), &e); !errors.Is(err, quark.ErrStaleEntity) {
				t.Fatalf("admitted stale %s on %s = %v, want ErrStaleEntity", w.name, engine, err)
			}
			e = wgeVersioned{ID: id, Name: "x", Version: 1}
			if n, err := w.write(quark.For[wgeVersioned](ctx, client).Where("owner", "=", "alice"), &e); err != nil || n != 1 || e.Version != 2 {
				t.Fatalf("admitted %s on %s = (%d, %v), version %d; want (1, nil), version 2", w.name, engine, n, err, e.Version)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// MySQL and MariaDB count changed rows
// ---------------------------------------------------------------------------

var wgeAfterUpdates atomic.Int64

type wgeHooked struct {
	ID    int64  `db:"id" pk:"true"`
	Owner string `db:"owner"`
	Name  string `db:"name"`
}

func (wgeHooked) TableName() string { return "wg_hooked" }

func (*wgeHooked) AfterUpdate(context.Context) error { wgeAfterUpdates.Add(1); return nil }

// An Update under a guard the row passes, writing the values the row already
// has, matched the row: it is not an exclusion, on any engine. MySQL and
// MariaDB report zero rows affected for it; the After hook still runs.
func testWhereGuardUnchangedRow(ctx context.Context, t *testing.T, client *quark.Client) {
	engine := client.Dialect().Name()
	dropTable(client, "wg_hooked")
	if err := client.Migrate(ctx, &wgeHooked{}); err != nil {
		t.Fatalf("migrate on %s: %v", engine, err)
	}
	t.Cleanup(func() { dropTable(client, "wg_hooked") })
	seed := wgeHooked{Owner: "alice", Name: "a"}
	if err := quark.For[wgeHooked](ctx, client).Create(&seed); err != nil {
		t.Fatalf("seed on %s: %v", engine, err)
	}

	before := wgeAfterUpdates.Load()
	same := wgeHooked{ID: seed.ID, Owner: "alice", Name: "a"}
	n, err := quark.For[wgeHooked](ctx, client).Where("owner", "=", "alice").Update(&same)
	if err != nil || n > 1 {
		t.Fatalf("unchanged Update under an admitting guard on %s = (%d, %v)", engine, n, err)
	}
	if got := wgeAfterUpdates.Load() - before; got != 1 {
		t.Errorf("unchanged Update under an admitting guard on %s ran AfterUpdate %d times, want 1 (rows affected %d)", engine, got, n)
	}

	before = wgeAfterUpdates.Load()
	n, err = quark.For[wgeHooked](ctx, client).Where("owner", "=", "bob").Update(&same)
	if err != nil || n != 0 {
		t.Fatalf("Update under an excluding guard on %s = (%d, %v), want (0, nil)", engine, n, err)
	}
	if got := wgeAfterUpdates.Load() - before; got != 0 {
		t.Errorf("Update under an excluding guard on %s ran AfterUpdate %d times, want 0", engine, got)
	}
}

// ---------------------------------------------------------------------------
// QK-41: the soft-delete scope across an Or group, per read path
// ---------------------------------------------------------------------------

func testWhereGuardSoftDeleteOr(ctx context.Context, t *testing.T, client *quark.Client) {
	engine := client.Dialect().Name()
	dropTable(client, "wg_tenant_rows")
	if err := client.Migrate(ctx, &wgeTenantRow{}); err != nil {
		t.Fatalf("migrate on %s: %v", engine, err)
	}
	t.Cleanup(func() { dropTable(client, "wg_tenant_rows") })
	wgeSeed(ctx, t, client) // ta-b and tb-b are in the trash

	// Tenant ta's a OR tenant ta's b: b is trashed.
	or := func(q *quark.Query[wgeTenantRow]) *quark.Query[wgeTenantRow] {
		return q.Where("tenant_id", "=", "ta").Where("name", "=", "a").
			Or(func(g *quark.Query[wgeTenantRow]) *quark.Query[wgeTenantRow] {
				return g.Where("tenant_id", "=", "ta").Where("name", "=", "b")
			})
	}
	labels := func(rows []wgeTenantRow) []string {
		out := []string{}
		for _, r := range rows {
			out = append(out, r.Label)
		}
		sort.Strings(out)
		return out
	}
	scopes := []struct {
		name string
		q    func() *quark.Query[wgeTenantRow]
		want []string
		sum  float64
	}{
		{"default", func() *quark.Query[wgeTenantRow] { return quark.For[wgeTenantRow](ctx, client) }, []string{"ta-a"}, 1},
		{"WithTrashed", func() *quark.Query[wgeTenantRow] { return quark.For[wgeTenantRow](ctx, client).WithTrashed() }, []string{"ta-a", "ta-b"}, 3},
		{"OnlyTrashed", func() *quark.Query[wgeTenantRow] { return quark.For[wgeTenantRow](ctx, client).OnlyTrashed() }, []string{"ta-b"}, 2},
	}
	for _, s := range scopes {
		t.Run("List/"+s.name, func(t *testing.T) {
			rows, err := or(s.q()).Limit(10).List()
			if err != nil {
				t.Fatalf("List on %s: %v", engine, err)
			}
			if got := labels(rows); !reflect.DeepEqual(got, s.want) {
				t.Errorf("List under %s on %s = %v, want %v", s.name, engine, got, s.want)
			}
		})
		t.Run("Count/"+s.name, func(t *testing.T) {
			n, err := or(s.q()).Count()
			if err != nil {
				t.Fatalf("Count on %s: %v", engine, err)
			}
			if n != int64(len(s.want)) {
				t.Errorf("Count under %s on %s = %d, want %d", s.name, engine, n, len(s.want))
			}
		})
		t.Run("Sum/"+s.name, func(t *testing.T) {
			sum, err := or(s.q()).Sum("weight")
			if err != nil {
				t.Fatalf("Sum on %s: %v", engine, err)
			}
			if sum != s.sum {
				t.Errorf("Sum(weight) under %s on %s = %v, want %v", s.name, engine, sum, s.sum)
			}
		})
		t.Run("Paginate/"+s.name, func(t *testing.T) {
			total, err := or(s.q()).Paginate(10, 0)
			if err != nil {
				t.Fatalf("Paginate on %s: %v", engine, err)
			}
			if got := labels(total.Items); !reflect.DeepEqual(got, s.want) || total.Total != int64(len(s.want)) {
				t.Errorf("Paginate under %s on %s = %v (total %d), want %v", s.name, engine, got, total.Total, s.want)
			}
		})
	}
}
