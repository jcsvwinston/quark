// Copyright 2026 jcsvwinston
// SPDX-License-Identifier: Apache-2.0

package quark

import (
	"context"
	"errors"
	"strings"
	"testing"
)

// A8 S9 (OPS-15). Paginate walks with OFFSET and two statements; PaginateAfter
// seeks to the last row read with one statement and hands back a token.

type s9Row struct {
	ID    int64  `db:"id" pk:"true"`
	Owner string `db:"owner"`
	Score int64  `db:"score"`
}

func (s9Row) TableName() string { return "s9_rows" }

func s9Client(t *testing.T) (*Client, *txStatementRecorder) {
	t.Helper()
	c, rec := likeClient(t, "s9_keyset_"+t.Name())
	ctx := context.Background()
	if err := c.Migrate(ctx, &s9Row{}); err != nil {
		t.Fatal(err)
	}
	// Owners and scores collide on purpose: the seek has to break ties by
	// the primary key, or rows are skipped or repeated between pages.
	for i, r := range []s9Row{{Owner: "a", Score: 10}, {Owner: "a", Score: 10}, {Owner: "b", Score: 5}, {Owner: "b", Score: 20}, {Owner: "c", Score: 5}} {
		r.ID = int64(i + 1)
		if err := For[s9Row](ctx, c).Create(&r); err != nil {
			t.Fatal(err)
		}
	}
	rec.reset()
	return c, rec
}

func TestPaginateAfterSeeksInsteadOfOffsetting(t *testing.T) {
	c, rec := s9Client(t)
	ctx := context.Background()
	first, err := For[s9Row](ctx, c).OrderBy("id", "ASC").PaginateAfter(2, "")
	if err != nil {
		t.Fatal(err)
	}
	if len(first.Items) != 2 || first.Items[1].ID != 2 || !first.HasMore || first.Next == "" {
		t.Fatalf("first page: %+v", first)
	}
	if strings.Contains(strings.ToUpper(rec.read()), "OFFSET") || strings.Contains(strings.ToUpper(rec.read()), " WHERE ") {
		t.Fatalf("the first page should be a plain limited select: %s", rec.read())
	}
	rec.reset()
	second, err := For[s9Row](ctx, c).OrderBy("id", "ASC").PaginateAfter(2, first.Next)
	if err != nil {
		t.Fatal(err)
	}
	if len(second.Items) != 2 || second.Items[0].ID != 3 || second.Items[1].ID != 4 || !second.HasMore {
		t.Fatalf("second page: %+v", second)
	}
	stmt := rec.read()
	if strings.Contains(strings.ToUpper(stmt), "OFFSET") || !strings.Contains(stmt, `"id" > ?`) {
		t.Fatalf("the second page has to seek with a predicate on the ordering column and no OFFSET: %s", stmt)
	}
	if rec.count() != 1 {
		t.Fatalf("a keyset page is one statement, got %d: %v", rec.count(), rec.all)
	}
	third, err := For[s9Row](ctx, c).OrderBy("id", "ASC").PaginateAfter(2, second.Next)
	if err != nil || len(third.Items) != 1 || third.Items[0].ID != 5 || third.HasMore {
		t.Fatalf("last page: %+v %v", third, err)
	}
}

// A composite order with ties and a DESC leg: every row exactly once, in
// order, across pages of size 2 — and the primary key is appended to make
// the order total.
func TestPaginateAfterWalksACompositeOrderWithoutSkippingOrRepeating(t *testing.T) {
	c, rec := s9Client(t)
	ctx := context.Background()
	var seen []int64
	token := ""
	for pages := 0; pages < 10; pages++ {
		rec.reset()
		page, err := For[s9Row](ctx, c).OrderBy("owner", "ASC").OrderBy("score", "DESC").PaginateAfter(2, token)
		if err != nil {
			t.Fatal(err)
		}
		for _, r := range page.Items {
			seen = append(seen, r.ID)
		}
		if token != "" {
			stmt := rec.read()
			for _, want := range []string{`"owner" > ?`, `"owner" = ?`, `"score" < ?`, `"id" > ?`} {
				if !strings.Contains(stmt, want) {
					t.Fatalf("the seek lacks %q: %s", want, stmt)
				}
			}
		}
		if !page.HasMore {
			break
		}
		token = page.Next
	}
	// owner ASC, score DESC, id ASC: a(10,#1), a(10,#2), b(20,#4), b(5,#3), c(5,#5)
	want := []int64{1, 2, 4, 3, 5}
	if len(seen) != len(want) {
		t.Fatalf("saw %v, want %v", seen, want)
	}
	for i := range want {
		if seen[i] != want[i] {
			t.Fatalf("saw %v, want %v", seen, want)
		}
	}
}

func TestPaginateAfterRefusesForeignTokensAndExpressions(t *testing.T) {
	c, _ := s9Client(t)
	ctx := context.Background()
	page, err := For[s9Row](ctx, c).OrderBy("id", "ASC").PaginateAfter(2, "")
	if err != nil {
		t.Fatal(err)
	}
	// The same token under another ORDER BY seeks to the wrong place: refused.
	if _, err := For[s9Row](ctx, c).OrderBy("owner", "ASC").PaginateAfter(2, page.Next); !errors.Is(err, ErrInvalidQuery) {
		t.Fatalf("a token from another ordering was accepted: %v", err)
	}
	if _, err := For[s9Row](ctx, c).OrderBy("id", "ASC").PaginateAfter(2, "not-a-token"); !errors.Is(err, ErrInvalidQuery) {
		t.Fatalf("garbage token: %v", err)
	}
	// An ordering column that is not a model column has no value to read back.
	if _, err := For[s9Row](ctx, c).OrderBy("nope", "ASC").PaginateAfter(2, ""); !errors.Is(err, ErrInvalidQuery) {
		t.Fatalf("unknown ordering column: %v", err)
	}
	// No ORDER BY at all: the primary key orders the seek.
	p, err := For[s9Row](ctx, c).PaginateAfter(3, "")
	if err != nil || len(p.Items) != 3 || p.Items[2].ID != 3 {
		t.Fatalf("pk-only order: %+v %v", p, err)
	}
}
