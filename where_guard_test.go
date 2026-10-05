// Copyright 2026 jcsvwinston
// SPDX-License-Identifier: Apache-2.0

package quark

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"reflect"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	_ "modernc.org/sqlite"
)

// QK-40. The writes that address a row by key — Update, UpdateBatch, Delete,
// HardDelete, DeleteBatch, Restore — wrote by the key alone and ignored the
// query's conditions, the tenant predicate of RowLevelSecurityClient among
// them. The tests below hold them to the property write_where_test.go holds
// UpdateFields to: a row is written exactly when a SELECT with the same
// conditions returns it. When nothing is written, nothing is reported: no
// After hook, no audit row, no event — and a version conflict is still
// ErrStaleEntity, but only for a row that passes the conditions.
//
// QK-42. Under RowLevelSecurityClient, Find(id) replaced the query's
// conditions with the key and read another tenant's row; Create stored a
// tenant id the entity carried, and Update wrote it into the tenant column.
// Find now ANDs the key with the conditions, and inserts and updates write
// the resolved tenant.
//
// QK-41. The scopes Quark adds — the soft-delete filter on reads, the tenant
// predicate — were prepended to the caller's conditions, and an Or group at
// the top level escaped them: trashed rows came back through it. Every scope
// now ANDs with the caller's expression as a whole.
//
// internal/enginesuite (WhereGuards) runs the same properties on the six
// engines.

// ---------------------------------------------------------------------------
// QK-40 — the property, per write path
// ---------------------------------------------------------------------------

// wgKeyWrite is a write path that addresses rows by key. run sends every
// fixture row through it on a query built by q, and returns the count each
// call reported for each id (nil for a path that reports one count for the
// whole call); touched reads back which rows it changed.
type wgKeyWrite struct {
	name    string
	run     func(t *testing.T, q func() *Query[wwRow]) map[int64]int64
	touched func(t *testing.T, c *Client) []int64
}

func wgHitsWritten(t *testing.T, c *Client) []int64 {
	return wwIDs(wwAll(t, c), func(r wwRow) bool { return r.Hits == 1 })
}

func wgRemoved(t *testing.T, c *Client) []int64 {
	left := map[int64]bool{}
	for _, r := range wwAll(t, c) {
		left[r.ID] = true
	}
	return wwIDs(wwFixture(), func(r wwRow) bool { return !left[r.ID] })
}

func wgKeyWrites() []wgKeyWrite {
	perRow := func(write func(q *Query[wwRow], e *wwRow) (int64, error)) func(*testing.T, func() *Query[wwRow]) map[int64]int64 {
		return func(t *testing.T, q func() *Query[wwRow]) map[int64]int64 {
			t.Helper()
			got := map[int64]int64{}
			for _, r := range wwFixture() {
				e := r
				e.Hits = 1
				n, err := write(q(), &e)
				if err != nil {
					t.Fatalf("row %d: %v", r.ID, err)
				}
				got[r.ID] = n
			}
			return got
		}
	}
	return []wgKeyWrite{
		{"Update", perRow(func(q *Query[wwRow], e *wwRow) (int64, error) { return q.Update(e) }), wgHitsWritten},
		{"UpdateFields", perRow(func(q *Query[wwRow], e *wwRow) (int64, error) { return q.UpdateFields(e, "hits") }), wgHitsWritten},
		{"Delete", perRow(func(q *Query[wwRow], e *wwRow) (int64, error) { return q.Delete(e) }), wgRemoved},
		{"HardDelete", perRow(func(q *Query[wwRow], e *wwRow) (int64, error) { return q.HardDelete(e) }), wgRemoved},
		{"DeleteBatch", func(t *testing.T, q func() *Query[wwRow]) map[int64]int64 {
			t.Helper()
			ids := []any{}
			for _, r := range wwFixture() {
				ids = append(ids, r.ID)
			}
			if _, err := q().DeleteBatch(ids); err != nil {
				t.Fatalf("DeleteBatch: %v", err)
			}
			return nil
		}, wgRemoved},
		{"UpdateBatch", func(t *testing.T, q func() *Query[wwRow]) map[int64]int64 {
			t.Helper()
			var all []*wwRow
			for _, r := range wwFixture() {
				e := r
				e.Hits = 1
				all = append(all, &e)
			}
			if err := q().UpdateBatch(all); err != nil {
				t.Fatalf("UpdateBatch: %v", err)
			}
			return nil
		}, wgHitsWritten},
	}
}

// Each write by key touches a row exactly when the SELECT with the same
// conditions returns it, and a per-row path reports 1 for those rows and 0
// for the others.
func TestWritesByKeyTouchARowOnlyWhenListReturnsIt(t *testing.T) {
	for _, path := range wgKeyWrites() {
		for i, tc := range wwCases() {
			t.Run(path.name+"/"+tc.name, func(t *testing.T) {
				c, rec := wwClient(t, fmt.Sprintf("qk40_%s_%d", strings.ToLower(path.name), i))
				selected := wwSelected(t, c, tc)
				rec.reset()
				counts := path.run(t, func() *Query[wwRow] { return tc.apply(For[wwRow](context.Background(), c)) })
				if got := path.touched(t, c); !reflect.DeepEqual(got, selected) {
					t.Errorf("%s touched %v, the SELECT returned %v\n%v", path.name, got, selected, rec.all)
				}
				for id, n := range counts {
					want := int64(0)
					for _, s := range selected {
						if s == id {
							want = 1
						}
					}
					if n != want {
						t.Errorf("%s(%d) reported %d rows, want %d", path.name, id, n, want)
					}
				}
			})
		}
	}
}

// The reported case, by name: the guard the CRUD reference used to promise.
func TestUpdateWithAWhereGuardLeavesTheOtherRowAlone(t *testing.T) {
	c, rec := wwClient(t, "qk40_reported")
	ctx := context.Background()
	beta := wwFixture()[1] // status active
	beta.Name = "renamed"
	n, err := For[wwRow](ctx, c).Where("status", "=", "archived").Update(&beta)
	if err != nil || n != 0 {
		t.Fatalf(`Where("status", "=", "archived").Update(beta) = (%d, %v), want (0, nil)`, n, err)
	}
	got, err := For[wwRow](ctx, c).Find(beta.ID)
	if err != nil {
		t.Fatalf("read back: %v", err)
	}
	if got.Name != "beta" {
		t.Fatalf("the guard excluded the row and it was written anyway: name = %q\n%v", got.Name, rec.all)
	}
	if !rec.any(`WHERE "id" = ? AND ("status" = ?)`) {
		t.Errorf("the UPDATE does not AND the condition with the key: %v", rec.all)
	}
}

// ---------------------------------------------------------------------------
// QK-40 — nothing written, nothing reported
// ---------------------------------------------------------------------------

var (
	wgAfterUpdates atomic.Int64
	wgAfterDeletes atomic.Int64
)

type wgHooked struct {
	ID    int64  `db:"id" pk:"true"`
	Owner string `db:"owner"`
	Name  string `db:"name"`
}

func (wgHooked) TableName() string { return "wg_hooked" }

func (*wgHooked) AfterUpdate(context.Context) error { wgAfterUpdates.Add(1); return nil }
func (*wgHooked) AfterDelete(context.Context) error { wgAfterDeletes.Add(1); return nil }

type wgBus struct {
	mu    sync.Mutex
	kinds []string
}

func (b *wgBus) Publish(_ context.Context, ev Event) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.kinds = append(b.kinds, ev.Kind())
	return nil
}

func (b *wgBus) count() int {
	b.mu.Lock()
	defer b.mu.Unlock()
	return len(b.kinds)
}

func wgHookedClient(t *testing.T, name string) (*Client, *wgBus) {
	t.Helper()
	ctx := context.Background()
	c, err := New("sqlite", "file:"+name+"?mode=memory&cache=shared",
		WithLogger(slog.New(slog.NewTextHandler(io.Discard, nil))))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { _ = c.Close() })
	if err := c.Migrate(ctx, &wgHooked{}); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	for _, r := range []wgHooked{{ID: 1, Owner: "alice", Name: "a"}, {ID: 2, Owner: "bob", Name: "b"}} {
		r := r
		if err := For[wgHooked](ctx, c).Create(&r); err != nil {
			t.Fatalf("seed: %v", err)
		}
	}
	if err := c.EnableAuditLog(ctx, AuditConfig{}); err != nil {
		t.Fatalf("audit: %v", err)
	}
	bus := &wgBus{}
	c.UseEventBus(bus)
	return c, bus
}

func TestAWriteTheConditionsExcludeReportsNothing(t *testing.T) {
	ctx := context.Background()
	writes := []struct {
		name  string
		hooks *atomic.Int64
		write func(q *Query[wgHooked], e *wgHooked) (int64, error)
	}{
		{"Update", &wgAfterUpdates, func(q *Query[wgHooked], e *wgHooked) (int64, error) { return q.Update(e) }},
		{"UpdateFields", &wgAfterUpdates, func(q *Query[wgHooked], e *wgHooked) (int64, error) { return q.UpdateFields(e, "name") }},
		{"Delete", &wgAfterDeletes, func(q *Query[wgHooked], e *wgHooked) (int64, error) { return q.Delete(e) }},
		{"HardDelete", &wgAfterDeletes, func(q *Query[wgHooked], e *wgHooked) (int64, error) { return q.HardDelete(e) }},
	}
	for i, w := range writes {
		t.Run(w.name, func(t *testing.T) {
			c, bus := wgHookedClient(t, fmt.Sprintf("qk40_hooks_%d", i))
			audits := func() int64 {
				n, err := For[quarkAuditRow](ctx, c).Count()
				if err != nil {
					t.Fatalf("count audit rows: %v", err)
				}
				return n
			}

			// Row 1 is alice's; the guard asks for bob's.
			hooksBefore := w.hooks.Load()
			n, err := w.write(For[wgHooked](ctx, c).Where("owner", "=", "bob"), &wgHooked{ID: 1, Name: "x"})
			if err != nil || n != 0 {
				t.Fatalf("excluded %s = (%d, %v), want (0, nil)", w.name, n, err)
			}
			if got := w.hooks.Load() - hooksBefore; got != 0 {
				t.Errorf("excluded %s ran the After hook %d times", w.name, got)
			}
			if got := audits(); got != 0 {
				t.Errorf("excluded %s wrote %d audit rows", w.name, got)
			}
			if got := bus.count(); got != 0 {
				t.Errorf("excluded %s emitted %d events", w.name, got)
			}
			if row, err := For[wgHooked](ctx, c).Find(1); err != nil || row.Name != "a" {
				t.Errorf("excluded %s changed the row: %+v, %v", w.name, row, err)
			}

			// The same write with a guard the row passes reports it once.
			hooksBefore = w.hooks.Load()
			n, err = w.write(For[wgHooked](ctx, c).Where("owner", "=", "alice"), &wgHooked{ID: 1, Name: "x"})
			if err != nil || n != 1 {
				t.Fatalf("admitted %s = (%d, %v), want (1, nil)", w.name, n, err)
			}
			if got := w.hooks.Load() - hooksBefore; got != 1 {
				t.Errorf("admitted %s ran the After hook %d times, want 1", w.name, got)
			}
			if got := audits(); got != 1 {
				t.Errorf("admitted %s wrote %d audit rows, want 1", w.name, got)
			}
			if got := bus.count(); got != 1 {
				t.Errorf("admitted %s emitted %d events, want 1", w.name, got)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// QK-40 — optimistic locking
// ---------------------------------------------------------------------------

type wgVersioned struct {
	ID      int64  `db:"id" pk:"true"`
	Owner   string `db:"owner"`
	Name    string `db:"name"`
	Version int64  `db:"version" quark:"version"`
}

func (wgVersioned) TableName() string { return "wg_versioned" }

// ErrStaleEntity keeps meaning a version conflict: the row passes the
// conditions and its version moved. A row the conditions exclude is not a
// conflict; it is a write that did not happen.
func TestVersionedWritesTellAConflictFromAnExclusion(t *testing.T) {
	ctx := context.Background()
	writes := []struct {
		name  string
		write func(q *Query[wgVersioned], e *wgVersioned) (int64, error)
	}{
		{"Update", func(q *Query[wgVersioned], e *wgVersioned) (int64, error) { return q.Update(e) }},
		{"UpdateFields", func(q *Query[wgVersioned], e *wgVersioned) (int64, error) { return q.UpdateFields(e, "name") }},
	}
	for i, w := range writes {
		t.Run(w.name, func(t *testing.T) {
			c, err := New("sqlite", fmt.Sprintf("file:qk40_version_%d?mode=memory&cache=shared", i),
				WithLogger(slog.New(slog.NewTextHandler(io.Discard, nil))))
			if err != nil {
				t.Fatalf("open: %v", err)
			}
			defer c.Close()
			if err := c.Migrate(ctx, &wgVersioned{}); err != nil {
				t.Fatalf("migrate: %v", err)
			}
			if err := For[wgVersioned](ctx, c).Create(&wgVersioned{ID: 1, Owner: "alice", Name: "a", Version: 1}); err != nil {
				t.Fatalf("seed: %v", err)
			}

			// Excluded, at the current version: zero rows, no error.
			e := wgVersioned{ID: 1, Name: "x", Version: 1}
			if n, err := w.write(For[wgVersioned](ctx, c).Where("owner", "=", "bob"), &e); err != nil || n != 0 {
				t.Fatalf("excluded %s = (%d, %v), want (0, nil) — not ErrStaleEntity", w.name, n, err)
			}
			if e.Version != 1 {
				t.Errorf("excluded %s bumped the in-memory version to %d", w.name, e.Version)
			}

			// Excluded AND stale: still not a conflict the caller can see —
			// the row is not one the conditions let it address.
			e = wgVersioned{ID: 1, Name: "x", Version: 7}
			if n, err := w.write(For[wgVersioned](ctx, c).Where("owner", "=", "bob"), &e); err != nil || n != 0 {
				t.Fatalf("excluded stale %s = (%d, %v), want (0, nil)", w.name, n, err)
			}

			// Admitted but stale: the conflict.
			e = wgVersioned{ID: 1, Name: "x", Version: 7}
			if _, err := w.write(For[wgVersioned](ctx, c).Where("owner", "=", "alice"), &e); !errors.Is(err, ErrStaleEntity) {
				t.Fatalf("admitted stale %s = %v, want ErrStaleEntity", w.name, err)
			}

			// Admitted at the current version: written, version bumped.
			e = wgVersioned{ID: 1, Name: "x", Version: 1}
			if n, err := w.write(For[wgVersioned](ctx, c).Where("owner", "=", "alice"), &e); err != nil || n != 1 || e.Version != 2 {
				t.Fatalf("admitted %s = (%d, %v), version %d; want (1, nil), version 2", w.name, n, err, e.Version)
			}

			// Without conditions a missing row stays a conflict, as before.
			if _, err := w.write(For[wgVersioned](ctx, c), &wgVersioned{ID: 99, Name: "x", Version: 1}); !errors.Is(err, ErrStaleEntity) {
				t.Fatalf("unguarded %s of a missing row = %v, want ErrStaleEntity as before", w.name, err)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// QK-40 — associations
// ---------------------------------------------------------------------------

type wgAuthor struct {
	ID       int64  `db:"id" pk:"true"`
	TenantID string `db:"tenant_id"`
	Name     string `db:"name"`
}

func (wgAuthor) TableName() string { return "wg_authors" }

type wgComment struct {
	ID       int64  `db:"id" pk:"true"`
	TenantID string `db:"tenant_id"`
	PostID   int64  `db:"post_id"`
	Body     string `db:"body"`
}

func (wgComment) TableName() string { return "wg_comments" }

type wgPost struct {
	ID       int64       `db:"id" pk:"true"`
	TenantID string      `db:"tenant_id"`
	Status   string      `db:"status"`
	AuthorID int64       `db:"author_id"`
	Author   *wgAuthor   `rel:"belongs_to" join:"author_id"`
	Comments []wgComment `rel:"has_many" join:"post_id"`
}

func (wgPost) TableName() string { return "wg_posts" }

func wgAssocClient(t *testing.T, name string) *Client {
	t.Helper()
	ctx := context.Background()
	c, err := New("sqlite", "file:"+name+"?mode=memory&cache=shared",
		WithLogger(slog.New(slog.NewTextHandler(io.Discard, nil))))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { _ = c.Close() })
	if err := c.Migrate(ctx, &wgAuthor{}, &wgPost{}, &wgComment{}); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	for _, a := range []wgAuthor{{ID: 1, TenantID: "ta", Name: "ann"}, {ID: 2, TenantID: "tb", Name: "ben"}} {
		a := a
		if err := For[wgAuthor](ctx, c).Create(&a); err != nil {
			t.Fatalf("seed author: %v", err)
		}
	}
	for _, p := range []wgPost{{ID: 1, TenantID: "ta", Status: "published", AuthorID: 1}, {ID: 2, TenantID: "tb", Status: "draft", AuthorID: 2}} {
		p := p
		if err := For[wgPost](ctx, c).WithoutAssociations().Create(&p); err != nil {
			t.Fatalf("seed post: %v", err)
		}
	}
	for _, cm := range []wgComment{{ID: 1, TenantID: "ta", PostID: 1, Body: "first"}, {ID: 2, TenantID: "tb", PostID: 2, Body: "second"}} {
		cm := cm
		if err := For[wgComment](ctx, c).Create(&cm); err != nil {
			t.Fatalf("seed comment: %v", err)
		}
	}
	return c
}

// A belongs_to association is written before the entity's own row; when the
// conditions exclude that row, neither it nor its associations are written.
func TestUpdateExcludedByWhereWritesNoAssociation(t *testing.T) {
	ctx := context.Background()
	c := wgAssocClient(t, "qk40_assoc_where")
	post := wgPost{ID: 1, TenantID: "ta", Status: "published", AuthorID: 1,
		Author:   &wgAuthor{ID: 1, TenantID: "ta", Name: "renamed"},
		Comments: []wgComment{{ID: 1, TenantID: "ta", PostID: 1, Body: "edited"}},
	}
	n, err := For[wgPost](ctx, c).Where("status", "=", "draft").Update(&post)
	if err != nil || n != 0 {
		t.Fatalf(`Where("status", "=", "draft").Update(published post) = (%d, %v), want (0, nil)`, n, err)
	}
	if a, _ := For[wgAuthor](ctx, c).Find(1); a.Name != "ann" {
		t.Errorf("the belongs_to author was rewritten for an excluded post: %q", a.Name)
	}
	if cm, _ := For[wgComment](ctx, c).Find(1); cm.Body != "first" {
		t.Errorf("the has_many comment was rewritten for an excluded post: %q", cm.Body)
	}
}

// Under RowLevelSecurityClient an association is a write by key of its own,
// and it carries the tenant: a loaded association of another tenant is not
// rewritten when the entity itself is.
func TestUpdateUnderTenantLeavesAnotherTenantsAssociation(t *testing.T) {
	ctx := context.Background()
	c := wgAssocClient(t, "qk40_assoc_tenant")
	router, ta := wgRouter(c)
	post := wgPost{ID: 1, TenantID: "ta", Status: "edited", AuthorID: 2,
		Author:   &wgAuthor{ID: 2, TenantID: "tb", Name: "renamed-by-ta"},
		Comments: []wgComment{{ID: 2, TenantID: "tb", PostID: 1, Body: "edited-by-ta"}},
	}
	n, err := For[wgPost](ta, router).Update(&post)
	if err != nil || n != 1 {
		t.Fatalf("Update of ta's own post = (%d, %v), want (1, nil)", n, err)
	}
	if a, _ := For[wgAuthor](ctx, c).Find(2); a.Name != "ben" {
		t.Errorf("tenant ta rewrote tb's author through an association: %q", a.Name)
	}
	if cm, _ := For[wgComment](ctx, c).Find(2); cm.Body != "second" || cm.PostID != 2 {
		t.Errorf("tenant ta rewrote tb's comment through an association: %+v", cm)
	}
}

// ---------------------------------------------------------------------------
// QK-40 / QK-41 — the tenant scope, per write path
// ---------------------------------------------------------------------------

type wgTenantKey struct{}

func wgRouter(c *Client) (*TenantRouter, context.Context) {
	cfg := DefaultTenantConfig()
	cfg.Strategy = RowLevelSecurityClient
	cfg.BaseClient = c
	router := NewTenantRouter(cfg, func(ctx context.Context) string {
		tenant, _ := ctx.Value(wgTenantKey{}).(string)
		return tenant
	}, nil)
	return router, context.WithValue(context.Background(), wgTenantKey{}, "ta")
}

type wgTenantRow struct {
	ID        int64      `db:"id" pk:"true"`
	TenantID  string     `db:"tenant_id"`
	Name      string     `db:"name"`
	Hits      int64      `db:"hits"`
	DeletedAt *time.Time `db:"deleted_at"`
}

func (wgTenantRow) TableName() string { return "wg_tenant_rows" }

// wgTenantClient seeds ids 1-2 for ta and 3-4 for tb; 2 and 4 are in the
// trash. opts are added after the defaults, so a WithLogger there wins.
func wgTenantClient(t *testing.T, name string, opts ...any) (*Client, *txStatementRecorder) {
	t.Helper()
	ctx := context.Background()
	rec := &txStatementRecorder{}
	c, err := New("sqlite", "file:"+name+"?mode=memory&cache=shared",
		append([]any{WithLogger(slog.New(slog.NewTextHandler(io.Discard, nil))), WithQueryObserver(rec)}, opts...)...)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { _ = c.Close() })
	if err := c.Migrate(ctx, &wgTenantRow{}); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	now := time.Now().UTC()
	for _, r := range []wgTenantRow{
		{ID: 1, TenantID: "ta", Name: "a"},
		{ID: 2, TenantID: "ta", Name: "b", DeletedAt: &now},
		{ID: 3, TenantID: "tb", Name: "a"},
		{ID: 4, TenantID: "tb", Name: "b", DeletedAt: &now},
	} {
		r := r
		if err := For[wgTenantRow](ctx, c).Create(&r); err != nil {
			t.Fatalf("seed: %v", err)
		}
	}
	rec.reset()
	return c, rec
}

func wgTenantRows(t *testing.T, c *Client) map[int64]wgTenantRow {
	t.Helper()
	rows, err := For[wgTenantRow](context.Background(), c).WithTrashed().Limit(100).List()
	if err != nil {
		t.Fatalf("read back: %v", err)
	}
	out := map[int64]wgTenantRow{}
	for _, r := range rows {
		out[r.ID] = r
	}
	return out
}

// Tenant ta aims every write by key at tb's row and at its own: tb's row
// comes back exactly as it was, ta's is written.
func TestWritesByKeyUnderTenantWriteOnlyTheTenantsRow(t *testing.T) {
	writes := []struct {
		name  string
		write func(q *Query[wgTenantRow], id int64) error
		// done reports whether the row with that id shows the write.
		done func(r wgTenantRow, ok bool) bool
	}{
		{"Update", func(q *Query[wgTenantRow], id int64) error {
			_, err := q.Update(&wgTenantRow{ID: id, Hits: 1})
			return err
		}, func(r wgTenantRow, ok bool) bool { return ok && r.Hits == 1 }},
		{"UpdateFields", func(q *Query[wgTenantRow], id int64) error {
			_, err := q.UpdateFields(&wgTenantRow{ID: id, Hits: 1}, "hits")
			return err
		}, func(r wgTenantRow, ok bool) bool { return ok && r.Hits == 1 }},
		{"UpdateBatch", func(q *Query[wgTenantRow], id int64) error {
			return q.UpdateBatch([]*wgTenantRow{{ID: id, Hits: 1}})
		}, func(r wgTenantRow, ok bool) bool { return ok && r.Hits == 1 }},
		{"Delete", func(q *Query[wgTenantRow], id int64) error {
			_, err := q.Delete(&wgTenantRow{ID: id})
			return err
		}, func(r wgTenantRow, ok bool) bool { return ok && r.DeletedAt != nil }},
		{"HardDelete", func(q *Query[wgTenantRow], id int64) error {
			_, err := q.HardDelete(&wgTenantRow{ID: id})
			return err
		}, func(_ wgTenantRow, ok bool) bool { return !ok }},
		{"DeleteBatch", func(q *Query[wgTenantRow], id int64) error {
			_, err := q.DeleteBatch([]any{id})
			return err
		}, func(_ wgTenantRow, ok bool) bool { return !ok }},
		{"Restore", func(q *Query[wgTenantRow], id int64) error {
			_, err := q.Restore(&wgTenantRow{ID: id})
			return err
		}, func(r wgTenantRow, ok bool) bool { return ok && r.DeletedAt == nil }},
	}
	for i, w := range writes {
		t.Run(w.name, func(t *testing.T) {
			c, rec := wgTenantClient(t, fmt.Sprintf("qk40_tenant_%d", i))
			router, ta := wgRouter(c)
			// Restore addresses trashed rows: ta's 2 and tb's 4. The others
			// address live ones: ta's 1 and tb's 3.
			own, foreign := int64(1), int64(3)
			if w.name == "Restore" {
				own, foreign = 2, 4
			}
			before := wgTenantRows(t, c)[foreign]

			if err := w.write(For[wgTenantRow](ta, router), foreign); err != nil {
				t.Fatalf("%s of tb's row under ta: %v", w.name, err)
			}
			after, ok := wgTenantRows(t, c)[foreign]
			if !ok || !reflect.DeepEqual(wgStrip(after), wgStrip(before)) {
				t.Errorf("tenant ta's %s changed tb's row %d: before %+v, after %+v (present=%v)\n%v",
					w.name, foreign, before, after, ok, rec.all)
			}

			if err := w.write(For[wgTenantRow](ta, router), own); err != nil {
				t.Fatalf("%s of ta's own row: %v", w.name, err)
			}
			mine, ok := wgTenantRows(t, c)[own]
			if !w.done(mine, ok) {
				t.Errorf("tenant ta's %s did not write its own row %d: %+v (present=%v)", w.name, own, mine, ok)
			}
		})
	}
}

// wgStrip drops the deleted_at pointer identity so rows compare by value.
func wgStrip(r wgTenantRow) wgTenantRow {
	if r.DeletedAt != nil {
		v := r.DeletedAt.UTC().Truncate(time.Second)
		r.DeletedAt = &v
	}
	return r
}

// ---------------------------------------------------------------------------
// QK-41 — every scope ANDs with the caller's whole expression
// ---------------------------------------------------------------------------

// The statement shape, per scope: no scope renders the caller's conditions
// as they were; soft delete and the tenant wrap them.
func TestScopesWrapTheCallersExpression(t *testing.T) {
	ctx := context.Background()
	c, rec := wgTenantClient(t, "qk41_shape")
	router, ta := wgRouter(c)
	or := func(q *Query[wgTenantRow]) *Query[wgTenantRow] {
		return q.Where("name", "=", "a").Or(func(g *Query[wgTenantRow]) *Query[wgTenantRow] { return g.Where("name", "=", "b") })
	}
	for _, tc := range []struct {
		name string
		q    *Query[wgTenantRow]
		want string
	}{
		{"no scope", or(For[wgTenantRow](ctx, c).WithTrashed()), `WHERE "name" = ? OR ("name" = ?)`},
		{"soft delete", or(For[wgTenantRow](ctx, c)), `WHERE "deleted_at" IS NULL AND ("name" = ? OR ("name" = ?))`},
		{"only trashed", or(For[wgTenantRow](ctx, c).OnlyTrashed()), `WHERE "deleted_at" IS NOT NULL AND ("name" = ? OR ("name" = ?))`},
		{"tenant", or(For[wgTenantRow](ta, router).WithTrashed()), `WHERE "tenant_id" = ? AND ("name" = ? OR ("tenant_id" = ? AND "name" = ?))`},
		{"soft delete and tenant", or(For[wgTenantRow](ta, router)), `WHERE "deleted_at" IS NULL AND "tenant_id" = ? AND ("name" = ? OR ("tenant_id" = ? AND "name" = ?))`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rec.reset()
			if _, err := tc.q.Limit(10).List(); err != nil {
				t.Fatalf("list: %v", err)
			}
			// Whitespace is collapsed: IS NULL renders with a trailing space.
			got := strings.Join(strings.Fields(rec.read()), " ")
			if !strings.Contains(got, tc.want) {
				t.Errorf("statement:\n got %s\nwant … %s …", got, tc.want)
			}
		})
	}
}

// The reported case and its siblings: with row 2 (name b) in the trash, an Or
// group naming it does not bring it back, on any read path.
func TestSoftDeleteScopeHoldsAcrossAnOrGroup(t *testing.T) {
	ctx := context.Background()
	c, rec := wgTenantClient(t, "qk41_soft_delete")
	or := func(q *Query[wgTenantRow]) *Query[wgTenantRow] {
		return q.Where("tenant_id", "=", "ta").Where("name", "=", "a").
			Or(func(g *Query[wgTenantRow]) *Query[wgTenantRow] {
				return g.Where("tenant_id", "=", "ta").Where("name", "=", "b")
			})
	}
	ids := func(rows []wgTenantRow) []int64 {
		out := []int64{}
		for _, r := range rows {
			out = append(out, r.ID)
		}
		sort.Slice(out, func(i, j int) bool { return out[i] < out[j] })
		return out
	}
	reads := []struct {
		name string
		read func(q *Query[wgTenantRow]) ([]int64, error)
		// want for the default scope, WithTrashed and OnlyTrashed.
		live, all, trash []int64
	}{
		{"List", func(q *Query[wgTenantRow]) ([]int64, error) {
			rows, err := q.Limit(10).List()
			return ids(rows), err
		}, []int64{1}, []int64{1, 2}, []int64{2}},
		{"Count", func(q *Query[wgTenantRow]) ([]int64, error) {
			n, err := q.Count()
			return []int64{n}, err
		}, []int64{1}, []int64{2}, []int64{1}},
		{"Sum", func(q *Query[wgTenantRow]) ([]int64, error) {
			s, err := q.Sum("id")
			return []int64{int64(s)}, err
		}, []int64{1}, []int64{3}, []int64{2}},
		{"Cursor", func(q *Query[wgTenantRow]) ([]int64, error) {
			cur, err := q.Limit(10).Cursor()
			if err != nil {
				return nil, err
			}
			defer cur.Close()
			var rows []wgTenantRow
			for cur.Next() {
				var r wgTenantRow
				if err := cur.Scan(&r); err != nil {
					return nil, err
				}
				rows = append(rows, r)
			}
			return ids(rows), cur.Err()
		}, []int64{1}, []int64{1, 2}, []int64{2}},
		{"Paginate", func(q *Query[wgTenantRow]) ([]int64, error) {
			p, err := q.Paginate(10, 0)
			if err != nil {
				return nil, err
			}
			return append(ids(p.Items), p.Total), nil
		}, []int64{1, 1}, []int64{1, 2, 2}, []int64{2, 1}},
	}
	for _, r := range reads {
		for _, scope := range []struct {
			name string
			q    func() *Query[wgTenantRow]
			want []int64
		}{
			{"default", func() *Query[wgTenantRow] { return For[wgTenantRow](ctx, c) }, r.live},
			{"WithTrashed", func() *Query[wgTenantRow] { return For[wgTenantRow](ctx, c).WithTrashed() }, r.all},
			{"OnlyTrashed", func() *Query[wgTenantRow] { return For[wgTenantRow](ctx, c).OnlyTrashed() }, r.trash},
		} {
			t.Run(r.name+"/"+scope.name, func(t *testing.T) {
				rec.reset()
				got, err := r.read(or(scope.q()))
				if err != nil {
					t.Fatalf("%s: %v", r.name, err)
				}
				if !reflect.DeepEqual(got, scope.want) {
					t.Errorf("%s under %s = %v, want %v\n%v", r.name, scope.name, got, scope.want, rec.all)
				}
			})
		}
	}
}

// An Or with nothing before it means its group alone whatever the scopes are.
// Before, it was ORed with the scopes: under soft delete it returned every
// live row plus the trashed one it named, and under the tenant every row of
// the tenant.
func TestALeadingOrMeansItsGroup(t *testing.T) {
	ctx := context.Background()
	c, _ := wgTenantClient(t, "qk41_leading_or")
	router, ta := wgRouter(c)
	group := func(g *Query[wgTenantRow]) *Query[wgTenantRow] { return g.Where("name", "=", "b") }

	live, err := For[wgTenantRow](ctx, c).Or(group).Limit(10).List()
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(live) != 0 {
		t.Errorf("Or(name = b) under soft delete returned %+v; both b rows are in the trash", live)
	}

	mine, err := For[wgTenantRow](ta, router).WithTrashed().Or(group).Limit(10).List()
	if err != nil {
		t.Fatalf("list under ta: %v", err)
	}
	if len(mine) != 1 || mine[0].ID != 2 {
		t.Errorf("Or(name = b) under tenant ta returned %+v, want ta's b (id 2) alone", mine)
	}
}

// Writes: the tenant scope ANDs with an Or group the caller writes, and the
// soft delete a Delete adds does too.
func TestScopesHoldAcrossAnOrGroupOnWrites(t *testing.T) {
	ctx := context.Background()
	or := func(q *Query[wgTenantRow]) *Query[wgTenantRow] {
		return q.Where("name", "=", "a").Or(func(g *Query[wgTenantRow]) *Query[wgTenantRow] { return g.Where("name", "=", "b") })
	}
	t.Run("tenant/UpdateMap", func(t *testing.T) {
		c, rec := wgTenantClient(t, "qk41_write_updatemap")
		router, ta := wgRouter(c)
		n, err := or(For[wgTenantRow](ta, router)).UpdateMap(map[string]any{"hits": 1})
		if err != nil {
			t.Fatalf("UpdateMap: %v", err)
		}
		for id, r := range wgTenantRows(t, c) {
			if (r.Hits == 1) != (r.TenantID == "ta") {
				t.Errorf("UpdateMap under ta: row %d of %s has hits %d\n%v", id, r.TenantID, r.Hits, rec.all)
			}
		}
		if n != 2 {
			t.Errorf("UpdateMap under ta reported %d rows, want ta's 2", n)
		}
	})
	t.Run("tenant/DeleteBy", func(t *testing.T) {
		c, rec := wgTenantClient(t, "qk41_write_deleteby")
		router, ta := wgRouter(c)
		if _, err := or(For[wgTenantRow](ta, router)).DeleteBy(); err != nil {
			t.Fatalf("DeleteBy: %v", err)
		}
		for id, r := range wgTenantRows(t, c) {
			if r.TenantID == "ta" {
				t.Errorf("DeleteBy under ta left ta's row %d\n%v", id, rec.all)
			}
		}
		if got := len(wgTenantRows(t, c)); got != 2 {
			t.Errorf("DeleteBy under ta left %d rows, want tb's 2", got)
		}
	})
	t.Run("soft delete/Delete", func(t *testing.T) {
		c, rec := wgTenantClient(t, "qk41_write_softdelete")
		// Row 3 is live and named a: the Or admits it. Row 1 the same. The
		// Delete addresses row 3 only.
		n, err := or(For[wgTenantRow](ctx, c)).Delete(&wgTenantRow{ID: 3})
		if err != nil || n != 1 {
			t.Fatalf("Delete = (%d, %v), want (1, nil)\n%v", n, err, rec.all)
		}
		rows := wgTenantRows(t, c)
		if rows[3].DeletedAt == nil || rows[1].DeletedAt != nil {
			t.Errorf("Delete trashed the wrong rows: 1=%v 3=%v\n%v", rows[1].DeletedAt, rows[3].DeletedAt, rec.all)
		}
		if !rec.any(`AND "deleted_at" IS NULL AND ("name" = ? OR ("name" = ?))`) {
			t.Errorf("the soft delete does not AND its scope with the whole Or: %v", rec.all)
		}
	})
}

// ---------------------------------------------------------------------------
// QK-42 — reads by key, and the tenant column on writes
// ---------------------------------------------------------------------------

// Find of another tenant's id is ErrNotFound, from a statement bound to the
// caller's tenant; Find of its own id returns the row. Track().Find goes the
// same way.
func TestFindUnderTenantReadsOnlyTheTenantsRow(t *testing.T) {
	c, rec := wgTenantClient(t, "qk42_find")
	router, ta := wgRouter(c)

	if got, err := For[wgTenantRow](ta, router).Find(int64(3)); !errors.Is(err, ErrNotFound) {
		t.Fatalf("Find(3) under ta = (%+v, %v), want ErrNotFound: row 3 is tb's\n%v", got, err, rec.all)
	}
	if !strings.Contains(rec.read(), `"tenant_id" = ?`) || len(rec.args) == 0 || rec.args[0] != "ta" {
		t.Errorf("Find under ta does not bind the tenant: %s %v", rec.read(), rec.args)
	}
	own, err := For[wgTenantRow](ta, router).Find(int64(1))
	if err != nil || own.TenantID != "ta" {
		t.Fatalf("Find(1) under ta = (%+v, %v), want ta's row", own, err)
	}
	if _, err := For[wgTenantRow](ta, router).Track().Find(int64(3)); !errors.Is(err, ErrNotFound) {
		t.Errorf("Track().Find(3) under ta = %v, want ErrNotFound", err)
	}
	if _, err := For[wgTenantRow](ta, router).WithTrashed().Find(int64(4)); !errors.Is(err, ErrNotFound) {
		t.Errorf("WithTrashed().Find(4) under ta = %v, want ErrNotFound: row 4 is tb's", err)
	}
}

// Find ANDs the key with the caller's Where, as the writes by key do.
func TestFindHonoursTheCallersWhere(t *testing.T) {
	ctx := context.Background()
	c, rec := wwClient(t, "qk42_find_where")
	// Row 2 is active.
	if _, err := For[wwRow](ctx, c).Where("status", "=", "archived").Find(int64(2)); !errors.Is(err, ErrNotFound) {
		t.Fatalf(`Where("status", "=", "archived").Find(2) = %v, want ErrNotFound\n%v`, err, rec.all)
	}
	got, err := For[wwRow](ctx, c).Where("status", "=", "active").Find(int64(2))
	if err != nil || got.ID != 2 {
		t.Fatalf(`Where("status", "=", "active").Find(2) = (%+v, %v), want row 2`, got, err)
	}
}

// Find and First leave the query they were called on as it was: its
// conditions and its limit.
func TestFindAndFirstLeaveTheirQueryAlone(t *testing.T) {
	c, _ := wgTenantClient(t, "qk42_receiver")
	router, ta := wgRouter(c)

	q := For[wgTenantRow](ta, router).WithTrashed()
	_, _ = q.Find(int64(3))
	rows, err := q.List()
	if err != nil {
		t.Fatalf("list after Find: %v", err)
	}
	for _, r := range rows {
		if r.TenantID != "ta" {
			t.Errorf("the query Find was called on lists tb's row %d afterwards", r.ID)
		}
	}
	if len(rows) != 2 {
		t.Errorf("the query Find was called on lists %d rows afterwards, want ta's 2", len(rows))
	}

	all := For[wgTenantRow](context.Background(), c).WithTrashed()
	if _, err := all.First(); err != nil {
		t.Fatalf("First: %v", err)
	}
	if rows, err := all.List(); err != nil || len(rows) != 4 {
		t.Errorf("the query First was called on lists %d rows afterwards (%v), want 4", len(rows), err)
	}
}

// Under RowLevelSecurityClient the tenant column holds the resolved tenant on
// every insert and update by entity, whatever the entity carried, and a
// foreign value is logged rather than written.
func TestWritesUnderTenantStoreTheResolvedTenant(t *testing.T) {
	var logs strings.Builder
	var mu sync.Mutex
	logger := slog.New(slog.NewTextHandler(&lockedWriter{w: &logs, mu: &mu}, nil))
	c, _ := wgTenantClient(t, "qk42_stamp", WithLogger(logger))
	router, ta := wgRouter(c)
	tenantOf := func(id int64) string { return wgTenantRows(t, c)[id].TenantID }

	planted := wgTenantRow{ID: 10, TenantID: "tb", Name: "planted"}
	if err := For[wgTenantRow](ta, router).Create(&planted); err != nil {
		t.Fatalf("Create: %v", err)
	}
	if got := tenantOf(10); got != "ta" || planted.TenantID != "ta" {
		t.Errorf("Create under ta of an entity carrying tb stored %q (entity now %q), want ta", got, planted.TenantID)
	}
	if err := For[wgTenantRow](ta, router).CreateBatch([]*wgTenantRow{{ID: 11, TenantID: "tb", Name: "batch"}}); err != nil {
		t.Fatalf("CreateBatch: %v", err)
	}
	if got := tenantOf(11); got != "ta" {
		t.Errorf("CreateBatch under ta stored %q, want ta", got)
	}

	// ta's own row 1, written with tb in the tenant field: it stays ta's.
	if n, err := For[wgTenantRow](ta, router).Update(&wgTenantRow{ID: 1, TenantID: "tb", Name: "moved"}); err != nil || n != 1 {
		t.Fatalf("Update = (%d, %v), want (1, nil)", n, err)
	}
	if got := tenantOf(1); got != "ta" {
		t.Errorf("Update under ta moved row 1 to %q", got)
	}
	if _, err := For[wgTenantRow](ta, router).UpdateFields(&wgTenantRow{ID: 1, TenantID: "tb"}, "tenant_id"); err != nil {
		t.Fatalf("UpdateFields: %v", err)
	}
	if got := tenantOf(1); got != "ta" {
		t.Errorf("UpdateFields(tenant_id) under ta moved row 1 to %q", got)
	}
	if err := For[wgTenantRow](ta, router).UpdateBatch([]*wgTenantRow{{ID: 1, TenantID: "tb", Name: "batch-moved"}}); err != nil {
		t.Fatalf("UpdateBatch: %v", err)
	}
	if got := tenantOf(1); got != "ta" {
		t.Errorf("UpdateBatch under ta moved row 1 to %q", got)
	}

	mu.Lock()
	out := logs.String()
	mu.Unlock()
	if !strings.Contains(out, "quark.tenant.foreign_value_replaced") {
		t.Errorf("a foreign tenant value was replaced without a log line")
	}
}

// lockedWriter serialises writes to a strings.Builder shared with the test.
type lockedWriter struct {
	w  *strings.Builder
	mu *sync.Mutex
}

func (l *lockedWriter) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.w.Write(p)
}
