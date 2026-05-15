package quark_test

import (
	"context"
	"sort"
	"strings"
	"sync"
	"testing"

	"github.com/jcsvwinston/quark"
)

// dtHooked is exported at package level so testDirtyTracking can use it as
// a hook receiver (BeforeUpdate / AfterUpdate are pointer-receiver methods).
type dtHooked struct {
	ID     int64  `db:"id" pk:"true"`
	Name   string `db:"name"`
	Active bool   `db:"active"`
}

var (
	dtHookedBefore int
	dtHookedAfter  int
)

func (u *dtHooked) BeforeUpdate(ctx context.Context) error {
	dtHookedBefore++
	return nil
}

func (u *dtHooked) AfterUpdate(ctx context.Context) error {
	dtHookedAfter++
	return nil
}

// trackingObserver captures emitted SQL so tests can inspect what Save wrote.
type trackingObserver struct {
	mu   sync.Mutex
	stmt []string
}

func (o *trackingObserver) ObserveQuery(e quark.QueryEvent) {
	if e.Operation != "EXEC" {
		return
	}
	o.mu.Lock()
	o.stmt = append(o.stmt, e.SQL)
	o.mu.Unlock()
}

func (o *trackingObserver) reset() {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.stmt = nil
}

func (o *trackingObserver) latest() string {
	o.mu.Lock()
	defer o.mu.Unlock()
	if len(o.stmt) == 0 {
		return ""
	}
	return o.stmt[len(o.stmt)-1]
}

// testDirtyTracking is the regression test for the Phase-1 dirty-tracking
// feature. It is the permanent fix for P0-4: Save must write zero values
// when they actually changed, must skip unchanged columns entirely, and
// must update its snapshot so subsequent Save calls remain correct.
func testDirtyTracking(ctx context.Context, t *testing.T, baseClient *quark.Client) {
	t.Helper()

	type DTUser struct {
		ID     int64  `db:"id" pk:"true"`
		Name   string `db:"name"`
		Active bool   `db:"active"`
		Score  int    `db:"score"`
		Title  string `db:"title"`
	}

	dropTable(baseClient, "dt_users")
	if err := baseClient.Migrate(ctx, &DTUser{}); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	defer dropTable(baseClient, "dt_users")

	obs := &trackingObserver{}
	client, err := baseClient.WithOptions(quark.WithQueryObserver(obs))
	if err != nil {
		t.Fatalf("WithOptions: %v", err)
	}

	t.Run("WritesZeroValuesWhenChanged", func(t *testing.T) {
		u := &DTUser{Name: "Alice", Active: true, Score: 10, Title: "captain"}
		if err := quark.For[DTUser](ctx, client).Create(u); err != nil {
			t.Fatalf("create: %v", err)
		}

		obs.reset()

		// Load with Track, mutate to zero values, Save.
		tracked, err := quark.For[DTUser](ctx, client).Track().Find(u.ID)
		if err != nil {
			t.Fatalf("track find: %v", err)
		}
		tracked.Entity.Active = false
		tracked.Entity.Score = 0
		tracked.Entity.Title = ""

		// Changed() should report the three mutated columns, ignoring name+id.
		changed := tracked.Changed()
		sort.Strings(changed)
		want := []string{"active", "score", "title"}
		if strings.Join(changed, ",") != strings.Join(want, ",") {
			t.Errorf("Changed() = %v, want %v", changed, want)
		}

		rows, err := tracked.Save(ctx)
		if err != nil {
			t.Fatalf("save: %v", err)
		}
		if rows != 1 {
			t.Errorf("expected 1 row affected, got %d", rows)
		}

		// Verify zero values landed.
		got, _ := quark.For[DTUser](ctx, client).Find(u.ID)
		if got.Active != false || got.Score != 0 || got.Title != "" {
			t.Errorf("expected zero values written, got %+v", got)
		}
		if got.Name != "Alice" {
			t.Errorf("Save unexpectedly touched name: %+v", got)
		}

		// Inspect emitted SQL: it must mention the three changed columns
		// and NOT mention "name" (untouched).
		sql := obs.latest()
		if sql == "" {
			t.Fatal("no EXEC observed")
		}
		for _, col := range []string{"active", "score", "title"} {
			if !strings.Contains(sql, col) {
				t.Errorf("SQL missing changed column %q: %s", col, sql)
			}
		}
		if strings.Contains(strings.ToLower(sql), "\"name\"") || strings.Contains(strings.ToLower(sql), "`name`") || strings.Contains(strings.ToLower(sql), "[name]") {
			t.Errorf("Save touched untouched column 'name': %s", sql)
		}
	})

	t.Run("NoChangeMeansNoSQL", func(t *testing.T) {
		u := &DTUser{Name: "Bob", Active: true, Score: 5, Title: "builder"}
		if err := quark.For[DTUser](ctx, client).Create(u); err != nil {
			t.Fatalf("create: %v", err)
		}

		obs.reset()

		tracked, err := quark.For[DTUser](ctx, client).Track().Find(u.ID)
		if err != nil {
			t.Fatalf("track find: %v", err)
		}
		// No mutation.
		rows, err := tracked.Save(ctx)
		if err != nil {
			t.Fatalf("save no-op: %v", err)
		}
		if rows != 0 {
			t.Errorf("expected 0 rows affected (no change), got %d", rows)
		}
		if obs.latest() != "" {
			t.Errorf("expected no SQL when nothing changed, got: %s", obs.latest())
		}
		if changed := tracked.Changed(); len(changed) != 0 {
			t.Errorf("Changed() with no mutation should be empty, got %v", changed)
		}
	})

	t.Run("SnapshotRefreshesAfterSave", func(t *testing.T) {
		u := &DTUser{Name: "Carol", Active: true, Score: 99, Title: "queen"}
		if err := quark.For[DTUser](ctx, client).Create(u); err != nil {
			t.Fatalf("create: %v", err)
		}

		tracked, err := quark.For[DTUser](ctx, client).Track().Find(u.ID)
		if err != nil {
			t.Fatalf("track find: %v", err)
		}
		// First Save: mutate score.
		tracked.Entity.Score = 0
		if _, err := tracked.Save(ctx); err != nil {
			t.Fatalf("save 1: %v", err)
		}

		// After Save the snapshot must reflect the new state. A second Save
		// without further mutation should be a no-op.
		obs.reset()
		rows, err := tracked.Save(ctx)
		if err != nil {
			t.Fatalf("save 2: %v", err)
		}
		if rows != 0 {
			t.Errorf("expected 0 rows on second Save (snapshot refreshed), got %d", rows)
		}

		// Mutate Title; only title should be in the next SQL.
		tracked.Entity.Title = "ex-queen"
		obs.reset()
		if _, err := tracked.Save(ctx); err != nil {
			t.Fatalf("save 3: %v", err)
		}
		sql := obs.latest()
		if !strings.Contains(sql, "title") {
			t.Errorf("expected title in SQL, got: %s", sql)
		}
		// score must NOT appear: snapshot is at 0 and entity is still 0.
		if strings.Contains(sql, "score") {
			t.Errorf("score should not be in SQL after snapshot refresh: %s", sql)
		}
	})

	t.Run("ListReturnsTrackedSlice", func(t *testing.T) {
		// Seed three rows.
		for _, n := range []string{"d1", "d2", "d3"} {
			u := &DTUser{Name: n, Active: true}
			if err := quark.For[DTUser](ctx, client).Create(u); err != nil {
				t.Fatalf("create %q: %v", n, err)
			}
		}

		tracked, err := quark.For[DTUser](ctx, client).
			Where("name", "LIKE", "d%").
			Track().
			List()
		if err != nil {
			t.Fatalf("track list: %v", err)
		}
		if len(tracked) < 3 {
			t.Fatalf("expected at least 3 tracked rows, got %d", len(tracked))
		}

		// Deactivate the first one.
		obs.reset()
		tracked[0].Entity.Active = false
		if _, err := tracked[0].Save(ctx); err != nil {
			t.Fatalf("save tracked[0]: %v", err)
		}

		// Verify only one EXEC ran.
		obs.mu.Lock()
		n := len(obs.stmt)
		obs.mu.Unlock()
		if n != 1 {
			t.Errorf("expected 1 EXEC for tracked[0].Save, got %d", n)
		}
	})

	t.Run("HooksRunOnSave", func(t *testing.T) {
		// BeforeUpdate / AfterUpdate must fire on Tracked.Save just like on
		// Update / UpdateFields. A future regression that drops the hook
		// calls would otherwise pass silently.
		dropTable(baseClient, "dt_hooked")
		if err := baseClient.Migrate(ctx, &dtHooked{}); err != nil {
			t.Fatalf("migrate hooked: %v", err)
		}
		t.Cleanup(func() { dropTable(baseClient, "dt_hooked") })

		dtHookedBefore = 0
		dtHookedAfter = 0
		u := &dtHooked{Name: "Eve", Active: true}
		if err := quark.For[dtHooked](ctx, client).Create(u); err != nil {
			t.Fatalf("create: %v", err)
		}

		dtHookedBefore = 0
		dtHookedAfter = 0
		tracked, err := quark.For[dtHooked](ctx, client).Track().Find(u.ID)
		if err != nil {
			t.Fatalf("track find: %v", err)
		}
		tracked.Entity.Active = false
		if _, err := tracked.Save(ctx); err != nil {
			t.Fatalf("save: %v", err)
		}
		if dtHookedBefore != 1 {
			t.Errorf("expected BeforeUpdate to fire once on Save, got %d", dtHookedBefore)
		}
		if dtHookedAfter != 1 {
			t.Errorf("expected AfterUpdate to fire once on Save, got %d", dtHookedAfter)
		}
	})

	t.Run("TenantPredicatePropagated", func(t *testing.T) {
		// Save propagates the loading query's tenantID into the WHERE so a
		// user that switched contexts cannot rewrite a foreign tenant's row.
		// This is the dirty-tracking counterpart of the cloneForGroup
		// invariant in tenant_router_test.go (P0-1 regression).
		type DTOrder struct {
			ID       int64  `db:"id" pk:"true"`
			TenantID string `db:"tenant_id"`
			Status   string `db:"status"`
		}
		dropTable(baseClient, "dt_orders")
		if err := baseClient.Migrate(ctx, &DTOrder{}); err != nil {
			t.Fatalf("migrate: %v", err)
		}
		t.Cleanup(func() { dropTable(baseClient, "dt_orders") })

		// Seed two rows directly through the base client (no router) so we
		// can plant rows under arbitrary tenant_ids.
		row := &DTOrder{TenantID: "ta", Status: "pending"}
		if err := quark.For[DTOrder](ctx, baseClient).Create(row); err != nil {
			t.Fatalf("seed ta: %v", err)
		}
		other := &DTOrder{TenantID: "tb", Status: "pending"}
		if err := quark.For[DTOrder](ctx, baseClient).Create(other); err != nil {
			t.Fatalf("seed tb: %v", err)
		}

		cfg := quark.DefaultTenantConfig()
		cfg.Strategy = quark.RowLevelSecurityClient
		cfg.BaseClient = baseClient
		type ctxKey string
		const tenantKey ctxKey = "tenant_id"
		resolver := func(c context.Context) string {
			if v, ok := c.Value(tenantKey).(string); ok {
				return v
			}
			return ""
		}
		// factory is nil; RLS reuses the BaseClient.
		router := quark.NewTenantRouter(cfg, resolver, nil)

		ctxA := context.WithValue(ctx, tenantKey, "ta")
		tracked, err := quark.For[DTOrder](ctxA, router).Track().Find(row.ID)
		if err != nil {
			t.Fatalf("track find under tenant ta: %v", err)
		}

		// Mutate a column AND attempt to switch tenant_id.
		tracked.Entity.Status = "paid"
		tracked.Entity.TenantID = "tb" // attempt to escape isolation

		rows, err := tracked.Save(ctx)
		if err != nil {
			t.Fatalf("save: %v", err)
		}
		if rows != 1 {
			t.Errorf("expected 1 row updated under tenant ta, got %d", rows)
		}

		// Verify the row stayed under tenant ta and only Status was written.
		got, _ := quark.For[DTOrder](ctx, baseClient).Find(row.ID)
		if got.TenantID != "ta" {
			t.Errorf("Save moved row across tenants: tenant_id=%q (expected ta)", got.TenantID)
		}
		if got.Status != "paid" {
			t.Errorf("expected status=paid, got %q", got.Status)
		}

		// Verify the foreign tenant's row was not touched.
		untouched, _ := quark.For[DTOrder](ctx, baseClient).Find(other.ID)
		if untouched.Status != "pending" {
			t.Errorf("foreign tenant's row was modified: status=%q", untouched.Status)
		}
	})

	t.Run("PrimaryKeyNeverMutated", func(t *testing.T) {
		u := &DTUser{Name: "Dave", Active: true}
		if err := quark.For[DTUser](ctx, client).Create(u); err != nil {
			t.Fatalf("create: %v", err)
		}

		tracked, err := quark.For[DTUser](ctx, client).Track().Find(u.ID)
		if err != nil {
			t.Fatalf("track find: %v", err)
		}
		// Try to mutate the PK and save. The Save must NOT write the PK
		// column even though its value differs from the snapshot — the PK
		// is always WHERE-only.
		original := tracked.Entity.ID
		tracked.Entity.ID = original + 99999
		tracked.Entity.Active = false

		obs.reset()
		_, err = tracked.Save(ctx)
		// The Save uses the (mutated) PK in the WHERE. With a fake PK there
		// will be 0 rows affected; that's fine. We only care that the SQL
		// did not include the PK in the SET clause.
		if err != nil {
			t.Fatalf("save with mutated PK: %v", err)
		}
		sql := obs.latest()
		if strings.Contains(sql, "\"id\" =") || strings.Contains(sql, "`id` =") || strings.Contains(sql, "[id] =") {
			// "WHERE id = ?" is acceptable; "SET id = ?, ..." is not. Look
			// for the PK assignment specifically by checking if "id" appears
			// before the WHERE keyword.
			idx := strings.Index(strings.ToUpper(sql), "WHERE")
			if idx < 0 {
				idx = len(sql)
			}
			set := sql[:idx]
			if strings.Contains(set, "\"id\"") || strings.Contains(set, "`id`") || strings.Contains(set, "[id]") {
				t.Errorf("Save included PK in SET clause: %s", sql)
			}
		}
	})
}
