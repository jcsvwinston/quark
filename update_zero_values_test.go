package quark_test

import (
	"context"
	"testing"

	"github.com/jcsvwinston/quark"
)

// testUpdateZeroValues is the regression test for P0-4. Update(entity) skips
// zero-value fields silently — that's the design while dirty tracking is
// still planned for Fase 1, but it must not be the only way to write zeros.
// UpdateFields(entity, fields...) is the explicit escape hatch and must
// write false / 0 / "" without filtering.
func testUpdateZeroValues(ctx context.Context, t *testing.T, baseClient *quark.Client) {
	t.Helper()

	type ZVUser struct {
		ID     int64  `db:"id" pk:"true"`
		Name   string `db:"name"`
		Active bool   `db:"active"`
		Score  int    `db:"score"`
		Title  string `db:"title"`
	}

	dropTable(baseClient, "zv_users")
	if err := baseClient.Migrate(ctx, &ZVUser{}); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	defer dropTable(baseClient, "zv_users")

	t.Run("UpdateSkipsZerosByDesign", func(t *testing.T) {
		u := &ZVUser{Name: "Alice", Active: true, Score: 10, Title: "captain"}
		if err := quark.For[ZVUser](ctx, baseClient).Create(u); err != nil {
			t.Fatalf("create: %v", err)
		}

		// Update with zero values explicitly set on the entity. Update will
		// silently skip them (that's the bug being documented; UpdateFields
		// is the escape hatch).
		u.Active = false
		u.Score = 0
		u.Title = ""
		if _, err := quark.For[ZVUser](ctx, baseClient).Update(u); err != nil {
			t.Fatalf("update: %v", err)
		}

		got, err := quark.For[ZVUser](ctx, baseClient).Find(u.ID)
		if err != nil {
			t.Fatalf("find: %v", err)
		}
		// Confirm Update did NOT write the zero values — the row still has
		// the originals. (When dirty tracking lands in Fase 1 this assertion
		// inverts; for now it documents the trap.)
		if got.Active != true || got.Score != 10 || got.Title != "captain" {
			t.Errorf("Update unexpectedly wrote zero values: got %+v", got)
		}
	})

	t.Run("UpdateFieldsWritesZeroBool", func(t *testing.T) {
		u := &ZVUser{Name: "Bob", Active: true, Score: 5, Title: "builder"}
		if err := quark.For[ZVUser](ctx, baseClient).Create(u); err != nil {
			t.Fatalf("create: %v", err)
		}

		u.Active = false
		rows, err := quark.For[ZVUser](ctx, baseClient).UpdateFields(u, "active")
		if err != nil {
			t.Fatalf("UpdateFields: %v", err)
		}
		if rows != 1 {
			t.Errorf("expected 1 row affected, got %d", rows)
		}

		got, _ := quark.For[ZVUser](ctx, baseClient).Find(u.ID)
		if got.Active != false {
			t.Errorf("expected active=false after UpdateFields, got %v", got.Active)
		}
		// Untouched fields stay as before.
		if got.Score != 5 || got.Title != "builder" {
			t.Errorf("UpdateFields touched fields it shouldn't have: %+v", got)
		}
	})

	t.Run("UpdateFieldsWritesZeroIntAndEmptyString", func(t *testing.T) {
		u := &ZVUser{Name: "Carol", Active: true, Score: 99, Title: "queen"}
		if err := quark.For[ZVUser](ctx, baseClient).Create(u); err != nil {
			t.Fatalf("create: %v", err)
		}

		u.Score = 0
		u.Title = ""
		rows, err := quark.For[ZVUser](ctx, baseClient).UpdateFields(u, "score", "title")
		if err != nil {
			t.Fatalf("UpdateFields: %v", err)
		}
		if rows != 1 {
			t.Errorf("expected 1 row, got %d", rows)
		}

		got, _ := quark.For[ZVUser](ctx, baseClient).Find(u.ID)
		if got.Score != 0 || got.Title != "" {
			t.Errorf("expected score=0 title=\"\", got %+v", got)
		}
		// Active untouched.
		if got.Active != true {
			t.Errorf("UpdateFields wrote a field it wasn't told to: active=%v", got.Active)
		}
	})

	t.Run("UpdateFieldsRejectsUnknownField", func(t *testing.T) {
		u := &ZVUser{ID: 1}
		_, err := quark.For[ZVUser](ctx, baseClient).UpdateFields(u, "definitely_not_a_column")
		if err == nil {
			t.Fatal("expected error for unknown field, got nil")
		}
	})

	t.Run("UpdateFieldsRefusesToOverwritePK", func(t *testing.T) {
		u := &ZVUser{ID: 1}
		_, err := quark.For[ZVUser](ctx, baseClient).UpdateFields(u, "id")
		if err == nil {
			t.Fatal("expected error when listing the PK in UpdateFields, got nil")
		}
	})

	t.Run("UpdateFieldsRejectsEmptyList", func(t *testing.T) {
		u := &ZVUser{ID: 1}
		_, err := quark.For[ZVUser](ctx, baseClient).UpdateFields(u)
		if err == nil {
			t.Fatal("expected error when no fields given, got nil")
		}
	})

	t.Run("UpdateFieldsRunsHooks", func(t *testing.T) {
		// Hooks run on UpdateFields just like on Update. A regression that
		// silently skipped them would not be caught by the other subtests.
		dropTable(baseClient, "hooked_users")
		if err := baseClient.Migrate(ctx, &HookedUser{}); err != nil {
			t.Fatalf("migrate hooked: %v", err)
		}
		t.Cleanup(func() { dropTable(baseClient, "hooked_users") })

		hookUserBefore = 0
		hookUserAfter = 0
		u := &HookedUser{Name: "Dora", Active: true}
		if err := quark.For[HookedUser](ctx, baseClient).Create(u); err != nil {
			t.Fatalf("create: %v", err)
		}

		// Reset after Create — only count UpdateFields calls.
		hookUserBefore = 0
		hookUserAfter = 0
		u.Active = false
		if _, err := quark.For[HookedUser](ctx, baseClient).UpdateFields(u, "active"); err != nil {
			t.Fatalf("UpdateFields: %v", err)
		}
		if hookUserBefore != 1 {
			t.Errorf("expected BeforeUpdate to fire once, got %d", hookUserBefore)
		}
		if hookUserAfter != 1 {
			t.Errorf("expected AfterUpdate to fire once, got %d", hookUserAfter)
		}
	})
}

// HookedUser implements BeforeUpdateHook and AfterUpdateHook to verify that
// UpdateFields runs the same hooks Update runs.
type HookedUser struct {
	ID     int64  `db:"id" pk:"true"`
	Name   string `db:"name"`
	Active bool   `db:"active"`
}

var (
	hookUserBefore int
	hookUserAfter  int
)

func (u *HookedUser) BeforeUpdate(ctx context.Context) error {
	hookUserBefore++
	return nil
}

func (u *HookedUser) AfterUpdate(ctx context.Context) error {
	hookUserAfter++
	return nil
}
