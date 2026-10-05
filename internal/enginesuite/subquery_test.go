// Copyright 2026 jcsvwinston
// SPDX-License-Identifier: Apache-2.0

package enginesuite

import (
	"context"
	"strings"
	"testing"

	"github.com/jcsvwinston/quark"
)

// subqueryUser / subqueryOrder are the canonical fixture for the subquery
// integration tests. Orders with a non-zero amount drive the EXISTS / IN
// subquery shapes; zero-amount orders are filtered out so the negated
// shapes (NotExists / NotInSub) have something to assert against.
type subqueryUser struct {
	ID   int64  `db:"id" pk:"true"`
	Name string `db:"name"`
}

type subqueryOrder struct {
	ID     int64 `db:"id" pk:"true"`
	UserID int64 `db:"user_id"`
	Amount int64 `db:"amount"`
}

// TestSubquery_QmarkCapture pins the dialect-agnostic capture contract:
// AsSubquery renders the inner SELECT with '?' as the bind marker
// regardless of the active dialect, and the args slice carries the WHERE
// values in the order they were enqueued. This is the precondition the
// outer AST relies on — `substitutePathMarkers` then renumbers each '?'
// to the outer dialect's placeholder syntax at the correct argIndex.
//
// SQLite is the harness here (its native placeholder is '?', so the
// captured fragment looks the same with or without the qmark wrapper),
// but the contract being asserted holds for any dialect: arg ordering
// matches the SQL fragment.
func TestSubquery_QmarkCapture(t *testing.T) {
	ctx := context.Background()
	client, err := quark.New("sqlite", ":memory:")
	if err != nil {
		t.Fatalf("client: %v", err)
	}
	defer client.Close()
	if err := client.Migrate(ctx, &subqueryUser{}, &subqueryOrder{}); err != nil {
		t.Fatalf("migrate: %v", err)
	}

	sub, err := quark.For[subqueryOrder](ctx, client).
		Select("user_id").
		Where("amount", ">", 100).
		Where("user_id", "<>", 0).
		AsSubquery()
	if err != nil {
		t.Fatalf("AsSubquery: %v", err)
	}
	sql, args := sub.SQL()

	wantArgs := []any{int64(100), int64(0)}
	if len(args) != len(wantArgs) {
		t.Fatalf("args = %v, want %v", args, wantArgs)
	}
	for i, a := range args {
		// SQLite drivers may pass int / int64 interchangeably; normalise.
		got := a
		switch v := a.(type) {
		case int:
			got = int64(v)
		case int64:
			got = v
		}
		if got != wantArgs[i] {
			t.Errorf("arg %d = %v, want %v", i, got, wantArgs[i])
		}
	}
	if !strings.Contains(sql, "WHERE") {
		t.Errorf("subquery sql missing WHERE: %q", sql)
	}
	// Native SQLite or qmark-via-AsSubquery: in both cases the fragment
	// must contain literal '?' (not '$1', '@p1', or ':1') because that
	// is the contract the outer AST consumes.
	if !strings.Contains(sql, "?") {
		t.Errorf("subquery sql missing '?' bind marker: %q", sql)
	}
	for _, bad := range []string{"$1", "@p1", ":1"} {
		if strings.Contains(sql, bad) {
			t.Errorf("subquery sql leaked dialect placeholder %q: %q", bad, sql)
		}
	}
}

// TestSubquery_RejectsLockOptions enforces the F2-subqueries decision: a
// subquery cannot carry pessimistic locks because dialect emission is
// inconsistent (MSSQL inlines `WITH (UPDLOCK)` in the FROM clause, which
// is illegal inside an `IN (SELECT ...)` context). Acquire locks on the
// outer query instead.
func TestSubquery_RejectsLockOptions(t *testing.T) {
	ctx := context.Background()
	client, err := quark.New("sqlite", ":memory:")
	if err != nil {
		t.Fatalf("client: %v", err)
	}
	defer client.Close()
	if err := client.Migrate(ctx, &subqueryOrder{}); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	// SQLite is allowed to fail at AsSubquery time too, before any dialect
	// would otherwise reject the lock — the rule is enforced uniformly in
	// `quark.AsSubquery` itself, not deferred to dialect.LockSuffix.
	_, err = quark.For[subqueryOrder](ctx, client).
		Where("amount", ">", 0).
		ForUpdate().
		AsSubquery()
	if err == nil {
		t.Fatalf("expected ErrUnsupportedFeature, got nil")
	}
	if !strings.Contains(err.Error(), "pessimistic lock") &&
		!strings.Contains(err.Error(), "Unsupported") {
		t.Errorf("error %q should mention pessimistic lock / unsupported", err)
	}
}
