// Copyright 2026 jcsvwinston
// SPDX-License-Identifier: Apache-2.0

// Regression tests for the v1.2.1 backlog P0 items (QK-P0-2, QK-P0-3,
// QK-P0-4). QK-P0-1 (tenant provision SQL injection) is covered in
// cmd/quark/commands/tenant_test.go.
package quark_test

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/jcsvwinston/quark"

	_ "modernc.org/sqlite"
)

// seedFourUsers inserts 2 active + 2 inactive users and returns the client.
func seedFourUsers(t *testing.T) (*quark.Client, func()) {
	t.Helper()
	client, cleanup := setupTestDB(t)
	ctx := context.Background()
	for i, u := range []User{
		{Email: "a1@test.com", Name: "A", Active: true},
		{Email: "a2@test.com", Name: "A", Active: true},
		{Email: "b1@test.com", Name: "B", Active: false},
		{Email: "b2@test.com", Name: "B", Active: false},
	} {
		u := u
		if err := quark.For[User](ctx, client).Create(&u); err != nil {
			cleanup()
			t.Fatalf("seed %d: %v", i, err)
		}
	}
	return client, cleanup
}

// QK-P0-2: Count()/Paginate() on a UNION query used to count only the base
// operand (List()=4 but Count()=2).
func TestUnionCountMatchesList(t *testing.T) {
	client, cleanup := seedFourUsers(t)
	defer cleanup()
	ctx := context.Background()

	base := quark.For[User](ctx, client).Where("active", "=", true)
	other := quark.For[User](ctx, client).Where("active", "=", false)
	union := base.Union(other)

	items, err := union.List()
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	count, err := union.Count()
	if err != nil {
		t.Fatalf("Count: %v", err)
	}
	if int(count) != len(items) {
		t.Fatalf("Count()=%d but List() returned %d rows — set-op ignored in Count", count, len(items))
	}
	if count != 4 {
		t.Fatalf("expected 4 combined rows, got %d", count)
	}

	page, err := union.Paginate(3, 0)
	if err != nil {
		t.Fatalf("Paginate: %v", err)
	}
	if page.Total != 4 {
		t.Fatalf("Paginate.Total=%d, want 4 (inherited the broken Count)", page.Total)
	}
	if page.TotalPages != 2 {
		t.Fatalf("Paginate.TotalPages=%d, want 2", page.TotalPages)
	}
}

// QK-P0-2 (variant): UNION with duplicate-eliminating semantics still counts
// the combined result, and UNION ALL counts duplicates.
func TestUnionAllCount(t *testing.T) {
	client, cleanup := seedFourUsers(t)
	defer cleanup()
	ctx := context.Background()

	// Same predicate on both sides: UNION collapses duplicates, UNION ALL keeps them.
	act1 := quark.For[User](ctx, client).Where("active", "=", true)
	act2 := quark.For[User](ctx, client).Where("active", "=", true)

	count, err := act1.Union(act2).Count()
	if err != nil {
		t.Fatalf("Union Count: %v", err)
	}
	if count != 2 {
		t.Fatalf("UNION count=%d, want 2 (deduplicated)", count)
	}

	countAll, err := act1.UnionAll(act2).Count()
	if err != nil {
		t.Fatalf("UnionAll Count: %v", err)
	}
	if countAll != 4 {
		t.Fatalf("UNION ALL count=%d, want 4 (duplicates kept)", countAll)
	}
}

// QK-P0-2 (CTE hoisting): a compound select whose outer query defines a CTE
// must hoist the WITH prefix outside the COUNT(*) derived table.
func TestUnionCountWithCTE(t *testing.T) {
	client, cleanup := seedFourUsers(t)
	defer cleanup()
	ctx := context.Background()

	sub, err := quark.For[User](ctx, client).Select("id").Where("active", "=", true).AsSubquery()
	if err != nil {
		t.Fatalf("AsSubquery: %v", err)
	}
	base := quark.For[User](ctx, client).
		With("active_ids", sub).
		Join("active_ids").On("users.id", "=", "active_ids.id")
	other := quark.For[User](ctx, client).Where("active", "=", false)

	count, cntErr := base.Union(other).Count()
	if cntErr != nil {
		t.Fatalf("Count with CTE + UNION: %v", cntErr)
	}
	if count != 4 {
		t.Fatalf("count=%d, want 4", count)
	}
}

// QK-P0-3: an empty conflict target used to diverge by engine (PG: silent DO
// NOTHING, MySQL: panic, MSSQL/Oracle: invalid SQL). Now ErrInvalidQuery everywhere.
func TestUpsertEmptyConflictColsRejected(t *testing.T) {
	client, cleanup := setupTestDB(t)
	defer cleanup()
	ctx := context.Background()

	u := User{Email: "up@test.com", Name: "Up"}
	err := quark.For[User](ctx, client).Upsert(&u, nil, nil)
	if !errors.Is(err, quark.ErrInvalidQuery) {
		t.Fatalf("Upsert(nil conflictCols): got %v, want ErrInvalidQuery", err)
	}

	err = quark.For[User](ctx, client).UpsertBatch([]*User{&u}, []string{}, nil)
	if !errors.Is(err, quark.ErrInvalidQuery) {
		t.Fatalf("UpsertBatch(empty conflictCols): got %v, want ErrInvalidQuery", err)
	}
}

// QK-P0-3 (defensive): the MySQL dialect fragment must not panic on an empty
// conflict target even if a future call-site skips the query-level guard.
func TestMySQLUpsertSQLEmptyConflictColsNoPanic(t *testing.T) {
	for _, d := range []quark.Dialect{quark.MySQL(), quark.MariaDB()} {
		got := d.UpsertSQL(nil, nil, 1)
		if got != "" {
			t.Errorf("%s UpsertSQL(nil, nil): got %q, want empty fragment", d.Name(), got)
		}
		if u := d.UpsertSQL(nil, []string{"name"}, 1); !strings.Contains(u, "DUPLICATE KEY") {
			t.Errorf("%s UpsertSQL(nil, updateCols): %q", d.Name(), u)
		}
	}
}

// QK-P0-4: Offset without Limit used to render invalid SQL on SQLite
// (`OFFSET n` alone is a syntax error). Iter() applies no implicit limit, so
// it exercises the offset-only path end-to-end.
func TestOffsetWithoutLimitSQLite(t *testing.T) {
	client, cleanup := seedFourUsers(t)
	defer cleanup()
	ctx := context.Background()

	var seen []string
	err := quark.For[User](ctx, client).
		OrderBy("id", "ASC").
		Offset(1).
		Iter(func(u User) error {
			seen = append(seen, u.Email)
			return nil
		})
	if err != nil {
		t.Fatalf("Iter with Offset-only: %v", err)
	}
	if len(seen) != 3 {
		t.Fatalf("expected 3 rows after skipping 1, got %d (%v)", len(seen), seen)
	}
	if seen[0] != "a2@test.com" {
		t.Fatalf("first row after offset = %q, want a2@test.com", seen[0])
	}
}
