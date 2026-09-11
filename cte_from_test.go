// Copyright 2026 jcsvwinston
// SPDX-License-Identifier: Apache-2.0

package quark

import (
	"context"
	"strings"
	"testing"
	"time"
)

type fromCTEUser struct {
	ID   int64  `db:"id" pk:"true"`
	Name string `db:"name"`
}

// FromCTE is the half of With() that was missing: With declares the CTE, and
// before this the outer SELECT still read the model's table, so a CTE could
// only be reached by joining it.

func TestFromCTEReplacesTheFromClause(t *testing.T) {
	ctx := context.Background()
	c, err := New("sqlite", "file:fromcte1?mode=memory&cache=shared")
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	if err := c.Migrate(ctx, &fromCTEUser{}); err != nil {
		t.Fatalf("migrate: %v", err)
	}

	sub, err := For[fromCTEUser](ctx, c).Where("id", ">", 0).AsSubquery()
	if err != nil {
		t.Fatalf("AsSubquery: %v", err)
	}
	q := For[fromCTEUser](ctx, c).With("recent", sub).FromCTE("recent")
	sql, _, err := q.buildSelect()
	if err != nil {
		t.Fatalf("buildSelect: %v", err)
	}
	// The base table legitimately appears INSIDE the CTE body; what matters
	// is the outer SELECT, so assert on the tail after the CTE closes.
	outer := sql[strings.LastIndex(sql, ") SELECT ")+1:]
	if !strings.Contains(outer, `FROM "recent"`) {
		t.Errorf("outer select = %q, want it to read FROM the CTE", outer)
	}
	if strings.Contains(outer, `FROM "from_cte_users"`) {
		t.Errorf("outer select = %q, still reads the base table", outer)
	}
}

func TestFromCTEAppliesToCount(t *testing.T) {
	// Count() builds its own statement. Without the same swap it would count
	// the base table while List() read the CTE — the kind of divergence that
	// shows up as a pagination total that does not match the page.
	ctx := context.Background()
	c, err := New("sqlite", "file:fromcte2?mode=memory&cache=shared")
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	if err := c.Migrate(ctx, &fromCTEUser{}); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	if err := For[fromCTEUser](ctx, c).CreateBatch([]*fromCTEUser{
		{ID: 1, Name: "a"}, {ID: 2, Name: "b"}, {ID: 3, Name: "c"},
	}); err != nil {
		t.Fatalf("seed: %v", err)
	}

	sub, err := For[fromCTEUser](ctx, c).Where("id", "<", 3).AsSubquery()
	if err != nil {
		t.Fatalf("AsSubquery: %v", err)
	}
	n, err := For[fromCTEUser](ctx, c).With("two", sub).FromCTE("two").Count()
	if err != nil {
		t.Fatalf("Count: %v", err)
	}
	if n != 2 {
		t.Errorf("Count over the CTE = %d, want 2 (3 rows in the base table)", n)
	}
}

func TestFromCTEDoesNotRedirectWrites(t *testing.T) {
	// A CTE is not a write target. Silently redirecting an UPDATE would be
	// the worst possible reading of this call, so writes keep going to the
	// model's table.
	ctx := context.Background()
	c, err := New("sqlite", "file:fromcte3?mode=memory&cache=shared")
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	if err := c.Migrate(ctx, &fromCTEUser{}); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	if err := For[fromCTEUser](ctx, c).CreateBatch([]*fromCTEUser{{ID: 1, Name: "a"}}); err != nil {
		t.Fatalf("seed: %v", err)
	}

	sub, err := For[fromCTEUser](ctx, c).AsSubquery()
	if err != nil {
		t.Fatalf("AsSubquery: %v", err)
	}
	n, err := For[fromCTEUser](ctx, c).With("all_rows", sub).FromCTE("all_rows").
		Where("id", "=", 1).UpdateMap(map[string]any{"name": "renamed"})
	if err != nil {
		t.Fatalf("UpdateMap through a FromCTE query must still write the table: %v", err)
	}
	if n != 1 {
		t.Errorf("rows affected = %d, want 1", n)
	}
	got, err := For[fromCTEUser](ctx, c).Find(int64(1))
	if err != nil {
		t.Fatalf("Find: %v", err)
	}
	if got.Name != "renamed" {
		t.Errorf("name = %q, want %q — the write did not reach the table", got.Name, "renamed")
	}
}

func TestFromCTERejectsAnInvalidName(t *testing.T) {
	ctx := context.Background()
	c, err := New("sqlite", "file:fromcte4?mode=memory&cache=shared")
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	// The deferred error surfaces at the terminal method, which is where
	// every other builder validation surfaces too.
	_, err = For[fromCTEUser](ctx, c).FromCTE(`x"; DROP TABLE users; --`).Limit(1).List()
	if err == nil {
		t.Fatal("an invalid CTE name must be rejected")
	}
}

type fromCTESoftDelete struct {
	ID        int64      `db:"id" pk:"true"`
	Name      string     `db:"name"`
	DeletedAt *time.Time `db:"deleted_at"`
}

func TestFromCTEDropsTheSoftDeleteFilter(t *testing.T) {
	// The filter belongs to the model's table. Against a CTE that does not
	// carry deleted_at, emitting it returned zero rows and no error — a
	// silent wrong answer, which is the one outcome worth a test of its own.
	ctx := context.Background()
	c, err := New("sqlite", "file:fromcte5?mode=memory&cache=shared")
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	if err := c.Migrate(ctx, &fromCTESoftDelete{}); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	if err := For[fromCTESoftDelete](ctx, c).CreateBatch([]*fromCTESoftDelete{
		{ID: 1, Name: "a"}, {ID: 2, Name: "b"},
	}); err != nil {
		t.Fatalf("seed: %v", err)
	}

	// A CTE projecting one column: it has no deleted_at to filter on.
	sub, err := For[fromCTESoftDelete](ctx, c).Select("id").AsSubquery()
	if err != nil {
		t.Fatalf("AsSubquery: %v", err)
	}
	q := For[fromCTESoftDelete](ctx, c).With("ids", sub).FromCTE("ids")
	sql, _, err := q.buildSelect()
	if err != nil {
		t.Fatalf("buildSelect: %v", err)
	}
	outer := sql[strings.LastIndex(sql, ") SELECT ")+1:]
	if strings.Contains(outer, "deleted_at") {
		t.Errorf("outer select = %q, must not filter on a column the CTE need not have", outer)
	}
	rows, err := q.Limit(10).List()
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(rows) != 2 {
		t.Errorf("rows = %d, want 2", len(rows))
	}
}
