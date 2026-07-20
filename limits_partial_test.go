// Copyright 2026 jcsvwinston
// SPDX-License-Identifier: Apache-2.0

package quark_test

import (
	"context"
	"testing"

	"github.com/jcsvwinston/quark"

	_ "modernc.org/sqlite"
)

// TestWithLimitsPartialLiteral is the red/green regression for #262: a
// partial Limits literal — the natural way to raise ONE limit — used to be
// copied verbatim into the client, leaving QueryTimeout at 0. Every builder
// execution path derives context.WithTimeout(ctx, limits.QueryTimeout), so
// the context was born already expired and every query failed instantly
// with a deadline error. After the fix, WithLimits fills zero-valued
// numeric fields from DefaultLimits and the same client works.
func TestWithLimitsPartialLiteral(t *testing.T) {
	client, err := quark.New("sqlite", ":memory:",
		quark.WithLimits(quark.Limits{MaxResults: 500, AllowRawQueries: true}),
	)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { client.Close() })

	ctx := context.Background()
	if err := client.Exec(ctx, `CREATE TABLE users (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		email TEXT NOT NULL,
		name TEXT NOT NULL,
		active BOOLEAN NOT NULL DEFAULT 1,
		created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
	)`); err != nil {
		t.Fatalf("create table: %v", err)
	}

	u := &User{Email: "partial@example.com", Name: "Partial", Active: true}
	if err := quark.For[User](ctx, client).Create(u); err != nil {
		t.Fatalf("Create under partial Limits literal failed: %v", err)
	}

	got, err := quark.For[User](ctx, client).Find(u.ID)
	if err != nil {
		t.Fatalf("Find under partial Limits literal failed: %v", err)
	}
	if got.Email != "partial@example.com" {
		t.Fatalf("Find returned wrong row: %+v", got)
	}

	list, err := quark.For[User](ctx, client).Where("active", "=", true).List()
	if err != nil {
		t.Fatalf("List under partial Limits literal failed: %v", err)
	}
	if len(list) != 1 {
		t.Fatalf("List returned %d rows, want 1", len(list))
	}
}
