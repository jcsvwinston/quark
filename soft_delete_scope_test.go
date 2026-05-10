package quark_test

import (
	"context"
	"testing"
	"time"

	"github.com/jcsvwinston/quark"
)

// testSoftDeleteScopes covers the Phase-1 F1-5 deliverables:
//   - Default queries hide soft-deleted rows.
//   - WithTrashed() returns both live + trashed rows.
//   - OnlyTrashed() returns only the trashed rows.
//   - Restore() clears deleted_at on a trashed row and is a no-op on live
//     rows (so a misuse can't corrupt non-trashed data).
//   - Unscoped() still works as a backward-compatible alias for WithTrashed.
func testSoftDeleteScopes(ctx context.Context, t *testing.T, baseClient *quark.Client) {
	t.Helper()

	type SDPost struct {
		ID        int64      `db:"id" pk:"true"`
		Title     string     `db:"title"`
		DeletedAt *time.Time `db:"deleted_at"`
	}

	dropTable(baseClient, "sd_posts")
	if err := baseClient.Migrate(ctx, &SDPost{}); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	defer dropTable(baseClient, "sd_posts")

	// Seed three rows: live, soft-deleted, and a second live one.
	live1 := &SDPost{Title: "live-1"}
	live2 := &SDPost{Title: "live-2"}
	trashed := &SDPost{Title: "trashed"}
	for _, p := range []*SDPost{live1, live2, trashed} {
		if err := quark.For[SDPost](ctx, baseClient).Create(p); err != nil {
			t.Fatalf("create %s: %v", p.Title, err)
		}
	}
	if _, err := quark.For[SDPost](ctx, baseClient).Delete(trashed); err != nil {
		t.Fatalf("soft-delete: %v", err)
	}

	t.Run("DefaultScopeHidesTrashed", func(t *testing.T) {
		got, err := quark.For[SDPost](ctx, baseClient).Limit(50).List()
		if err != nil {
			t.Fatalf("list: %v", err)
		}
		if len(got) != 2 {
			t.Errorf("expected 2 live rows, got %d", len(got))
		}
		for _, p := range got {
			if p.Title == "trashed" {
				t.Errorf("default scope leaked a trashed row: %+v", p)
			}
		}
	})

	t.Run("WithTrashedReturnsAll", func(t *testing.T) {
		got, err := quark.For[SDPost](ctx, baseClient).WithTrashed().Limit(50).List()
		if err != nil {
			t.Fatalf("list: %v", err)
		}
		if len(got) != 3 {
			t.Errorf("expected 3 rows (2 live + 1 trashed), got %d", len(got))
		}
	})

	t.Run("UnscopedAliasOfWithTrashed", func(t *testing.T) {
		got, err := quark.For[SDPost](ctx, baseClient).Unscoped().Limit(50).List()
		if err != nil {
			t.Fatalf("list: %v", err)
		}
		if len(got) != 3 {
			t.Errorf("expected Unscoped to behave like WithTrashed, got %d rows", len(got))
		}
	})

	t.Run("OnlyTrashedReturnsTrashed", func(t *testing.T) {
		got, err := quark.For[SDPost](ctx, baseClient).OnlyTrashed().Limit(50).List()
		if err != nil {
			t.Fatalf("list: %v", err)
		}
		if len(got) != 1 {
			t.Fatalf("expected exactly 1 trashed row, got %d: %+v", len(got), got)
		}
		if got[0].Title != "trashed" {
			t.Errorf("expected the trashed row, got %q", got[0].Title)
		}
	})

	t.Run("CountRespectsScopes", func(t *testing.T) {
		// Default count → live only.
		n, err := quark.For[SDPost](ctx, baseClient).Count()
		if err != nil {
			t.Fatalf("count default: %v", err)
		}
		if n != 2 {
			t.Errorf("default Count expected 2, got %d", n)
		}
		// WithTrashed → all.
		n, err = quark.For[SDPost](ctx, baseClient).WithTrashed().Count()
		if err != nil {
			t.Fatalf("count with-trashed: %v", err)
		}
		if n != 3 {
			t.Errorf("WithTrashed Count expected 3, got %d", n)
		}
		// OnlyTrashed → trashed only.
		n, err = quark.For[SDPost](ctx, baseClient).OnlyTrashed().Count()
		if err != nil {
			t.Fatalf("count only-trashed: %v", err)
		}
		if n != 1 {
			t.Errorf("OnlyTrashed Count expected 1, got %d", n)
		}
	})

	t.Run("RestoreUntrashesARow", func(t *testing.T) {
		// Reload the trashed row via OnlyTrashed and Restore it.
		ts, err := quark.For[SDPost](ctx, baseClient).OnlyTrashed().Limit(1).List()
		if err != nil || len(ts) == 0 {
			t.Fatalf("could not load trashed row: %v len=%d", err, len(ts))
		}
		row := ts[0]
		rows, err := quark.For[SDPost](ctx, baseClient).Restore(&row)
		if err != nil {
			t.Fatalf("restore: %v", err)
		}
		if rows != 1 {
			t.Errorf("expected 1 row restored, got %d", rows)
		}
		if row.DeletedAt != nil {
			t.Errorf("expected entity DeletedAt cleared in memory, got %v", row.DeletedAt)
		}

		// The default scope must now find the row.
		all, _ := quark.For[SDPost](ctx, baseClient).WithTrashed().Limit(50).List()
		if len(all) != 3 {
			t.Errorf("expected 3 total rows, got %d", len(all))
		}
		live, _ := quark.For[SDPost](ctx, baseClient).Limit(50).List()
		if len(live) != 3 {
			t.Errorf("expected 3 live rows after restore, got %d", len(live))
		}
		trashedNow, _ := quark.For[SDPost](ctx, baseClient).OnlyTrashed().Limit(50).List()
		if len(trashedNow) != 0 {
			t.Errorf("expected 0 trashed rows after restore, got %d", len(trashedNow))
		}
	})

	t.Run("RestoreOnLiveRowIsNoop", func(t *testing.T) {
		// All rows are live again from the previous subtest; restoring one
		// of them must be a 0-row no-op (the IS NOT NULL guard) — never a
		// stealth NULL write.
		live, _ := quark.For[SDPost](ctx, baseClient).Limit(1).List()
		if len(live) == 0 {
			t.Fatal("expected at least one live row to seed this subtest")
		}
		row := live[0]
		rows, err := quark.For[SDPost](ctx, baseClient).Restore(&row)
		if err != nil {
			t.Fatalf("restore on live row: %v", err)
		}
		if rows != 0 {
			t.Errorf("Restore on a live row should be 0-row no-op, got %d", rows)
		}
	})
}
